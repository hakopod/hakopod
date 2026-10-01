#!/usr/bin/env python3
"""Exercise ClickHouse public endpoints with owned development fixtures.

Run this only inside the bounded development-VM systemd unit after both the
root coordinator and Vitess lane owner have written a current lane grant. The
runner creates its own control database, API process, scoped key, managed
ClickHouse database and probe namespace. It never changes the shared HAProxy
deployment or deletes a CRD. Faults are injected through an owned loopback API
proxy used only by the owned Hakopod server process.
"""
import argparse
import base64
from datetime import datetime, timedelta, timezone
import fcntl
import hashlib
import importlib.util
import ipaddress
import json
import os
from pathlib import Path
import platform
import re
import secrets
import selectors
import shutil
import signal
import socket
import stat
import subprocess
import sys
import time
import urllib.error
import urllib.parse
import urllib.request


def load_capacity_module():
    path = Path(__file__).with_name('development_public_endpoint_capacity.py')
    spec = importlib.util.spec_from_file_location('development_public_endpoint_capacity', path)
    if spec is None or spec.loader is None:
        raise ValueError('shared public-endpoint capacity source is unavailable')
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


capacity = load_capacity_module()

def load_fault_proxy_module():
    path = Path(__file__).with_name('development_public_endpoint_fault_proxy.py')
    spec = importlib.util.spec_from_file_location('development_public_endpoint_fault_proxy', path)
    if spec is None or spec.loader is None:
        raise ValueError('shared public-endpoint fault proxy source is unavailable')
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


fault_proxy_module = load_fault_proxy_module()



def load_fixture_module():
    path = Path(__file__).with_name('development_clickhouse_public_endpoint_fixture.py')
    spec = importlib.util.spec_from_file_location('development_clickhouse_public_endpoint_fixture', path)
    if spec is None or spec.loader is None:
        raise ValueError('ClickHouse fixture source is unavailable')
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


fixture = load_fixture_module()


def load_planner_module():
    path = Path(__file__).with_name('plan-development-clickhouse-public-endpoint-acceptance.py')
    spec = importlib.util.spec_from_file_location('clickhouse_public_endpoint_planner', path)
    if spec is None or spec.loader is None:
        raise ValueError('public-endpoint preflight planner is unavailable')
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


CONTEXT = 'k3d-hakopod-dev'
NODES = ('k3d-hakopod-dev-server-0', 'k3d-hakopod-database-worker-0')
MAX_RESPONSE = 1024 * 1024
MAX_INVENTORY = 2 * 1024 * 1024
MAX_REPORT = 4 * 1024 * 1024
MAX_SOURCE_FILES = 4096
FIXTURE_LABEL = 'hakopod.io/development-fixture'
IMAGE = re.compile(r'^[A-Za-z0-9._:/-]+@sha256:[a-f0-9]{64}$')
RUN_ID = re.compile(r'^pe-[a-f0-9]{16}$')
HAPROXY_IMAGE = ('docker.io/haproxytech/kubernetes-ingress:3.2.15@sha256:'
                 '6185ab228aa6a8f56fd8909e55fa4bbf82f6c765a28fe7e6cea69a7668f487e8')
CLICKHOUSE_SERVER_IMAGE = ('docker.io/clickhouse/clickhouse-server:26.3.33.24@sha256:'
                          '810861a2e2d0188744f5f23b2d3ec9ff95812bcb9ddbb8fed13a377a7f305893')
CLICKHOUSE_OPERATOR_IMAGE = ('docker.io/altinity/clickhouse-operator:0.27.4@sha256:'
                            'c60c872fedd85017f843167dfdd2b900bb06b668a3deaf02b21782e59af859e3')
CLICKHOUSE_KEEPER_IMAGE = ('docker.io/clickhouse/clickhouse-keeper:26.3.33.24@sha256:'
                          '3fd59d9efb8c9e9136c3c924ceaa004f65c0b14699f34e4eae4eb63e2f860803')
HAPROXY_CHART_SHA256 = 'cf1869175352e87866b2caf8c58c31fda32bf763d7e87734e229fba14ecee9ca'
CONFIG_FIELDS = (
    'schema_version', 'postgres_admin_url_file', 'psql', 'kubeconfig', 'kubectl', 'server_binary', 'fault_proxy_binary',
    'probe_image', 'probe_command', 'app_domain', 'public_address', 'public_domain', 'public_port',
    'tls_issuer', 'ingress_class', 'proxy_namespace', 'proxy_configmap', 'proxy_release',
    'allowed_node', 'denied_node', 'server_environment', 'docker', 'helm', 'haproxy_chart',
    'host_budget_file', 'fixture_shape',
)
ACCEPTANCE_LOCK = 'database-public-endpoint-acceptance.lock'


def protected(path, maximum=64 * 1024):
    descriptor = os.open(path, os.O_RDONLY | os.O_NOFOLLOW)
    try:
        info = os.fstat(descriptor)
        if (not stat.S_ISREG(info.st_mode) or info.st_uid != os.getuid() or
                info.st_mode & 0o077 or info.st_size > maximum):
            raise ValueError('protected input must be an owned regular file with mode 0600 or tighter')
        blocks = []
        remaining = maximum + 1
        while remaining:
            block = os.read(descriptor, min(remaining, 8192))
            if not block:
                break
            blocks.append(block)
            remaining -= len(block)
        data = b''.join(blocks)
        if len(data) > maximum:
            raise ValueError('protected input exceeds its bound')
        return data
    finally:
        os.close(descriptor)


def strict_json(path, fields, maximum=64 * 1024):
    value = json.loads(protected(path, maximum))
    if not isinstance(value, dict) or set(value) != set(fields):
        raise ValueError('protected JSON has missing or unknown fields')
    return value


def file_hash(path, maximum=64 * 1024 * 1024):
    if path.is_symlink() or not path.is_file() or path.stat().st_size > maximum:
        raise ValueError('hashed file is missing, symbolic or oversized')
    digest = hashlib.sha256()
    with path.open('rb') as source:
        for block in iter(lambda: source.read(1024 * 1024), b''):
            digest.update(block)
    return digest.hexdigest()


def valid_domain(value, maximum=220):
    if not isinstance(value, str) or not 1 <= len(value) <= maximum or value != value.lower():
        return False
    labels = value.split('.')
    return len(labels) >= 2 and all(re.fullmatch(r'[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?', label) for label in labels)


def validate_cgroup_limits(memory_value, cpu_value):
    try:
        memory = int(memory_value.strip())
        quota_text, period_text = cpu_value.split()
        quota, period = int(quota_text), int(period_text)
    except (ValueError, TypeError):
        raise ValueError('acceptance requires explicit cgroup v2 memory and CPU limits') from None
    if memory <= 0 or memory > 3 * 1024 ** 3 or quota <= 0 or period <= 0 or quota > period:
        raise ValueError('acceptance requires MemoryMax at most 3G and CPUQuota at most 100%')


def current_cgroup_limits():
    entries = Path('/proc/self/cgroup').read_text().splitlines()
    unified = next((line.split(':', 2)[2] for line in entries if line.startswith('0::')), None)
    if unified is None or '..' in Path(unified).parts:
        raise ValueError('acceptance requires a bounded unified cgroup')
    base = Path('/sys/fs/cgroup') / unified.lstrip('/')
    validate_cgroup_limits((base / 'memory.max').read_text(), (base / 'cpu.max').read_text())


def source_hashes(root):
    files = set()
    for directory in ('api', 'auth', 'cmd', 'internal', 'templates', 'examples/clickhouse-public-endpoint-acceptance',
                      'examples/public-endpoint-fault-proxy'):
        base = root / directory
        if not base.is_dir():
            raise ValueError('acceptance source snapshot is incomplete')
        for suffix in ('*.go', '*.sql', '*.sh', '*.py', '*.toml', '*.json'):
            files.update(base.rglob(suffix))
    files.update(root / name for name in (
        'go.mod', 'go.sum',
        'scripts/run-development-clickhouse-public-endpoint-acceptance.py',
        'scripts/plan-development-clickhouse-public-endpoint-acceptance.py',
        'scripts/development_public_endpoint_capacity.py',
        'scripts/development_public_endpoint_fault_proxy.py',
        'scripts/development_clickhouse_public_endpoint_fixture.py',
        'scripts/test-development-clickhouse-public-endpoint-plan.py',
        'scripts/test-development-clickhouse-public-endpoint-acceptance.py',
        'examples/clickhouse-public-endpoint-acceptance/README.md',
        'examples/clickhouse-public-endpoint-acceptance/Dockerfile',
    ))
    if len(files) > MAX_SOURCE_FILES:
        raise ValueError('acceptance source inventory exceeds its bound')
    result = {path.relative_to(root).as_posix(): file_hash(path) for path in sorted(files)}
    encoded = json.dumps(result, sort_keys=True, separators=(',', ':')).encode()
    if len(encoded) > MAX_REPORT:
        raise ValueError('source hash evidence exceeds its bound')
    return result


def command(args, *, stdin=None, maximum=MAX_INVENTORY, timeout=30, env=None, allowed=(0,)):
    if stdin is not None and len(stdin) > MAX_INVENTORY:
        raise ValueError('bounded development command input is too large')
    return capacity.bounded_command(args, stdin=stdin, maximum=maximum, timeout=timeout,
                                    env=env, allowed=allowed)


def acquire_acceptance_lock(root):
    path = root / ACCEPTANCE_LOCK
    descriptor = os.open(path, os.O_RDWR | os.O_CREAT | os.O_NOFOLLOW | os.O_CLOEXEC, 0o600)
    try:
        info = os.fstat(descriptor)
        if (not stat.S_ISREG(info.st_mode) or info.st_uid != os.getuid() or
                stat.S_IMODE(info.st_mode) != 0o600 or info.st_nlink != 1):
            raise ValueError('acceptance lock must be an owned, singly linked mode-0600 regular file')
        try:
            fcntl.flock(descriptor, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError:
            raise ValueError('another database public-endpoint acceptance owns the mutation lock') from None
        return os.fdopen(descriptor, 'r+b')
    except BaseException:
        os.close(descriptor)
        raise


def free_port():
    with socket.socket() as listener:
        listener.bind(('127.0.0.1', 0))
        return listener.getsockname()[1]


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, request, fp, code, msg, headers, newurl):
        raise ValueError('acceptance redirects are forbidden')


class API:
    def __init__(self, origin, key, sensitive=()):
        parsed = urllib.parse.urlsplit(origin)
        if (parsed.scheme != 'http' or parsed.hostname not in ('127.0.0.1', 'localhost', '::1') or
                parsed.username or parsed.path not in ('', '/') or parsed.query or parsed.fragment):
            raise ValueError('acceptance API must use an owned loopback HTTP origin')
        self.origin = origin.rstrip('/')
        self.key = key
        self.sensitive = [item.encode() for item in sensitive if item]
        self.opener = urllib.request.build_opener(urllib.request.ProxyHandler({}), NoRedirect())

    def call(self, method, path, body=None, expected=200, idem=None):
        if not path.startswith('/') or '..' in path:
            raise ValueError('invalid acceptance API path')
        headers = {'Authorization': 'Bearer ' + self.key, 'Content-Type': 'application/json'}
        if idem:
            headers['Idempotency-Key'] = idem
        payload = None if body is None else json.dumps(body, separators=(',', ':')).encode()
        request = urllib.request.Request(self.origin + '/api/v1' + path, data=payload, headers=headers, method=method)
        try:
            with self.opener.open(request, timeout=20) as response:
                status, raw = response.status, response.read(MAX_RESPONSE + 1)
        except urllib.error.HTTPError as error:
            status, raw = error.code, error.read(MAX_RESPONSE + 1)
        if len(raw) > MAX_RESPONSE or any(secret in raw for secret in self.sensitive):
            raise ValueError('API response exceeded its bound or exposed a credential')
        if status != expected:
            raise ValueError(f'{method} {path} expected HTTP {expected}, received {status}')
        result = json.loads(raw) if raw else None
        if result is not None and not isinstance(result, (dict, list)):
            raise ValueError('API returned an unexpected JSON value')
        return result

    def wait_operation(self, path, timeout, wanted='succeeded'):
        deadline = time.monotonic() + timeout
        last = None
        while time.monotonic() < deadline:
            last = self.call('GET', path)
            if last.get('status') not in ('queued', 'running'):
                if last.get('status') != wanted:
                    raise ValueError('operation reached an unexpected terminal status')
                return last
            time.sleep(1)
        raise ValueError('operation exceeded its bounded wait')


class Evidence:
    def __init__(self, path, run_id):
        self.path = path
        self.run_id = run_id
        self.descriptor = os.open(path, os.O_CREAT | os.O_EXCL | os.O_WRONLY, 0o600)
        self.output = os.fdopen(self.descriptor, 'w')
        self.events = 0

    def add(self, check, **facts):
        if self.events >= 256:
            raise ValueError('evidence event inventory exceeds its bound')
        clean = {}
        for key, value in facts.items():
            if not re.fullmatch(r'[a-z][a-z0-9_]{0,63}', key):
                raise ValueError('invalid evidence field')
            if isinstance(value, str) and len(value) > 512:
                raise ValueError('evidence value exceeds its bound')
            clean[key] = value
        event = {'schema_version': 1, 'run_id': self.run_id, 'at': datetime.now(timezone.utc).isoformat(),
                 'check': check, **clean}
        encoded = json.dumps(event, sort_keys=True, separators=(',', ':'))
        if len(encoded) > 4096:
            raise ValueError('evidence event exceeds its bound')
        self.output.write(encoded + '\n')
        self.output.flush()
        self.events += 1

    def close(self):
        if not self.output.closed:
            self.output.close()


class ServerProcess:
    def __init__(self, binary, source, environment, origin):
        self.binary, self.source, self.environment, self.origin = binary, source, environment, origin
        self.process = None

    def start(self, kubeconfig):
        if self.process is not None:
            raise ValueError('owned server is already running')
        env = dict(self.environment, HAKOPOD_KUBECONFIG=str(kubeconfig))
        self.process = subprocess.Popen([str(self.binary)], cwd=self.source, env=env,
                                        stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
                                        start_new_session=True)
        deadline = time.monotonic() + 30
        opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
        while time.monotonic() < deadline:
            if self.process.poll() is not None:
                raise ValueError('owned Hakopod server exited during startup')
            try:
                with opener.open(self.origin + '/healthz', timeout=1) as response:
                    if response.status == 200 and len(response.read(MAX_RESPONSE + 1)) <= MAX_RESPONSE:
                        return
            except (OSError, urllib.error.URLError):
                time.sleep(.2)
        self.stop()
        raise ValueError('owned Hakopod server did not become ready')

    def stop(self):
        if self.process is None:
            return
        process, self.process = self.process, None
        if process.poll() is None:
            try:
                os.killpg(process.pid, signal.SIGTERM)
            except ProcessLookupError:
                pass
            try:
                process.wait(timeout=15)
            except subprocess.TimeoutExpired:
                os.killpg(process.pid, signal.SIGKILL)
                process.wait(timeout=10)

    def crash(self):
        """Abruptly stop only the owned fixture to exercise durable retry."""
        if self.process is None:
            raise ValueError('owned server is not running')
        process, self.process = self.process, None
        try:
            os.killpg(process.pid, signal.SIGKILL)
        except ProcessLookupError:
            pass
        process.wait(timeout=10)


FaultProxy = fault_proxy_module.FaultProxy

def kube_json(kube, args, missing=False):
    suffix = ['--ignore-not-found'] if missing else []
    raw = command(kube + args + suffix + ['-o', 'json'], maximum=MAX_INVENTORY, timeout=20)
    if not raw and missing:
        return None
    return json.loads(raw)


def owned_clickhouse_root(kube, database_id):
    namespace = endpoint_namespace(database_id)
    ns = kube_json(kube, ['get', 'namespace', namespace])
    root = kube_json(kube, ['-n', namespace, 'get', 'clickhouseinstallations.clickhouse.altinity.com', 'database'])
    for item in (ns, root):
        meta = item.get('metadata', {})
        if (meta.get('deletionTimestamp') or not meta.get('uid') or
                meta.get('labels', {}).get('hakopod.io/database-id') != database_id or
                meta.get('labels', {}).get('app.kubernetes.io/managed-by') != 'hakopod'):
            raise ValueError('ClickHouse fixture ownership changed')
    return ns, root


def delete_owned_clickhouse_pod(kube, database_id, member, keeper=False):
    ns, root = owned_clickhouse_root(kube, database_id)
    namespace = ns['metadata']['name']
    pod = kube_json(kube, ['-n', namespace, 'get', 'pod', member['name']])
    meta = pod.get('metadata', {})
    if (meta.get('uid') != member['uid'] or not meta.get('resourceVersion') or meta.get('deletionTimestamp') or
            meta.get('labels', {}).get('hakopod.io/database-id') != database_id or
            (meta.get('labels', {}).get('hakopod.io/database-keeper') == 'true') != keeper):
        raise ValueError('fault target no longer matches the owned observed member')
    containers = pod.get('spec', {}).get('containers', [])
    if (len(containers) != 1 or containers[0].get('image') !=
            (CLICKHOUSE_KEEPER_IMAGE if keeper else CLICKHOUSE_SERVER_IMAGE)):
        raise ValueError('fault target image differs from the owned ClickHouse fixture')
    # Walk only the expected controller chain; never infer ownership from labels alone.
    current = pod
    kinds = {'ReplicaSet': 'replicasets.apps', 'Deployment': 'deployments.apps', 'StatefulSet': 'statefulsets.apps'}
    for _ in range(4):
        refs = [ref for ref in current.get('metadata', {}).get('ownerReferences', [])
                if ref.get('controller') is True or keeper and ref.get('kind') == 'Namespace']
        if len(refs) != 1:
            raise ValueError('fault target has ambiguous controller ownership')
        ref = refs[0]
        if ref.get('kind') == 'ClickHouseInstallation':
            if (ref.get('apiVersion') != 'clickhouse.altinity.com/v1' or ref.get('name') != 'database' or
                    ref.get('uid') != root['metadata']['uid']):
                raise ValueError('fault target belongs to another ClickHouse controller')
            break
        if keeper and ref.get('kind') == 'Namespace':
            if (ref.get('apiVersion') != 'v1' or ref.get('name') != namespace or
                    ref.get('uid') != ns['metadata']['uid']):
                raise ValueError('Keeper controller namespace identity changed')
            break
        if ref.get('kind') not in kinds or ref.get('apiVersion') != 'apps/v1':
            raise ValueError('fault target has unexpected controller type')
        current = kube_json(kube, ['-n', namespace, 'get', kinds[ref['kind']], ref['name']])
        if current.get('metadata', {}).get('uid') != ref.get('uid') or current['metadata'].get('deletionTimestamp'):
            raise ValueError('fault controller identity changed')
    else:
        raise ValueError('fault controller chain exceeded its bound')
    options = {'apiVersion': 'v1', 'kind': 'DeleteOptions', 'gracePeriodSeconds': 0,
               'preconditions': {'uid': meta['uid'], 'resourceVersion': meta['resourceVersion']}}
    path = '/api/v1/namespaces/' + namespace + '/pods/' + meta['name']
    command(kube + ['delete', '--raw', path, '-f', '-'], stdin=json.dumps(options).encode(), maximum=65536)


def expire_owned_clickhouse_ca(kube, database_id):
    # The private key stays in runner memory; the fenced JSON patch sends only
    # the public certificate. Do not persist or log private material.
    from cryptography import x509
    from cryptography.hazmat.primitives import hashes, serialization
    from cryptography.hazmat.primitives.asymmetric import ec
    ns, _ = owned_clickhouse_root(kube, database_id)
    namespace = ns['metadata']['name']
    secret = kube_json(kube, ['-n', namespace, 'get', 'secret', 'database-ca'])
    meta = secret.get('metadata', {})
    if (meta.get('deletionTimestamp') or not meta.get('resourceVersion') or not meta.get('uid') or
            meta.get('labels', {}).get('hakopod.io/database-id') != database_id or
            meta.get('labels', {}).get('app.kubernetes.io/managed-by') != 'hakopod' or
            not any(ref.get('apiVersion') == 'v1' and ref.get('kind') == 'Namespace' and
                    ref.get('name') == namespace and ref.get('uid') == ns['metadata']['uid']
                    for ref in meta.get('ownerReferences', []))):
        raise ValueError('fixture CA ownership changed')
    encoded = secret.get('data', {})
    if any(not isinstance(encoded.get(name), str) or len(encoded[name]) > 65536 for name in ('ca.crt', 'ca.key')):
        raise ValueError('fixture CA material is missing or oversized')
    certificate = x509.load_pem_x509_certificate(base64.b64decode(encoded['ca.crt'], validate=True))
    key = serialization.load_pem_private_key(base64.b64decode(encoded['ca.key'], validate=True), password=None)
    if (not isinstance(key, ec.EllipticCurvePrivateKey) or certificate.subject != certificate.issuer or
            key.public_key().public_numbers() != certificate.public_key().public_numbers() or
            not certificate.extensions.get_extension_for_class(x509.BasicConstraints).value.ca):
        raise ValueError('fixture issuer identity is invalid')
    now = datetime.now(timezone.utc)
    builder = (x509.CertificateBuilder().subject_name(certificate.subject).issuer_name(certificate.issuer)
               .public_key(certificate.public_key()).serial_number(certificate.serial_number)
               .not_valid_before(certificate.not_valid_before_utc).not_valid_after(now + timedelta(hours=24)))
    for extension in certificate.extensions:
        builder = builder.add_extension(extension.value, extension.critical)
    shortened = builder.sign(key, hashes.SHA256()).public_bytes(serialization.Encoding.PEM)
    secret['data']['ca.crt'] = base64.b64encode(shortened).decode()
    # JSON patch tests fence both UID and resourceVersion without resending the key.
    patch = [{'op': 'test', 'path': '/metadata/uid', 'value': meta['uid']},
             {'op': 'test', 'path': '/metadata/resourceVersion', 'value': meta['resourceVersion']},
             {'op': 'replace', 'path': '/data/ca.crt', 'value': secret['data']['ca.crt']}]
    command(kube + ['-n', namespace, 'patch', 'secret', 'database-ca', '--type=json', '--patch-file=/dev/stdin', '-o', 'name'],
            stdin=json.dumps(patch).encode(), maximum=1024)


def wait_clickhouse_observation(api, database_id, shape, predicate, timeout=900):
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        resource = api.call('GET', '/databases/' + database_id)
        observed = resource.get('observation', {})
        tls = observed.get('tls') or {}
        if (resource.get('status') == 'ready' and observed.get('status') == 'ready' and
                tls.get('verified') and tls.get('plaintext_rejected') and
                len(observed.get('members', [])) == shape['members'] and
                (shape['keepers'] == 0 or (observed.get('coordination') or {}).get('ready') and
                 len(observed['coordination'].get('members', [])) == shape['keepers']) and predicate(observed)):
            return observed
        time.sleep(3)
    raise ValueError('owned ClickHouse fault did not recover within its bound')


def owned_secret(kube, database_id, name):
    ns, _ = owned_clickhouse_root(kube, database_id)
    namespace = ns['metadata']['name']
    value = kube_json(kube, ['-n', namespace, 'get', 'secret', name])
    meta = value.get('metadata', {})
    if (meta.get('deletionTimestamp') or not meta.get('uid') or not meta.get('resourceVersion') or
            meta.get('labels', {}).get('hakopod.io/database-id') != database_id or
            meta.get('labels', {}).get('app.kubernetes.io/managed-by') != 'hakopod' or
            not any(ref.get('kind') == 'Namespace' and ref.get('apiVersion') == 'v1' and
                    ref.get('name') == namespace and ref.get('uid') == ns['metadata']['uid']
                    for ref in meta.get('ownerReferences', []))):
        raise ValueError('fixture identity ownership changed')
    return value


def identity_snapshot(kube, database_id, shape):
    from cryptography import x509
    from cryptography.hazmat.primitives import hashes
    result = {}
    for name in ('database-ca', 'database-tls', 'database-client-tls'):
        secret = owned_secret(kube, database_id, name)
        data = secret.get('data', {})
        if not data or len(json.dumps(data)) > 128 * 1024:
            raise ValueError('fixture identity has invalid size')
        result[name] = hashlib.sha256(json.dumps(data, sort_keys=True).encode()).hexdigest()
        if name == 'database-client-tls':
            leaf = x509.load_pem_x509_certificate(base64.b64decode(data['tls.crt'], validate=True))
            result['client_fingerprint'] = leaf.fingerprint(hashes.SHA256()).hex()
            result['client_names'] = list(leaf.extensions.get_extension_for_class(
                x509.SubjectAlternativeName).value.get_values_for_type(x509.DNSName))
    if shape['keepers']:
        namespace = endpoint_namespace(database_id)
        keeper = kube_json(kube, ['-n', namespace, 'get', 'statefulset', 'database-keeper'])
        pods = kube_json(kube, ['-n', namespace, 'get', 'pods', '-l', 'hakopod.io/database-keeper=true'])['items']
        if len(pods) != shape['keepers'] or keeper.get('spec', {}).get('replicas') != shape['keepers']:
            raise ValueError('Keeper identity snapshot is incomplete')
        result['keeper_template'] = hashlib.sha256(json.dumps(
            keeper['spec']['template'], sort_keys=True).encode()).hexdigest()
        result['keeper_uids'] = sorted(pod['metadata']['uid'] for pod in pods)
    return result


def require_internal_identity_unchanged(before, after):
    for key in ('database-ca', 'database-tls', 'keeper_template', 'keeper_uids'):
        if before.get(key) != after.get(key):
            raise ValueError('public SAN edit changed the internal or Keeper identity')


def expected_client_dns_names(database_id, shape, public_hosts=()):
    namespace = endpoint_namespace(database_id)
    names = ['database', 'database.' + namespace, 'database.' + namespace + '.svc',
             'database.' + namespace + '.svc.cluster.local']
    for shard in range(shape['shards']):
        for replica in range(shape['replicas'] + 1):
            host = f'chi-database-managed-{shard}-{replica}'
            names.extend((host, host + '.' + namespace + '.svc',
                          host + '.' + namespace + '.svc.cluster.local',
                          host + '-0.' + host + '.' + namespace + '.svc.cluster.local'))
    for keeper in range(shape['keepers']):
        names.append(f'database-keeper-{keeper}.{namespace}.svc.cluster.local')
    return names + sorted(set(public_hosts))


def require_exact_client_dns_names(snapshot, database_id, shape, public_hosts=()):
    expected = expected_client_dns_names(database_id, shape, public_hosts)
    if snapshot.get('client_names') != expected:
        raise ValueError('ClickHouse client certificate DNS names differ from the exact ordered identity')
    encoded = json.dumps(expected, separators=(',', ':')).encode()
    return {'dns_name_count': len(expected), 'dns_names_sha256': hashlib.sha256(encoded).hexdigest()}


def inspect_public_network_policy(kube, database_id, config, expected):
    policy = kube_json(kube, ['-n', endpoint_namespace(database_id), 'get', 'networkpolicy', 'database'])
    labels = policy.get('metadata', {}).get('labels', {})
    if (labels.get('hakopod.io/database-id') != database_id or
            labels.get('app.kubernetes.io/managed-by') != 'hakopod'):
        raise ValueError('database network policy ownership changed')
    ingress = policy.get('spec', {}).get('ingress', [])
    count = 0
    for rule in ingress:
        for peer in rule.get('from', []):
            if peer.get('namespaceSelector', {}).get('matchLabels', {}).get(
                    'kubernetes.io/metadata.name') != config['proxy_namespace']:
                continue
            count += 1
            if (peer != {'namespaceSelector': {'matchLabels': {'kubernetes.io/metadata.name': config['proxy_namespace']}},
                         'podSelector': {'matchLabels': {'app.kubernetes.io/name': 'kubernetes-ingress',
                                                       'app.kubernetes.io/instance': config['proxy_release']}}} or
                    sorted(rule.get('ports', []), key=lambda item: item.get('port', 0)) != [
                        {'port': 8443, 'protocol': 'TCP'}, {'port': 9440, 'protocol': 'TCP'}]):
                raise ValueError('public ingress includes an unreviewed source or nonclient port')
    if count != int(expected):
        raise ValueError('public ingress does not match the endpoint lifecycle')


def owned_volume_inventory(kube, database_id, shape, complete=True):
    namespace = endpoint_namespace(database_id)
    claims = kube_json(kube, ['-n', namespace, 'get', 'persistentvolumeclaims'])['items']
    expected = 2 * shape['members'] + shape['keepers']
    if len(claims) > expected or complete and len(claims) != expected:
        raise ValueError('ClickHouse data, backup and Keeper claims do not match the admitted fixture')
    result = []
    for claim in claims:
        meta = claim.get('metadata', {})
        if not complete and claim.get('status', {}).get('phase') == 'Pending' and not claim.get('spec', {}).get('volumeName'):
            continue
        if not meta.get('uid') or meta.get('namespace') != namespace or claim.get('status', {}).get('phase') != 'Bound':
            raise ValueError('fixture claim is not bound to the owned namespace')
        name = claim.get('spec', {}).get('volumeName', '')
        volume = kube_json(kube, ['get', 'persistentvolume', name])
        ref = volume.get('spec', {}).get('claimRef', {})
        if (ref.get('uid') != meta['uid'] or ref.get('name') != meta['name'] or ref.get('namespace') != namespace or
                not volume.get('metadata', {}).get('uid') or volume['spec'].get('persistentVolumeReclaimPolicy') != 'Delete'):
            raise ValueError('fixture volume binding or automatic reclaim policy changed')
        result.append({'name': name, 'uid': volume['metadata']['uid'], 'claim_uid': meta['uid']})
    return result


def wait_volume_cleanup(kube, volumes, namespace, timeout=180):
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        inventory = kube_json(kube, ['get', 'persistentvolumes']).get('items', [])
        if len(inventory) > 1024:
            raise ValueError('persistent volume inventory exceeds its bound')
        remaining = any(item.get('spec', {}).get('claimRef', {}).get('namespace') == namespace for item in inventory)
        for volume in volumes:
            current = kube_json(kube, ['get', 'persistentvolume', volume['name']], missing=True)
            if current is not None:
                if current.get('metadata', {}).get('uid') != volume['uid']:
                    raise ValueError('fixture volume name was reused; cleanup cannot be inferred')
                remaining = True
        if not remaining:
            return
        time.sleep(2)
    raise ValueError('owned persistent volumes were not reclaimed')


def install_legacy_identity(kube, database_id, shape):
    """Downgrade only the owned, recognized client projection while the API is stopped."""
    _, root = owned_clickhouse_root(kube, database_id)
    meta = root['metadata']
    templates = root.get('spec', {}).get('templates', {}).get('podTemplates', [])
    if len(templates) != 1 or templates[0].get('name') != 'managed':
        raise ValueError('fixture pod template is not the managed ClickHouse template')
    pod = templates[0]['spec']
    containers = pod.get('containers', [])
    if (len(containers) != 1 or containers[0].get('name') != 'clickhouse' or
            containers[0].get('image') != CLICKHOUSE_SERVER_IMAGE or pod.get('initContainers')):
        raise ValueError('fixture pod template identity changed')
    client = [v for v in pod.get('volumes', []) if v.get('name') == 'database-client-tls']
    mounts = [v for v in containers[0].get('volumeMounts', []) if v.get('name') == 'database-client-tls']
    if (len(client) != 1 or client[0].get('secret', {}).get('secretName') != 'database-client-tls' or
            len(mounts) != 1 or mounts[0].get('mountPath') != '/etc/hakopod-client-tls' or
            mounts[0].get('readOnly') is not True):
        raise ValueError('fixture client identity projection changed')
    configuration = root['spec']['configuration']['files']['zz-hakopod.xml']
    if '/etc/hakopod-client-tls/' not in configuration:
        raise ValueError('fixture client identity configuration changed')
    old_uids = sorted(item['metadata']['uid'] for item in kube_json(kube, [
        '-n', endpoint_namespace(database_id), 'get', 'pods', '-l',
        'clickhouse.altinity.com/chi=database'])['items'])
    pod['volumes'] = [v for v in pod['volumes'] if v.get('name') != 'database-client-tls']
    containers[0]['volumeMounts'] = [v for v in containers[0]['volumeMounts'] if v.get('name') != 'database-client-tls']
    patch = [{'op': 'test', 'path': '/metadata/uid', 'value': meta['uid']},
             {'op': 'test', 'path': '/metadata/resourceVersion', 'value': meta['resourceVersion']},
             {'op': 'replace', 'path': '/spec/templates/podTemplates', 'value': templates},
             {'op': 'replace', 'path': '/spec/configuration/files/zz-hakopod.xml',
              'value': configuration.replace('/etc/hakopod-client-tls/', '/etc/hakopod-tls/')}]
    command(kube + ['-n', endpoint_namespace(database_id), 'patch',
                    'clickhouseinstallations.clickhouse.altinity.com', 'database', '--type=json',
                    '--patch-file=/dev/stdin', '-o', 'name'], stdin=json.dumps(patch).encode(), maximum=1024)
    deadline = time.monotonic() + 900
    while time.monotonic() < deadline:
        pods = kube_json(kube, ['-n', endpoint_namespace(database_id), 'get', 'pods',
                               '-l', 'clickhouse.altinity.com/chi=database'])['items']
        if (len(pods) == shape['members'] and all(
                pod['metadata']['uid'] not in old_uids and not pod['metadata'].get('deletionTimestamp') and
                all(v.get('name') != 'database-client-tls' for v in pod['spec'].get('volumes', [])) and
                any(c.get('type') == 'Ready' and c.get('status') == 'True' for c in pod.get('status', {}).get('conditions', []))
                for pod in pods)):
            leaf = owned_secret(kube, database_id, 'database-client-tls')
            options = {'apiVersion': 'v1', 'kind': 'DeleteOptions', 'preconditions': {
                'uid': leaf['metadata']['uid'], 'resourceVersion': leaf['metadata']['resourceVersion']}}
            command(kube + ['delete', '--raw', '/api/v1/namespaces/' + endpoint_namespace(database_id) +
                            '/secrets/database-client-tls', '-f', '-'],
                    stdin=json.dumps(options).encode(), maximum=65536)
            return sorted(pod['metadata']['uid'] for pod in pods)
        time.sleep(3)
    raise ValueError('legacy ClickHouse projection did not roll out within its bound')


class DurableCheckpoints:
    """Delay scheduling in this run's control DB without changing operation state."""
    PHASES = ('accepted', 'identity', 'tls', 'publishing', 'observing')

    def __init__(self, psql, environment):
        self.psql, self.environment = psql, environment

    def sql(self, statement):
        return command([self.psql, '-X', '-qAt', '-v', 'ON_ERROR_STOP=1', '-c', statement],
                       env=self.environment, maximum=4096, timeout=20).decode().strip()

    def install(self, phase='accepted'):
        if phase not in self.PHASES:
            raise ValueError('unknown native checkpoint')
        self.sql("""CREATE TABLE acceptance_checkpoint (phase text NOT NULL);
INSERT INTO acceptance_checkpoint VALUES ('%s');
CREATE FUNCTION acceptance_schedule_checkpoint() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.status='queued' AND NEW.phase=(SELECT phase FROM acceptance_checkpoint)
    AND (TG_OP='INSERT' OR OLD.status='running' OR OLD.phase IS DISTINCT FROM NEW.phase) THEN
   NEW.next_attempt_at=clock_timestamp()+interval '20 minutes';
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER acceptance_schedule_checkpoint BEFORE INSERT OR UPDATE
ON managed_database_public_endpoint_operations FOR EACH ROW EXECUTE FUNCTION acceptance_schedule_checkpoint();""" % phase)

    def resume(self, operation, database_id, endpoint_id, phase, next_phase):
        if phase not in self.PHASES or next_phase not in (*self.PHASES, 'done'):
            raise ValueError('unknown native checkpoint')
        for value in (operation, database_id, endpoint_id):
            if not isinstance(value, str) or not re.fullmatch(r'[A-Za-z0-9_-]{1,100}', value):
                raise ValueError('checkpoint resource identity is invalid')
        statement = f"""BEGIN;
UPDATE acceptance_checkpoint SET phase='{next_phase}';
WITH resumed AS (UPDATE managed_database_public_endpoint_operations SET next_attempt_at=clock_timestamp()
 WHERE id='{operation}' AND database_id='{database_id}' AND endpoint_id='{endpoint_id}'
 AND status='queued' AND phase='{phase}' AND lease='' AND lease_until IS NULL RETURNING id)
SELECT count(*) FROM resumed;
COMMIT;"""
        if self.sql(statement) != '1':
            raise ValueError('native checkpoint no longer matches the real stored operation')

    def remove(self):
        self.sql('DROP TRIGGER acceptance_schedule_checkpoint ON managed_database_public_endpoint_operations; '
                 'DROP FUNCTION acceptance_schedule_checkpoint(); DROP TABLE acceptance_checkpoint;')


def wait_checkpoint(api, operation_id, phase, timeout=900):
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        operation = api.call('GET', '/database-public-endpoint-operations/' + operation_id)
        if operation.get('status') == 'queued' and operation.get('phase') == phase:
            return operation
        if operation.get('status') not in ('queued', 'running'):
            raise ValueError('publication finished before the required native checkpoint')
        time.sleep(.25)
    raise ValueError('publication did not reach its real native checkpoint')


def wait_pod(kube, namespace, name, timeout=120):
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        pod = kube_json(kube, ['-n', namespace, 'get', 'pod', name])
        if pod.get('status', {}).get('phase') == 'Running' and pod.get('status', {}).get('podIP'):
            return pod
        if pod.get('status', {}).get('phase') in ('Failed', 'Succeeded'):
            raise ValueError('probe pod terminated before acceptance')
        time.sleep(1)
    raise ValueError('probe pod did not become ready')


def endpoint_namespace(database_id):
    return 'hdb-' + database_id


def route_name(endpoint_id):
    return 'database-public-' + endpoint_id[:16]


def claim_name(port):
    return 'hakopod-tcp-port-' + str(port)


def validate_lane_grant(path):
    grant = strict_json(path, ('schema_version', 'cluster', 'purpose', 'approvals', 'expires_at', 'nonce'), 4096)
    expires = datetime.fromisoformat(grant['expires_at'].replace('Z', '+00:00'))
    if (grant['schema_version'] != 1 or grant['cluster'] != CONTEXT or
            grant['purpose'] != 'clickhouse-public-endpoint' or not isinstance(grant['approvals'], list) or
            len(grant['approvals']) != 2 or set(grant['approvals']) != {'root', 'vitess_backend'} or
            expires <= datetime.now(timezone.utc) or expires > datetime.now(timezone.utc) + timedelta(hours=8) or
            not re.fullmatch(r'[a-f0-9]{32}', grant['nonce'])):
        raise ValueError('a current root and Vitess lane grant is required')


def source_manifest_hash(source):
    encoded = json.dumps(source_hashes(source), sort_keys=True, separators=(',', ':')).encode()
    return hashlib.sha256(encoded).hexdigest()


def validate_preflight_plan(path, root, source, config_path, grant_path, binary, attempt):
    fields = ('schema_version', 'status', 'mutation_mode', 'generated_at', 'grant_expires_at', 'context',
              'attempt', 'source', 'source_manifest_sha256', 'config_sha256', 'host_budget_sha256', 'lane_grant_sha256',
              'server_binary_sha256', 'fault_proxy_binary_sha256', 'images', 'nodes', 'host_budget', 'haproxy', 'attempt_paths',
              'cleanup_contract', 'fixture', 'storage')
    plan = strict_json(path, fields, MAX_REPORT)
    now = datetime.now(timezone.utc)
    try:
        generated = datetime.fromisoformat(plan['generated_at'].replace('Z', '+00:00'))
        expires = datetime.fromisoformat(plan['grant_expires_at'].replace('Z', '+00:00'))
    except (AttributeError, TypeError, ValueError):
        raise ValueError('ClickHouse public endpoint preflight plan timestamps are invalid') from None
    stem = f'clickhouse-public-endpoint-v{attempt}'
    expected_paths = {'work': str(root / (stem + '.work')), 'events': str(root / (stem + '.jsonl')),
                      'evidence': str(root / (stem + '.evidence.json')), 'plan': str(root / (stem + '.plan.json'))}
    nodes = plan.get('nodes')
    valid_nodes = (isinstance(nodes, dict) and set(nodes) == set(NODES) and
                   all(isinstance(nodes[name], dict) and nodes[name].get('role') == role and
                       isinstance(nodes[name].get('cpus'), (int, float)) and not isinstance(nodes[name].get('cpus'), bool) and
                       nodes[name]['cpus'] > 0 and nodes[name].get('cpu_millis') == int(nodes[name]['cpus'] * 1000) and
                       isinstance(nodes[name].get('memory_bytes'), int) and nodes[name]['memory_bytes'] > 0 and
                       isinstance(nodes[name].get('cpuset'), list) and
                       re.fullmatch(r'sha256:[a-f0-9]{64}', nodes[name].get('image_id', '')) and
                       isinstance(nodes[name].get('internal_ip'), str) and nodes[name]['internal_ip']
                       for name, role in zip(NODES, ('server', 'agent'))))
    host = plan.get('host_budget')
    valid_host = valid_nodes and capacity.validate_plan_evidence(host, nodes)
    config = strict_json(config_path, CONFIG_FIELDS)
    shape = fixture.fixture(config['fixture_shape'])
    expected_peak = fixture.node_peak(config['fixture_shape'])
    storage = plan.get('storage')
    valid_storage = (isinstance(storage, dict) and set(storage) == set(NODES) and all(
        isinstance(item, dict) and item.get('required_free_bytes') == shape['scratch_free_bytes_required'] and
        isinstance(item.get('free_bytes_observed'), int) and item['free_bytes_observed'] >= item['required_free_bytes']
        for item in storage.values()))
    valid_peak = valid_nodes and plan.get('fixture') == shape and all(
        isinstance(nodes[name].get('allocatable'), dict) and
        isinstance(nodes[name].get('occupied_requests'), dict) and
        nodes[name].get('remaining_before_plan') == {
            key: nodes[name]['allocatable'][key] - nodes[name]['occupied_requests'][key]
            for key in ('cpu_millis', 'memory_bytes')} and
        nodes[name].get('planned_peak') == expected_peak and
        nodes[name].get('remaining_after_peak') == {
            key: nodes[name]['remaining_before_plan'][key] - expected_peak[key]
            for key in ('cpu_millis', 'memory_bytes')} and
        all(nodes[name]['remaining_after_peak'][key] >= 0 for key in ('cpu_millis', 'memory_bytes')) and
        nodes[name]['allocatable']['cpu_millis'] <= nodes[name]['cpu_millis'] and
        nodes[name]['allocatable']['memory_bytes'] <= nodes[name]['memory_bytes']
        for name in NODES)
    haproxy = plan.get('haproxy')
    installed = haproxy.get('installed') if isinstance(haproxy, dict) else None
    valid_haproxy = (isinstance(haproxy, dict) and haproxy.get('host_ports') == [15432] and
                     haproxy.get('node') == NODES[0] and
                     haproxy.get('chart_sha256') == HAPROXY_CHART_SHA256 and
                     re.fullmatch(r'[a-f0-9]{64}', haproxy.get('render_sha256', '')) and
                     isinstance(installed, dict) and
                     set(installed) == {'deployment_uid', 'configmap_uid', 'tcp_crd_uid'} and
                     all(isinstance(value, str) and value for value in installed.values()))
    config = strict_json(config_path, CONFIG_FIELDS)
    host_budget_path = Path(config['host_budget_file'])
    capacity.validate_budget(json.loads(protected(host_budget_path, 1024 * 1024)), CONTEXT)
    if (generated.tzinfo is None or expires.tzinfo is None or generated > now or generated < now - timedelta(minutes=15) or
            expires <= now or plan['schema_version'] != 1 or plan['status'] != 'ready' or
            plan['mutation_mode'] != 'read_only_preflight' or plan['context'] != CONTEXT or
            plan['attempt'] != attempt or plan['source'] != str(source) or plan['attempt_paths'] != expected_paths or
            Path(path).resolve() != Path(expected_paths['plan']).resolve() or
            plan['config_sha256'] != file_hash(config_path) or
            plan['host_budget_sha256'] != file_hash(host_budget_path, 1024 * 1024) or
            plan['lane_grant_sha256'] != file_hash(grant_path) or
            plan['server_binary_sha256'] != file_hash(binary) or
            plan['fault_proxy_binary_sha256'] != file_hash(Path(config['fault_proxy_binary'])) or
            plan['source_manifest_sha256'] != source_manifest_hash(source) or
            plan['images'] != {'haproxy': HAPROXY_IMAGE, 'clickhouse_server': CLICKHOUSE_SERVER_IMAGE,
                               'clickhouse_operator': CLICKHOUSE_OPERATOR_IMAGE, 'clickhouse_keeper': CLICKHOUSE_KEEPER_IMAGE,
                               'probe': config['probe_image']} or
            not valid_host or not valid_peak or not valid_haproxy or not valid_storage or
            not isinstance(plan.get('cleanup_contract'), list) or len(plan['cleanup_contract']) != 5 or
            not all(isinstance(item, str) for item in plan['cleanup_contract'])):
        raise ValueError('a fresh matching read-only ClickHouse public endpoint preflight plan is required')
    return plan


def reinspect_before_mutation(plan, root, source, config_path, grant_path, binary, attempt,
                              planner=None):
    """Reacquire all reviewed capacity and cluster facts at the mutation boundary."""
    planner = planner or load_planner_module()
    current = validate_preflight_plan(
        plan['attempt_paths']['plan'], root, source, config_path, grant_path, binary, attempt)
    if current != plan:
        raise ValueError('reviewed public endpoint plan changed before mutation')
    planner.validate_lane_grant(grant_path)
    config = planner.validate_config(config_path, root, source)
    docker_nodes, host_budget, host_budget_sha = planner.inspect_docker(config)
    kubernetes_nodes, installed_haproxy = planner.inspect_kubernetes(config, docker_nodes)
    storage = planner.inspect_storage(config, fixture.fixture(config['fixture_shape']))
    planner.check_port_available(config['public_address'], planner.PUBLIC_PORT)
    if shutil.disk_usage(root).free < fixture.fixture(config['fixture_shape'])['scratch_free_bytes_required']:
        raise ValueError('ClickHouse storage headroom changed before mutation')
    render_sha = planner.dry_render(config)
    nodes = {name: {**docker_nodes[name], **kubernetes_nodes[name]} for name in planner.NODES}
    if (nodes != plan['nodes'] or not planner.same_storage(plan['storage'], storage) or host_budget != plan['host_budget'] or
            host_budget_sha != plan['host_budget_sha256'] or
            installed_haproxy != plan['haproxy']['installed'] or
            render_sha != plan['haproxy']['render_sha256'] or
            file_hash(config_path) != plan['config_sha256'] or
            file_hash(Path(config['host_budget_file']), 1024 * 1024) != plan['host_budget_sha256'] or
            file_hash(grant_path) != plan['lane_grant_sha256'] or
            file_hash(binary) != plan['server_binary_sha256'] or
            file_hash(Path(config['fault_proxy_binary'])) != plan['fault_proxy_binary_sha256'] or
            source_manifest_hash(source) != plan['source_manifest_sha256']):
        raise ValueError('capacity or Kubernetes state changed after the reviewed preflight')
    return config


def postgres_environment(url, database=None):
    parsed = urllib.parse.urlsplit(url)
    if parsed.scheme not in ('postgres', 'postgresql') or not parsed.hostname or parsed.fragment:
        raise ValueError('PostgreSQL admin URL is invalid')
    query = urllib.parse.parse_qs(parsed.query, strict_parsing=True)
    if any(key not in ('sslmode', 'sslrootcert') or len(values) != 1 for key, values in query.items()):
        raise ValueError('PostgreSQL admin URL has unsupported connection options')
    env = {key: value for key, value in os.environ.items() if not key.startswith(('PG', 'HAKOPOD_', 'AWS_'))}
    env.update(PGHOST=parsed.hostname, PGPORT=str(parsed.port or 5432), PGUSER=urllib.parse.unquote(parsed.username or ''),
               PGPASSWORD=urllib.parse.unquote(parsed.password or ''), PGDATABASE=database or urllib.parse.unquote(parsed.path.lstrip('/') or 'postgres'),
               PGCONNECT_TIMEOUT='10')
    if query.get('sslmode'):
        env['PGSSLMODE'] = query['sslmode'][0]
    if query.get('sslrootcert'):
        env['PGSSLROOTCERT'] = query['sslrootcert'][0]
    if not env['PGUSER'] or not env['PGPASSWORD']:
        raise ValueError('PostgreSQL admin URL requires a user and password')
    return env


def server_database_url(admin_url, database):
    parsed = urllib.parse.urlsplit(admin_url)
    return urllib.parse.urlunsplit((parsed.scheme, parsed.netloc, '/' + database, parsed.query, ''))


def write_json_exclusive(path, value):
    encoded = json.dumps(value, indent=2, sort_keys=True).encode() + b'\n'
    if len(encoded) > MAX_REPORT:
        raise ValueError('report exceeds its bound')
    descriptor = os.open(path, os.O_CREAT | os.O_EXCL | os.O_WRONLY, 0o600)
    with os.fdopen(descriptor, 'wb') as output:
        output.write(encoded)


def create_probe_fixtures(kube, namespace, run_id, image, probe_command, nodes, password, ca, database_id):
    labels = {FIXTURE_LABEL: run_id, 'app.kubernetes.io/managed-by': 'hakopod-public-endpoint-acceptance'}
    namespace_object = {'apiVersion': 'v1', 'kind': 'Namespace', 'metadata': {'name': namespace, 'labels': dict(labels, **{'hakopod.io/database-access-' + database_id: 'true'})}}
    command(kube + ['create', '-f', '-'], stdin=json.dumps(namespace_object).encode())
    secret = {'apiVersion': 'v1', 'kind': 'Secret', 'metadata': {'name': 'database-access', 'namespace': namespace, 'labels': labels},
              'type': 'Opaque', 'data': {'password': base64.b64encode(password.encode()).decode(),
                                         'ca.crt': base64.b64encode(ca.encode()).decode()}}
    command(kube + ['create', '-f', '-'], stdin=json.dumps(secret).encode())
    for name, node in nodes.items():
        pod = {'apiVersion': 'v1', 'kind': 'Pod', 'metadata': {'name': name, 'namespace': namespace, 'labels': labels},
               'spec': {'restartPolicy': 'Never', 'automountServiceAccountToken': False, 'nodeName': node,
                        'hostNetwork': name != 'private', 'dnsPolicy': 'ClusterFirst' if name == 'private' else 'ClusterFirstWithHostNet', 'terminationGracePeriodSeconds': 1,
                        'securityContext': {'runAsNonRoot': True, 'runAsUser': 65532, 'runAsGroup': 65532,
                                            'fsGroup': 65532, 'seccompProfile': {'type': 'RuntimeDefault'}},
                        'containers': [{'name': 'probe', 'image': image, 'imagePullPolicy': 'IfNotPresent',
                                        'command': [probe_command], 'args': ['--mode', 'hold'],
                                        'env': [{'name': 'CLICKHOUSE_PASSWORD', 'valueFrom': {'secretKeyRef': {'name': 'database-access', 'key': 'password'}}}],
                                        'volumeMounts': [{'name': 'trust', 'mountPath': '/acceptance', 'readOnly': True},
                                                         {'name': 'temporary', 'mountPath': '/tmp'}],
                                        'resources': {'requests': {'cpu': '250m', 'memory': '256Mi'}, 'limits': {'cpu': '250m', 'memory': '256Mi'}},
                                        'securityContext': {'allowPrivilegeEscalation': False, 'readOnlyRootFilesystem': True,
                                                            'capabilities': {'drop': ['ALL']}}}],
                        'volumes': [{'name': 'trust', 'secret': {'secretName': 'database-access', 'defaultMode': 292}},
                                    {'name': 'temporary', 'emptyDir': {'sizeLimit': '16Mi'}}]}}
        command(kube + ['create', '-f', '-'], stdin=json.dumps(pod).encode())
    pods = {name: wait_pod(kube, namespace, name) for name in nodes}
    addresses = {name: pod['status']['podIP'] for name, pod in pods.items()}
    if (len(set(addresses.values())) != len(addresses) or
            any(not ipaddress.ip_address(value).is_private for value in addresses.values()) or
            any(pods[name]['status'].get('hostIP') != addresses[name] for name in ('allowed', 'denied'))):
        raise ValueError('host-network probes require distinct private node source addresses')
    return addresses


def inspect_claim(kube, proxy_namespace, port, database_id, endpoint_id):
    claim = kube_json(kube, ['-n', proxy_namespace, 'get', 'configmap', claim_name(port)], missing=True)
    if claim is None:
        return None
    labels, data = claim.get('metadata', {}).get('labels', {}), claim.get('data', {})
    if (labels.get('hakopod.io/database-public-endpoint-claim') != 'true' or
            labels.get('hakopod.io/database-id') != database_id or
            labels.get('hakopod.io/database-public-endpoint-id') != endpoint_id or
            data.get('database_id') != database_id or data.get('endpoint_id') != endpoint_id):
        raise ValueError('public endpoint claim ownership changed')
    return claim


def inspect_route(kube, database_id, endpoint_id, missing=False):
    return kube_json(kube, ['-n', endpoint_namespace(database_id), 'get',
                            'tcp.ingress.v3.haproxy.org', route_name(endpoint_id)], missing=missing)


def validate_route(route, endpoint, allowed_source):
    models = route.get('spec') if isinstance(route, dict) else None
    if not isinstance(models, list) or len(models) != 1 or not isinstance(models[0], dict):
        raise ValueError('public endpoint route model is missing or unbounded')
    model = models[0]
    frontend, service = model.get('frontend', {}), model.get('service', {})
    binds, acls, rules = frontend.get('binds', {}), frontend.get('acl_list', []), frontend.get('tcp_request_rule_list', [])
    expected_port = {'native': 9440, 'https': 8443}[endpoint['spec']['purpose']]
    if (service != {'name': 'database', 'port': expected_port} or frontend.get('maxconn') != endpoint['spec']['max_connections'] or
            binds.get('v4', {}).get('address') != '0.0.0.0' or binds.get('v4', {}).get('port') != endpoint['allocation']['port'] or
            acls != [{'acl_name': 'allowed_source', 'criterion': 'src', 'value': allowed_source + '/32'}] or
            rules != [{'type': 'connection', 'action': 'reject', 'cond': 'unless', 'cond_test': 'allowed_source'}]):
        raise ValueError('public endpoint route differs from the reviewed source, cap, port or backend')


def wait_retained(api, operation_id, claim, timeout=90, phase='closing'):
    deadline = time.monotonic() + timeout
    last = None
    while time.monotonic() < deadline:
        last = api.call('GET', '/database-public-endpoint-operations/' + operation_id)
        if last.get('status') == 'queued' and last.get('phase') == phase and claim() is not None:
            return last
        if last.get('status') not in ('queued', 'running'):
            raise ValueError('faulted revocation reached a terminal status')
        time.sleep(.5)
    raise ValueError('faulted revocation did not retain its claim')


def wait_interruption_point(api, operation_id, route, kind, timeout=90):
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        operation = api.call('GET', '/database-public-endpoint-operations/' + operation_id)
        current = route()
        models = None if current is None else current.get('spec')
        publishing = kind == 'publish' and operation.get('phase') == 'publishing' and isinstance(models, list) and models
        closing = kind == 'revoke' and operation.get('phase') == 'closing' and models == []
        if operation.get('status') in ('queued', 'running') and (publishing or closing):
            return operation, current
        if operation.get('status') not in ('queued', 'running'):
            raise ValueError('operation finished before its interruption point')
        time.sleep(.1)
    raise ValueError('operation did not expose its durable interruption point')


def probe(kube, namespace, pod, probe_command, endpoint, purpose, check, timeout=180, fingerprint=None, shape=None):
    args = kube + ['-n', namespace, 'exec', pod, '--', probe_command, '--host', endpoint['host'], '--port',
                   str(endpoint['port']), '--ca', '/acceptance/ca.crt', '--purpose', purpose, '--check', check,
                   '--expect-address', endpoint['address']]
    if shape is not None:
        args += ['--shards', str(shape['shards']), '--replicas', str(shape['replicas'] + 1)]
    if fingerprint is not None:
        if not re.fullmatch(r'[a-f0-9]{64}', fingerprint):
            raise ValueError('issued leaf fingerprint is invalid')
        args += ['--expect-fingerprint', fingerprint]
    output = command(args, maximum=1024, timeout=timeout).decode().strip()
    expected = 'PASS ' + check
    if output != expected:
        raise ValueError('negative probe returned unexpected evidence')
    return output


def start_revocation_session(kube, namespace, probe_command, endpoint, purpose, shape):
    args = kube + ['-n', namespace, 'exec', 'allowed', '--', probe_command, '--host', endpoint['host'], '--port',
                   str(endpoint['port']), '--ca', '/acceptance/ca.crt', '--purpose', purpose,
                   '--check', 'verified', '--expect-address', endpoint['address'], '--expect-revocation-within', '4m',
                   '--shards', str(shape['shards']), '--replicas', str(shape['replicas'] + 1)]
    process = subprocess.Popen(args, stdin=subprocess.DEVNULL, stdout=subprocess.PIPE,
                               stderr=subprocess.PIPE, start_new_session=True)
    line, remainder, stderr = capacity.bounded_read_line(process, maximum=1024, timeout=20)
    if not re.fullmatch(rb'READY tls=TLSv1\.[23] transport=(native|https)\n', line):
        stop_process(process)
        raise ValueError('revocation probe did not open a verified session')
    process.hakopod_stdout = remainder
    process.hakopod_stderr = stderr
    return process


def finish_revocation_session(process):
    try:
        output = capacity.bounded_finish(
            process, stdout=process.hakopod_stdout, stderr=process.hakopod_stderr,
            maximum=1024, timeout=260)
    except subprocess.TimeoutExpired:
        stop_process(process)
        raise ValueError('existing public endpoint session was not closed')
    if process.returncode != 0 or output != b'REVOKED existing ClickHouse session closed\n':
        raise ValueError('revocation probe returned unexpected closure evidence')


def stop_process(process):
    if process is None or process.poll() is not None:
        return
    try:
        os.killpg(process.pid, signal.SIGTERM)
    except ProcessLookupError:
        return
    try:
        process.wait(timeout=5)
    except subprocess.TimeoutExpired:
        os.killpg(process.pid, signal.SIGKILL)
        process.wait(timeout=5)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--root', type=Path, default=Path('/srv/hakopod-backup-scratch/database-cockpit-20260929'))
    parser.add_argument('--source', type=Path, required=True)
    parser.add_argument('--config', type=Path, required=True)
    parser.add_argument('--lane-grant', type=Path, required=True)
    parser.add_argument('--plan', type=Path, required=True)
    parser.add_argument('--attempt', type=int, required=True)
    args = parser.parse_args()
    root, source = args.root.resolve(), args.source.resolve()
    if (not source.is_relative_to(root) or source == root or args.attempt < 1 or args.attempt > 100 or
            'public-endpoint' not in source.name or platform.system() != 'Linux' or
            platform.machine() not in ('x86_64', 'amd64')):
        raise ValueError('use an isolated Linux amd64 development snapshot and attempt 1 through 100')
    gate = source / 'internal/database/public_endpoint.go'
    if not re.search(r'(?m)^const clickhousePublicEndpointsQualified = true\s*$', gate.read_text()):
        raise ValueError('ClickHouse public endpoint gate is closed; use a separately approved qualification snapshot')
    from cryptography import x509  # Preflight the fixture-only expiry helper before mutations.
    from cryptography.hazmat.primitives.asymmetric import ec
    if not hasattr(x509.Certificate, 'not_valid_before_utc'):
        raise ValueError('expiry qualification requires cryptography 42 or later')
    current_cgroup_limits()
    validate_lane_grant(args.lane_grant)
    config = strict_json(args.config, CONFIG_FIELDS)
    shape = fixture.fixture(config['fixture_shape'])
    if shutil.disk_usage(root).free < shape['scratch_free_bytes_required']:
        raise ValueError('scratch free space does not cover the admitted ClickHouse PVCs and 16 GiB reserve')
    if (config['schema_version'] != 1 or not isinstance(config['probe_image'], str) or
            not IMAGE.fullmatch(config['probe_image'])):
        raise ValueError('config requires schema version 1 and a digest-pinned probe image')
    if config['allowed_node'] not in NODES or config['denied_node'] not in NODES or config['allowed_node'] == config['denied_node']:
        raise ValueError('probe nodes must be the two distinct named development nodes')
    address = ipaddress.ip_address(config['public_address'])
    if (address.version != 4 or address.is_unspecified or address.is_multicast or
            not isinstance(config['public_port'], int) or isinstance(config['public_port'], bool) or
            not 1024 <= config['public_port'] <= 65535 or not valid_domain(config['public_domain']) or
            not valid_domain(config['app_domain'], 190) or not isinstance(config['probe_command'], str) or
            not config['probe_command'].startswith('/') or len(config['probe_command']) > 256):
        raise ValueError('public endpoint inventory is invalid')
    for name in ('psql', 'kubectl', 'server_binary', 'fault_proxy_binary'):
        path = Path(config[name]).resolve()
        if not path.is_file() or not os.access(path, os.X_OK):
            raise ValueError('configured development executable is unavailable')
        config[name] = str(path)
    binary = Path(config['server_binary'])
    if (not binary.is_relative_to(source) or
            not Path(config['fault_proxy_binary']).is_relative_to(source)):
        raise ValueError('owned server and fault proxy binaries must be inside the isolated source snapshot')
    plan = validate_preflight_plan(args.plan, root, source, args.config, args.lane_grant, binary, args.attempt)
    kubeconfig = Path(config['kubeconfig']).resolve()
    if kubeconfig.is_symlink() or not kubeconfig.is_file():
        raise ValueError('named development kubeconfig is unavailable')
    if (not isinstance(config['server_environment'], dict) or len(config['server_environment']) > 64 or
            any(not re.fullmatch(r'HAKOPOD_[A-Z0-9_]{1,80}', key) or not isinstance(value, str) or
                len(value) > 4096 or '\x00' in value for key, value in config['server_environment'].items())):
        raise ValueError('server_environment must contain bounded HAKOPOD settings')
    forbidden = {'HAKOPOD_DATABASE_URL', 'HAKOPOD_AUTH_ENCRYPTION_KEY', 'HAKOPOD_KUBECONFIG', 'HAKOPOD_LISTEN',
                 'HAKOPOD_WEB_ORIGIN', 'HAKOPOD_PUBLIC_TCP_PORTS', 'HAKOPOD_DATABASE_PUBLIC_ADDRESS',
                 'HAKOPOD_DATABASE_PUBLIC_DOMAIN', 'HAKOPOD_DATABASE_PUBLIC_PORTS', 'HAKOPOD_DATABASE_URL_FILE',
                 'HAKOPOD_AUTH_ENCRYPTION_KEY_FILE', 'HAKOPOD_CONFIG_FILE'}
    if forbidden.intersection(config['server_environment']):
        raise ValueError('runner-owned server settings cannot be overridden')
    kube = [config['kubectl'], '--kubeconfig', str(kubeconfig), '--context', CONTEXT]
    if command(kube + ['config', 'current-context'], maximum=128, timeout=10).decode().strip() != CONTEXT:
        raise ValueError('named development context is required')
    node_data = kube_json(kube, ['get', 'nodes', *NODES]).get('items', [])
    if len(node_data) != 2 or {node['metadata']['name'] for node in node_data} != set(NODES):
        raise ValueError('both named development nodes are required')
    for node in node_data:
        conditions = {item['type']: item['status'] for item in node['status'].get('conditions', [])}
        info = node['status']['nodeInfo']
        if (info.get('architecture') != 'amd64' or info.get('operatingSystem') != 'linux' or
                node.get('spec', {}).get('unschedulable') or conditions.get('Ready') != 'True' or
                any(conditions.get(name) != 'False' for name in ('DiskPressure', 'MemoryPressure', 'PIDPressure'))):
            raise ValueError('development nodes are not healthy enough for native acceptance')
    acceptance_lock = acquire_acceptance_lock(root)
    config = reinspect_before_mutation(
        plan, root, source, args.config, args.lane_grant, binary, args.attempt)
    kubeconfig = Path(config['kubeconfig'])
    kube = [config['kubectl'], '--kubeconfig', str(kubeconfig), '--context', CONTEXT]
    run_id = 'pe-' + secrets.token_hex(8)
    if not RUN_ID.fullmatch(run_id):
        raise AssertionError('invalid generated run ID')
    stem = f'clickhouse-public-endpoint-v{args.attempt}'
    work, event_path, report_path = root / (stem + '.work'), root / (stem + '.jsonl'), root / (stem + '.evidence.json')
    if any(path.exists() or path.is_symlink() for path in (work, event_path, report_path)):
        raise ValueError('acceptance attempt paths must be new')
    work.mkdir(mode=0o700)
    evidence = Evidence(event_path, run_id)
    before = source_hashes(source)
    admin_url = protected(Path(config['postgres_admin_url_file']), 8192).decode().strip()
    control_database = 'hakopod_' + run_id.replace('-', '_')
    admin_env = postgres_environment(admin_url, 'postgres')
    sql_identifier = '"' + control_database.replace('"', '""') + '"'
    server = None
    fault = None
    scoped_api = None
    database_id = None
    endpoint_ids = set()
    probe_namespace = 'hp-' + run_id
    session = None
    volumes = []
    created_control_database = False
    passed = False
    cleanup = []
    started = time.monotonic()
    try:
        # The generated name is unique to this run, so cleanup may safely issue
        # DROP DATABASE IF EXISTS even if CREATE loses its response.
        created_control_database = True
        command([config['psql'], '-X', '-v', 'ON_ERROR_STOP=1', '-d', 'postgres', '-c', 'CREATE DATABASE ' + sql_identifier],
                env=admin_env, maximum=4096, timeout=30)
        evidence.add('owned_control_database_created', database=control_database)
        api_port = free_port()
        origin = f'http://127.0.0.1:{api_port}'
        environment = {key: value for key, value in os.environ.items() if not key.startswith(('HAKOPOD_', 'AWS_', 'PG'))}
        environment.update(config['server_environment'])
        environment.update(HAKOPOD_DATABASE_URL=server_database_url(admin_url, control_database),
                           HAKOPOD_AUTH_ENCRYPTION_KEY=secrets.token_hex(32), HAKOPOD_DEPLOYMENT_MODE='self-hosted',
                           HAKOPOD_LISTEN=f'127.0.0.1:{api_port}', HAKOPOD_WEB_ORIGIN=origin,
                           HAKOPOD_APP_DOMAIN=config['app_domain'], HAKOPOD_PUBLIC_TCP_PORTS=str(config['public_port']),
                           HAKOPOD_DATABASE_PUBLIC_ADDRESS=config['public_address'], HAKOPOD_DATABASE_PUBLIC_DOMAIN=config['public_domain'],
                           HAKOPOD_DATABASE_PUBLIC_PORTS=str(config['public_port']), HAKOPOD_INGRESS_CLASS=config['ingress_class'],
                           HAKOPOD_HAPROXY_NAMESPACE=config['proxy_namespace'], HAKOPOD_HAPROXY_CONFIGMAP=config['proxy_configmap'],
                           HAKOPOD_HAPROXY_RELEASE=config['proxy_release'], HAKOPOD_BACKUP_STATE_DIR=str(work / 'backups'))
        if config['tls_issuer']:
            environment['HAKOPOD_TLS_ISSUER'] = config['tls_issuer']
        environment['PATH'] = str(Path(config['kubectl']).parent) + ':' + environment.get('PATH', '')
        write_json_exclusive(work / 'recovery-runtime.json', environment)
        admin_key_file = work / 'bootstrap-key'
        command([str(binary), 'bootstrap', '--name', 'public-endpoint-acceptance', '--key-file', str(admin_key_file)],
                env=dict(environment, HAKOPOD_KUBECONFIG=str(kubeconfig)), maximum=4096, timeout=60)
        admin_key = protected(admin_key_file, 4096).decode().strip()
        server = ServerProcess(binary, source, environment, origin)
        server.start(kubeconfig)
        admin_api = API(origin, admin_key, [admin_key])
        key_result = admin_api.call('POST', '/keys', {'name': run_id, 'project': 'demo', 'environment': 'development',
                                    'application': '', 'permissions': ['deployments:read', 'deployments:write'],
                                    'expires_at': (datetime.now(timezone.utc) + timedelta(hours=4)).isoformat()}, expected=201)
        scoped_key = key_result['key']
        scoped_api = API(origin, scoped_key, [admin_key, scoped_key])
        identity = scoped_api.call('GET', '/me')
        if (identity.get('admin') or identity.get('project') != 'demo' or identity.get('environment') != 'development' or
                set(identity.get('permissions', [])) != {'deployments:read', 'deployments:write'}):
            raise ValueError('runner did not obtain the required scoped bearer key')
        admin_key_file.unlink()
        evidence.add('scoped_api_ready', api_origin=origin)
        spec = fixture.database_spec(config['fixture_shape'], run_id, NODES)
        operation = scoped_api.call('POST', '/databases', {'project': 'demo', 'environment': 'development', 'spec': spec},
                                    expected=202, idem=run_id + '-database')
        database_id = operation['database_id']
        scoped_api.wait_operation('/database-operations/' + operation['id'], 25 * 60)
        observation = wait_clickhouse_observation(scoped_api, database_id, shape, lambda _: True)
        evidence.add('clickhouse_fixture_ready', database_id=database_id, shape=config['fixture_shape'],
                     members=shape['members'], keepers=shape['keepers'])
        volumes = owned_volume_inventory(kube, database_id, shape)
        inspect_public_network_policy(kube, database_id, config, False)
        before_migration = identity_snapshot(kube, database_id, shape)
        server.stop()
        legacy_uids = install_legacy_identity(kube, database_id, shape)
        server.start(kubeconfig)
        observation = wait_clickhouse_observation(scoped_api, database_id, shape,
            lambda o: all(m['uid'] not in legacy_uids for m in o['members']))
        migrated = identity_snapshot(kube, database_id, shape)
        require_internal_identity_unchanged(before_migration, migrated)
        private_dns = require_exact_client_dns_names(migrated, database_id, shape)
        migration_path = work / 'migration-identities.json'
        write_json_exclusive(migration_path, {'legacy': legacy_uids, 'current': sorted(m['uid'] for m in observation['members'])})
        evidence.add('legacy_client_identity_migrated', pod_identities_sha256=file_hash(migration_path),
                     members=shape['members'], client_fingerprint=migrated['client_fingerprint'], **private_dns)
        capabilities = scoped_api.call('GET', '/databases/' + database_id + '/public-endpoint-capabilities')
        if capabilities.get('engine') != 'clickhouse' or capabilities.get('available') is not True:
            raise ValueError('running server has not enabled the approved ClickHouse qualification capability')
        evidence.add('clickhouse_qualification_capability_verified')
        credentials = scoped_api.call('POST', '/databases/' + database_id + '/credentials', {}, expected=200,
                                      idem=run_id + '-credentials')
        trust = scoped_api.call('GET', '/databases/' + database_id + '/trust')
        password, ca = credentials['password'], trust['certificate_pem']
        scoped_api.sensitive.append(password.encode())
        addresses = create_probe_fixtures(kube, probe_namespace, run_id, config['probe_image'], config['probe_command'],
                                          {'allowed': config['allowed_node'], 'denied': config['denied_node'], 'private': config['allowed_node']}, password, ca, database_id)
        evidence.add('distinct_probe_sources_ready', allowed_source=addresses['allowed'], denied_source=addresses['denied'])

        for purpose, port in (('native', 9440), ('https', 8443)):
            service = kube_json(kube, ['-n', endpoint_namespace(database_id), 'get', 'service', 'database'])
            private_endpoint = {'host': 'database.' + endpoint_namespace(database_id) + '.svc.cluster.local', 'port': port,
                                'address': service['spec']['clusterIP']}
            probe(kube, probe_namespace, 'private', config['probe_command'], private_endpoint, purpose, 'verified', shape=shape)
        evidence.add('private_native_and_https_verified_after_migration')
        approved_members = sorted((m['name'], m['uid'], m.get('role', '')) for m in observation['members'])
        checkpoints = DurableCheckpoints(config['psql'], postgres_environment(admin_url, control_database))

        def publish(purpose, interrupt=False):
            internal_before = identity_snapshot(kube, database_id, shape)
            plan = scoped_api.call('POST', '/databases/' + database_id + '/public-endpoint-plan',
                                   {'purpose': purpose, 'source_cidrs': [addresses['allowed'] + '/32'], 'max_connections': 8})
            review = plan['plan']
            allocation = review['allocation']
            if (allocation['address'] != config['public_address'] or allocation['port'] != config['public_port'] or
                    allocation['host'] != f"database-{config['public_port']}.{config['public_domain']}"):
                raise ValueError('operator public endpoint allocation changed')
            if interrupt:
                checkpoints.install()
            accepted = scoped_api.call('POST', '/databases/' + database_id + '/public-endpoints',
                                       {'review_id': plan['id'], 'expected_database_revision': review['database_revision'],
                                        'expected_endpoint_revision': review['endpoint_revision']}, expected=202,
                                       idem=run_id + '-publish-' + secrets.token_hex(4))
            endpoint_ids.add(accepted['endpoint_id'])
            route = lambda: inspect_route(kube, database_id, accepted['endpoint_id'], True)
            if interrupt:
                for index, phase in enumerate(checkpoints.PHASES):
                    state = wait_checkpoint(scoped_api, accepted['id'], phase)
                    server.crash()
                    current = route()
                    if phase in ('accepted', 'identity', 'tls', 'publishing') and current is not None and current.get('spec'):
                        raise ValueError('route opened before the durable publication step')
                    if phase == 'observing' and (current is None or not current.get('spec') or
                            not current.get('metadata', {}).get('annotations', {}).get('hakopod.io/tcp-acknowledged')):
                        raise ValueError('final observation checkpoint lacks an actual acknowledged route')
                    next_phase = checkpoints.PHASES[index + 1] if index + 1 < len(checkpoints.PHASES) else 'done'
                    checkpoints.resume(accepted['id'], database_id, accepted['endpoint_id'], phase, next_phase)
                    evidence.add('publication_checkpoint_replayed', operation_id=accepted['id'], phase=state['phase'])
                    server.start(kubeconfig)
                checkpoints.remove()
            scoped_api.wait_operation('/database-public-endpoint-operations/' + accepted['id'], 900)
            items = scoped_api.call('GET', '/databases/' + database_id + '/public-endpoints')['items']
            endpoint = next((item for item in items if item['id'] == accepted['endpoint_id']), None)
            if endpoint is None or endpoint.get('status') != 'active' or not endpoint.get('observation', {}).get('configured'):
                raise ValueError('published endpoint did not become active and configured')
            claim = inspect_claim(kube, config['proxy_namespace'], allocation['port'], database_id, endpoint['id'])
            current = route()
            if (claim is None or claim.get('data', {}).get('route_state') != 'published' or
                    not current.get('metadata', {}).get('annotations', {}).get('hakopod.io/tcp-acknowledged')):
                raise ValueError('published route lacks durable claim or exact HAProxy acknowledgement')
            validate_route(current, endpoint, addresses['allowed'])
            fresh = scoped_api.call('GET', '/databases/' + database_id).get('observation', {})
            if sorted((m['name'], m['uid'], m.get('role', '')) for m in fresh.get('members', [])) != approved_members:
                raise ValueError('public certificate rollout changed reviewed ClickHouse member identity or role')
            internal_after = identity_snapshot(kube, database_id, shape)
            require_internal_identity_unchanged(internal_before, internal_after)
            public_dns = require_exact_client_dns_names(
                internal_after, database_id, shape, (allocation['host'],))
            evidence.add('clickhouse_member_topology_preserved', endpoint_id=endpoint['id'])
            inspect_public_network_policy(kube, database_id, config, True)
            evidence.add('endpoint_published', endpoint_id=endpoint['id'], purpose=purpose,
                         revision=endpoint['revision'], **public_dns)
            return endpoint

        def revoke(endpoint, fault_mode=None, interrupt=False, open_session=False):
            nonlocal fault, session
            endpoint_id, port = endpoint['id'], endpoint['allocation']['port']
            if open_session:
                session = start_revocation_session(kube, probe_namespace, config['probe_command'], endpoint['allocation'], endpoint['spec']['purpose'], shape)
                evidence.add('verified_session_open', endpoint_id=endpoint_id)
            if fault_mode:
                server.stop()
                fault_work = work / ('fault-' + fault_mode + '-' + secrets.token_hex(4))
                fault_work.mkdir(mode=0o700)
                fault = FaultProxy(config['fault_proxy_binary'], plan['fault_proxy_binary_sha256'],
                                   config['kubectl'], kubeconfig, CONTEXT, config['proxy_namespace'], fault_work)
                proxy_kubeconfig = fault.start(mode=fault_mode, database_namespace=endpoint_namespace(database_id))
                server.start(proxy_kubeconfig)
            accepted = scoped_api.call('DELETE', '/databases/' + database_id + '/public-endpoints/' + endpoint_id,
                                       {'expected_endpoint_revision': endpoint['revision']}, expected=202,
                                       idem=run_id + '-revoke-' + secrets.token_hex(4))
            claim = lambda: inspect_claim(kube, config['proxy_namespace'], port, database_id, endpoint_id)
            route = lambda: inspect_route(kube, database_id, endpoint_id, True)
            if fault_mode:
                wait_retained(scoped_api, accepted['id'], claim)
                evidence.add('fault_retained_allocation_claim', endpoint_id=endpoint_id, fault=fault_mode)
                server.stop()
                fault.stop()
                fault = None
                server.start(kubeconfig)
            elif interrupt:
                state, _ = wait_interruption_point(scoped_api, accepted['id'], route, 'revoke')
                if claim() is None:
                    raise ValueError('revocation released its claim before durable cleanup')
                server.crash()
                evidence.add('revoke_interrupted_after_route_close', operation_id=accepted['id'], phase=state['phase'])
                server.start(kubeconfig)
            scoped_api.wait_operation('/database-public-endpoint-operations/' + accepted['id'], 900)
            if route() is not None or claim() is not None:
                raise ValueError('successful revocation retained its route or allocation claim')
            if session is not None:
                finish_revocation_session(session)
                session = None
                evidence.add('existing_session_closed', endpoint_id=endpoint_id)
            inspect_public_network_policy(kube, database_id, config, False)
            private_dns = require_exact_client_dns_names(
                identity_snapshot(kube, database_id, shape), database_id, shape)
            evidence.add('endpoint_revoked', endpoint_id=endpoint_id, **private_dns)

        def stale_publication_closure(purpose):
            nonlocal fault, session, approved_members
            reviewed = scoped_api.call('POST', '/databases/' + database_id + '/public-endpoint-plan',
                {'purpose': purpose, 'source_cidrs': [addresses['allowed'] + '/32'], 'max_connections': 8})
            checkpoints.install('observing')
            accepted = scoped_api.call('POST', '/databases/' + database_id + '/public-endpoints',
                {'review_id': reviewed['id'], 'expected_database_revision': reviewed['plan']['database_revision'],
                 'expected_endpoint_revision': reviewed['plan']['endpoint_revision']}, expected=202,
                idem=run_id + '-stale-' + secrets.token_hex(4))
            endpoint_id = accepted['endpoint_id']
            endpoint_ids.add(endpoint_id)
            wait_checkpoint(scoped_api, accepted['id'], 'observing')
            route = inspect_route(kube, database_id, endpoint_id)
            if not route.get('metadata', {}).get('annotations', {}).get('hakopod.io/tcp-acknowledged'):
                raise ValueError('stale-publication test requires a real acknowledged route')
            before_loss = wait_clickhouse_observation(scoped_api, database_id, shape, lambda _: True)
            victim = before_loss['members'][0]
            delete_owned_clickhouse_pod(kube, database_id, victim)
            recovered = wait_clickhouse_observation(scoped_api, database_id, shape,
                lambda o: all(m['uid'] != victim['uid'] for m in o['members']))
            allocation = reviewed['plan']['allocation']
            session = start_revocation_session(kube, probe_namespace, config['probe_command'], allocation, purpose, shape)
            server.stop()
            fault_work = work / ('fault-stale-publication-' + secrets.token_hex(4))
            fault_work.mkdir(mode=0o700)
            fault = FaultProxy(config['fault_proxy_binary'], plan['fault_proxy_binary_sha256'],
                               config['kubectl'], kubeconfig, CONTEXT, config['proxy_namespace'], fault_work)
            proxy_kubeconfig = fault.start(mode='master_socket_unavailable',
                                          database_namespace=endpoint_namespace(database_id))
            checkpoints.resume(accepted['id'], database_id, endpoint_id, 'observing', 'done')
            server.start(proxy_kubeconfig)
            claim = lambda: inspect_claim(kube, config['proxy_namespace'], allocation['port'], database_id, endpoint_id)
            wait_retained(scoped_api, accepted['id'], claim, phase='identity_closing')
            server.crash()
            evidence.add('stale_publication_retained_identity_closing', transport=purpose, endpoint_id=endpoint_id,
                         removed_uid=victim['uid'])
            fault.stop()
            fault = None
            checkpoints.remove()
            server.start(kubeconfig)
            scoped_api.wait_operation('/database-public-endpoint-operations/' + accepted['id'], 900, wanted='failed')
            finish_revocation_session(session)
            session = None
            closed = inspect_route(kube, database_id, endpoint_id, True)
            if closed is not None and closed.get('spec') or claim() is None:
                raise ValueError('stale publication failed before acknowledged closure and reservation retention')
            approved_members = sorted((m['name'], m['uid'], m.get('role', '')) for m in recovered['members'])
            endpoints = scoped_api.call('GET', '/databases/' + database_id + '/public-endpoints')['items']
            failed = next(item for item in endpoints if item['id'] == endpoint_id)
            retained_dns = require_exact_client_dns_names(
                identity_snapshot(kube, database_id, shape), database_id, shape, (allocation['host'],))
            evidence.add('stale_publication_closed_identity_retained', transport=purpose,
                         endpoint_id=endpoint_id, **retained_dns)
            revoke(failed)
            evidence.add('stale_publication_closed_and_rejected', transport=purpose, endpoint_id=endpoint_id)

        for purpose in ('native', 'https'):
            endpoint = publish(purpose, interrupt=True)
            identity = identity_snapshot(kube, database_id, shape)
            for check in ('verified', 'crud', 'plaintext-rejected', 'wrong-hostname-rejected', 'wrong-ca-rejected', 'bad-password-rejected'):
                result = probe(kube, probe_namespace, 'allowed', config['probe_command'], endpoint['allocation'],
                               purpose, check, shape=shape, fingerprint=identity['client_fingerprint'])
                evidence.add('allowed_probe_passed', endpoint_id=endpoint['id'], transport=purpose, check_name=check)
            probe(kube, probe_namespace, 'allowed', config['probe_command'], endpoint['allocation'], purpose, 'verified', shape=shape)
            probe(kube, probe_namespace, 'denied', config['probe_command'], endpoint['allocation'], purpose, 'unreachable', shape=shape)
            probe(kube, probe_namespace, 'allowed', config['probe_command'], endpoint['allocation'], purpose, 'verified', shape=shape)
            evidence.add('denied_source_refused', endpoint_id=endpoint['id'], transport=purpose)
            before_renewal = wait_clickhouse_observation(scoped_api, database_id, shape, lambda _: True)
            previous_tls = before_renewal['tls']
            expire_owned_clickhouse_ca(kube, database_id)
            renewed = wait_clickhouse_observation(scoped_api, database_id, shape,
                lambda o: o['tls'].get('ca_fingerprint') != previous_tls.get('ca_fingerprint'))
            renewed_identity = identity_snapshot(kube, database_id, shape)
            if renewed_identity['client_fingerprint'] == identity['client_fingerprint']:
                raise ValueError('issuer renewal did not replace the client leaf')
            renewed_dns = require_exact_client_dns_names(
                renewed_identity, database_id, shape, (endpoint['allocation']['host'],))
            probe(kube, probe_namespace, 'allowed', config['probe_command'], endpoint['allocation'], purpose,
                  'data-preserved', fingerprint=renewed_identity['client_fingerprint'], shape=shape)
            evidence.add('same_key_renewal_with_original_ca_verified', transport=purpose,
                         before_fingerprint=identity['client_fingerprint'],
                         after_fingerprint=renewed_identity['client_fingerprint'], **renewed_dns)
            victim = renewed['members'][0]
            delete_owned_clickhouse_pod(kube, database_id, victim)
            recovered = wait_clickhouse_observation(scoped_api, database_id, shape,
                lambda o: all(m['uid'] != victim['uid'] for m in o['members']))
            probe(kube, probe_namespace, 'allowed', config['probe_command'], endpoint['allocation'], purpose, 'data-preserved', shape=shape)
            evidence.add('data_member_loss_and_data_verified', transport=purpose, removed_uid=victim['uid'])
            if shape['keepers']:
                victim = recovered['coordination']['members'][0]
                delete_owned_clickhouse_pod(kube, database_id, victim, keeper=True)
                recovered = wait_clickhouse_observation(scoped_api, database_id, shape,
                    lambda o: all(m['uid'] != victim['uid'] for m in o['coordination']['members']))
                probe(kube, probe_namespace, 'allowed', config['probe_command'], endpoint['allocation'], purpose, 'data-preserved', shape=shape)
                evidence.add('keeper_member_loss_and_data_verified', transport=purpose, removed_uid=victim['uid'])
            approved_members = sorted((m['name'], m['uid'], m.get('role', '')) for m in recovered['members'])
            before_removal = identity_snapshot(kube, database_id, shape)
            revoke(endpoint, fault_mode='master_socket_unavailable', open_session=True)
            after_removal = identity_snapshot(kube, database_id, shape)
            require_internal_identity_unchanged(before_removal, after_removal)
            if endpoint['allocation']['host'] in after_removal['client_names']:
                raise ValueError('revocation retained the public certificate hostname')
            probe(kube, probe_namespace, 'allowed', config['probe_command'], endpoint['allocation'], purpose, 'unreachable', shape=shape)
            retry = publish(purpose)
            revoke(retry, fault_mode='missing_tcp_crd', open_session=True)
            retry = publish(purpose)
            revoke(retry, interrupt=True, open_session=True)
            stale_publication_closure(purpose)
        passed = True
        evidence.add('fixture_checks_completed', database_id=database_id, probe_namespace=probe_namespace)
    finally:
        stop_process(session)
        endpoints_closed = not endpoint_ids
        # Supported revocation must acknowledge closure before a claim can be
        # released. On failure preserve the claim and database for investigation.
        if database_id and scoped_api is not None and server is not None:
            try:
                if fault is not None:
                    server.stop()
                    fault.stop()
                    fault = None
                if server.process is not None and server.process.poll() is not None:
                    server.stop()
                if server.process is None:
                    server.start(kubeconfig)
                items = scoped_api.call('GET', '/databases/' + database_id + '/public-endpoints')['items']
                if len(items) > len(endpoint_ids) or any(item['id'] not in endpoint_ids for item in items):
                    raise ValueError('fixture endpoint inventory changed')
                for endpoint in items:
                    if endpoint.get('status') == 'revoked':
                        continue
                    accepted = scoped_api.call('DELETE', '/databases/' + database_id + '/public-endpoints/' + endpoint['id'],
                        {'expected_endpoint_revision': endpoint['revision']}, expected=202,
                        idem=run_id + '-cleanup-' + secrets.token_hex(4))
                    scoped_api.wait_operation('/database-public-endpoint-operations/' + accepted['id'], 300)
                endpoints_closed = all(inspect_route(kube, database_id, endpoint_id, True) is None
                                       for endpoint_id in endpoint_ids)
                claim = kube_json(kube, ['-n', config['proxy_namespace'], 'get', 'configmap',
                                         claim_name(config['public_port'])], missing=True)
                if claim is not None:
                    endpoints_closed = False
                if not endpoints_closed:
                    raise ValueError('public closure is incomplete; preserving its reservation')
                cleanup.append('endpoint_routes_sessions_and_claims')
            except Exception:
                cleanup.append('endpoint_closure_incomplete')
                endpoints_closed = False
        if database_id and endpoints_closed and scoped_api is not None and server is not None:
            try:
                namespace = kube_json(kube, ['get', 'namespace', endpoint_namespace(database_id)], missing=True)
                if namespace is not None:
                    volumes = owned_volume_inventory(kube, database_id, shape, complete=False)
                    database = scoped_api.call('GET', '/databases/' + database_id)
                    deletion = scoped_api.call('DELETE', '/databases/' + database_id,
                        {'expected_revision': database['revision'], 'confirm_name': run_id}, expected=202,
                        idem=run_id + '-cleanup-database')
                    scoped_api.wait_operation('/database-operations/' + deletion['id'], 600)
                wait_volume_cleanup(kube, volumes, endpoint_namespace(database_id))
                if kube_json(kube, ['get', 'namespace', endpoint_namespace(database_id)], missing=True) is not None:
                    raise ValueError('database namespace cleanup is incomplete')
                cleanup.append('database_namespace_and_volumes')
            except Exception:
                cleanup.append('database_namespace_incomplete')
        if server is not None:
            server.stop()
        if fault is not None:
            fault.stop()
        try:
            namespace = kube_json(kube, ['get', 'namespace', probe_namespace], missing=True)
            if namespace is not None:
                if namespace.get('metadata', {}).get('labels', {}).get(FIXTURE_LABEL) != run_id:
                    raise ValueError('probe namespace ownership changed; cleanup refused')
                metadata = namespace['metadata']
                options = {'apiVersion': 'v1', 'kind': 'DeleteOptions',
                           'preconditions': {'uid': metadata['uid'], 'resourceVersion': metadata['resourceVersion']}}
                command(kube + ['delete', '--raw', '/api/v1/namespaces/' + probe_namespace, '-f', '-'],
                        stdin=json.dumps(options).encode(), maximum=65536, timeout=30)
                deadline = time.monotonic() + 120
                while kube_json(kube, ['get', 'namespace', probe_namespace], missing=True) is not None:
                    if time.monotonic() >= deadline:
                        raise ValueError('probe namespace cleanup did not finish')
                    time.sleep(2)
                cleanup.append('probe_namespace')
        except Exception:
            cleanup.append('probe_namespace_incomplete')
        if created_control_database and endpoints_closed and not any(item.endswith('_incomplete') for item in cleanup):
            try:
                command([config['psql'], '-X', '-v', 'ON_ERROR_STOP=1', '-d', 'postgres', '-c',
                         'DROP DATABASE IF EXISTS ' + sql_identifier + ' WITH (FORCE)'], env=admin_env, maximum=4096, timeout=30)
                cleanup.append('control_database')
                (work / 'recovery-runtime.json').unlink(missing_ok=True)
            except Exception:
                cleanup.append('control_database_incomplete')
        after = source_hashes(source)
        evidence.close()
        report = {'schema_version': 1, 'run_id': run_id, 'attempt': args.attempt, 'context': CONTEXT,
                  'platform': 'linux/amd64', 'engine': 'clickhouse', 'fixture': shape, 'qualification_only': True, 'source_files': before, 'source_files_after': after,
                  'server_binary_sha256': file_hash(binary),
                  'fault_proxy_binary_sha256': file_hash(Path(config['fault_proxy_binary'])), 'probe_image': config['probe_image'],
                  'event_log_sha256': file_hash(event_path), 'events': evidence.events,
                  'elapsed_seconds': round(time.monotonic() - started, 3), 'cleanup': cleanup,
                  'passed': passed and before == after and not any(item.endswith('_incomplete') for item in cleanup)}
        report['binaries_match_plan'] = (
            report['server_binary_sha256'] == plan['server_binary_sha256'] and
            report['fault_proxy_binary_sha256'] == plan['fault_proxy_binary_sha256'])
        report['passed'] = report['passed'] and report['binaries_match_plan']
        write_json_exclusive(report_path, report)
        passed = report['passed']
        print('ClickHouse public endpoint acceptance passed:', report['passed'], flush=True)
        print('Protected event log:', event_path, flush=True)
        print('Protected structural evidence:', report_path, flush=True)
        acceptance_lock.close()
    return 0 if passed else 1


if __name__ == '__main__':
    try:
        sys.exit(main())
    except Exception as error:
        print('ClickHouse public endpoint acceptance stopped:', type(error).__name__, file=sys.stderr, flush=True)
        sys.exit(1)
