#!/usr/bin/env python3
"""Exercise Oracle public endpoints with owned development fixtures.

Run this only inside the bounded development-VM systemd unit after the root
coordinator and Oracle validation lane owner have written a current grant. The
runner creates its own control database, API process, scoped key, managed
Oracle database and probe namespace. It never changes the shared HAProxy
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
        raise ValueError('shared public-endpoint fault proxy launcher is unavailable')
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


fault_proxy_module = load_fault_proxy_module()


def load_planner_module():
    path = Path(__file__).with_name('plan-development-oracle-public-endpoint-acceptance.py')
    spec = importlib.util.spec_from_file_location('oracle_public_endpoint_planner', path)
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
ORACLE_FREE_IMAGE = ('container-registry.oracle.com/database/free:23.26.3.0@sha256:'
                     'f988b0c04c4c386cd306a2a914c0d7a9702d83acc31b064a28ad8eb6278a8fba')
HAPROXY_CHART_SHA256 = 'cf1869175352e87866b2caf8c58c31fda32bf763d7e87734e229fba14ecee9ca'
CONFIG_FIELDS = (
    'schema_version', 'postgres_admin_url_file', 'psql', 'kubeconfig', 'kubectl',
    'qualification_binary', 'withdrawal_binary', 'fault_proxy_binary', 'qualification_approval_file',
    'withdrawal_approval_file',
    'probe_image', 'probe_command', 'app_domain', 'public_address', 'public_domain', 'public_port',
    'tls_issuer', 'ingress_class', 'proxy_namespace', 'proxy_configmap', 'proxy_release',
    'allowed_node', 'denied_node', 'server_environment', 'docker', 'helm', 'haproxy_chart',
    'host_budget_file', 'external_probe_command', 'external_probe_config_file',
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
    for directory in ('api', 'auth', 'cmd', 'internal', 'templates', 'examples/oracle-public-endpoint-acceptance',
                      'examples/public-endpoint-fault-proxy'):
        base = root / directory
        if not base.is_dir():
            raise ValueError('acceptance source snapshot is incomplete')
        for suffix in ('*.go', '*.sql', '*.sh', '*.py', '*.toml', '*.json'):
            files.update(base.rglob(suffix))
    files.update(root / name for name in (
        'go.mod', 'go.sum',
        'scripts/run-development-oracle-public-endpoint-acceptance.py',
        'scripts/plan-development-oracle-public-endpoint-acceptance.py',
        'scripts/development_public_endpoint_capacity.py',
        'scripts/development_public_endpoint_fault_proxy.py',
        'scripts/test-development-public-endpoint-fault-proxy.py',
        'scripts/test-development-oracle-public-endpoint-plan.py',
        'scripts/test-development-oracle-public-endpoint-acceptance.py',
        'examples/oracle-public-endpoint-acceptance/README.md',
        'examples/oracle-public-endpoint-acceptance/Dockerfile',
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
    def __init__(self, binary, source, environment, origin, expected_hash):
        self.binary, self.source, self.environment, self.origin = binary, source, environment, origin
        self.expected_hash = expected_hash
        self.process = None

    def start(self, kubeconfig):
        if self.process is not None:
            raise ValueError('owned server is already running')
        if file_hash(self.binary) != self.expected_hash:
            raise ValueError('approved server binary changed before restart')
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


def endpoint_namespace(database_id):
    return 'hdb-' + database_id


def route_name(endpoint_id):
    return 'database-public-' + endpoint_id[:16]


def claim_name(port):
    return 'hakopod-tcp-port-' + str(port)


def _owned(meta, database_id, namespace=None):
    return (isinstance(meta, dict) and not meta.get('deletionTimestamp') and meta.get('uid') and
            meta.get('resourceVersion') and meta.get('labels', {}).get('hakopod.io/database-id') == database_id and
            meta.get('labels', {}).get('app.kubernetes.io/managed-by') == 'hakopod' and
            (namespace is None or meta.get('namespace') == namespace))


def owned_oracle_workload(kube, database_id):
    namespace = endpoint_namespace(database_id)
    ns = kube_json(kube, ['get', 'namespace', namespace])
    ns_meta = ns.get('metadata', {})
    if not _owned(ns_meta, database_id):
        raise ValueError('Oracle fixture namespace ownership changed')
    statefulset = kube_json(kube, ['-n', namespace, 'get', 'statefulset', 'database'])
    meta, spec, status = statefulset.get('metadata', {}), statefulset.get('spec', {}), statefulset.get('status', {})
    containers = spec.get('template', {}).get('spec', {}).get('containers', [])
    if (not _owned(meta, database_id, namespace) or spec.get('replicas') != 1 or
            len(containers) != 1 or containers[0].get('name') != 'oracle' or
            containers[0].get('image') != ORACLE_FREE_IMAGE or
            status.get('currentReplicas') != 1 or status.get('readyReplicas') != 1 or
            status.get('observedGeneration') != meta.get('generation')):
        raise ValueError('Oracle singleton StatefulSet identity or image changed')
    pods = kube_json(kube, ['-n', namespace, 'get', 'pods', '-l',
                            f'hakopod.io/database-id={database_id},app.kubernetes.io/managed-by=hakopod'])['items']
    pods = [pod for pod in pods if any(container.get('name') == 'oracle' for container in pod.get('spec', {}).get('containers', []))]
    if len(pods) != 1:
        raise ValueError('Oracle fixture does not have one owned member')
    pod = pods[0]
    pod_meta = pod.get('metadata', {})
    ready = any(item.get('type') == 'Ready' and item.get('status') == 'True'
                for item in pod.get('status', {}).get('conditions', []))
    if (not _owned(pod_meta, database_id, namespace) or not ready or
            pod.get('spec', {}).get('containers', [{}])[0].get('image') != ORACLE_FREE_IMAGE):
        raise ValueError('Oracle member identity, readiness, or image changed')
    return ns, statefulset, pod


def owned_secret_hash(kube, database_id, name):
    namespace = endpoint_namespace(database_id)
    secret = kube_json(kube, ['-n', namespace, 'get', 'secret', name])
    if not _owned(secret.get('metadata', {}), database_id, namespace):
        raise ValueError('Oracle Secret ownership changed')
    data = secret.get('data', {})
    encoded = json.dumps(data, sort_keys=True, separators=(',', ':')).encode()
    if not data or len(encoded) > 128 * 1024:
        raise ValueError('Oracle Secret material is missing or oversized')
    return hashlib.sha256(encoded).hexdigest(), secret


def certificate_facts(secret):
    from cryptography import x509
    from cryptography.hazmat.primitives import hashes
    raw = base64.b64decode(secret.get('data', {}).get('tls.crt', ''), validate=True)
    certificate = x509.load_pem_x509_certificate(raw)
    names = certificate.extensions.get_extension_for_class(
        x509.SubjectAlternativeName).value.get_values_for_type(x509.DNSName)
    return certificate.fingerprint(hashes.SHA256()).hex(), names


def oracle_private_dns_names(database_id):
    namespace = endpoint_namespace(database_id)
    return ['database', 'database.' + namespace, 'database.' + namespace + '.svc',
            'database.' + namespace + '.svc.cluster.local']


def require_oracle_dns_names(snapshot, database_id, public_host=None):
    expected = oracle_private_dns_names(database_id)
    if public_host is not None:
        expected.append(public_host)
    if snapshot.get('dns_names') != expected:
        raise ValueError('Oracle leaf does not contain the exact ordered private and public DNS names')


def backing_fingerprint(volume):
    spec = volume.get('spec', {})
    identity = {key: spec.get(key) for key in ('csi', 'local', 'hostPath', 'nodeAffinity',
                                               'storageClassName', 'volumeMode') if spec.get(key) is not None}
    if not identity:
        raise ValueError('Oracle backing volume identity is unavailable')
    return hashlib.sha256(json.dumps(identity, sort_keys=True, separators=(',', ':')).encode()).hexdigest()


def owned_volume_inventory(kube, database_id, complete=True):
    namespace = endpoint_namespace(database_id)
    claims = kube_json(kube, ['-n', namespace, 'get', 'persistentvolumeclaims'])['items']
    if len(claims) > 2 or complete and len(claims) != 2:
        raise ValueError('Oracle requires exactly its data and backup claims')
    result = []
    for claim in claims:
        meta = claim.get('metadata', {})
        if not complete and claim.get('status', {}).get('phase') == 'Pending' and not claim.get('spec', {}).get('volumeName'):
            continue
        if (not _owned(meta, database_id, namespace) or claim.get('status', {}).get('phase') != 'Bound' or
                not re.fullmatch(r'(data|backup)-database-0', meta.get('name', ''))):
            raise ValueError('Oracle claim identity or binding changed')
        volume_name = claim.get('spec', {}).get('volumeName', '')
        volume = kube_json(kube, ['get', 'persistentvolume', volume_name])
        ref = volume.get('spec', {}).get('claimRef', {})
        volume_meta = volume.get('metadata', {})
        if (not volume_meta.get('uid') or ref.get('uid') != meta['uid'] or ref.get('name') != meta['name'] or
                ref.get('namespace') != namespace or volume.get('spec', {}).get('persistentVolumeReclaimPolicy') != 'Delete'):
            raise ValueError('Oracle PV claimRef or reclaim policy changed')
        result.append({'claim_name': meta['name'], 'claim_uid': meta['uid'], 'volume_name': volume_name,
                       'volume_uid': volume_meta['uid'], 'backing_fingerprint': backing_fingerprint(volume)})
    return sorted(result, key=lambda item: item['claim_name'])


def oracle_snapshot(kube, database_id):
    _, statefulset, pod = owned_oracle_workload(kube, database_id)
    ca_hash, ca = owned_secret_hash(kube, database_id, 'database-ca')
    tls_hash, tls = owned_secret_hash(kube, database_id, 'database-tls')
    app_hash, _ = owned_secret_hash(kube, database_id, 'database-credentials')
    leaf_fingerprint, names = certificate_facts(tls)
    from cryptography import x509
    from cryptography.hazmat.primitives import hashes
    ca_certificate = x509.load_pem_x509_certificate(base64.b64decode(ca['data']['ca.crt'], validate=True))
    return {'statefulset_uid': statefulset['metadata']['uid'],
            'statefulset_generation': statefulset['metadata']['generation'],
            'template_sha256': hashlib.sha256(json.dumps(statefulset['spec']['template'], sort_keys=True,
                                                        separators=(',', ':')).encode()).hexdigest(),
            'pod_name': pod['metadata']['name'], 'pod_uid': pod['metadata']['uid'],
            'volumes': owned_volume_inventory(kube, database_id), 'ca_secret_sha256': ca_hash,
            'tls_secret_sha256': tls_hash, 'credentials_secret_sha256': app_hash,
            'ca_fingerprint': ca_certificate.fingerprint(hashes.SHA256()).hex(),
            'leaf_fingerprint': leaf_fingerprint, 'dns_names': names}


def require_singleton_transition(before, after, *, ca_may_change=False):
    if before['statefulset_uid'] != after['statefulset_uid']:
        raise ValueError('Oracle StatefulSet UID changed during singleton replacement')
    if before['pod_uid'] == after['pod_uid']:
        raise ValueError('Oracle did not replace its singleton pod')
    if before['volumes'] != after['volumes'] or len(after['volumes']) != 2:
        raise ValueError('Oracle PVC, PV, claimRef, or backing identity changed')
    if before['credentials_secret_sha256'] != after['credentials_secret_sha256']:
        raise ValueError('Oracle APP credential Secret changed during identity work')
    if not ca_may_change and before['ca_fingerprint'] != after['ca_fingerprint']:
        raise ValueError('Oracle issuer changed during public identity transition')


def delete_owned_oracle_pod(kube, database_id, expected_uid):
    _, _, pod = owned_oracle_workload(kube, database_id)
    meta = pod['metadata']
    if meta['uid'] != expected_uid:
        raise ValueError('Oracle recovery target changed before deletion')
    options = {'apiVersion': 'v1', 'kind': 'DeleteOptions', 'preconditions': {
        'uid': meta['uid'], 'resourceVersion': meta['resourceVersion']}}
    command(kube + ['delete', '--raw', '/api/v1/namespaces/' + endpoint_namespace(database_id) +
                    '/pods/' + urllib.parse.quote(meta['name']), '-f', '-'],
            stdin=json.dumps(options).encode(), maximum=65536)


def wait_oracle_observation(api, database_id, predicate=lambda _: True, timeout=1200):
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        resource = api.call('GET', '/databases/' + database_id)
        observed = resource.get('observation', {})
        tls = observed.get('tls') or {}
        members = observed.get('members', [])
        if (resource.get('status') == 'ready' and observed.get('status') == 'ready' and
                tls.get('verified') and tls.get('plaintext_rejected') and len(members) == 1 and
                members[0].get('ready') and members[0].get('role') == 'primary' and
                observed.get('primary') == members[0].get('name') and predicate(observed)):
            return observed
        time.sleep(3)
    raise ValueError('Oracle fixture did not reach the required verified observation')


def wait_snapshot_pod_change(kube, database_id, old_uid, timeout=1200):
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        try:
            snapshot = oracle_snapshot(kube, database_id)
            if snapshot['pod_uid'] != old_uid:
                return snapshot
        except ValueError:
            pass
        time.sleep(.5)
    raise ValueError('Oracle singleton replacement was not observed')


def inspect_public_network_policy(kube, database_id, config, expected):
    policy = kube_json(kube, ['-n', endpoint_namespace(database_id), 'get', 'networkpolicy', 'database'])
    if not _owned(policy.get('metadata', {}), database_id, endpoint_namespace(database_id)):
        raise ValueError('Oracle NetworkPolicy ownership changed')
    base = {'from': [
        {'podSelector': {}},
        {'namespaceSelector': {'matchLabels': {'hakopod.io/database-access-' + database_id: 'true'}}},
    ], 'ports': [{'port': 2484, 'protocol': 'TCP'}]}
    ingress = [base]
    if expected:
        ingress.append({'from': [{'namespaceSelector': {'matchLabels': {
            'kubernetes.io/metadata.name': config['proxy_namespace']}},
            'podSelector': {'matchLabels': {'app.kubernetes.io/name': 'kubernetes-ingress',
                                            'app.kubernetes.io/instance': config['proxy_release']}}}],
                        'ports': [{'port': 2484, 'protocol': 'TCP'}]})
    if policy.get('spec', {}).get('ingress') != ingress:
        raise ValueError('Oracle ingress policy differs from the exact private and public lifecycle rules')
    return hashlib.sha256(json.dumps(policy.get('spec', {}), sort_keys=True,
                                    separators=(',', ':')).encode()).hexdigest()


def wait_volume_cleanup(kube, volumes, namespace, timeout=240):
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        inventory = kube_json(kube, ['get', 'persistentvolumes']).get('items', [])
        if len(inventory) > 1024:
            raise ValueError('persistent volume inventory exceeds its bound')
        remaining = any(item.get('spec', {}).get('claimRef', {}).get('namespace') == namespace for item in inventory)
        for volume in volumes:
            current = kube_json(kube, ['get', 'persistentvolume', volume['volume_name']], missing=True)
            if current is not None:
                if current.get('metadata', {}).get('uid') != volume['volume_uid']:
                    raise ValueError('Oracle PV name was reused; cleanup cannot be inferred')
                remaining = True
        if not remaining:
            return
        time.sleep(2)
    raise ValueError('Oracle data and backup volumes were not reclaimed')


def expire_owned_oracle_ca(kube, database_id):
    from cryptography import x509
    from cryptography.hazmat.primitives import hashes, serialization
    from cryptography.hazmat.primitives.asymmetric import ec
    namespace = endpoint_namespace(database_id)
    _, secret = owned_secret_hash(kube, database_id, 'database-ca')
    encoded = secret.get('data', {})
    certificate = x509.load_pem_x509_certificate(base64.b64decode(encoded['ca.crt'], validate=True))
    key = serialization.load_pem_private_key(base64.b64decode(encoded['ca.key'], validate=True), password=None)
    if (not isinstance(key, ec.EllipticCurvePrivateKey) or certificate.subject != certificate.issuer or
            key.public_key().public_numbers() != certificate.public_key().public_numbers()):
        raise ValueError('Oracle fixture issuer is invalid')
    now = datetime.now(timezone.utc)
    builder = (x509.CertificateBuilder().subject_name(certificate.subject).issuer_name(certificate.issuer)
               .public_key(certificate.public_key()).serial_number(certificate.serial_number)
               .not_valid_before(certificate.not_valid_before_utc).not_valid_after(now + timedelta(hours=24)))
    for extension in certificate.extensions:
        builder = builder.add_extension(extension.value, extension.critical)
    shortened = builder.sign(key, hashes.SHA256()).public_bytes(serialization.Encoding.PEM)
    meta = secret['metadata']
    patch = [{'op': 'test', 'path': '/metadata/uid', 'value': meta['uid']},
             {'op': 'test', 'path': '/metadata/resourceVersion', 'value': meta['resourceVersion']},
             {'op': 'replace', 'path': '/data/ca.crt',
              'value': base64.b64encode(shortened).decode()}]
    command(kube + ['-n', namespace, 'patch', 'secret', 'database-ca', '--type=json',
                    '--patch-file=/dev/stdin', '-o', 'name'], stdin=json.dumps(patch).encode(), maximum=1024)


class DurableCheckpoints:
    """Delay only scheduling; never write an operation phase or result."""
    PHASES = ('identity_issuing', 'identity_rolling', 'identity_converging', 'cancelling_identity', 'tls',
              'publishing', 'observing', 'closing', 'release')

    def __init__(self, psql, environment):
        self.psql, self.environment = psql, environment

    def sql(self, statement):
        return command([self.psql, '-X', '-qAt', '-v', 'ON_ERROR_STOP=1', '-c', statement],
                       env=self.environment, maximum=4096, timeout=20).decode().strip()

    def install(self, phase):
        if phase not in self.PHASES:
            raise ValueError('unknown Oracle checkpoint')
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

    def resume(self, operation, database_id, endpoint_id, phase, next_phase=None):
        if phase not in self.PHASES or next_phase is not None and next_phase not in self.PHASES:
            raise ValueError('unknown Oracle checkpoint')
        for value in (operation, database_id, endpoint_id):
            if not isinstance(value, str) or not re.fullmatch(r'[A-Za-z0-9_-]{1,100}', value):
                raise ValueError('checkpoint resource identity is invalid')
        armed = next_phase or 'disabled'
        statement = f"""BEGIN;
UPDATE acceptance_checkpoint SET phase='{armed}';
WITH resumed AS (UPDATE managed_database_public_endpoint_operations SET next_attempt_at=clock_timestamp()
 WHERE id='{operation}' AND database_id='{database_id}' AND endpoint_id='{endpoint_id}'
 AND status='queued' AND phase='{phase}' AND lease='' AND lease_until IS NULL RETURNING id)
SELECT count(*) FROM resumed;
COMMIT;"""
        if self.sql(statement) != '1':
            raise ValueError('checkpoint no longer matches the real stored operation')

    def marker(self, operation, phase):
        if phase not in self.PHASES or not isinstance(operation, str) or not re.fullmatch(
                r'[A-Za-z0-9_-]{1,100}', operation):
            raise ValueError('checkpoint resource identity is invalid')
        value = self.sql("SELECT revision::text||':'||extract(epoch FROM next_attempt_at)::text "
                         "FROM managed_database_public_endpoint_operations "
                         f"WHERE id='{operation}' AND status='queued' AND phase='{phase}'")
        if not re.fullmatch(r'[0-9]+:[0-9]+(?:\.[0-9]+)?', value):
            raise ValueError('durable checkpoint marker is unavailable')
        return value

    def remove(self):
        self.sql('DROP TRIGGER acceptance_schedule_checkpoint ON managed_database_public_endpoint_operations; '
                 'DROP FUNCTION acceptance_schedule_checkpoint(); DROP TABLE acceptance_checkpoint;')


class FixtureSQL:
    def __init__(self, psql, environment):
        self.psql, self.environment = psql, environment

    def sql(self, statement):
        return command([self.psql, '-X', '-qAt', '-v', 'ON_ERROR_STOP=1', '-c', statement],
                       env=self.environment, maximum=4096, timeout=20).decode().strip()

    @staticmethod
    def identity(value):
        if not isinstance(value, str) or not re.fullmatch(r'[A-Za-z0-9_-]{1,100}', value):
            raise ValueError('fixture SQL identity is invalid')
        return value

    def expire_review(self, review_id):
        review_id = self.identity(review_id)
        if self.sql("UPDATE managed_database_public_endpoint_reviews SET expires_at=clock_timestamp()-interval '1 second' "
                    f"WHERE id='{review_id}' AND consumed_at IS NULL RETURNING id") != review_id:
            raise ValueError('stale-review fixture did not match one review')

    def hold_maintenance(self, database_id):
        database_id = self.identity(database_id)
        if self.sql("UPDATE managed_databases SET maintenance_lease='oracle-acceptance', "
                    "maintenance_lease_until=clock_timestamp()+interval '5 minutes' "
                    f"WHERE id='{database_id}' AND deleted_at IS NULL RETURNING id") != database_id:
            raise ValueError('maintenance fixture did not match the Oracle database')

    def release_maintenance(self, database_id):
        database_id = self.identity(database_id)
        if self.sql("UPDATE managed_databases SET maintenance_lease='',maintenance_lease_until=NULL "
                    f"WHERE id='{database_id}' AND maintenance_lease='oracle-acceptance' RETURNING id") != database_id:
            raise ValueError('maintenance fixture ownership changed')

    def mask_endpoint_purpose(self, endpoint_id, purpose):
        endpoint_id, purpose = self.identity(endpoint_id), self.identity(purpose)
        if self.sql("UPDATE managed_database_public_endpoints SET spec=jsonb_set(spec,'{purpose}',"
                    f"'\"{purpose}\"'::jsonb,false) WHERE id='{endpoint_id}' AND status='active' "
                    "AND revoked_at IS NULL RETURNING id") != endpoint_id:
            raise ValueError('allocation-conflict fixture did not match the active endpoint')


def wait_checkpoint(api, operation_id, phase, timeout=1200, gate=None, after_marker=None, terminal=()):
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        operation = api.call('GET', '/database-public-endpoint-operations/' + operation_id)
        if operation.get('status') == 'queued' and operation.get('phase') == phase:
            if after_marker is None or gate is not None and gate.marker(operation_id, phase) != after_marker:
                return operation
        if operation.get('status') in terminal:
            return operation
        if operation.get('status') not in ('queued', 'running'):
            raise ValueError('operation finished before the required Oracle checkpoint')
        time.sleep(.25)
    raise ValueError('operation did not reach its real Oracle checkpoint')


def wait_terminal(api, operation_id, wanted, timeout=1800):
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        operation = api.call('GET', '/database-public-endpoint-operations/' + operation_id)
        if operation.get('status') not in ('queued', 'running'):
            if operation.get('status') not in wanted:
                raise ValueError('Oracle endpoint operation reached an unexpected terminal status')
            return operation
        time.sleep(1)
    raise ValueError('Oracle endpoint operation exceeded its bounded wait')


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


def validate_lane_grant(path):
    grant = strict_json(path, ('schema_version', 'cluster', 'purpose', 'approvals', 'expires_at', 'nonce'), 4096)
    expires = datetime.fromisoformat(grant['expires_at'].replace('Z', '+00:00'))
    if (grant['schema_version'] != 1 or grant['cluster'] != CONTEXT or
            grant['purpose'] != 'oracle-free-public-tcps' or not isinstance(grant['approvals'], list) or
            len(grant['approvals']) != 2 or set(grant['approvals']) != {'root', 'oracle_transition_review'} or
            expires <= datetime.now(timezone.utc) or expires > datetime.now(timezone.utc) + timedelta(hours=8) or
            not re.fullmatch(r'[a-f0-9]{32}', grant['nonce'])):
        raise ValueError('a current root and Oracle validation lane grant is required')


def source_manifest_hash(source):
    return hashlib.sha256(json.dumps(source_hashes(source), sort_keys=True,
                                    separators=(',', ':')).encode()).hexdigest()


def validate_preflight_plan(path, root, source, config_path, grant_path, qualification_binary,
                            withdrawal_binary, attempt):
    fields = ('schema_version', 'status', 'mutation_mode', 'generated_at', 'grant_expires_at', 'context',
              'attempt', 'source', 'source_manifest_sha256', 'config_sha256', 'host_budget_sha256',
              'lane_grant_sha256', 'qualification_binary_sha256', 'withdrawal_binary_sha256',
              'fault_proxy_binary_sha256',
              'external_probe_command_sha256', 'external_probe_config_sha256',
              'external_probe_attestation_sha256', 'binary_approvals', 'images', 'nodes', 'host_budget', 'fixture', 'storage',
              'external_probe', 'haproxy', 'attempt_paths', 'cleanup_contract')
    plan = strict_json(path, fields, MAX_REPORT)
    now = datetime.now(timezone.utc)
    try:
        generated = datetime.fromisoformat(plan['generated_at'].replace('Z', '+00:00'))
        expires = datetime.fromisoformat(plan['grant_expires_at'].replace('Z', '+00:00'))
        external_observed = datetime.fromisoformat(plan['external_probe']['inventory_observed_at'].replace('Z', '+00:00'))
        external_expires = datetime.fromisoformat(plan['external_probe']['expires_at'].replace('Z', '+00:00'))
    except (AttributeError, TypeError, ValueError, KeyError):
        raise ValueError('Oracle preflight timestamps are invalid') from None
    stem = f'oracle-public-endpoint-v{attempt}'
    expected_paths = {'work': str(root / (stem + '.work')), 'events': str(root / (stem + '.jsonl')),
                      'evidence': str(root / (stem + '.evidence.json')), 'plan': str(root / (stem + '.plan.json'))}
    config = strict_json(config_path, CONFIG_FIELDS)
    planner = load_planner_module()
    expected_shape = planner.oracle_shape(config['allowed_node'])
    expected_peak = planner.plan_oracle_peak(plan.get('nodes', {}), config['allowed_node'])
    nodes = plan.get('nodes', {})
    valid_nodes = (set(nodes) == set(NODES) and all(
        nodes[name].get('planned_peak') == expected_peak[name] and
        nodes[name].get('remaining_after_peak') == {
            key: nodes[name]['remaining_before_plan'][key] - expected_peak[name][key]
            for key in ('cpu_millis', 'memory_bytes')} and
        all(nodes[name]['remaining_after_peak'][key] >= 0 for key in ('cpu_millis', 'memory_bytes'))
        for name in NODES))
    storage = plan.get('storage', {})
    valid_storage = (set(storage) == set(NODES) and all(
        item.get('free_bytes_observed', -1) >= item.get('required_free_bytes', 0) > 0
        for item in storage.values()))
    valid_host = valid_nodes and capacity.validate_plan_evidence(plan.get('host_budget'), nodes)
    haproxy = plan.get('haproxy', {})
    installed = haproxy.get('installed', {})
    valid_haproxy = (haproxy.get('host_ports') == [15432] and haproxy.get('node') == NODES[0] and
                     haproxy.get('chart_sha256') == HAPROXY_CHART_SHA256 and
                     re.fullmatch(r'[a-f0-9]{64}', haproxy.get('render_sha256', '')) and
                     set(installed) == {'deployment_uid', 'configmap_uid', 'tcp_crd_uid'} and
                     all(isinstance(value, str) and value for value in installed.values()))
    external = plan.get('external_probe', {})
    valid_external = (external.get('outside_development_cluster') is True and
                      external_observed >= now - timedelta(minutes=15) and external_observed <= now and
                      external_expires > now and external_expires <= now + timedelta(hours=2))
    host_budget_path = Path(config['host_budget_file'])
    capacity.validate_budget(json.loads(protected(host_budget_path, 1024 * 1024)), CONTEXT)
    approvals = planner.validate_binary_approvals(config, plan['source_manifest_sha256'], now)
    if (generated.tzinfo is None or expires.tzinfo is None or generated > now or generated < now - timedelta(minutes=15) or
            expires <= now or plan['schema_version'] != 1 or plan['status'] != 'ready' or
            plan['mutation_mode'] != 'read_only_preflight' or plan['context'] != CONTEXT or
            plan['attempt'] != attempt or plan['source'] != str(source) or plan['attempt_paths'] != expected_paths or
            Path(path).resolve() != Path(expected_paths['plan']).resolve() or
            plan['config_sha256'] != file_hash(config_path) or plan['host_budget_sha256'] != file_hash(host_budget_path, 1024 * 1024) or
            plan['lane_grant_sha256'] != file_hash(grant_path) or
            plan['qualification_binary_sha256'] != file_hash(qualification_binary) or
            plan['withdrawal_binary_sha256'] != file_hash(withdrawal_binary) or
            plan['fault_proxy_binary_sha256'] != file_hash(Path(config['fault_proxy_binary'])) or
            plan['external_probe_command_sha256'] != file_hash(Path(config['external_probe_command'])) or
            plan['external_probe_config_sha256'] != file_hash(Path(config['external_probe_config_file'])) or
            plan['source_manifest_sha256'] != source_manifest_hash(source) or
            plan.get('binary_approvals') != approvals or
            plan['images'] != {'haproxy': HAPROXY_IMAGE, 'oracle_free': ORACLE_FREE_IMAGE,
                               'probe': config['probe_image']} or plan.get('fixture') != expected_shape or
            not valid_host or not valid_storage or not valid_haproxy or not valid_external or
            not isinstance(plan.get('cleanup_contract'), list) or len(plan['cleanup_contract']) != 6):
        raise ValueError('a fresh matching read-only Oracle public endpoint preflight plan is required')
    return plan


def reinspect_before_mutation(plan, root, source, config_path, grant_path, qualification_binary,
                              withdrawal_binary, attempt, planner=None):
    planner = planner or load_planner_module()
    current = validate_preflight_plan(plan['attempt_paths']['plan'], root, source, config_path, grant_path,
                                      qualification_binary, withdrawal_binary, attempt)
    if current != plan:
        raise ValueError('reviewed Oracle plan changed before mutation')
    planner.validate_lane_grant(grant_path)
    config = planner.validate_config(config_path, root, source)
    docker_nodes, host_budget, host_budget_sha = planner.inspect_docker(config)
    kubernetes_nodes, installed = planner.inspect_kubernetes(config, docker_nodes)
    shape = planner.oracle_shape(config['allowed_node'])
    storage = planner.inspect_storage(config, shape)
    external, external_sha = planner.inspect_external_provider(config)
    planner.check_port_available(config['public_address'], planner.PUBLIC_PORT)
    render_sha = planner.dry_render(config)
    nodes = {name: {**docker_nodes[name], **kubernetes_nodes[name]} for name in planner.NODES}
    if (nodes != plan['nodes'] or not planner.same_storage(plan['storage'], storage) or
            host_budget != plan['host_budget'] or host_budget_sha != plan['host_budget_sha256'] or
            installed != plan['haproxy']['installed'] or render_sha != plan['haproxy']['render_sha256'] or
            external != plan['external_probe'] or external_sha != plan['external_probe_attestation_sha256'] or
            file_hash(config_path) != plan['config_sha256'] or file_hash(grant_path) != plan['lane_grant_sha256'] or
            file_hash(qualification_binary) != plan['qualification_binary_sha256'] or
            file_hash(withdrawal_binary) != plan['withdrawal_binary_sha256'] or
            file_hash(Path(config['fault_proxy_binary'])) != plan['fault_proxy_binary_sha256'] or
            source_manifest_hash(source) != plan['source_manifest_sha256']):
        raise ValueError('capacity, inventory, provider, or source state changed after preflight')
    return config


def postgres_environment(url, database=None):
    parsed = urllib.parse.urlsplit(url)
    if parsed.scheme not in ('postgres', 'postgresql') or not parsed.hostname or parsed.fragment:
        raise ValueError('PostgreSQL admin URL is invalid')
    query = urllib.parse.parse_qs(parsed.query, strict_parsing=True)
    if any(key not in ('sslmode', 'sslrootcert') or len(values) != 1 for key, values in query.items()):
        raise ValueError('PostgreSQL admin URL has unsupported options')
    env = {key: value for key, value in os.environ.items() if not key.startswith(('PG', 'HAKOPOD_', 'AWS_'))}
    env.update(PGHOST=parsed.hostname, PGPORT=str(parsed.port or 5432),
               PGUSER=urllib.parse.unquote(parsed.username or ''),
               PGPASSWORD=urllib.parse.unquote(parsed.password or ''),
               PGDATABASE=database or urllib.parse.unquote(parsed.path.lstrip('/') or 'postgres'),
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
    ns = {'apiVersion': 'v1', 'kind': 'Namespace', 'metadata': {'name': namespace, 'labels': labels}}
    command(kube + ['create', '-f', '-'], stdin=json.dumps(ns).encode())
    secret = {'apiVersion': 'v1', 'kind': 'Secret', 'metadata': {'name': 'database-access', 'namespace': namespace,
              'labels': labels}, 'type': 'Opaque', 'data': {
              'password': base64.b64encode(password.encode()).decode(),
              'ca.crt': base64.b64encode(ca.encode()).decode()}}
    command(kube + ['create', '-f', '-'], stdin=json.dumps(secret).encode())
    for name, node in nodes.items():
        pod = {'apiVersion': 'v1', 'kind': 'Pod', 'metadata': {'name': name, 'namespace': namespace, 'labels': labels},
               'spec': {'restartPolicy': 'Never', 'automountServiceAccountToken': False, 'nodeName': node,
                        'hostNetwork': True, 'dnsPolicy': 'ClusterFirstWithHostNet', 'terminationGracePeriodSeconds': 1,
                        'securityContext': {'runAsNonRoot': True, 'runAsUser': 65532, 'runAsGroup': 65532,
                                            'fsGroup': 65532, 'seccompProfile': {'type': 'RuntimeDefault'}},
                        'containers': [{'name': 'probe', 'image': image, 'imagePullPolicy': 'IfNotPresent',
                                       'command': [probe_command], 'args': ['--mode', 'hold'],
                                       'env': [{'name': 'ORACLE_PASSWORD', 'valueFrom': {'secretKeyRef': {
                                           'name': 'database-access', 'key': 'password'}}}],
                                       'volumeMounts': [{'name': 'trust', 'mountPath': '/acceptance', 'readOnly': True},
                                                        {'name': 'temporary', 'mountPath': '/tmp'}],
                                       'resources': {'requests': {'cpu': '250m', 'memory': '256Mi'},
                                                     'limits': {'cpu': '250m', 'memory': '256Mi'}},
                                       'securityContext': {'allowPrivilegeEscalation': False,
                                                           'readOnlyRootFilesystem': True,
                                                           'capabilities': {'drop': ['ALL']}}}],
                        'volumes': [{'name': 'trust', 'secret': {'secretName': 'database-access', 'defaultMode': 292}},
                                    {'name': 'temporary', 'emptyDir': {'sizeLimit': '16Mi'}}]}}
        command(kube + ['create', '-f', '-'], stdin=json.dumps(pod).encode())
    pods = {name: wait_pod(kube, namespace, name) for name in nodes}
    addresses = {name: pod['status']['podIP'] for name, pod in pods.items()}
    if (len(set(addresses.values())) != len(addresses) or
            any(not ipaddress.ip_address(value).is_private for value in addresses.values()) or
            any(pods[name]['status'].get('hostIP') != addresses[name] for name in nodes)):
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
            data.get('database_id') != database_id or data.get('endpoint_id') != endpoint_id or
            data.get('port') != str(port)):
        raise ValueError('public TCP allocation claim ownership changed')
    return claim


def inspect_route(kube, database_id, endpoint_id, missing=False):
    return kube_json(kube, ['-n', endpoint_namespace(database_id), 'get',
                            'tcps.ingress.v3.haproxy.org', route_name(endpoint_id)], missing=missing)


def inspect_route_reload_retention(fault, kube, proxy_namespace, port, database_id, endpoint_id):
    status = fault.status()
    if not status['route_mutation_seen']:
        return None
    if (status['successful_route_mutations'] < 1 or status['pre_mutation_proxy_upgrade_relays'] < 1 or
            status['pre_mutation_database_upgrade_relays'] < 1):
        raise ValueError('route fault did not prove both pre-mutation Kubernetes exec upgrades')
    route = inspect_route(kube, database_id, endpoint_id, True)
    if route is None or 'hakopod.io/tcp-acknowledged' in route.get('metadata', {}).get('annotations', {}):
        return None
    return inspect_claim(kube, proxy_namespace, port, database_id, endpoint_id)


def require_operator_allocation_conflict(result):
    expected = 'revision or idempotency conflict: every operator endpoint allocation is reserved'
    error = result.get('error', {}) if isinstance(result, dict) else {}
    if error.get('code') != 'conflict' or error.get('message') != expected:
        raise ValueError('competing allocation did not exhaust the exact operator endpoint allocation')
    return expected


def validate_route(route, endpoint, source_cidrs):
    specs = route.get('spec')
    if not isinstance(specs, list) or len(specs) != 1:
        raise ValueError('Oracle HAProxy route shape changed')
    item = specs[0]
    frontend = item.get('frontend', {})
    if (item.get('service') != {'name': 'database', 'port': 2484} or
            frontend.get('maxconn') != endpoint['spec']['max_connections'] or
            frontend.get('binds', {}).get('v4') != {'address': '0.0.0.0', 'port': endpoint['allocation']['port']} or
            frontend.get('acl_list') != [{'acl_name': 'allowed_source', 'criterion': 'src',
                                          'value': ' '.join(source_cidrs)}] or
            frontend.get('tcp_request_rule_list') != [{'type': 'connection', 'action': 'reject',
                                                       'cond': 'unless', 'cond_test': 'allowed_source'}]):
        raise ValueError('Oracle HAProxy route differs from the reviewed direct TCPS route')


def wait_retained(api, operation_id, claim, phase, timeout=120):
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        state = api.call('GET', '/database-public-endpoint-operations/' + operation_id)
        if state.get('status') == 'queued' and state.get('phase') == phase and claim() is not None:
            return state
        if state.get('status') not in ('queued', 'running'):
            raise ValueError('fault did not retain the Oracle allocation')
        time.sleep(.5)
    raise ValueError('Oracle allocation retention was not observed')


def probe(kube, namespace, pod, probe_command, endpoint, check, timeout=180, fingerprint=None):
    args = [probe_command, '--mode', 'probe', '--check', check, '--host', endpoint['host'],
            '--expect-address', endpoint['address'], '--port', str(endpoint['port']),
            '--ca', '/acceptance/ca.crt']
    if fingerprint:
        args += ['--expect-fingerprint', fingerprint]
    raw = command(kube + ['-n', namespace, 'exec', pod, '--', *args], maximum=4096, timeout=timeout)
    expected = ('PASS ' + check).encode()
    if expected not in raw or b'ORACLE_PASSWORD' in raw:
        raise ValueError('Oracle probe did not return its exact success marker')
    return raw.decode().strip()


def start_revocation_session(kube, namespace, probe_command, endpoint):
    args = kube + ['-n', namespace, 'exec', 'allowed', '--', probe_command, '--mode', 'hold',
                   '--check', 'verified', '--host', endpoint['host'], '--expect-address', endpoint['address'],
                   '--port', str(endpoint['port']), '--ca', '/acceptance/ca.crt',
                   '--expect-revocation-within', '90s']
    process = subprocess.Popen(args, stdin=subprocess.DEVNULL, stdout=subprocess.PIPE, stderr=subprocess.PIPE,
                               start_new_session=True)
    line, remainder, error = capacity.bounded_read_line(process, maximum=4096, timeout=30)
    if line != b'READY existing Oracle session\n' or remainder or error:
        stop_process(process)
        raise ValueError('Oracle revocation session did not become ready')
    return process


def finish_revocation_session(process):
    output = capacity.bounded_finish(process, maximum=4096, timeout=120)
    if output.strip() != b'REVOKED existing Oracle session closed':
        raise ValueError('existing Oracle session was not closed after revocation')


def stop_process(process):
    if process is None or process.poll() is not None:
        return
    try:
        os.killpg(process.pid, signal.SIGKILL)
    except ProcessLookupError:
        return
    try:
        process.wait(timeout=5)
    except subprocess.TimeoutExpired:
        os.killpg(process.pid, signal.SIGKILL)
        process.wait(timeout=5)


def external_probe(config, endpoint, password, ca, fingerprint, attestation):
    source_cidr = attestation['source_cidr']
    request = {'schema_version': 1, 'endpoint': endpoint, 'username': 'APP', 'database': 'FREEPDB1',
               'password': password, 'ca_pem': ca, 'expected_leaf_fingerprint': fingerprint,
               'expected_source_cidr': source_cidr,
               'required_checks': ['verified', 'wrong_ca_rejected', 'wrong_hostname_rejected',
                                   'wrong_password_rejected', 'plaintext_rejected',
                                   'port_1521_refused', 'port_5500_refused',
                                   'valid_reconnect_after_each_negative']}
    raw = command([config['external_probe_command'], '--run', config['external_probe_config_file']],
                  stdin=json.dumps(request).encode(), maximum=16 * 1024, timeout=300)
    if password.encode() in raw or ca.encode() in raw:
        raise ValueError('external probe output exposed protected material')
    result = json.loads(raw)
    checks = request['required_checks']
    fields = {'schema_version', 'provider', 'nonce', 'outside_development_cluster', 'source_cidr',
              'leaf_fingerprint', 'identity', 'checks'}
    if (not isinstance(result, dict) or set(result) != fields or result.get('schema_version') != 1 or
            result.get('provider') != attestation['provider'] or result.get('nonce') != attestation['nonce'] or
            result.get('outside_development_cluster') is not True or
            result.get('source_cidr') != source_cidr or result.get('leaf_fingerprint') != fingerprint or
            result.get('identity') != 'APP|FREEPDB1' or result.get('checks') != {name: True for name in checks}):
        raise ValueError('external Oracle probe did not prove the complete TCPS contract')
    return {'provider': result.get('provider'), 'source_cidr': source_cidr,
            'leaf_fingerprint': fingerprint, 'checks': checks}


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
    current_cgroup_limits()
    validate_lane_grant(args.lane_grant)
    config = strict_json(args.config, CONFIG_FIELDS)
    for name in ('psql', 'kubectl', 'qualification_binary', 'withdrawal_binary', 'fault_proxy_binary',
                 'external_probe_command'):
        candidate = Path(config[name]).resolve()
        if not candidate.is_file() or not os.access(candidate, os.X_OK):
            raise ValueError('configured acceptance executable is unavailable')
        config[name] = str(candidate)
    qualification_binary = Path(config['qualification_binary'])
    withdrawal_binary = Path(config['withdrawal_binary'])
    fault_proxy_binary = Path(config['fault_proxy_binary'])
    binaries = (qualification_binary, withdrawal_binary, fault_proxy_binary)
    if (any(not binary.is_relative_to(source) for binary in binaries) or len(set(binaries)) != len(binaries) or
            len({file_hash(binary) for binary in binaries}) != len(binaries)):
        raise ValueError('distinct qualification, gate-closed withdrawal and fault-proxy binaries are required')
    plan = validate_preflight_plan(args.plan, root, source, args.config, args.lane_grant,
                                   qualification_binary, withdrawal_binary, args.attempt)
    kubeconfig = Path(config['kubeconfig']).resolve()
    if kubeconfig.is_symlink() or not kubeconfig.is_file():
        raise ValueError('named development kubeconfig is unavailable')
    acceptance_lock = acquire_acceptance_lock(root)
    config = reinspect_before_mutation(plan, root, source, args.config, args.lane_grant,
                                       qualification_binary, withdrawal_binary, args.attempt)
    kube = [config['kubectl'], '--kubeconfig', str(kubeconfig), '--context', CONTEXT]
    if command(kube + ['config', 'current-context'], maximum=128, timeout=10).decode().strip() != CONTEXT:
        raise ValueError('named development context is required')
    run_id = 'pe-' + secrets.token_hex(8)
    stem = f'oracle-public-endpoint-v{args.attempt}'
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
    server = fault = scoped_api = admin_api = None
    database_id = None
    endpoint_ids = set()
    probe_namespace = 'hp-' + run_id
    session = None
    volumes = []
    created_control_database = False
    endpoints_closed = False
    passed = False
    cleanup = []
    started = time.monotonic()
    try:
        created_control_database = True
        command([config['psql'], '-X', '-v', 'ON_ERROR_STOP=1', '-d', 'postgres', '-c',
                 'CREATE DATABASE ' + sql_identifier], env=admin_env, maximum=4096, timeout=30)
        evidence.add('owned_control_database_created', database=control_database)
        api_port = free_port()
        origin = f'http://127.0.0.1:{api_port}'
        environment = {key: value for key, value in os.environ.items() if not key.startswith(('HAKOPOD_', 'AWS_', 'PG'))}
        environment.update(config['server_environment'])
        environment.update(HAKOPOD_DATABASE_URL=server_database_url(admin_url, control_database),
                           HAKOPOD_AUTH_ENCRYPTION_KEY=secrets.token_hex(32), HAKOPOD_DEPLOYMENT_MODE='self-hosted',
                           HAKOPOD_LISTEN=f'127.0.0.1:{api_port}', HAKOPOD_WEB_ORIGIN=origin,
                           HAKOPOD_APP_DOMAIN=config['app_domain'], HAKOPOD_PUBLIC_TCP_PORTS=str(config['public_port']),
                           HAKOPOD_DATABASE_PUBLIC_ADDRESS=config['public_address'],
                           HAKOPOD_DATABASE_PUBLIC_DOMAIN=config['public_domain'],
                           HAKOPOD_DATABASE_PUBLIC_PORTS=str(config['public_port']),
                           HAKOPOD_INGRESS_CLASS=config['ingress_class'],
                           HAKOPOD_HAPROXY_NAMESPACE=config['proxy_namespace'],
                           HAKOPOD_HAPROXY_CONFIGMAP=config['proxy_configmap'],
                           HAKOPOD_HAPROXY_RELEASE=config['proxy_release'],
                           HAKOPOD_BACKUP_STATE_DIR=str(work / 'backups'))
        if config['tls_issuer']:
            environment['HAKOPOD_TLS_ISSUER'] = config['tls_issuer']
        environment['PATH'] = str(Path(config['kubectl']).parent) + ':' + environment.get('PATH', '')
        admin_key_file = work / 'bootstrap-key'
        command([str(qualification_binary), 'bootstrap', '--name', 'oracle-public-endpoint-acceptance',
                 '--key-file', str(admin_key_file)], env=dict(environment, HAKOPOD_KUBECONFIG=str(kubeconfig)),
                maximum=4096, timeout=60)
        admin_key = protected(admin_key_file, 4096).decode().strip()
        server = ServerProcess(qualification_binary, source, environment, origin,
                               plan['qualification_binary_sha256'])
        server.start(kubeconfig)
        admin_api = API(origin, admin_key, [admin_key])
        key_result = admin_api.call('POST', '/keys', {'name': run_id, 'project': 'demo',
                                    'environment': 'development', 'application': '',
                                    'permissions': ['deployments:read', 'deployments:write'],
                                    'expires_at': (datetime.now(timezone.utc) + timedelta(hours=6)).isoformat()}, expected=201)
        scoped_key = key_result['key']
        scoped_api = API(origin, scoped_key, [admin_key, scoped_key])
        admin_key_file.unlink()
        evidence.add('scoped_api_ready', api_origin=origin)
        spec = {'schema_version': 1, 'name': run_id, 'engine': 'oracle', 'version': '23.26',
                'mode': 'standalone', 'replicas': 0, 'shards': 1, 'cpu': '1', 'memory': '4Gi',
                'storage_gib': 10, 'placement': {'node_names': [config['allowed_node']]},
                'tls': {'mode': 'required'}, 'oracle': {'edition': 'free'}}
        operation = scoped_api.call('POST', '/databases', {'project': 'demo', 'environment': 'development',
                                    'spec': spec}, expected=202, idem=run_id + '-database')
        database_id = operation['database_id']
        scoped_api.wait_operation('/database-operations/' + operation['id'], 40 * 60)
        observation = wait_oracle_observation(scoped_api, database_id)
        volumes = owned_volume_inventory(kube, database_id)
        baseline = oracle_snapshot(kube, database_id)
        require_oracle_dns_names(baseline, database_id)
        inspect_public_network_policy(kube, database_id, config, False)
        evidence.add('oracle_fixture_ready', database_id=database_id, statefulset_uid=baseline['statefulset_uid'],
                     pod_uid=baseline['pod_uid'], leaf_fingerprint=baseline['leaf_fingerprint'],
                     ca_fingerprint=baseline['ca_fingerprint'], volume_count=len(volumes))
        capabilities = scoped_api.call('GET', '/databases/' + database_id + '/public-endpoint-capabilities')
        if capabilities.get('engine') != 'oracle' or capabilities.get('available') is not True:
            raise ValueError('approved qualification binary did not expose Oracle capability')
        credentials = scoped_api.call('POST', '/databases/' + database_id + '/credentials', {}, expected=200,
                                      idem=run_id + '-credentials')
        trust = scoped_api.call('GET', '/databases/' + database_id + '/trust')
        password, ca = credentials['password'], trust['certificate_pem']
        scoped_api.sensitive.append(password.encode())
        addresses = create_probe_fixtures(kube, probe_namespace, run_id, config['probe_image'],
                                          config['probe_command'], {'allowed': config['allowed_node'],
                                          'denied': config['denied_node']}, password, ca, database_id)
        evidence.add('local_probe_sources_ready', allowed_source=addresses['allowed'], denied_source=addresses['denied'])
        checkpoints = DurableCheckpoints(config['psql'], postgres_environment(admin_url, control_database))
        fixture_sql = FixtureSQL(config['psql'], postgres_environment(admin_url, control_database))
        source_cidrs = sorted([addresses['allowed'] + '/32', plan['external_probe']['source_cidr']])

        stale = scoped_api.call('POST', '/databases/' + database_id + '/public-endpoint-plan',
                                {'purpose': 'read_write', 'source_cidrs': source_cidrs,
                                 'max_connections': 8})
        fixture_sql.expire_review(stale['id'])
        scoped_api.call('POST', '/databases/' + database_id + '/public-endpoints',
                        {'review_id': stale['id'], 'expected_database_revision': stale['plan']['database_revision'],
                         'expected_endpoint_revision': stale['plan']['endpoint_revision']}, expected=409,
                        idem=run_id + '-stale-review')
        evidence.add('stale_review_rejected')
        fixture_sql.hold_maintenance(database_id)
        busy = scoped_api.call('POST', '/databases/' + database_id + '/public-endpoint-plan',
                               {'purpose': 'read_write', 'source_cidrs': source_cidrs,
                                'max_connections': 8})
        scoped_api.call('POST', '/databases/' + database_id + '/public-endpoints',
                        {'review_id': busy['id'], 'expected_database_revision': busy['plan']['database_revision'],
                         'expected_endpoint_revision': busy['plan']['endpoint_revision']}, expected=409,
                        idem=run_id + '-maintenance-conflict')
        fixture_sql.release_maintenance(database_id)
        evidence.add('concurrent_maintenance_rejected')

        def endpoint_by_id(endpoint_id):
            items = scoped_api.call('GET', '/databases/' + database_id + '/public-endpoints')['items']
            return next((item for item in items if item['id'] == endpoint_id), None)

        def accept_publication(checkpoint=None, use_api=None):
            api = use_api or scoped_api
            review_result = api.call('POST', '/databases/' + database_id + '/public-endpoint-plan',
                                     {'purpose': 'read_write', 'source_cidrs': source_cidrs,
                                      'max_connections': 8})
            review = review_result['plan']
            allocation = review['allocation']
            if (allocation != {'address': config['public_address'],
                               'host': f"database-{config['public_port']}.{config['public_domain']}",
                               'port': config['public_port']}):
                raise ValueError('Oracle allocation changed from the reviewed public endpoint')
            if checkpoint:
                checkpoints.install(checkpoint)
            accepted = api.call('POST', '/databases/' + database_id + '/public-endpoints',
                                {'review_id': review_result['id'],
                                 'expected_database_revision': review['database_revision'],
                                 'expected_endpoint_revision': review['endpoint_revision']}, expected=202,
                                idem=run_id + '-publish-' + secrets.token_hex(4))
            endpoint_ids.add(accepted['endpoint_id'])
            if checkpoint:
                wait_checkpoint(api, accepted['id'], checkpoint)
            return accepted, allocation

        def finish_publication(accepted, before_snapshot, checkpoint=None, api=None):
            api = api or scoped_api
            if checkpoint:
                inspect_public_network_policy(kube, database_id, config, False)
                server.crash()
                server.start(kubeconfig)
                checkpoints.resume(accepted['id'], database_id, accepted['endpoint_id'], checkpoint)
            api.wait_operation('/database-public-endpoint-operations/' + accepted['id'], 35 * 60)
            if checkpoint:
                checkpoints.remove()
            endpoint = endpoint_by_id(accepted['endpoint_id'])
            if endpoint is None or endpoint.get('status') != 'active' or not endpoint.get('observation', {}).get('configured'):
                raise ValueError('Oracle endpoint did not become active and configured')
            after_snapshot = oracle_snapshot(kube, database_id)
            require_singleton_transition(before_snapshot, after_snapshot)
            require_oracle_dns_names(after_snapshot, database_id, endpoint['allocation']['host'])
            claim = inspect_claim(kube, config['proxy_namespace'], config['public_port'], database_id, endpoint['id'])
            route = inspect_route(kube, database_id, endpoint['id'])
            if claim.get('data', {}).get('route_state') != 'published' or not route.get('metadata', {}).get(
                    'annotations', {}).get('hakopod.io/tcp-acknowledged'):
                raise ValueError('Oracle publication lacks allocation or HAProxy acknowledgement')
            validate_route(route, endpoint, source_cidrs)
            inspect_public_network_policy(kube, database_id, config, True)
            evidence.add('singleton_publication_completed', checkpoint=checkpoint or 'none',
                         old_pod_uid=before_snapshot['pod_uid'], new_pod_uid=after_snapshot['pod_uid'],
                         statefulset_uid=after_snapshot['statefulset_uid'],
                         leaf_fingerprint=after_snapshot['leaf_fingerprint'])
            return endpoint, after_snapshot

        def revoke(endpoint, *, fault_mode=None, withdrawal=False, hold_session=False):
            nonlocal fault, server, session
            before_revoke = oracle_snapshot(kube, database_id)
            if hold_session:
                session = start_revocation_session(kube, probe_namespace, config['probe_command'], endpoint['allocation'])
            api = scoped_api
            if withdrawal:
                server.stop()
                server = ServerProcess(withdrawal_binary, source, environment, origin,
                                       plan['withdrawal_binary_sha256'])
                server.start(kubeconfig)
                api = API(origin, scoped_key, [admin_key, scoped_key, password])
                closed = api.call('GET', '/databases/' + database_id + '/public-endpoint-capabilities')
                if closed.get('available') is not False:
                    raise ValueError('withdrawal binary did not keep Oracle publication closed')
                evidence.add('gate_closed_binary_started', binary_sha256=file_hash(withdrawal_binary))
            if fault_mode:
                server.stop()
                fault_work = work / ('fault-' + fault_mode + '-' + secrets.token_hex(3))
                fault_work.mkdir(mode=0o700)
                fault = FaultProxy(config['fault_proxy_binary'], plan['fault_proxy_binary_sha256'],
                                   config['kubectl'], kubeconfig, CONTEXT, config['proxy_namespace'], fault_work)
                proxy_kubeconfig = fault.start(mode=fault_mode, database_namespace=endpoint_namespace(database_id))
                server.start(proxy_kubeconfig)
            accepted = api.call('DELETE', '/databases/' + database_id + '/public-endpoints/' + endpoint['id'],
                                {'expected_endpoint_revision': endpoint['revision']}, expected=202,
                                idem=run_id + '-revoke-' + secrets.token_hex(4))
            claim = lambda: inspect_claim(kube, config['proxy_namespace'], config['public_port'],
                                          database_id, endpoint['id'])
            if fault_mode:
                wait_retained(api, accepted['id'], claim, 'closing')
                inspect_public_network_policy(kube, database_id, config, False)
                evidence.add('cleanup_fault_retained_allocation', fault=fault_mode, endpoint_id=endpoint['id'])
                server.stop()
                fault.stop()
                fault = None
                server.start(kubeconfig)
            api.wait_operation('/database-public-endpoint-operations/' + accepted['id'], 35 * 60)
            if inspect_route(kube, database_id, endpoint['id'], True) is not None or claim() is not None:
                raise ValueError('Oracle revocation retained route or allocation after success')
            inspect_public_network_policy(kube, database_id, config, False)
            after_revoke = oracle_snapshot(kube, database_id)
            require_singleton_transition(before_revoke, after_revoke)
            require_oracle_dns_names(after_revoke, database_id)
            if session is not None:
                finish_revocation_session(session)
                session = None
                evidence.add('existing_session_closed', endpoint_id=endpoint['id'])
            evidence.add('endpoint_revoked', endpoint_id=endpoint['id'], withdrawal_binary=withdrawal,
                         old_pod_uid=before_revoke['pod_uid'], new_pod_uid=after_revoke['pod_uid'],
                         statefulset_uid=after_revoke['statefulset_uid'])

        # Each phase is a real durable pause followed by an owned process crash.
        for phase in ('identity_issuing', 'identity_rolling', 'identity_converging'):
            before_phase = oracle_snapshot(kube, database_id)
            accepted, _ = accept_publication(phase)
            endpoint, _ = finish_publication(accepted, before_phase, phase)
            revoke(endpoint)

        # Authority loss before issuance must complete closed cleanup, never publication.
        authority_key = admin_api.call('POST', '/keys', {'name': run_id + '-authority', 'project': 'demo',
                                      'environment': 'development', 'application': '',
                                      'permissions': ['deployments:read', 'deployments:write'],
                                      'expires_at': (datetime.now(timezone.utc) + timedelta(hours=2)).isoformat()}, expected=201)
        authority_api = API(origin, authority_key['key'], [admin_key, scoped_key, authority_key['key'], password])
        authority_before = oracle_snapshot(kube, database_id)
        accepted, authority_allocation = accept_publication('identity_issuing', authority_api)
        admin_api.call('DELETE', '/keys/' + authority_key['metadata']['id'], expected=200)
        checkpoints.resume(accepted['id'], database_id, accepted['endpoint_id'], 'identity_issuing',
                           next_phase='cancelling_identity')
        checkpoint = wait_checkpoint(admin_api, accepted['id'], 'cancelling_identity')
        authority_replacements = []
        previous = authority_before
        terminal = None
        for _ in range(32):
            inspect_public_network_policy(kube, database_id, config, False)
            if inspect_route(kube, database_id, accepted['endpoint_id'], True) is not None:
                raise ValueError('authority-revoked Oracle operation opened a route during cancellation')
            try:
                current = oracle_snapshot(kube, database_id)
            except ValueError:
                current = None
            if current is not None and current['pod_uid'] != previous['pod_uid']:
                require_singleton_transition(previous, current)
                authority_replacements.append(current)
                previous = current
                if len(authority_replacements) == 1:
                    require_oracle_dns_names(current, database_id, authority_allocation['host'])
                elif len(authority_replacements) == 2:
                    require_oracle_dns_names(current, database_id)
                else:
                    raise ValueError('authority cancellation replaced the Oracle singleton more than twice')
            marker = checkpoints.marker(accepted['id'], 'cancelling_identity')
            checkpoints.resume(accepted['id'], database_id, accepted['endpoint_id'], 'cancelling_identity',
                               next_phase='cancelling_identity')
            checkpoint = wait_checkpoint(admin_api, accepted['id'], 'cancelling_identity',
                                         gate=checkpoints, after_marker=marker, terminal={'cancelled'})
            if checkpoint.get('status') == 'cancelled':
                terminal = checkpoint
                break
        if terminal is None:
            raise ValueError('authority cancellation exceeded its bounded durable checkpoints')
        checkpoints.remove()
        authority_after = oracle_snapshot(kube, database_id)
        if len(authority_replacements) != 2:
            raise ValueError('authority cancellation did not prove both singleton replacements')
        authority_intermediate, authority_cleanup = authority_replacements
        if any(authority_cleanup[key] != authority_after[key] for key in (
                'statefulset_uid', 'pod_uid', 'volumes', 'credentials_secret_sha256',
                'ca_fingerprint', 'leaf_fingerprint', 'dns_names')):
            raise ValueError('authority cleanup changed after its second singleton replacement')
        if inspect_route(kube, database_id, accepted['endpoint_id'], True) is not None:
            raise ValueError('authority-revoked Oracle operation opened a route')
        inspect_public_network_policy(kube, database_id, config, False)
        evidence.add('authority_revoked_before_issuance_closed', terminal_status=terminal['status'],
                     public_rollout_pod_uid=authority_intermediate['pod_uid'],
                     private_cleanup_pod_uid=authority_cleanup['pod_uid'])

        # A converged identity whose recorded member disappears must fail without opening ingress.
        failed_before = oracle_snapshot(kube, database_id)
        accepted, failed_allocation = accept_publication('tls')
        wait_checkpoint(scoped_api, accepted['id'], 'tls')
        transition_snapshot = oracle_snapshot(kube, database_id)
        require_singleton_transition(failed_before, transition_snapshot)
        require_oracle_dns_names(transition_snapshot, database_id, failed_allocation['host'])
        delete_owned_oracle_pod(kube, database_id, transition_snapshot['pod_uid'])
        wait_oracle_observation(scoped_api, database_id,
                                lambda observed: observed['members'][0]['uid'] != transition_snapshot['pod_uid'])
        checkpoints.resume(accepted['id'], database_id, accepted['endpoint_id'], 'tls')
        wait_terminal(scoped_api, accepted['id'], {'failed'}, 20 * 60)
        checkpoints.remove()
        failed_after = oracle_snapshot(kube, database_id)
        if (failed_after['statefulset_uid'] != transition_snapshot['statefulset_uid'] or
                failed_after['volumes'] != transition_snapshot['volumes']):
            raise ValueError('failed Oracle recovery changed StatefulSet or persistent identity')
        require_oracle_dns_names(failed_after, database_id, failed_allocation['host'])
        if inspect_route(kube, database_id, accepted['endpoint_id'], True) is not None:
            raise ValueError('failed Oracle recovery opened public ingress')
        inspect_public_network_policy(kube, database_id, config, False)
        evidence.add('failed_recovery_never_opened_ingress', removed_pod_uid=transition_snapshot['pod_uid'])
        failed_endpoint = endpoint_by_id(accepted['endpoint_id'])
        revoke(failed_endpoint)

        # Hold the real publishing phase, then fail after route creation and before reload acknowledgement.
        before_fault = oracle_snapshot(kube, database_id)
        accepted, _ = accept_publication('publishing')
        wait_checkpoint(scoped_api, accepted['id'], 'publishing')
        server.stop()
        fault_work = work / 'fault-route-reload'
        fault_work.mkdir(mode=0o700)
        fault = FaultProxy(config['fault_proxy_binary'], plan['fault_proxy_binary_sha256'],
                           config['kubectl'], kubeconfig, CONTEXT, config['proxy_namespace'], fault_work)
        proxy_kubeconfig = fault.start(
            mode='route_reload_after_mutation', database_namespace=endpoint_namespace(database_id),
            route_namespace=endpoint_namespace(database_id), route_resource=route_name(accepted['endpoint_id']))
        server.start(proxy_kubeconfig)
        checkpoints.resume(accepted['id'], database_id, accepted['endpoint_id'], 'publishing')
        wait_retained(scoped_api, accepted['id'], lambda: inspect_route_reload_retention(
            fault, kube, config['proxy_namespace'], config['public_port'], database_id,
            accepted['endpoint_id']), 'publishing')
        interrupted_route = inspect_route(kube, database_id, accepted['endpoint_id'])
        fault_status = fault.status()
        if not fault_status['route_mutation_seen']:
            raise ValueError('route reload fault did not observe the owned TCP route mutation')
        if 'hakopod.io/tcp-acknowledged' in interrupted_route.get('metadata', {}).get('annotations', {}):
            raise ValueError('route reload fault occurred after acknowledgement')
        pending_endpoint = endpoint_by_id(accepted['endpoint_id'])
        validate_route(interrupted_route, pending_endpoint, source_cidrs)
        inspect_public_network_policy(kube, database_id, config, True)
        evidence.add('route_create_reload_fault_retained_claim',
                     pre_mutation_proxy_upgrade_relays=fault_status['pre_mutation_proxy_upgrade_relays'],
                     pre_mutation_database_upgrade_relays=fault_status['pre_mutation_database_upgrade_relays'])
        server.stop()
        fault.stop()
        fault = None
        server.start(kubeconfig)
        scoped_api.wait_operation('/database-public-endpoint-operations/' + accepted['id'], 35 * 60)
        checkpoints.remove()
        endpoint = endpoint_by_id(accepted['endpoint_id'])
        after_fault = oracle_snapshot(kube, database_id)
        require_singleton_transition(before_fault, after_fault)
        validate_route(inspect_route(kube, database_id, endpoint['id']), endpoint, source_cidrs)

        claim = inspect_claim(kube, config['proxy_namespace'], config['public_port'], database_id, endpoint['id'])
        claim_before = {'uid': claim['metadata']['uid'], 'resourceVersion': claim['metadata']['resourceVersion'],
                        'labels': claim['metadata'].get('labels', {}), 'data': claim.get('data', {})}
        fixture_sql.mask_endpoint_purpose(endpoint['id'], 'read_only')
        try:
            conflict = scoped_api.call('POST', '/databases/' + database_id + '/public-endpoint-plan',
                                       {'purpose': 'read_write', 'source_cidrs': source_cidrs,
                                        'max_connections': 8}, expected=409)
            conflict_reason = require_operator_allocation_conflict(conflict)
        finally:
            fixture_sql.mask_endpoint_purpose(endpoint['id'], 'read_write')
        claim = inspect_claim(kube, config['proxy_namespace'], config['public_port'], database_id, endpoint['id'])
        claim_after = {'uid': claim['metadata']['uid'], 'resourceVersion': claim['metadata']['resourceVersion'],
                       'labels': claim['metadata'].get('labels', {}), 'data': claim.get('data', {})}
        if claim_after != claim_before:
            raise ValueError('competing Hakopod allocation attempt changed the existing claim')
        evidence.add('duplicate_allocation_rejected', claim_uid=claim['metadata']['uid'], reason=conflict_reason)
        for check in ('verified', 'crud', 'privileges', 'wrong-ca-rejected', 'wrong-hostname-rejected',
                      'bad-password-rejected', 'plaintext-rejected', 'raw-ports-refused'):
            probe(kube, probe_namespace, 'allowed', config['probe_command'], endpoint['allocation'], check,
                  fingerprint=after_fault['leaf_fingerprint'])
            evidence.add('local_oracle_probe_passed', check_name=check,
                         leaf_fingerprint=after_fault['leaf_fingerprint'])
        probe(kube, probe_namespace, 'denied', config['probe_command'], endpoint['allocation'], 'unreachable')
        probe(kube, probe_namespace, 'allowed', config['probe_command'], endpoint['allocation'], 'verified',
              fingerprint=after_fault['leaf_fingerprint'])
        evidence.add('denied_source_refused')
        external = external_probe(config, endpoint['allocation'], password, ca, after_fault['leaf_fingerprint'],
                                  plan['external_probe'])
        evidence.add('external_probe_passed', provider=external['provider'],
                     source_cidr=external['source_cidr'], leaf_fingerprint=external['leaf_fingerprint'])

        # Renew the issuer and leaf, require one replacement and unchanged storage/data.
        renewal_before = oracle_snapshot(kube, database_id)
        expire_owned_oracle_ca(kube, database_id)
        renewed_observation = wait_oracle_observation(scoped_api, database_id, lambda observed:
            observed['members'][0]['uid'] != renewal_before['pod_uid'] and
            observed['tls']['fingerprint'] != renewal_before['leaf_fingerprint'] and
            observed['tls']['ca_fingerprint'] != renewal_before['ca_fingerprint'])
        renewal_after = oracle_snapshot(kube, database_id)
        require_singleton_transition(renewal_before, renewal_after, ca_may_change=True)
        require_oracle_dns_names(renewal_after, database_id, endpoint['allocation']['host'])
        probe(kube, probe_namespace, 'allowed', config['probe_command'], endpoint['allocation'], 'data-preserved',
              fingerprint=renewal_after['leaf_fingerprint'])
        evidence.add('certificate_renewal_preserved_data_and_volumes',
                     old_pod_uid=renewal_before['pod_uid'], new_pod_uid=renewal_after['pod_uid'],
                     old_ca_fingerprint=renewal_before['ca_fingerprint'],
                     new_ca_fingerprint=renewal_after['ca_fingerprint'],
                     new_leaf_fingerprint=renewal_after['leaf_fingerprint'])

        # Route-close/master-socket failure must retain the claim and close policy.
        revoke(endpoint, fault_mode='master_socket_unavailable', hold_session=True)

        # Republish once and revoke after a restart under the separately built gate-closed binary.
        withdrawal_before = oracle_snapshot(kube, database_id)
        accepted, _ = accept_publication()
        withdrawal_endpoint, withdrawal_snapshot = finish_publication(accepted, withdrawal_before)
        revoke(withdrawal_endpoint, withdrawal=True, hold_session=True)
        endpoints_closed = True
        passed = True
        evidence.add('fixture_checks_completed', database_id=database_id,
                     final_pod_uid=oracle_snapshot(kube, database_id)['pod_uid'])
    finally:
        stop_process(session)
        if fault is not None:
            if server is not None:
                server.stop()
            fault.stop()
            fault = None
        if database_id and scoped_api is not None and server is not None:
            try:
                if server.process is None:
                    server.start(kubeconfig)
                items = scoped_api.call('GET', '/databases/' + database_id + '/public-endpoints')['items']
                for endpoint in items:
                    if endpoint.get('status') == 'revoked':
                        continue
                    accepted = scoped_api.call('DELETE', '/databases/' + database_id + '/public-endpoints/' + endpoint['id'],
                                               {'expected_endpoint_revision': endpoint['revision']}, expected=202,
                                               idem=run_id + '-cleanup-' + secrets.token_hex(4))
                    scoped_api.wait_operation('/database-public-endpoint-operations/' + accepted['id'], 35 * 60)
                endpoints_closed = all(inspect_route(kube, database_id, endpoint_id, True) is None
                                       for endpoint_id in endpoint_ids)
                if kube_json(kube, ['-n', config['proxy_namespace'], 'get', 'configmap',
                                    claim_name(config['public_port'])], missing=True) is not None:
                    endpoints_closed = False
                if not endpoints_closed:
                    raise ValueError('public closure is incomplete; preserving database and allocation')
                cleanup.append('endpoint_routes_sessions_identities_and_claims')
            except Exception:
                cleanup.append('endpoint_closure_incomplete')
                endpoints_closed = False
        if database_id and endpoints_closed and scoped_api is not None and server is not None:
            try:
                namespace = kube_json(kube, ['get', 'namespace', endpoint_namespace(database_id)], missing=True)
                if namespace is not None:
                    volumes = owned_volume_inventory(kube, database_id, complete=False)
                    database = scoped_api.call('GET', '/databases/' + database_id)
                    deletion = scoped_api.call('DELETE', '/databases/' + database_id,
                        {'expected_revision': database['revision'], 'confirm_name': run_id}, expected=202,
                        idem=run_id + '-cleanup-database')
                    scoped_api.wait_operation('/database-operations/' + deletion['id'], 20 * 60)
                wait_volume_cleanup(kube, volumes, endpoint_namespace(database_id))
                if kube_json(kube, ['get', 'namespace', endpoint_namespace(database_id)], missing=True) is not None:
                    raise ValueError('Oracle namespace cleanup is incomplete')
                cleanup.append('database_namespace_data_backup_pvcs_and_pvs')
            except Exception:
                cleanup.append('database_namespace_or_volume_cleanup_incomplete')
        if server is not None:
            server.stop()
        if database_id is None:
            endpoints_closed = True
        try:
            namespace = kube_json(kube, ['get', 'namespace', probe_namespace], missing=True)
            if namespace is not None:
                if namespace.get('metadata', {}).get('labels', {}).get(FIXTURE_LABEL) != run_id:
                    raise ValueError('probe namespace ownership changed; cleanup refused')
                meta = namespace['metadata']
                options = {'apiVersion': 'v1', 'kind': 'DeleteOptions', 'preconditions': {
                    'uid': meta['uid'], 'resourceVersion': meta['resourceVersion']}}
                command(kube + ['delete', '--raw', '/api/v1/namespaces/' + probe_namespace, '-f', '-'],
                        stdin=json.dumps(options).encode(), maximum=65536, timeout=30)
                cleanup.append('probe_namespace')
        except Exception:
            cleanup.append('probe_namespace_incomplete')
        if created_control_database and endpoints_closed and not any(item.endswith('_incomplete') for item in cleanup):
            try:
                command([config['psql'], '-X', '-v', 'ON_ERROR_STOP=1', '-d', 'postgres', '-c',
                         'DROP DATABASE IF EXISTS ' + sql_identifier + ' WITH (FORCE)'],
                        env=admin_env, maximum=4096, timeout=30)
                cleanup.append('control_database')
            except Exception:
                cleanup.append('control_database_incomplete')
        after = source_hashes(source)
        evidence.close()
        qualification_binary_sha256 = file_hash(qualification_binary)
        withdrawal_binary_sha256 = file_hash(withdrawal_binary)
        fault_proxy_binary_sha256 = file_hash(fault_proxy_binary)
        binaries_match_plan = (
            qualification_binary_sha256 == plan['qualification_binary_sha256'] and
            withdrawal_binary_sha256 == plan['withdrawal_binary_sha256'] and
            fault_proxy_binary_sha256 == plan['fault_proxy_binary_sha256'])
        report = {'schema_version': 1, 'run_id': run_id, 'attempt': args.attempt, 'context': CONTEXT,
                  'platform': 'linux/amd64', 'engine': 'oracle', 'edition': 'free',
                  'transport': 'oracle_tcps', 'qualification_only': True,
                  'source_files': before, 'source_files_after': after,
                  'qualification_binary_sha256': qualification_binary_sha256,
                  'withdrawal_binary_sha256': withdrawal_binary_sha256,
                  'fault_proxy_binary_sha256': fault_proxy_binary_sha256,
                  'binaries_match_plan': binaries_match_plan,
                  'probe_image': config['probe_image'], 'external_probe': plan['external_probe'],
                  'event_log_sha256': file_hash(event_path), 'events': evidence.events,
                  'elapsed_seconds': round(time.monotonic() - started, 3), 'cleanup': cleanup,
                  'passed': passed and before == after and endpoints_closed and binaries_match_plan and
                            not any(item.endswith('_incomplete') for item in cleanup)}
        write_json_exclusive(report_path, report)
        passed = report['passed']
        print('Oracle Free public TCPS acceptance passed:', passed, flush=True)
        print('Protected event log:', event_path, flush=True)
        print('Protected structural evidence:', report_path, flush=True)
        acceptance_lock.close()
    return 0 if passed else 1


if __name__ == '__main__':
    try:
        sys.exit(main())
    except Exception as error:
        print('Oracle Free public TCPS acceptance stopped:', type(error).__name__, file=sys.stderr, flush=True)
        sys.exit(1)
