#!/usr/bin/env python3
"""Plan MySQL public-endpoint acceptance without changing the cluster.

This command performs read-only Kubernetes and Docker inspection plus a local
Helm render. It never applies resources, starts fixtures, pulls images, or
executes commands in a container. A successful plan is consumed by the native
acceptance runner before that runner is allowed to create anything.
"""
import argparse
from datetime import datetime, timedelta, timezone
import hashlib
import importlib.util
import ipaddress
import json
import os
from pathlib import Path
import platform
import re
import socket
import stat
import sys


def load_capacity_module():
    path = Path(__file__).with_name('development_public_endpoint_capacity.py')
    spec = importlib.util.spec_from_file_location('development_public_endpoint_capacity', path)
    if spec is None or spec.loader is None:
        raise ValueError('shared public-endpoint capacity source is unavailable')
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


capacity = load_capacity_module()


CONTEXT = 'k3d-hakopod-dev'
NODES = {
    'k3d-hakopod-dev-server-0': 'server',
    'k3d-hakopod-database-worker-0': 'agent',
}
PUBLIC_PORT = 15432
MAX_OUTPUT = 8 * 1024 * 1024
RUNNER_CPU_MILLIS = capacity.RUNNER_CPU_MILLIS
RUNNER_MEMORY_BYTES = capacity.RUNNER_MEMORY_BYTES
CONTROL_POSTGRES_CPU_MILLIS = capacity.CONTROL_POSTGRES_CPU_MILLIS
CONTROL_POSTGRES_MEMORY_BYTES = capacity.CONTROL_POSTGRES_MEMORY_BYTES
MEMBER_CPU_MILLIS = 600
MEMBER_MEMORY_BYTES = 1280 * 1024 ** 2
ROUTER_CPU_MILLIS = 100
ROUTER_MEMORY_BYTES = 128 * 1024 ** 2
PROBE_CPU_MILLIS = 100
PROBE_MEMORY_BYTES = 96 * 1024 ** 2
HAPROXY_CHART_SHA256 = 'cf1869175352e87866b2caf8c58c31fda32bf763d7e87734e229fba14ecee9ca'
HAPROXY_IMAGE = ('docker.io/haproxytech/kubernetes-ingress:3.2.15@sha256:'
                 '6185ab228aa6a8f56fd8909e55fa4bbf82f6c765a28fe7e6cea69a7668f487e8')
MYSQL_SERVER_IMAGE = ('container-registry.oracle.com/mysql/community-server:8.4.12@sha256:'
                      '7dcc4add9183664de3a214daf85a50c3ba6cccfd7534f700b6561bf5b41885be')
MYSQL_OPERATOR_IMAGE = ('container-registry.oracle.com/mysql/community-operator:26.7.0-2.3.0@sha256:'
                        '01ecaa57bf952850ff9ffd7caeb231a9c3335764fdb12488f132ea4605d8f364')
MYSQL_ROUTER_IMAGE = ('container-registry.oracle.com/mysql/community-router:8.4.10@sha256:'
                      'd704471c2bb78fa833790dc197959c004d09af5aa18f5f153ff40e7268f54043')
IMAGE = re.compile(r'^[A-Za-z0-9._:/-]+@sha256:[a-f0-9]{64}$')
CONFIG_FIELDS = (
    'schema_version', 'postgres_admin_url_file', 'psql', 'kubeconfig', 'kubectl', 'server_binary', 'fault_proxy_binary',
    'probe_image', 'probe_command', 'app_domain', 'public_address', 'public_domain', 'public_port',
    'tls_issuer', 'ingress_class', 'proxy_namespace', 'proxy_configmap', 'proxy_release',
    'allowed_node', 'denied_node', 'server_environment', 'docker', 'helm', 'haproxy_chart',
    'host_budget_file',
)


def protected(path, maximum=64 * 1024, read=True):
    descriptor = os.open(path, os.O_RDONLY | os.O_NOFOLLOW)
    try:
        info = os.fstat(descriptor)
        if (not stat.S_ISREG(info.st_mode) or info.st_uid != os.getuid() or
                info.st_mode & 0o077 or info.st_size > maximum):
            raise ValueError('protected input must be an owned regular file with mode 0600 or tighter')
        if not read:
            return b''
        blocks = []
        remaining = maximum + 1
        while remaining:
            block = os.read(descriptor, min(remaining, 8192))
            if not block:
                break
            blocks.append(block)
            remaining -= len(block)
        value = b''.join(blocks)
        if len(value) > maximum:
            raise ValueError('protected input exceeds its bound')
        return value
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


def load_acceptance(source):
    path = source / 'scripts' / 'run-development-mysql-public-endpoint-acceptance.py'
    spec = importlib.util.spec_from_file_location('mysql_public_endpoint_acceptance', path)
    if spec is None or spec.loader is None:
        raise ValueError('acceptance source is unavailable')
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def command(args, *, stdin=None, maximum=MAX_OUTPUT, timeout=30, allowed=(0,)):
    if stdin is not None and len(stdin) > 1024 * 1024:
        raise ValueError('bounded read-only preflight input is too large')
    return capacity.bounded_command(args, stdin=stdin, maximum=maximum, timeout=timeout, allowed=allowed)


def executable(value, name):
    path = Path(value).resolve()
    if not path.is_file() or not os.access(path, os.X_OK):
        raise ValueError(f'configured {name} executable is unavailable')
    return path


def valid_domain(value, maximum=220):
    if not isinstance(value, str) or not 1 <= len(value) <= maximum or value != value.lower():
        return False
    labels = value.split('.')
    return len(labels) >= 2 and all(re.fullmatch(r'[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?', item)
                                    for item in labels)


def validate_lane_grant(path, now=None):
    grant = strict_json(path, ('schema_version', 'cluster', 'purpose', 'approvals', 'expires_at', 'nonce'), 4096)
    now = now or datetime.now(timezone.utc)
    try:
        expires = datetime.fromisoformat(grant['expires_at'].replace('Z', '+00:00'))
    except (AttributeError, TypeError, ValueError):
        raise ValueError('lane grant expiry is invalid') from None
    if (expires.tzinfo is None or grant['schema_version'] != 1 or grant['cluster'] != CONTEXT or
            grant['purpose'] != 'mysql-public-endpoint' or not isinstance(grant['approvals'], list) or
            len(grant['approvals']) != 2 or set(grant['approvals']) != {'root', 'vitess_backend'} or
            expires <= now or expires > now + timedelta(hours=8) or
            not isinstance(grant['nonce'], str) or not re.fullmatch(r'[a-f0-9]{32}', grant['nonce'])):
        raise ValueError('a current root and Vitess lane grant is required')
    return grant, expires


def validate_config(path, root, source):
    config = strict_json(path, CONFIG_FIELDS)
    if (config['schema_version'] != 1 or not isinstance(config.get('probe_image'), str) or
            not IMAGE.fullmatch(config['probe_image'])):
        raise ValueError('config requires schema version 1 and a digest-pinned probe image')
    if (config['public_port'] != PUBLIC_PORT or config['allowed_node'] not in NODES or
            config['denied_node'] not in NODES or config['allowed_node'] == config['denied_node']):
        raise ValueError('config must use port 15432 and the two distinct named development nodes')
    try:
        address = ipaddress.ip_address(config['public_address'])
    except ValueError:
        raise ValueError('public endpoint address is invalid') from None
    if (address.version != 4 or address.is_unspecified or address.is_multicast or
            not valid_domain(config['public_domain']) or not valid_domain(config['app_domain'], 190) or
            not isinstance(config['probe_command'], str) or not config['probe_command'].startswith('/') or
            len(config['probe_command']) > 256):
        raise ValueError('public endpoint inventory is invalid')
    binary_inputs = {name: Path(config[name]) for name in ('server_binary', 'fault_proxy_binary')}
    chart_input = Path(config['haproxy_chart'])
    for name in ('psql', 'kubectl', 'server_binary', 'fault_proxy_binary', 'docker', 'helm'):
        config[name] = str(executable(config[name], name))
    for name, original in binary_inputs.items():
        binary = Path(config[name])
        if original.is_symlink() or not binary.is_relative_to(source) or binary.stat().st_uid != os.getuid():
            raise ValueError('server and fault proxy binaries must be owned regular files inside the isolated source snapshot')
    chart = Path(config['haproxy_chart']).resolve()
    if chart_input.is_symlink() or not chart.is_relative_to(source) or file_hash(chart) != HAPROXY_CHART_SHA256:
        raise ValueError('HAProxy chart is not the reviewed 1.54.0 archive')
    config['haproxy_chart'] = str(chart)
    for name, maximum in (('postgres_admin_url_file', 8192), ('kubeconfig', 1024 * 1024),
                          ('host_budget_file', 1024 * 1024)):
        supplied = Path(config[name])
        if supplied.is_symlink():
            raise ValueError(f'protected {name} cannot be a symbolic link')
        target = supplied.resolve()
        protected(target, maximum, read=False)
        config[name] = str(target)
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
    if source == root or not source.is_relative_to(root) or 'public-endpoint' not in source.name:
        raise ValueError('use an isolated public-endpoint source snapshot below the scratch root')
    return config


parse_cpuset = capacity.parse_cpuset


def inspect_docker(config):
    budget_bytes = protected(Path(config['host_budget_file']), 1024 * 1024)
    budget = json.loads(budget_bytes)
    capacity.validate_budget(budget, CONTEXT)
    nodes, evidence = capacity.inspect(config['docker'], command, budget, NODES, context=CONTEXT)
    return nodes, evidence, hashlib.sha256(budget_bytes).hexdigest()

def cpu_millis(value):
    if not isinstance(value, str) or not value:
        raise ValueError('Kubernetes CPU quantity is invalid')
    try:
        if value.endswith('m'):
            return int(value[:-1])
        if value.endswith('u'):
            return (int(value[:-1]) + 999) // 1000
        if value.endswith('n'):
            return (int(value[:-1]) + 999999) // 1000000
        return int(float(value) * 1000)
    except ValueError:
        raise ValueError('Kubernetes CPU quantity is invalid') from None


def memory_bytes(value):
    if not isinstance(value, str) or not value:
        raise ValueError('Kubernetes memory quantity is invalid')
    units = {'Ki': 1024, 'Mi': 1024 ** 2, 'Gi': 1024 ** 3,
             'K': 1000, 'M': 1000 ** 2, 'G': 1000 ** 3}
    match = re.fullmatch(r'([0-9]+)(Ki|Mi|Gi|K|M|G)?', value)
    if not match:
        raise ValueError('Kubernetes memory quantity is invalid')
    return int(match.group(1)) * units.get(match.group(2), 1)


def pod_requests(items):
    cpu = memory = 0
    for pod in items:
        if pod.get('status', {}).get('phase') in ('Succeeded', 'Failed'):
            continue
        spec = pod.get('spec', {})
        app_cpu = app_memory = 0
        for container in spec.get('containers', []):
            requests = container.get('resources', {}).get('requests', {})
            app_cpu += cpu_millis(requests.get('cpu', '0'))
            app_memory += memory_bytes(requests.get('memory', '0'))
        # This deliberately overestimates ordinary sequential init containers
        # so restartable init sidecars cannot disappear from the admission sum.
        init_cpu = init_memory = 0
        for container in spec.get('initContainers', []):
            requests = container.get('resources', {}).get('requests', {})
            init_cpu += cpu_millis(requests.get('cpu', '0'))
            init_memory += memory_bytes(requests.get('memory', '0'))
        overhead = spec.get('overhead', {})
        cpu += app_cpu + init_cpu + cpu_millis(overhead.get('cpu', '0'))
        memory += app_memory + init_memory + memory_bytes(overhead.get('memory', '0'))
    return {'cpu_millis': cpu, 'memory_bytes': memory}


def available_node_capacity(docker_capacity, allocatable, occupied):
    if (allocatable['cpu_millis'] > docker_capacity['cpu_millis'] or
            allocatable['memory_bytes'] > docker_capacity['memory_bytes']):
        raise ValueError('Kubernetes allocatable capacity exceeds the Docker container limit')
    remaining = {key: allocatable[key] - occupied[key] for key in allocatable}
    if any(value < 0 for value in remaining.values()):
        raise ValueError('existing pod requests exceed the node capacity')
    return {'allocatable': allocatable, 'occupied_requests': occupied,
            'remaining_before_plan': remaining}


def plan_mysql_peak(nodes):
    names = list(NODES)
    capacities = {name: nodes[name]['remaining_before_plan'].copy() for name in names}
    for name in names:
        capacities[name]['cpu_millis'] -= PROBE_CPU_MILLIS
        capacities[name]['memory_bytes'] -= PROBE_MEMORY_BYTES
        if min(capacities[name].values()) < 0:
            raise ValueError('a fixed endpoint probe does not fit on each development node')
    workloads = ([('member', MEMBER_CPU_MILLIS, MEMBER_MEMORY_BYTES)] * 3 +
                 [('router', ROUTER_CPU_MILLIS, ROUTER_MEMORY_BYTES)] * 2 +
                 [('surge_member', MEMBER_CPU_MILLIS, MEMBER_MEMORY_BYTES),
                  ('surge_router', ROUTER_CPU_MILLIS, ROUTER_MEMORY_BYTES)])
    best = None
    for mask in range(1 << len(workloads)):
        used = {name: {'cpu_millis': PROBE_CPU_MILLIS, 'memory_bytes': PROBE_MEMORY_BYTES,
                       'members': 0, 'routers': 0, 'surge_members': 0, 'surge_routers': 0}
                for name in names}
        valid = True
        for index, (kind, cpu, memory) in enumerate(workloads):
            name = names[(mask >> index) & 1]
            used[name]['cpu_millis'] += cpu
            used[name]['memory_bytes'] += memory
            used[name][kind.replace('surge_member', 'surge_members').replace('surge_router', 'surge_routers') +
                       ('' if kind.startswith('surge_') else 's')] += 1
        for name in names:
            if (used[name]['cpu_millis'] > nodes[name]['remaining_before_plan']['cpu_millis'] or
                    used[name]['memory_bytes'] > nodes[name]['remaining_before_plan']['memory_bytes']):
                valid = False
                break
        if valid:
            score = max(used[name]['cpu_millis'] / nodes[name]['remaining_before_plan']['cpu_millis'] +
                        used[name]['memory_bytes'] / nodes[name]['remaining_before_plan']['memory_bytes']
                        for name in names)
            if best is None or score < best[0]:
                best = score, used
    if best is None:
        raise ValueError('existing requests leave no placement for three MySQL members, two Routers and rollout surge')
    return best[1]


def kube_json(kube, args, missing=False):
    suffix = ['--ignore-not-found'] if missing else []
    raw = command(kube + args + suffix + ['-o', 'json'], maximum=2 * 1024 * 1024, timeout=20)
    if not raw and missing:
        return None
    return json.loads(raw)


def validate_installed_haproxy(deployment, proxy_config, crd, release):
    labels = deployment.get('metadata', {}).get('labels', {})
    pod_spec = deployment.get('spec', {}).get('template', {}).get('spec', {})
    containers = pod_spec.get('containers', [])
    host_ports = [port['hostPort'] for container in containers for port in container.get('ports', [])
                  if isinstance(port.get('hostPort'), int) and not isinstance(port.get('hostPort'), bool)]
    images = [container.get('image') for container in containers]
    if (deployment.get('spec', {}).get('replicas') != 1 or labels.get('app.kubernetes.io/managed-by') != 'Helm' or
            labels.get('app.kubernetes.io/instance') != release or
            pod_spec.get('nodeSelector', {}).get('kubernetes.io/hostname') != 'k3d-hakopod-dev-server-0' or
            host_ports != [PUBLIC_PORT] or images != [HAPROXY_IMAGE]):
        raise ValueError('installed HAProxy ownership, image, placement or host ports differ from the reviewed plan')
    config_labels = proxy_config.get('metadata', {}).get('labels', {})
    if (config_labels.get('app.kubernetes.io/managed-by') != 'Helm' or
            config_labels.get('app.kubernetes.io/instance') != release):
        raise ValueError('installed HAProxy configuration ownership is invalid')
    versions = crd.get('spec', {}).get('versions', [])
    if (crd.get('spec', {}).get('group') != 'ingress.v3.haproxy.org' or
            crd.get('spec', {}).get('names', {}).get('plural') != 'tcps' or
            not any(item.get('name') == 'v3' and item.get('served') for item in versions)):
        raise ValueError('the reviewed HAProxy TCP CRD is unavailable')
    return {'deployment_uid': deployment.get('metadata', {}).get('uid', ''),
            'configmap_uid': proxy_config.get('metadata', {}).get('uid', ''),
            'tcp_crd_uid': crd.get('metadata', {}).get('uid', '')}


def inspect_kubernetes(config, docker_nodes):
    kube = [config['kubectl'], '--kubeconfig', config['kubeconfig'], '--context', CONTEXT]
    current = command(kube + ['config', 'current-context'], maximum=128, timeout=10).decode().strip()
    if current != CONTEXT:
        raise ValueError('named development context is required')
    items = kube_json(kube, ['get', 'nodes', *NODES]).get('items', [])
    if len(items) != 2 or {item.get('metadata', {}).get('name') for item in items} != set(NODES):
        raise ValueError('both named development nodes are required')
    result = {}
    for node in items:
        metadata, status = node.get('metadata', {}), node.get('status', {})
        name = metadata.get('name')
        conditions = {item.get('type'): item.get('status') for item in status.get('conditions', [])}
        info = status.get('nodeInfo', {})
        addresses = {item.get('type'): item.get('address') for item in status.get('addresses', [])}
        if (metadata.get('labels', {}).get('kubernetes.io/hostname') != name or
                info.get('architecture') != 'amd64' or info.get('operatingSystem') != 'linux' or
                node.get('spec', {}).get('unschedulable') or conditions.get('Ready') != 'True' or
                any(conditions.get(key) != 'False' for key in ('DiskPressure', 'MemoryPressure', 'PIDPressure')) or
                not addresses.get('InternalIP')):
            raise ValueError('development node identity or health is invalid')
        allocatable = status.get('allocatable', {})
        capacity = {'cpu_millis': cpu_millis(allocatable.get('cpu', '')),
                    'memory_bytes': memory_bytes(allocatable.get('memory', ''))}
        docker_capacity = docker_nodes[name]
        pods = kube_json(kube, ['get', 'pods', '--all-namespaces', '--field-selector', f'spec.nodeName={name}']).get('items', [])
        if len(pods) > 1024:
            raise ValueError('scheduled pod inventory exceeds its bound')
        occupied = pod_requests(pods)
        budget = available_node_capacity(docker_capacity, capacity, occupied)
        result[name] = {'internal_ip': addresses['InternalIP'], 'kubelet_version': info.get('kubeletVersion', ''),
                        **budget}
    server_ip = result['k3d-hakopod-dev-server-0']['internal_ip']
    if config['public_address'] != server_ip:
        raise ValueError('public endpoint address must be the named development server node InternalIP')
    peak = plan_mysql_peak(result)
    for name in NODES:
        result[name]['planned_peak'] = peak[name]
        result[name]['remaining_after_peak'] = {
            key: result[name]['remaining_before_plan'][key] - peak[name][key]
            for key in ('cpu_millis', 'memory_bytes')}
    deployment = kube_json(kube, ['-n', config['proxy_namespace'], 'get', 'deployment',
                                  config['proxy_configmap']])
    proxy_config = kube_json(kube, ['-n', config['proxy_namespace'], 'get', 'configmap',
                                    config['proxy_configmap']])
    crd = kube_json(kube, ['get', 'customresourcedefinition', 'tcps.ingress.v3.haproxy.org'])
    installed = validate_installed_haproxy(deployment, proxy_config, crd, config['proxy_release'])
    claim = kube_json(kube, ['-n', config['proxy_namespace'], 'get', 'configmap',
                             f'hakopod-tcp-port-{PUBLIC_PORT}'], missing=True)
    if claim is not None:
        raise ValueError('public endpoint port 15432 already has an allocation claim')
    return result, installed


def render_values(config):
    return {'controller': {
        'replicaCount': 1,
        'image': {'repository': 'docker.io/haproxytech/kubernetes-ingress',
                  'tag': HAPROXY_IMAGE.split(':', 1)[1]},
        'ingressClass': config['ingress_class'],
        'nodeSelector': {'kubernetes.io/hostname': 'k3d-hakopod-dev-server-0'},
        'deployment': {'useHostNetwork': False, 'useHostPort': True,
                       'hostPorts': {'http': 0, 'https': 0, 'stat': 0}},
        'service': {'enabled': True, 'type': 'ClusterIP',
                    'enablePorts': {'http': False, 'https': False, 'quic': False,
                                    'stat': False, 'admin': False},
                    'tcpPorts': [{'name': 'mysql-15432', 'port': PUBLIC_PORT,
                                  'targetPort': PUBLIC_PORT}]},
        'prometheus': {'enabled': False}, 'pprof': {'enabled': False},
    }}


def document_kind(document):
    match = re.search(r'(?m)^kind:\s*([^\s#]+)\s*$', document)
    return match.group(1) if match else ''


def validate_render(rendered):
    text = rendered.decode()
    documents = [item for item in re.split(r'(?m)^---\s*$', text) if item.strip()]
    deployments = [item for item in documents if document_kind(item) == 'Deployment']
    services = [item for item in documents if document_kind(item) == 'Service']
    if len(deployments) != 1 or len(services) != 1:
        raise ValueError('HAProxy render must contain one controller Deployment and one Service')
    deployment, service = deployments[0], services[0]
    host_ports = [int(value) for value in re.findall(r'(?m)^\s+hostPort:\s*([0-9]+)\s*$', deployment)]
    images = re.findall(r'(?m)^\s+image:\s*["\']?([^\s"\']+)["\']?\s*$', deployment)
    if (host_ports != [PUBLIC_PORT] or images != [HAPROXY_IMAGE] or
            len(re.findall(r'(?m)^\s+(?:-\s*)?containerPort:\s*15432\s*$', deployment)) != 1 or
            not re.search(r'(?m)^\s+kubernetes\.io/hostname:\s*["\']?k3d-hakopod-dev-server-0["\']?\s*$', deployment)):
        raise ValueError('HAProxy render differs from the reviewed image, node or sole host port 15432')
    service_ports = [int(value) for value in re.findall(r'(?m)^\s+port:\s*([0-9]+)\s*$', service)]
    if service_ports != [PUBLIC_PORT] or not re.search(r'(?m)^\s+type:\s*ClusterIP\s*$', service):
        raise ValueError('HAProxy Service must expose only TCP 15432 as ClusterIP')
    return hashlib.sha256(rendered).hexdigest()


def dry_render(config):
    values = json.dumps(render_values(config), sort_keys=True, separators=(',', ':')).encode()
    rendered = command([config['helm'], 'template', 'hakopod-public-endpoint-preflight',
                        config['haproxy_chart'], '--namespace', config['proxy_namespace'],
                        '--kube-version', '1.35.0', '--values', '-'], stdin=values, timeout=30)
    return validate_render(rendered)


def check_port_available(address, port):
    with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as client:
        client.settimeout(0.5)
        if client.connect_ex((address, port)) == 0:
            raise ValueError('public endpoint port 15432 already accepts connections')


def attempt_paths(root, attempt):
    stem = f'mysql-public-endpoint-v{attempt}'
    return {
        'work': str(root / (stem + '.work')),
        'events': str(root / (stem + '.jsonl')),
        'evidence': str(root / (stem + '.evidence.json')),
        'plan': str(root / (stem + '.plan.json')),
    }


def write_json_exclusive(path, value):
    encoded = json.dumps(value, indent=2, sort_keys=True).encode() + b'\n'
    descriptor = os.open(path, os.O_CREAT | os.O_EXCL | os.O_WRONLY, 0o600)
    with os.fdopen(descriptor, 'wb') as output:
        output.write(encoded)


def build_plan(root, source, config_path, grant_path, attempt):
    now = datetime.now(timezone.utc)
    grant, expires = validate_lane_grant(grant_path, now)
    config = validate_config(config_path, root, source)
    acceptance = load_acceptance(source)
    source_inventory = acceptance.source_hashes(source)
    source_manifest = hashlib.sha256(json.dumps(source_inventory, sort_keys=True,
                                                separators=(',', ':')).encode()).hexdigest()
    paths = attempt_paths(root, attempt)
    if any(Path(value).exists() or Path(value).is_symlink() for value in paths.values()):
        raise ValueError('acceptance attempt paths must all be new')
    docker_nodes, host_budget, host_budget_sha = inspect_docker(config)
    kubernetes_nodes, installed_haproxy = inspect_kubernetes(config, docker_nodes)
    check_port_available(config['public_address'], PUBLIC_PORT)
    render_sha = dry_render(config)
    nodes = {name: {**docker_nodes[name], **kubernetes_nodes[name]} for name in NODES}
    return {
        'schema_version': 1,
        'status': 'ready',
        'mutation_mode': 'read_only_preflight',
        'generated_at': now.isoformat(),
        'grant_expires_at': expires.isoformat(),
        'context': CONTEXT,
        'attempt': attempt,
        'source': str(source),
        'source_manifest_sha256': source_manifest,
        'config_sha256': file_hash(config_path),
        'host_budget_sha256': host_budget_sha,
        'lane_grant_sha256': file_hash(grant_path),
        'server_binary_sha256': file_hash(Path(config['server_binary'])),
        'fault_proxy_binary_sha256': file_hash(Path(config['fault_proxy_binary'])),
        'images': {'haproxy': HAPROXY_IMAGE, 'mysql_server': MYSQL_SERVER_IMAGE,
                   'mysql_operator': MYSQL_OPERATOR_IMAGE, 'mysql_router': MYSQL_ROUTER_IMAGE,
                   'probe': config['probe_image']},
        'nodes': nodes,
        'host_budget': host_budget,
        'haproxy': {'chart_sha256': HAPROXY_CHART_SHA256, 'render_sha256': render_sha,
                    'node': 'k3d-hakopod-dev-server-0', 'host_ports': [PUBLIC_PORT],
                    'installed': installed_haproxy},
        'attempt_paths': paths,
        'cleanup_contract': [
            'Delete the probe namespace only when its development-fixture label matches the run ID.',
            'Delete the database namespace only when its database ID and Hakopod manager labels match.',
            'Delete the TCP allocation claim only when its database and endpoint IDs match the run.',
            'Drop only the uniquely generated control database for the run.',
            'Never delete the shared HAProxy Deployment, namespace, or TCP CRD.',
        ],
    }


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--root', type=Path, default=Path('/srv/hakopod-backup-scratch/database-cockpit-20260929'))
    parser.add_argument('--source', type=Path, required=True)
    parser.add_argument('--config', type=Path, required=True)
    parser.add_argument('--lane-grant', type=Path, required=True)
    parser.add_argument('--attempt', type=int, required=True)
    args = parser.parse_args()
    root, source = args.root.resolve(), args.source.resolve()
    if (args.source.is_symlink() or args.attempt < 1 or args.attempt > 100 or
            sys.platform != 'linux' or platform.machine() not in ('x86_64', 'amd64')):
        raise ValueError('use an isolated Linux source snapshot and attempt 1 through 100')
    config_path = Path(os.path.abspath(args.config))
    grant_path = Path(os.path.abspath(args.lane_grant))
    plan = build_plan(root, source, config_path, grant_path, args.attempt)
    path = Path(plan['attempt_paths']['plan'])
    write_json_exclusive(path, plan)
    print(json.dumps({'status': 'ready', 'plan': str(path), 'host_ports': [PUBLIC_PORT]}, sort_keys=True))


if __name__ == '__main__':
    main()
