#!/usr/bin/env python3
"""Installer validation/rendering. No shell evaluation; Python standard library only."""
import argparse
import base64
import hashlib
import ipaddress
import json
import os
from pathlib import Path, PurePosixPath
import platform
import re
import secrets
import shutil
import socket
import stat
import subprocess
import sys
import tarfile
from urllib.parse import urlsplit

HERE = Path(__file__).resolve().parent
PINS = json.loads((HERE / 'pins.json').read_text())
DEFAULTS = dict(schema_version=1, version='0.1.0-dev', app_domain='', node_ip='',
    node_name='hakopod-server', supervisor_host='', dashboard_mode='ssh',
    dashboard_origin='http://localhost:3000', dashboard_port=3000,
    tls_cert_file='', tls_key_file='', acme='production', acme_email='', storage=False,
    k3s_memory_mib=2048, api_memory_mib=256, dashboard_memory_mib=320,
    postgres_memory_mib=256, max_pods=50)
LABEL = 'hakopod.com/installation'
ROOTS = ('/etc/hakopod', '/opt/hakopod', '/var/lib/hakopod')
UNITS = ('hakopod-k3s', 'hakopod-api', 'hakopod-dashboard')

def fail(message):
    raise ValueError(message)

def no_duplicates(pairs):
    result = {}
    for key, value in pairs:
        if key in result: fail('Duplicate JSON key: ' + key)
        result[key] = value
    return result

def read_json(path):
    if Path(path).stat().st_size > 65536: fail('Configuration exceeds 64 KiB')
    return json.loads(Path(path).read_text(), object_pairs_hook=no_duplicates)

def dns(value):
    return (isinstance(value, str) and len(value) <= 190 and
        re.fullmatch(r'[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?(?:\.[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?)*', value))

def config(path):
    incoming = read_json(path)
    if not isinstance(incoming, dict): fail('Expected a JSON object')
    unknown = set(incoming) - set(DEFAULTS)
    if unknown: fail('Unknown configuration keys: ' + ', '.join(sorted(unknown)))
    c = dict(DEFAULTS, **incoming)
    for key, default in DEFAULTS.items():
        if type(c[key]) is not type(default): fail(key + ' has the wrong value type')
        if isinstance(c[key], str) and (len(c[key]) > 1024 or any(ord(ch) < 32 for ch in c[key])):
            fail(key + ' contains control characters or is too long')
    if c['schema_version'] != 1: fail('Only schema_version 1 is supported')
    if not re.fullmatch(r'[A-Za-z0-9][A-Za-z0-9._-]{0,63}', c['version']): fail('Invalid version')
    if not dns(c['app_domain']) or '.' not in c['app_domain']: fail('app_domain must be an operator-owned DNS subdomain')
    if not dns(c['node_name']) or len(c['node_name']) > 63: fail('Invalid node_name')
    try: address = ipaddress.IPv4Address(c['node_ip'])
    except ValueError: fail('node_ip must be the server IPv4 address reachable by workers')
    if address.is_loopback or address.is_unspecified or address.is_multicast: fail('node_ip must be a unicast nonloopback IPv4 address')
    if not c['supervisor_host']: c['supervisor_host'] = c['node_ip']
    if not dns(c['supervisor_host']): fail('supervisor_host must be a DNS name or IPv4 address, without a port')
    for key, low, high in [('dashboard_port', 1024, 65535), ('k3s_memory_mib', 1536, 65536),
            ('api_memory_mib', 256, 4096), ('dashboard_memory_mib', 256, 4096),
            ('postgres_memory_mib', 192, 4096), ('max_pods', 20, 250)]:
        if not low <= c[key] <= high: fail(f'{key} must be {low}–{high}')
    if c['dashboard_port'] in (6443, 10250, 8080): fail('dashboard_port conflicts with a platform port')
    origin = urlsplit(c['dashboard_origin'])
    try: port = origin.port
    except ValueError: fail('Invalid dashboard origin port')
    if origin.username or origin.password or origin.path or origin.query or origin.fragment:
        fail('dashboard_origin must be an origin without path, credentials, query or fragment')
    if c['dashboard_mode'] == 'ssh':
        if origin.scheme != 'http' or origin.hostname not in ('localhost', '127.0.0.1') or port != c['dashboard_port']:
            fail('SSH mode requires http://localhost:<dashboard_port> or http://127.0.0.1:<dashboard_port>')
        if c['tls_cert_file'] or c['tls_key_file']: fail('TLS file inputs require dashboard_mode=https')
    elif c['dashboard_mode'] == 'https':
        if origin.scheme != 'https' or not dns(origin.hostname) or (port or 443) != c['dashboard_port']:
            fail('HTTPS mode requires a DNS origin with the configured dashboard_port')
        if origin.hostname == c['app_domain'] or origin.hostname.endswith('.' + c['app_domain']):
            fail('Dashboard must have a separate origin outside the application domain')
        for key in ('tls_cert_file', 'tls_key_file'):
            if not c[key].startswith('/') or not re.fullmatch(r'/[A-Za-z0-9_./-]+', c[key]):
                fail(key + ' must be an absolute file path without whitespace')
    else: fail('dashboard_mode must be ssh or https')
    if c['acme'] not in ('off', 'staging', 'production'): fail('acme must be off, staging or production')
    if c['acme'] != 'off' and not re.fullmatch(r'[^\s@]+@[^\s@]+\.[^\s@]+', c['acme_email']):
        fail('ACME needs an operator-provided acme_email')
    return c

def architecture(value=None):
    value = value or platform.machine()
    aliases = {'x86_64': 'amd64', 'amd64': 'amd64', 'aarch64': 'arm64', 'arm64': 'arm64'}
    if value not in aliases: fail('Supported architectures are Linux amd64 and arm64')
    return aliases[value]

def digest(path):
    h = hashlib.sha256()
    with Path(path).open('rb') as stream:
        for block in iter(lambda: stream.read(1024 * 1024), b''): h.update(block)
    return h.hexdigest()

def fingerprint(c, arch, artifacts):
    # Moving the same verified input bytes does not invalidate a resume.
    return hashlib.sha256(json.dumps(dict(config=c, arch=arch, artifacts=artifacts,
        pins=PINS), sort_keys=True).encode()).hexdigest()

def regular(path, private=False):
    p = Path(path)
    if p.is_symlink() or not p.is_file(): fail('Expected a regular non-symlink file: ' + str(path))
    if private and p.stat().st_mode & 0o077: fail('Secret file must have mode0600 or0400: ' + str(path))
    return p

def artifacts(directory, c, arch):
    directory = Path(directory).resolve(strict=True)
    hashes = {}
    for line in regular(directory / 'SHA256SUMS').read_text().splitlines():
        match = re.fullmatch(r'([0-9a-f]{64})  ([A-Za-z0-9][A-Za-z0-9._-]*)', line)
        if not match or match[2] in hashes: fail('Invalid or duplicate SHA256SUMS entry')
        hashes[match[2]] = match[1]
    wanted = [f'hakopod_{c["version"]}_linux_{arch}.tar.gz', f'hakopod_{c["version"]}_dashboard.tar.gz']
    verified = {}
    for name in wanted:
        if name not in hashes: fail('Missing checksum for ' + name + '; build both local release artifacts first')
        actual = digest(regular(directory / name))
        if actual != hashes[name]: fail('Artifact checksum mismatch: ' + name)
        verified[name] = actual
    return verified

def unpack(source, destination, expected_root):
    """Validate all members before writing; safe internal symlinks are last."""
    destination = Path(destination)
    if destination.exists() and any(destination.iterdir()): fail('Extraction destination must be empty')
    with tarfile.open(source) as archive:
        members = archive.getmembers()
        if len(members) > 50000 or sum(x.size for x in members) > 1024 ** 3: fail('Archive exceeds extraction bounds')
        paths, links = set(), set()
        for member in members:
            path = PurePosixPath(member.name)
            if path.is_absolute() or '..' in path.parts or not path.parts or path.parts[0] != expected_root or '\\' in member.name:
                fail('Unsafe or unexpected archive path')
            if str(path) in paths: fail('Duplicate archive path')
            paths.add(str(path))
            if not (member.isfile() or member.isdir() or member.issym()): fail('Archive has a device, hardlink or unsupported entry')
            if member.issym():
                if PurePosixPath(member.linkname).is_absolute(): fail('Absolute archive symlink')
                target = os.path.normpath(str(path.parent / member.linkname))
                if not target.startswith(expected_root + '/') or '\\' in target: fail('Archive symlink escapes bundle')
                links.add(str(path))
        for name in paths:
            if any(str(parent) in links for parent in PurePosixPath(name).parents): fail('Archive entry is beneath a symlink')
        for member in members:
            if member.issym(): continue
            path = destination / member.name
            path.parent.mkdir(parents=True, exist_ok=True)
            if member.isdir(): path.mkdir(exist_ok=True)
            else:
                with archive.extractfile(member) as src, path.open('xb') as dst: shutil.copyfileobj(src, dst, 1024 * 1024)
                path.chmod(0o755 if member.mode & 0o111 else 0o644)
        for member in members:
            if member.issym():
                path = destination / member.name
                path.parent.mkdir(parents=True, exist_ok=True)
                path.symlink_to(member.linkname)
                if not path.resolve().is_relative_to((destination / expected_root).resolve()): fail('Symlink chain escapes bundle')
        # Installer umask077 must not make copied runtime directories inaccessible
        # to the separate service users. Archive files keep explicit safe modes.
        for directory, _, _ in os.walk(destination, followlinks=False): os.chmod(directory, 0o755)

def plan(c, arch, directory):
    verified = artifacts(directory, c, arch)
    print(f'Hakopod {c["version"]}, Linux/{arch}; local artifacts verified against supplied SHA256SUMS.')
    print('This is a review plan. No host paths, services, credentials or cluster resources are changed.')
    print('Install: dedicated K3s ' + PINS['k3s']['version'] + ', Helm ' + PINS['helm']['version'] + ', Node LTS ' + PINS['node']['version'])
    print(f'Node: {c["node_name"]} / {c["node_ip"]}; workers connect to https://{c["supervisor_host"]}:6443')
    print('Data: /var/lib/hakopod; configuration/secrets: /etc/hakopod; binaries: /opt/hakopod')
    print('K3s encryption at rest; Traefik, ServiceLB and default storage disabled. No adoption of another cluster.')
    print('HAProxy: one controller pinned to this node; host ports80/443; private administration/metrics.')
    print('Application DNS: *.' + c['app_domain'] + ' → operator-managed public IP/NAT; DNS/firewall unchanged.')
    print('Dashboard: ' + c['dashboard_origin'] + (' via SSH tunnel; API127.0.0.1:8080' if c['dashboard_mode'] == 'ssh' else '; supplied certificate; API127.0.0.1:8080'))
    print('PostgreSQL: dedicated namespace, static20Gi local PV (Retain), service10.43.0.20:5432; no host PostgreSQL reuse.')
    print('Local application storage: ' + ('enabled, node-local Delete reclaim' if c['storage'] else 'off'))
    print('ACME: ' + c['acme'] + ('; staging certificates are untrusted by browsers' if c['acme'] == 'staging' else ''))
    print(f'Hard memory limits: K3s{c["k3s_memory_mib"]}MiB, API{c["api_memory_mib"]}MiB, dashboard{c["dashboard_memory_mib"]}MiB, PostgreSQL{c["postgres_memory_mib"]}MiB, HAProxy256MiB.')
    print('API: GOMEMLIMIT192MiB /2 processors; dashboard JS heap192MiB. At least4GiB RAM and30GiB free disk; workload capacity is additional.')
    print('Only a random setup token is generated. Choose your own name, email and password in the dashboard.')
    print('Upstream binaries use checked-in hashes. Local SHA256SUMS must come from your trusted build; they are not signatures.')
    print('Config/artifact fingerprint: ' + fingerprint(c, arch, verified))

def no_symlink_ancestors(path):
    for p in (Path(path), *Path(path).parents):
        if p.is_symlink(): fail('Refusing symlink in installation path: ' + str(p))

def preflight(c, arch, resume):
    if platform.system() != 'Linux': fail('Installation requires a supported Linux host; use --dry-run for cross-platform review')
    if os.geteuid() != 0: fail('Installation requires root; --dry-run does not')
    if architecture() != arch: fail('Target architecture differs from this Linux host')
    os_info = {}
    for line in Path('/etc/os-release').read_text().splitlines():
        if '=' in line:
            key, value = line.split('=', 1); os_info[key] = value.strip('"')
    supported = (os_info.get('ID') == 'ubuntu' and os_info.get('VERSION_ID') in ('24.04', '26.04')) or (os_info.get('ID') == 'debian' and os_info.get('VERSION_ID') in ('12', '13'))
    if not supported: fail('Supported host OS: Ubuntu24.04/26.04 or Debian12/13 with systemd and cgroupv2')
    if not Path('/run/systemd/system').is_dir() or not Path('/sys/fs/cgroup/cgroup.controllers').is_file(): fail('A running systemd host with cgroupv2 is required; ordinary containers are only suitable for dry-run tests')
    for command in ('systemctl', 'curl', 'ip', 'openssl', 'flock', 'useradd', 'getent', 'mount', 'modprobe', 'swapon', 'sha256sum'):
        if not shutil.which(command): fail('Install this prerequisite with your OS package manager: ' + command)
    if subprocess.check_output(['swapon', '--noheadings', '--show'], text=True).strip(): fail('Disable swap explicitly before installation; installer does not change swap configuration')
    memory = int(re.search(r'^MemTotal:\s+(\d+)', Path('/proc/meminfo').read_text(), re.M)[1]) // 1024
    required = max(3900, c['k3s_memory_mib'] + c['api_memory_mib'] + c['dashboard_memory_mib'] + c['postgres_memory_mib'] + 768 + (384 if c['acme'] != 'off' else 0))
    if memory < required: fail(f'Host has{memory}MiB RAM; this configuration requires at least{required}MiB plus application capacity')
    if shutil.disk_usage('/var/lib').free < 30 * 1024 ** 3: fail('At least30GiB free disk is required under /var/lib')
    addresses = json.loads(subprocess.check_output(['ip', '-j', 'address', 'show'], text=True))
    if not any(x.get('local') == c['node_ip'] for item in addresses for x in item.get('addr_info', [])):
        fail('node_ip is not assigned to a local interface')
    for root in ROOTS: no_symlink_ancestors(root)
    marker = Path('/etc/hakopod/installation.json')
    if marker.exists():
        regular(marker, True)
        if not resume: fail('An installation marker exists; use --resume with the original inputs')
    else:
        if resume: fail('--resume requires the original installation marker')
        for root in ROOTS:
            if Path(root).exists(): fail('Refusing an unowned installation path: ' + root)
        for path in ('/etc/rancher/k3s', '/var/lib/rancher/k3s', '/etc/kubernetes', '/var/lib/etcd', '/usr/local/bin/k3s'):
            if Path(path).exists(): fail('Existing Kubernetes state detected; use a fresh dedicated host: ' + path)
        for unit in (*UNITS, 'k3s', 'k3s-agent', 'kubelet', 'rke2-server', 'rke2-agent'):
            state = subprocess.run(['systemctl', 'show', unit, '--property=LoadState', '--value'], capture_output=True, text=True).stdout.strip()
            if state and state != 'not-found': fail('Refusing existing service: ' + unit)
        for user in ('hakopod-api', 'hakopod-dashboard'):
            if subprocess.run(['getent', 'passwd', user], stdout=subprocess.DEVNULL).returncode == 0: fail('Refusing existing service account: ' + user)
        for port in (80, 443, 6443, 8080, 10250, c['dashboard_port']):
            for family, address in ((socket.AF_INET, '0.0.0.0'), (socket.AF_INET6, '::')):
                try:
                    with socket.socket(family, socket.SOCK_STREAM) as s:
                        if family == socket.AF_INET6: s.setsockopt(socket.IPPROTO_IPV6, socket.IPV6_V6ONLY, 1)
                        s.bind((address, port))
                except OSError as error:
                    if error.errno not in (97, 99): fail(f'Port{port} is already in use')
        routes = json.loads(subprocess.check_output(['ip', '-j', 'route', 'show', 'table', 'all'], text=True))
        for route in routes:
            if route.get('dst', 'default') == 'default': continue
            try: network = ipaddress.ip_network(route['dst'], strict=False)
            except ValueError: continue
            if network.version == 4 and any(network.overlaps(ipaddress.ip_network(cidr)) for cidr in ('10.42.0.0/16', '10.43.0.0/16')):
                fail('Existing route overlaps the fixed K3s pod/service CIDRs; use a fresh network')
    if c['dashboard_mode'] == 'https':
        cert_path, key_path = c['tls_cert_file'], c['tls_key_file']
        if resume and Path('/etc/hakopod/dashboard.crt').is_file() and Path('/etc/hakopod/dashboard.key').is_file():
            cert_path, key_path = '/etc/hakopod/dashboard.crt', '/etc/hakopod/dashboard.key'
        cert = regular(cert_path); key = regular(key_path, True)
        subprocess.run(['openssl', 'x509', '-in', str(cert), '-noout', '-checkend', '86400'], check=True, stdout=subprocess.DEVNULL)
        subprocess.run(['openssl', 'x509', '-in', str(cert), '-noout', '-checkhost', urlsplit(c['dashboard_origin']).hostname], check=True, stdout=subprocess.DEVNULL)
        cert_pub = subprocess.check_output(['openssl', 'x509', '-in', str(cert), '-pubkey', '-noout'])
        key_pub = subprocess.check_output(['openssl', 'pkey', '-in', str(key), '-pubout', '-passin', 'pass:'], stderr=subprocess.DEVNULL)
        if cert_pub != key_pub: fail('TLS certificate and key do not match')
    print('Linux host preflight passed. DNS, firewall reachability and certificate trust still require operator verification.')

def write(path, content, mode=0o600):
    path = Path(path); no_symlink_ancestors(path)
    path.parent.mkdir(parents=True, exist_ok=True)
    temp = path.with_name(path.name + '.new')
    no_symlink_ancestors(temp)
    descriptor = os.open(temp, os.O_WRONLY | os.O_CREAT | os.O_TRUNC | os.O_NOFOLLOW, mode)
    with os.fdopen(descriptor, 'w') as stream:
        stream.write(content); stream.flush(); os.fsync(stream.fileno())
    os.chmod(temp, mode); os.replace(temp, path)

def prepare(c, arch, directory, resume):
    verified = artifacts(directory, c, arch)
    fp = fingerprint(c, arch, verified)
    marker = Path('/etc/hakopod/installation.json')
    if marker.exists():
        old = read_json(marker)
        if old.get('fingerprint') != fp: fail('Resume inputs or artifact bytes changed; this installer does not perform upgrades')
        if old.get('schema_version') != 1 or not re.fullmatch(r'[0-9a-f]{32}', old.get('id', '')): fail('Invalid installation marker')
        installation = old['id']
    else:
        if resume: fail('Missing resume marker')
        installation = secrets.token_hex(16)
        for root in ROOTS: Path(root).mkdir(mode=0o711)
        write(marker, json.dumps(dict(schema_version=1, id=installation, fingerprint=fp, completed=False)) + '\n')
    for root in ROOTS: os.chmod(root, 0o711)
    write('/etc/hakopod/config.json', json.dumps(c, indent=2) + '\n')
    secret_dir = Path('/etc/hakopod/secrets'); secret_dir.mkdir(mode=0o711, exist_ok=True); secret_dir.chmod(0o711)
    for name in ('setup-token', 'auth-encryption-key', 'postgres-password', 'session-secret'):
        path = secret_dir / name
        if path.exists():
            regular(path, True)
            if not re.fullmatch(r'[0-9a-f]{64}\n?', path.read_text()): fail('Invalid preserved secret file: ' + name)
        else:
            if resume and old.get('secrets_created'): fail('Missing preserved secret; restore from backup: ' + name)
            write(path, secrets.token_hex(32) + '\n')
    state = read_json(marker); state['secrets_created'] = True
    write(marker, json.dumps(state) + '\n')
    Path('/var/lib/hakopod/postgres').mkdir(mode=0o700, exist_ok=True)
    print(installation)

def owned(path, installation):
    value = read_json(path)
    items = value.get('items', [value])
    for item in items:
        if item.get('metadata', {}).get('labels', {}).get(LABEL) != installation:
            fail('Refusing unrelated Kubernetes object: ' + item.get('kind', '?') + '/' + item.get('metadata', {}).get('name', '?'))

def render(c, arch, installation, out):
    """Render root-private files; secret bodies never appear on command lines."""
    if not re.fullmatch(r'[0-9a-f]{32}', installation): fail('Invalid installation id')
    out.mkdir(mode=0o700, parents=True, exist_ok=True)
    def emit(name, value): write(out / name, value)
    def obj(kind, name, namespace=None, **fields):
        metadata = dict(name=name, labels={LABEL: installation, 'app.kubernetes.io/managed-by': 'hakopod'})
        if namespace: metadata['namespace'] = namespace
        api = {'Deployment': 'apps/v1', 'NetworkPolicy': 'networking.k8s.io/v1',
               'ClusterIssuer': 'cert-manager.io/v1'}.get(kind, 'v1')
        return dict(apiVersion=api, kind=kind, metadata=metadata, **fields)
    # JSON is also valid YAML, and avoids interpolated YAML scalar parsing.
    emit('k3s.yaml', json.dumps({
        'data-dir': '/var/lib/hakopod/k3s', 'write-kubeconfig': '/etc/hakopod/admin-kubeconfig',
        'write-kubeconfig-mode': '0400', 'node-name': c['node_name'], 'node-ip': c['node_ip'],
        'advertise-address': c['node_ip'], 'tls-san': [c['supervisor_host']],
        'node-label': [LABEL + '=' + installation],
        'secrets-encryption': True, 'disable': ['traefik', 'servicelb', 'local-storage'],
        'flannel-backend': 'vxlan', 'cluster-cidr': '10.42.0.0/16', 'service-cidr': '10.43.0.0/16',
        'kubelet-arg': ['max-pods=' + str(c['max_pods']), 'container-log-max-size=10Mi', 'container-log-max-files=3'],
        'kube-apiserver-arg': ['max-requests-inflight=100', 'max-mutating-requests-inflight=50', 'event-ttl=1h'],
    }, indent=2) + '\n')
    emit('haproxy.json', json.dumps({'kubernetes-ingress': {'controller': {
        'nodeSelector': {'kubernetes.io/hostname': c['node_name']},
        'podLabels': {LABEL: installation},
        'strategy': {'type': 'Recreate'},
        'deployment': {'useHostPort': True, 'useHostNetwork': False,
                       'hostPorts': {'http': 80, 'https': 443, 'stat': 0}},
        'service': {'type': 'ClusterIP', 'nodePorts': {'http': None, 'https': None}},
    }}}, indent=2) + '\n')
    ns = 'hakopod-system'; selector = {'app': 'hakopod-postgres'}
    password = regular('/etc/hakopod/secrets/postgres-password', True).read_text().strip()
    session_secret = regular('/etc/hakopod/secrets/session-secret', True).read_text().strip()
    pg = [obj('Namespace', ns), obj('Secret', 'postgres', ns, type='Opaque',
        data={'password': base64.b64encode(password.encode()).decode()}),
        obj('PersistentVolume', 'hakopod-postgres', spec={
            'capacity': {'storage': '20Gi'}, 'volumeMode': 'Filesystem', 'accessModes': ['ReadWriteOnce'],
            'persistentVolumeReclaimPolicy': 'Retain', 'storageClassName': '',
            'claimRef': {'namespace': ns, 'name': 'postgres'},
            'local': {'path': '/var/lib/hakopod/postgres'},
            'nodeAffinity': {'required': {'nodeSelectorTerms': [{'matchExpressions': [{
                'key': 'kubernetes.io/hostname', 'operator': 'In', 'values': [c['node_name']]}]}]}}}),
        obj('PersistentVolumeClaim', 'postgres', ns, spec={'accessModes': ['ReadWriteOnce'],
            'storageClassName': '', 'volumeName': 'hakopod-postgres', 'resources': {'requests': {'storage': '20Gi'}}}),
        obj('Service', 'postgres', ns, spec={'clusterIP': '10.43.0.20', 'selector': selector,
            'ports': [{'port': 5432, 'targetPort': 5432, 'protocol': 'TCP'}]}),
        obj('NetworkPolicy', 'postgres-private', ns, spec={'podSelector': {'matchLabels': selector},
            'policyTypes': ['Ingress', 'Egress'], 'ingress': [{'from': [{'ipBlock': {'cidr': c['node_ip'] + '/32'}}],
                'ports': [{'port': 5432, 'protocol': 'TCP'}]}], 'egress': []}),
        obj('Deployment', 'postgres', ns, spec={'replicas': 1, 'strategy': {'type': 'Recreate'},
            'selector': {'matchLabels': selector}, 'template': {
                'metadata': {'labels': {**selector, LABEL: installation}}, 'spec': {
                    'automountServiceAccountToken': False,
                    'nodeSelector': {'kubernetes.io/hostname': c['node_name']},
                    'securityContext': {'runAsUser': 70, 'runAsGroup': 70, 'fsGroup': 70, 'runAsNonRoot': True,
                        'seccompProfile': {'type': 'RuntimeDefault'}},
                    'terminationGracePeriodSeconds': 60,
                    'containers': [{'name': 'postgres', 'image': PINS['postgres'],
                        'args': ['-c', 'shared_buffers=32MB', '-c', 'max_connections=30', '-c', 'work_mem=2MB',
                            '-c', 'maintenance_work_mem=32MB', '-c', 'wal_buffers=4MB', '-c', 'password_encryption=scram-sha-256',
                            '-c', 'log_statement=none', '-c', 'log_min_error_statement=panic'],
                        'securityContext': {'allowPrivilegeEscalation': False, 'capabilities': {'drop': ['ALL']}},
                        'env': [{'name': 'POSTGRES_USER', 'value': 'hakopod'}, {'name': 'POSTGRES_DB', 'value': 'hakopod'},
                            {'name': 'PGDATA', 'value': '/var/lib/postgresql/data/pgdata'},
                            {'name': 'POSTGRES_PASSWORD', 'valueFrom': {'secretKeyRef': {'name': 'postgres', 'key': 'password'}}},
                            {'name': 'POSTGRES_INITDB_ARGS', 'value': '--auth-host=scram-sha-256 --auth-local=trust'}],
                        'ports': [{'containerPort': 5432}],
                        'resources': {'requests': {'cpu': '50m', 'memory': '96Mi'},
                            'limits': {'cpu': '500m', 'memory': str(c['postgres_memory_mib']) + 'Mi'}},
                        'startupProbe': {'exec': {'command': ['pg_isready', '-U', 'hakopod']}, 'periodSeconds': 3, 'failureThreshold': 40},
                        'readinessProbe': {'exec': {'command': ['pg_isready', '-U', 'hakopod']}, 'periodSeconds': 5},
                        'volumeMounts': [{'name': 'data', 'mountPath': '/var/lib/postgresql/data'}]}],
                    'volumes': [{'name': 'data', 'persistentVolumeClaim': {'claimName': 'postgres'}}]}}})]
    emit('postgres.json', json.dumps({'apiVersion': 'v1', 'kind': 'List', 'items': pg}, indent=2) + '\n')
    if c['acme'] != 'off':
        endpoint = 'https://acme-staging-v02.api.letsencrypt.org/directory' if c['acme'] == 'staging' else 'https://acme-v02.api.letsencrypt.org/directory'
        emit('issuer.json', json.dumps(obj('ClusterIssuer', 'hakopod-acme', spec={'acme': {
            'email': c['acme_email'], 'server': endpoint, 'privateKeySecretRef': {'name': 'hakopod-acme-account'},
            'solvers': [{'http01': {'ingress': {'ingressClassName': 'haproxy'}}}]
        }}), indent=2) + '\n')
    def environment(values):
        # Inputs were strictly validated; quote anyway for systemd EnvironmentFile.
        return ''.join(key + '=' + json.dumps(str(value)) + '\n' for key, value in values.items())
    emit('api.env', environment({
        'GOMEMLIMIT': '192MiB', 'GOMAXPROCS': '2',
        'HAKOPOD_DATABASE_URL': 'postgres://hakopod:' + password + '@10.43.0.20:5432/hakopod?sslmode=disable',
        'HAKOPOD_KUBECONFIG': '/etc/hakopod/api-kubeconfig',
        'HAKOPOD_APP_DOMAIN': c['app_domain'], 'HAKOPOD_INGRESS_CLASS': 'haproxy',
        'HAKOPOD_PUBLIC_PORT': '80', 'HAKOPOD_PUBLIC_HTTPS_PORT': '443',
        'HAKOPOD_K3S_SUPERVISOR_URL': 'https://' + c['supervisor_host'] + ':6443',
        'HAKOPOD_LISTEN': '127.0.0.1:8080', 'HAKOPOD_WEB_ORIGIN': c['dashboard_origin'],
        'HAKOPOD_SETUP_SECRET_FILE': '/etc/hakopod/secrets/setup-token',
        'HAKOPOD_AUTH_ENCRYPTION_KEY_FILE': '/etc/hakopod/secrets/auth-encryption-key',
        'HAKOPOD_TLS_ISSUER': 'hakopod-acme' if c['acme'] != 'off' else '',
    }))
    dashboard_env = dict(NODE_ENV='production', HOST='127.0.0.1' if c['dashboard_mode'] == 'ssh' else c['node_ip'],
        PORT=c['dashboard_port'], HAKOPOD_API_URL='http://127.0.0.1:8080',
        HAKOPOD_WEB_ORIGIN=c['dashboard_origin'], HAKOPOD_SESSION_SECRET=session_secret)
    if c['dashboard_mode'] == 'https': dashboard_env.update(HAKOPOD_DASHBOARD_TLS_CERT='/etc/hakopod/dashboard.crt', HAKOPOD_DASHBOARD_TLS_KEY='/etc/hakopod/dashboard.key')
    emit('dashboard.env', environment(dashboard_env))
    common = f'# Hakopod installation {installation}\n'
    emit('hakopod-k3s.service', common + f'''[Unit]
Description=Hakopod dedicated K3s server
Wants=network-online.target
After=network-online.target
StartLimitIntervalSec=60
StartLimitBurst=5

[Service]
Type=notify
ExecStart=/opt/hakopod/tools/k3s server --config /etc/hakopod/k3s.yaml
ExecStartPre=/sbin/modprobe br_netfilter
ExecStartPre=/sbin/modprobe overlay
Environment=GOMEMLIMIT={min(1536, c['k3s_memory_mib'] * 3 // 4)}MiB GOMAXPROCS=2
Delegate=yes
KillMode=process
Restart=on-failure
RestartSec=5
TimeoutStartSec=300
TimeoutStopSec=60
LimitNOFILE=1048576
TasksMax=4096
MemoryAccounting=yes
MemoryMax={c['k3s_memory_mib']}M
CPUAccounting=yes
CPUQuota=200%

[Install]
WantedBy=multi-user.target
''')
    for name, memory, command in (
        ('api', c['api_memory_mib'], '/opt/hakopod/current/hakopod-server'),
        ('dashboard', c['dashboard_memory_mib'], '/opt/hakopod/tools/node/bin/node --max-old-space-size=192 /opt/hakopod/current/dashboard/serve.mjs')):
        emit('hakopod-' + name + '.service', common + f'''[Unit]
Description=Hakopod {name}
After=network-online.target hakopod-k3s.service
Wants=network-online.target hakopod-k3s.service
StartLimitIntervalSec=0

[Service]
Type=simple
User=hakopod-{name}
Group=hakopod-{name}
WorkingDirectory=/opt/hakopod/current
EnvironmentFile=/etc/hakopod/{name}.env
ExecStart={command}
Restart=on-failure
RestartSec=5
TimeoutStopSec=30
UMask=0077
NoNewPrivileges=yes
PrivateTmp=yes
ProtectSystem=strict
ProtectHome=yes
ProtectKernelTunables=yes
ProtectKernelModules=yes
ProtectControlGroups=yes
RestrictSUIDSGID=yes
RestrictAddressFamilies=AF_UNIX AF_INET AF_INET6
CapabilityBoundingSet=
LockPersonality=yes
LimitNOFILE=4096
TasksMax=128
MemoryAccounting=yes
MemoryMax={memory}M
CPUQuota=100%

[Install]
WantedBy=multi-user.target
''')

def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument('action', choices=('plan', 'config', 'preflight', 'prepare', 'render', 'unpack', 'pin', 'owned', 'complete'))
    p.add_argument('--config'); p.add_argument('--arch'); p.add_argument('--artifact-dir'); p.add_argument('--resume', action='store_true')
    p.add_argument('--source'); p.add_argument('--destination'); p.add_argument('--root'); p.add_argument('--name'); p.add_argument('--id')
    a = p.parse_args()
    if a.action == 'unpack': return unpack(a.source, a.destination, a.root)
    if a.action == 'pin':
        pin = PINS[a.name][architecture(a.arch)]; print(pin['url'] + '\t' + pin['sha256']); return
    if a.action == 'owned': return owned(a.source, a.id)
    if a.action == 'complete':
        marker = read_json('/etc/hakopod/installation.json'); marker['completed'] = True
        write('/etc/hakopod/installation.json', json.dumps(marker) + '\n'); return
    c = config(a.config); arch = architecture(a.arch)
    if a.action == 'config':
        for key, value in c.items(): print(key + '\t' + (str(value).lower() if isinstance(value, bool) else str(value)))
    elif a.action == 'plan': plan(c, arch, a.artifact_dir)
    elif a.action == 'preflight': preflight(c, arch, a.resume)
    elif a.action == 'prepare': prepare(c, arch, a.artifact_dir, a.resume)
    elif a.action == 'render': render(c, arch, a.id, Path(a.destination))


if __name__ == '__main__':
    try: main()
    except (ValueError, OSError, KeyError, subprocess.CalledProcessError, tarfile.TarError) as error:
        print('Installer: ' + str(error), file=sys.stderr); sys.exit(1)
