#!/usr/bin/env python3
"""Plan Oracle Database Free public-TCPS acceptance without changing the cluster.

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
import shutil
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
MEMBER_CPU_MILLIS = 1000
MEMBER_MEMORY_BYTES = 4 * 1024 ** 3
PROBE_CPU_MILLIS = 250
PROBE_MEMORY_BYTES = 256 * 1024 ** 2
STORAGE_BYTES = 10 * 1024 ** 3
STORAGE_HEADROOM_BYTES = 8 * 1024 ** 3
HAPROXY_CHART_SHA256 = 'cf1869175352e87866b2caf8c58c31fda32bf763d7e87734e229fba14ecee9ca'
HAPROXY_IMAGE = ('docker.io/haproxytech/kubernetes-ingress:3.2.15@sha256:'
                 '6185ab228aa6a8f56fd8909e55fa4bbf82f6c765a28fe7e6cea69a7668f487e8')
ORACLE_FREE_IMAGE = ('container-registry.oracle.com/database/free:23.26.3.0@sha256:'
                     'f988b0c04c4c386cd306a2a914c0d7a9702d83acc31b064a28ad8eb6278a8fba')
IMAGE = re.compile(r'^[A-Za-z0-9._:/-]+@sha256:[a-f0-9]{64}$')
CONFIG_FIELDS = (
    'schema_version', 'postgres_admin_url_file', 'psql', 'kubeconfig', 'kubectl',
    'qualification_binary', 'withdrawal_binary', 'fault_proxy_binary', 'qualification_approval_file',
    'withdrawal_approval_file',
    'probe_image', 'probe_command', 'app_domain', 'public_address', 'public_domain', 'public_port',
    'tls_issuer', 'ingress_class', 'proxy_namespace', 'proxy_configmap', 'proxy_release',
    'allowed_node', 'denied_node', 'server_environment', 'docker', 'helm', 'haproxy_chart',
    'host_budget_file', 'external_probe_command', 'external_probe_config_file',
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
    path = source / 'scripts' / 'run-development-oracle-public-endpoint-acceptance.py'
    spec = importlib.util.spec_from_file_location('oracle_public_endpoint_acceptance', path)
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
            grant['purpose'] != 'oracle-free-public-tcps' or not isinstance(grant['approvals'], list) or
            len(grant['approvals']) != 2 or set(grant['approvals']) != {'root', 'oracle_transition_review'} or
            expires <= now or expires > now + timedelta(hours=8) or
            not isinstance(grant['nonce'], str) or not re.fullmatch(r'[a-f0-9]{32}', grant['nonce'])):
        raise ValueError('a current root and Oracle validation lane grant is required')
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
    chart_input = Path(config['haproxy_chart'])
    binary_inputs = {name: Path(config[name]) for name in ('qualification_binary', 'withdrawal_binary',
                                                           'fault_proxy_binary')}
    external_input = Path(config['external_probe_command'])
    for name in ('psql', 'kubectl', 'qualification_binary', 'withdrawal_binary', 'fault_proxy_binary', 'docker', 'helm',
                 'external_probe_command'):
        config[name] = str(executable(config[name], name))
    binaries = {name: Path(config[name]) for name in binary_inputs}
    if any(binary_inputs[name].is_symlink() or not binary.is_relative_to(source) or
           binary.stat().st_uid != os.getuid() for name, binary in binaries.items()):
        raise ValueError('server and fault-proxy binaries must be owned regular files inside the isolated source snapshot')
    if (len(set(binaries.values())) != len(binaries) or
            len({file_hash(binary) for binary in binaries.values()}) != len(binaries)):
        raise ValueError('qualification, withdrawal and fault-proxy binaries must be separate hashed builds')
    external = Path(config['external_probe_command'])
    if external_input.is_symlink() or external.stat().st_uid != os.getuid():
        raise ValueError('external probe provider must be an owned executable')
    chart = Path(config['haproxy_chart']).resolve()
    if chart_input.is_symlink() or not chart.is_relative_to(source) or file_hash(chart) != HAPROXY_CHART_SHA256:
        raise ValueError('HAProxy chart is not the reviewed 1.54.0 archive')
    config['haproxy_chart'] = str(chart)
    for name, maximum in (('postgres_admin_url_file', 8192), ('kubeconfig', 1024 * 1024),
                          ('host_budget_file', 1024 * 1024), ('external_probe_config_file', 64 * 1024),
                          ('qualification_approval_file', 16 * 1024), ('withdrawal_approval_file', 16 * 1024)):
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
                 'HAKOPOD_AUTH_ENCRYPTION_KEY_FILE', 'HAKOPOD_CONFIG_FILE',
                 'HAKOPOD_ORACLE_FREE_PUBLIC_ENDPOINTS_QUALIFIED'}
    if forbidden.intersection(config['server_environment']):
        raise ValueError('runner-owned server settings cannot be overridden')
    if source == root or not source.is_relative_to(root) or 'public-endpoint' not in source.name:
        raise ValueError('use an isolated public-endpoint source snapshot below the scratch root')
    return config


def validate_binary_approvals(config, source_manifest, now=None):
    now = now or datetime.now(timezone.utc)
    fields = ('schema_version', 'purpose', 'binary_sha256', 'source_manifest_sha256', 'gate',
              'reviewed_by', 'approved_at', 'expires_at', 'nonce')
    result = {}
    cases = (('qualification', True, 'oracle-free-public-tcps-qualification'),
             ('withdrawal', False, 'oracle-free-public-tcps-withdrawal'))
    for name, gate, purpose in cases:
        value = strict_json(Path(config[name + '_approval_file']), fields, 16 * 1024)
        try:
            approved = datetime.fromisoformat(value['approved_at'].replace('Z', '+00:00'))
            expires = datetime.fromisoformat(value['expires_at'].replace('Z', '+00:00'))
        except (AttributeError, TypeError, ValueError):
            raise ValueError('binary approval timestamp is invalid') from None
        source_matches = (value['source_manifest_sha256'] == source_manifest if name == 'withdrawal' else
                          re.fullmatch(r'[a-f0-9]{64}', value['source_manifest_sha256']) and
                          value['source_manifest_sha256'] != source_manifest)
        if (value['schema_version'] != 1 or value['purpose'] != purpose or value['gate'] is not gate or
                value['binary_sha256'] != file_hash(Path(config[name + '_binary'])) or
                not source_matches or
                value['reviewed_by'] != ['root', 'oracle_transition_review'] or
                approved.tzinfo is None or expires.tzinfo is None or approved > now or
                approved < now - timedelta(hours=8) or expires <= now or expires > now + timedelta(hours=8) or
                not isinstance(value['nonce'], str) or not re.fullmatch(r'[a-f0-9]{32}', value['nonce'])):
            raise ValueError('separate current root approvals are required for both gate states')
        result[name] = {'approval_sha256': file_hash(Path(config[name + '_approval_file'])),
                        'binary_sha256': value['binary_sha256'],
                        'source_manifest_sha256': value['source_manifest_sha256'],
                        'gate': gate, 'approved_at': value['approved_at'], 'expires_at': value['expires_at']}
    return result


def inspect_external_provider(config, now=None):
    """Require a fresh attestation for a probe outside the development cluster."""
    now = now or datetime.now(timezone.utc)
    raw = command([config['external_probe_command'], '--preflight',
                   config['external_probe_config_file']], maximum=16 * 1024, timeout=30)
    value = json.loads(raw)
    fields = {'schema_version', 'provider', 'source_cidr', 'outside_development_cluster',
              'inventory_observed_at', 'expires_at', 'nonce'}
    if not isinstance(value, dict) or set(value) != fields:
        raise ValueError('external probe attestation has missing or unknown fields')
    try:
        observed = datetime.fromisoformat(value['inventory_observed_at'].replace('Z', '+00:00'))
        expires = datetime.fromisoformat(value['expires_at'].replace('Z', '+00:00'))
        network = ipaddress.ip_network(value['source_cidr'], strict=True)
    except (AttributeError, TypeError, ValueError):
        raise ValueError('external probe attestation is invalid') from None
    node_ips = {ipaddress.ip_address(config['public_address'])}
    if (value['schema_version'] != 1 or value['outside_development_cluster'] is not True or
            not isinstance(value['provider'], str) or not re.fullmatch(r'[a-z][a-z0-9_-]{1,63}', value['provider']) or
            not isinstance(value['nonce'], str) or not re.fullmatch(r'[a-f0-9]{32}', value['nonce']) or
            network.version != 4 or network.num_addresses != 1 or network.is_loopback or
            network.is_link_local or network.is_multicast or any(address in network for address in node_ips) or
            observed.tzinfo is None or expires.tzinfo is None or observed > now or
            observed < now - timedelta(minutes=5) or expires <= now or expires > now + timedelta(hours=2)):
        raise ValueError('a fresh external probe outside the development cluster is required')
    return value, hashlib.sha256(raw).hexdigest()


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


def plan_oracle_peak(nodes, allowed_node):
    """Reserve a conservative old-plus-replacement Oracle peak and both local probes."""
    result = {name: {'cpu_millis': PROBE_CPU_MILLIS, 'memory_bytes': PROBE_MEMORY_BYTES,
                     'oracle_members': 0, 'replacement_members': 0, 'probes': 1}
              for name in NODES}
    result[allowed_node]['cpu_millis'] += 2 * MEMBER_CPU_MILLIS
    result[allowed_node]['memory_bytes'] += 2 * MEMBER_MEMORY_BYTES
    result[allowed_node]['oracle_members'] = 1
    result[allowed_node]['replacement_members'] = 1
    for name in NODES:
        remaining = nodes[name]['remaining_before_plan']
        if (result[name]['cpu_millis'] > remaining['cpu_millis'] or
                result[name]['memory_bytes'] > remaining['memory_bytes']):
            raise ValueError('existing requests leave no capacity for Oracle replacement and probes')
    return result


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
        active = [pod for pod in pods if pod.get('status', {}).get('phase') not in ('Succeeded', 'Failed') and
                  (pod.get('metadata', {}).get('namespace', '').startswith('hdb-') or
                   pod.get('metadata', {}).get('labels', {}).get('hakopod.io/development-fixture'))]
        if active:
            raise ValueError('the Oracle validation lane contains active database or acceptance workloads')
        occupied = pod_requests(pods)
        budget = available_node_capacity(docker_capacity, capacity, occupied)
        result[name] = {'internal_ip': addresses['InternalIP'], 'kubelet_version': info.get('kubeletVersion', ''),
                        **budget}
    server_ip = result['k3d-hakopod-dev-server-0']['internal_ip']
    if config['public_address'] != server_ip:
        raise ValueError('public endpoint address must be the named development server node InternalIP')
    peak = plan_oracle_peak(result, config['allowed_node'])
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


def oracle_shape(allowed_node):
    return {'engine': 'oracle', 'edition': 'free', 'version': '23.26', 'mode': 'standalone',
            'members': 1, 'statefulsets': 1, 'claims': 2, 'volumes': 2,
            'storage_gib_per_claim': 10, 'placement_node': allowed_node,
            'scratch_free_bytes_required': 2 * STORAGE_BYTES + STORAGE_HEADROOM_BYTES}


def inspect_storage(config, shape):
    """Resolve the real local-path backing filesystem without creating storage."""
    kube = [config['kubectl'], '--kubeconfig', config['kubeconfig'], '--context', CONTEXT]
    classes = kube_json(kube, ['get', 'storageclasses'])['items']
    defaults = [item for item in classes if item.get('metadata', {}).get('annotations', {}).get(
        'storageclass.kubernetes.io/is-default-class') == 'true']
    if len(defaults) != 1 or defaults[0].get('provisioner') != 'rancher.io/local-path':
        raise ValueError('Oracle acceptance requires one known local-path default storage class')
    cm = kube_json(kube, ['-n', 'kube-system', 'get', 'configmap', 'local-path-config'])
    mapping = json.loads(cm.get('data', {}).get('config.json', '{}')).get('nodePathMap')
    if not isinstance(mapping, list) or not 1 <= len(mapping) <= 16:
        raise ValueError('local-path node storage mapping is missing or unbounded')
    containers = json.loads(command([config['docker'], 'inspect', *NODES], maximum=2 * 1024 * 1024))
    if len(containers) != 2:
        raise ValueError('development storage container inventory changed')
    result = {}
    for name in NODES:
        candidates = [item for item in containers if item.get('Name') == '/' + name]
        entries = [item for item in mapping if item.get('node') == name]
        if not entries:
            entries = [item for item in mapping if item.get('node') == 'DEFAULT_PATH_FOR_NON_LISTED_NODES']
        if len(candidates) != 1 or len(entries) != 1 or len(entries[0].get('paths', [])) != 1:
            raise ValueError('development storage identity changed')
        destination = Path(entries[0]['paths'][0])
        if not destination.is_absolute() or '..' in destination.parts or destination == Path('/'):
            raise ValueError('local-path storage directory is invalid')
        container = candidates[0]
        mounts = [mount for mount in container.get('Mounts', []) if isinstance(mount.get('Destination'), str) and
                  destination.is_relative_to(Path(mount['Destination']))]
        if mounts:
            mount = max(mounts, key=lambda item: len(item['Destination']))
            host_path = Path(mount['Source']) / destination.relative_to(Path(mount['Destination']))
        else:
            merged = container.get('GraphDriver', {}).get('Data', {}).get('MergedDir', '')
            if not isinstance(merged, str) or not merged.startswith('/'):
                raise ValueError('development storage backing filesystem is unavailable')
            host_path = Path(merged) / str(destination).lstrip('/')
        if host_path.is_symlink() or not host_path.is_dir():
            raise ValueError('development storage must be an existing nonsymbolic directory')
        host_path = host_path.resolve()
        usage = shutil.disk_usage(host_path)
        required = shape['scratch_free_bytes_required'] if name == shape['placement_node'] else STORAGE_HEADROOM_BYTES
        if usage.free < required:
            raise ValueError('development storage cannot fit two Oracle claims and reserve')
        result[name] = {'container_id': container.get('Id'), 'container_path': str(destination),
                        'host_path': str(host_path), 'device': host_path.stat().st_dev,
                        'total_bytes': usage.total, 'free_bytes_observed': usage.free,
                        'required_free_bytes': required}
    return result


def same_storage(before, after):
    if not isinstance(before, dict) or set(before) != set(NODES) or set(after) != set(NODES):
        return False
    for name in NODES:
        if (before[name].get('free_bytes_observed', -1) < before[name].get('required_free_bytes', 0) or
                after[name].get('free_bytes_observed', -1) < after[name].get('required_free_bytes', 0)):
            return False
        if ({key: value for key, value in before[name].items() if key != 'free_bytes_observed'} !=
                {key: value for key, value in after[name].items() if key != 'free_bytes_observed'}):
            return False
    return True


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
                    'tcpPorts': [{'name': 'oracle-tcps-15432', 'port': PUBLIC_PORT,
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
    stem = f'oracle-public-endpoint-v{attempt}'
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
    binary_approvals = validate_binary_approvals(config, source_manifest, now)
    paths = attempt_paths(root, attempt)
    if any(Path(value).exists() or Path(value).is_symlink() for value in paths.values()):
        raise ValueError('acceptance attempt paths must all be new')
    docker_nodes, host_budget, host_budget_sha = inspect_docker(config)
    kubernetes_nodes, installed_haproxy = inspect_kubernetes(config, docker_nodes)
    shape = oracle_shape(config['allowed_node'])
    storage = inspect_storage(config, shape)
    external_probe, external_probe_sha = inspect_external_provider(config, now)
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
        'qualification_binary_sha256': file_hash(Path(config['qualification_binary'])),
        'withdrawal_binary_sha256': file_hash(Path(config['withdrawal_binary'])),
        'fault_proxy_binary_sha256': file_hash(Path(config['fault_proxy_binary'])),
        'external_probe_command_sha256': file_hash(Path(config['external_probe_command'])),
        'external_probe_config_sha256': file_hash(Path(config['external_probe_config_file'])),
        'external_probe_attestation_sha256': external_probe_sha,
        'binary_approvals': binary_approvals,
        'images': {'haproxy': HAPROXY_IMAGE, 'oracle_free': ORACLE_FREE_IMAGE,
                   'probe': config['probe_image']},
        'nodes': nodes,
        'host_budget': host_budget,
        'fixture': shape,
        'storage': storage,
        'external_probe': external_probe,
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
            'Retain the allocation and database whenever route closure, identity cleanup, or volume cleanup fails.',
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
