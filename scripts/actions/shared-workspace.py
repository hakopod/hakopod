#!/usr/bin/env python3
"""Opt-in disk-backed gVisor workspace checks for the disposable dev runtime.

No production installer or Pod constructor imports this module. The pinned shim
requires a bind hint: it converts an empty EmptyDir to an internal tmpfs master
backed by a named file in that same disk volume. A tmpfs input is memory-backed.
"""
import json
import fnmatch
import re
import time
import tomllib
import threading

FLAG = 'HAKOPOD_ACTIONS_SHARED_WORKSPACE'
DRIVER_FLAG = 'HAKOPOD_ACTIONS_DOCKER_STORAGE_DRIVER'
PREFIX = 'dev.gvisor.spec.mount.runner.'
KEYS = [PREFIX + name for name in ('type', 'share', 'options')]
RUNTIME_TABLE = '[plugins."io.containerd.cri.v1.runtime".containerd.runtimes.hakopod-actions]'
NODE = 'k3d-hakopod-dev-server-0'
GIB = 1024 ** 3
MIB = 1024 ** 2
PROBE_BYTES = 16 * MIB
CAPABILITY = '0100000200040000000000000000000000000000'
BUSYBOX = 'docker.io/library/busybox:1.37.0@sha256:9db7b59979c38555a39def84a31fb98b5296952f9e3afd4f6f11f05b07adfab0'
DAEMON_IMAGE = 'docker.io/library/docker:29.8.1-dind@sha256:3f3c01aaaebf7cce837356b688b7c059a4749f10bd7660dec7c58fc454a283f0'


def require(condition, message):
    if not condition:
        raise RuntimeError(message)


def enabled(environment):
    flag = environment.get(FLAG, '0')
    require(flag in ('0', '1'), 'Shared workspace flag must be 0 or 1')
    driver = environment.get(DRIVER_FLAG, 'vfs')
    require(driver in ('vfs', 'overlay2'), 'Development Docker storage driver must be vfs or overlay2')
    require(driver != 'overlay2' or flag == '1', 'Development Docker overlay2 requires the shared workspace')
    mode = environment.get('HAKOPOD_ACTIONS_SHARED_WORKSPACE_MODE')
    if mode is not None:
        modes = {'disabled': ('0', 'vfs'), 'vfs': ('1', 'vfs'), 'overlay2': ('1', 'overlay2')}
        require(mode in modes and (flag, driver) == modes[mode],
                'Shared workspace mode must be disabled, vfs, or overlay2 and match its derived selection')
    require(not (flag == '1' and any(environment.get(key) == '1' for key in
        ('HAKOPOD_ACTIONS_BUILDKIT_CANDIDATE', 'HAKOPOD_ACTIONS_PUBLISH_CANDIDATE'))),
        'Shared workspace experiments cannot build or publish candidate images')
    return flag == '1'


def storage_driver(environment):
    enabled(environment)
    return environment.get(DRIVER_FLAG, 'vfs')


def annotations(workspace_gib):
    require(type(workspace_gib) is int and workspace_gib in (2, 4, 8), 'Unexpected development workspace size')
    return dict(zip(KEYS, ['bind', 'pod', f'rw,rprivate,mode=0770,uid=1001,gid=1001,size={workspace_gib + 1}g']))


def runtime_section(section, selected):
    require(type(selected) is bool, 'Shared workspace selection must be a boolean')
    if not selected:
        return section
    require(section.count(RUNTIME_TABLE + '\n') == 1 and 'pod_annotations' not in section and 'dev.gvisor.' not in section,
            'Unexpected development runtime section')
    result = section.replace(RUNTIME_TABLE + '\n', RUNTIME_TABLE + '\n  pod_annotations = ' + json.dumps(KEYS) + '\n', 1)
    verify_runtime_config(result)
    return result


def verify_runtime_config(text):
    require(isinstance(text, str) and 0 < len(text) <= 262144, 'Unexpected rendered runtime configuration size')
    config = tomllib.loads(text)
    runtimes = config.get('plugins', {}).get('io.containerd.cri.v1.runtime', {}).get('containerd', {}).get('runtimes', {})
    runtime = runtimes.get('hakopod-actions', {})
    require(runtime.get('runtime_type') == 'io.containerd.runsc.v1' and runtime.get('pod_annotations') == KEYS,
            'Development runtime must forward exactly the three workspace mount hints')
    for name, other in runtimes.items():
        if name != 'hakopod-actions':
            require(not any(pattern.startswith('dev.gvisor.') or fnmatch.fnmatchcase(KEYS[0], pattern)
                for pattern in other.get('pod_annotations', [])), 'Other runtimes may not forward the experiment annotations')
    return {'runtime_type': runtime['runtime_type'], 'pod_annotations': runtime['pod_annotations']}


def verify_runtime_profile(text):
    require(isinstance(text, str) and len(text) <= 8192, 'Unexpected runtime profile size')
    expected = {'binary_name': '/opt/hakopod-actions-fixture/runsc', 'runsc_config': {
        'platform': 'systrap', 'net-raw': 'true', 'allow-packet-socket-write': 'true', 'overlay2': 'root:memory,size=512m'}}
    require(tomllib.loads(text) == expected, 'Shared workspace experiment changed the runtime sandbox profile')
    return expected['runsc_config']


def pod_identity(pod, driver='vfs'):
    metadata, spec = pod.get('metadata', {}), pod.get('spec', {})
    uid, namespace = metadata.get('uid', ''), metadata.get('namespace', '')
    require(re.fullmatch(r'[a-f0-9]{8}(?:-[a-f0-9]{4}){3}-[a-f0-9]{12}', uid) is not None and
            re.fullmatch(r'hp-[a-z0-9-]{1,59}', namespace) is not None and metadata.get('name') == 'actions-runtime-fixture',
            'Shared workspace requires an owned development Pod identity')
    require(spec.get('nodeName') == NODE and spec.get('runtimeClassName') == 'hakopod-actions',
            'Shared workspace escaped the named development node or runtime')
    require(driver in ('vfs', 'overlay2'), 'Unexpected development Docker storage driver')
    daemons = [item for item in spec.get('initContainers', []) if item.get('name') == 'docker']
    require(len(daemons) == 1 and daemons[0].get('image') == DAEMON_IMAGE,
            'Shared workspace Docker daemon image differs from its pinned product image')
    command = daemons[0].get('command', [])
    require(len(command) == 3 and command[:2] == ['sh', '-c'] and
            re.findall(r'--storage-driver=([^\s]+)', command[2]) == [driver] and
            '--feature=containerd-snapshotter=false' in command[2],
            'Shared workspace Docker command differs from the selected classic storage driver')
    volumes = [volume for volume in spec.get('volumes', []) if volume.get('name') == 'runner']
    require(len(volumes) == 1 and set(volumes[0]) == {'name', 'emptyDir'}, 'Shared workspace must remain an EmptyDir')
    volume = volumes[0]['emptyDir']
    sizes = {'2Gi': 2, '4Gi': 4, '8Gi': 8}
    require(volume.get('medium', '') == '' and volume.get('sizeLimit') in sizes, 'Shared workspace disk limit changed')
    size = sizes[volume['sizeLimit']]
    actual = {key: value for key, value in metadata.get('annotations', {}).items() if key.startswith('dev.gvisor.')}
    require(actual == annotations(size), 'Shared workspace Pod has unexpected runtime annotations')
    containers = spec.get('containers', [])
    require(len(containers) == 1 and containers[0].get('name') == 'runner' and
            containers[0].get('resources', {}).get('limits', {}).get('ephemeral-storage') == f'{size + 1}Gi',
            'Shared workspace runner storage limit changed')
    statuses = [status for status in pod.get('status', {}).get('containerStatuses', []) if status.get('name') == 'runner']
    require(len(statuses) == 1 and re.fullmatch(r'containerd://[a-f0-9]{64}', statuses[0].get('containerID', '')),
            'Shared workspace runner container identity is missing')
    return {'uid': uid, 'namespace': namespace, 'workspace_gib': size, 'storage_driver': driver,
            'source': f'/var/lib/kubelet/pods/{uid}/volumes/kubernetes.io~empty-dir/runner',
            'container_id': statuses[0]['containerID'].removeprefix('containerd://')}


def bounded_json(text, limit=262144):
    require(isinstance(text, str) and 0 < len(text) <= limit, 'Shared workspace observation exceeded its bound')
    return json.loads(text)


def verify_docker(value, driver, empty=False):
    info, version = value.get('info', {}), value.get('version', {})
    require(driver in ('vfs', 'overlay2') and info.get('Driver') == driver and
            info.get('DockerRootDir') == '/home/runner/.docker-data',
            'Docker did not use the selected driver and private workspace data root')
    require(version.get('Version') == '29.8.1' and version.get('GitCommit') == '464cd50' and
            version.get('Os') == 'linux' and version.get('Arch') in ('amd64', 'arm64'),
            'Docker daemon version or source differs from the pinned image')
    require(version['Arch'] == {'x86_64': 'amd64', 'aarch64': 'arm64'}.get(value.get('machine')),
            'Docker daemon architecture differs from its runner')
    if empty:
        require(info.get('Images') == 0 and info.get('Containers') == 0,
                'Storage experiments require a fresh daemon with no images or containers')
    pairs = info.get('DriverStatus') or []
    require(isinstance(pairs, list) and len(pairs) <= 16 and
            all(isinstance(pair, list) and len(pair) == 2 and all(isinstance(item, str) for item in pair) for pair in pairs),
            'Unexpected Docker storage driver status')
    status = dict(pairs)
    require(len(status) == len(pairs), 'Repeated Docker storage driver status')
    if driver == 'overlay2':
        require(status.get('Backing Filesystem') == 'tmpfs' and status.get('Supports d_type') == 'true' and
                status.get('Native Overlay Diff') == 'true',
                'Docker overlay2 requires internal tmpfs, directory types, and native overlay diff')
    return {'driver': driver, 'data_root': info['DockerRootDir'], 'version': version['Version'],
            'source_commit': version['GitCommit'], 'architecture': version['Arch'], 'driver_status': status,
            'fresh_daemon_verified': empty}


DOCKER_PROBE = """import json, platform, subprocess
def read(arguments):
    result = subprocess.run(['docker', *arguments], check=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=20)
    assert len(result.stdout) <= 131072
    return json.loads(result.stdout)
print(json.dumps({'machine': platform.machine(), 'info': read(['info', '--format', '{{json .}}']), 'version': read(['version', '--format', '{{json .Server}}'])}))
"""


def verify_mount(value, identity):
    lines = value.get('mountinfo', '').splitlines()
    require(len(lines) <= 4096, 'Workspace mount inventory exceeded its bound')
    matches = []
    for line in lines:
        parts = line.split()
        if len(parts) >= 10 and parts[4] == '/home/runner' and '-' in parts:
            separator = parts.index('-')
            require(len(parts) == separator + 4, 'Workspace mount record is malformed')
            matches.append((parts, separator))
    require(len(matches) == 1, 'Workspace must have one exact mount')
    parts, separator = matches[0]
    require(parts[separator + 1] == 'tmpfs' and 'rw' in parts[5].split(','), 'Workspace did not use the internal writable tmpfs master')
    sizes = [item[5:] for item in parts[-1].split(',') if item.startswith('size=')]
    expected = (identity['workspace_gib'] + 1) * GIB
    accepted = {str(expected), str(expected // 1024) + 'k', str(expected // MIB) + 'm', str(expected // GIB) + 'g'}
    require(len(sizes) == 1 and sizes[0] in accepted, 'Internal workspace bound differs from the explicit limit')
    require(value.get('root') == {'uid': 1001, 'gid': 1001, 'mode': 0o770} and value.get('process') == {'uid': 1001, 'gid': 1001, 'effective_capabilities': 0} and value.get('prepare') is True and value.get('ready') is True,
            'Workspace ownership, init sharing, or checkpoint is incorrect')
    return {'filesystem': 'tmpfs', 'limit_bytes': expected, 'root': value['root'], 'process': value['process'], 'prepare_visible': True}


def verify_container_source(value, identity):
    info = value.get('info', {})
    spec = info.get('runtimeSpec', {})
    if isinstance(spec, str):
        spec = bounded_json(spec)
    mounts = [mount for mount in spec.get('mounts', []) if mount.get('destination') == '/home/runner']
    require(len(mounts) == 1 and mounts[0].get('source') == identity['source'] and mounts[0].get('type') == 'bind',
            'Runner OCI workspace source differs from the owned kubelet EmptyDir')
    sandbox_id = info.get('sandboxID', '')
    require(re.fullmatch(r'[a-f0-9]{64}', sandbox_id), 'Runner sandbox identity is missing')
    return sandbox_id


def verify_sandbox(value, identity, sandbox_id):
    items = value.get('items', [])
    require(len(items) == 1 and items[0].get('id') == sandbox_id and items[0].get('state') == 'SANDBOX_READY' and
            items[0].get('metadata', {}).get('uid') == identity['uid'] and
            items[0].get('metadata', {}).get('namespace') == identity['namespace'] and
            items[0].get('metadata', {}).get('name') == 'actions-runtime-fixture',
            'Filestore sandbox does not belong to the observed development Pod')


def verify_filestore(text):
    require(isinstance(text, str) and len(text) < 1024, 'Filestore observation exceeded its bound')
    lines = text.strip().splitlines()
    require(len(lines) == 2, 'Filestore observation is incomplete')
    filesystem = lines[0].split('|')
    # BusyBox names ext4 "ext2/ext3". Match Linux filesystem identifiers, not
    # utility-specific display names, and keep unknown backing stores closed.
    require(len(filesystem) == 2 and 0 < len(filesystem[0]) <= 64 and
            filesystem[1] in ('ef53', '58465342', '9123683e'),
            'Filestore requires ext, XFS, or Btrfs node storage; observed ' + repr(lines[0][:96]))
    fields = lines[1].split('|')
    require(len(fields) == 6 and fields[0] == 'regular file' and fields[1] == '1' and
            all(re.fullmatch(r'[0-9]+', value) for value in fields[2:]), 'Filestore is not a singly linked regular file')
    device, inode, size, blocks = map(int, fields[2:])
    require(device >= 0 and inode > 0 and 0 < blocks * 512 <= size <= 64 * GIB, 'Filestore size or physical allocation is invalid')
    return {'host_filesystem': filesystem[0], 'host_filesystem_id': filesystem[1],
            'device': device, 'inode': inode, 'logical_bytes': size, 'allocated_bytes': blocks * 512}


def fs_observation(value):
    """Keep only bounded filesystem counters and timestamps, never full stats."""
    result = {}
    for key in ('name', 'time', 'usedBytes', 'capacityBytes', 'availableBytes', 'inodes', 'inodesFree', 'inodesUsed'):
        if key not in value:
            continue
        item = value[key]
        result[key] = item[:96] if isinstance(item, str) else item if item is None or type(item) is bool or type(item) is int and abs(item) <= 2 ** 64 else {'type': type(item).__name__}
    return result


def volume_accounting(stats, identity, observation=None):
    require(isinstance(stats, dict) and isinstance(stats.get('pods', []), list) and len(stats.get('pods', [])) <= 256,
            'Kubelet workspace Pod inventory is malformed or excessive')
    inventory = stats.get('pods', [])
    require(all(isinstance(pod, dict) and isinstance(pod.get('podRef', {}), dict) for pod in inventory), 'Kubelet Pod reference is malformed')
    pods = [pod for pod in inventory if pod.get('podRef', {}).get('uid') == identity['uid'] and
            pod.get('podRef', {}).get('namespace') == identity['namespace'] and pod.get('podRef', {}).get('name') == 'actions-runtime-fixture']
    if observation is not None:
        observation.update(summary_pod_count=len(inventory), matched_pod_count=len(pods),
            same_name_pod_uids=[str(pod['podRef'].get('uid', ''))[:64] for pod in inventory
                if pod['podRef'].get('namespace') == identity['namespace'] and
                pod['podRef'].get('name') == 'actions-runtime-fixture'][:4])
    require(len(pods) <= 1, 'Kubelet returned duplicate workspace observations')
    listed = pods[0].get('volume', []) if pods else []
    if observation is not None:
        observation.update(volume_field_present=bool(pods and 'volume' in pods[0]),
            volume_field_type=type(listed).__name__, volume_count=len(listed) if isinstance(listed, list) else None)
        if pods and isinstance(pods[0].get('ephemeral-storage'), dict):
            observation['pod_ephemeral_storage'] = fs_observation(pods[0]['ephemeral-storage'])
    require(isinstance(listed, list) and len(listed) <= 32 and all(isinstance(volume, dict) for volume in listed),
            'Kubelet workspace volume inventory is malformed or excessive')
    volumes = [volume for volume in listed if volume.get('name') == 'runner']
    if observation is not None:
        observation.update(volume_names=[str(volume.get('name', ''))[:64] for volume in listed], matched_volume_count=len(volumes))
        if volumes:
            observation['runner_volume'] = fs_observation(volumes[0])
    require(len(volumes) <= 1, 'Kubelet returned duplicate runner volume observations')
    value = volumes[0].get('usedBytes') if volumes else None
    require(value is None or type(value) is int and 0 <= value <= 64 * GIB, 'Kubelet returned invalid workspace usage')
    return value


def verify_growth(before, after):
    require((before['device'], before['inode']) == (after['device'], after['inode']), 'Workspace filestore changed during the accounting probe')
    physical = after['allocated_bytes'] - before['allocated_bytes']
    accounted = after['kubelet_used_bytes'] - before['kubelet_used_bytes']
    require(physical >= PROBE_BYTES and accounted >= PROBE_BYTES - MIB and abs(accounted - physical) <= MIB,
            'Kubelet did not account for the workspace physical allocation growth')
    return {'probe_bytes': PROBE_BYTES, 'physical_growth_bytes': physical, 'kubelet_growth_bytes': accounted}


MOUNT_PROBE = """import json, pathlib, os, stat
root = pathlib.Path('/home/runner')
value = root.stat()
with open('/proc/self/mountinfo') as source:
    mounts = source.read(262145)
assert len(mounts) <= 262144
with open('/proc/self/status') as source:
    status = source.read(16385)
assert len(status) <= 16384
capabilities = int(next(line.split()[1] for line in status.splitlines() if line.startswith('CapEff:')), 16)
print(json.dumps({'mountinfo': mounts, 'root': {'uid': value.st_uid, 'gid': value.st_gid, 'mode': stat.S_IMODE(value.st_mode)},
    'process': {'uid': os.getuid(), 'gid': os.getgid(), 'effective_capabilities': capabilities},
    'prepare': (root / '.hakopod-shared-prepare').read_text() == 'shared-workspace-v1\\n',
    'ready': (root / '.hakopod-shared-ready').is_file()}))
"""

CREATE_PROBE = """import io, json, os, pathlib, struct, subprocess, tarfile
root = pathlib.Path('/home/runner/.hakopod-shared-proof')
root.mkdir(mode=0o770)
(root / 'runner').write_text('runner-to-docker')
os.setxattr(root / 'runner', 'user.hakopod-shared', b'shared-metadata-v1')
archive = io.BytesIO()
payload = b'capability-survives-sidecar-restart\\n'
with tarfile.open(fileobj=archive, mode='w', format=tarfile.PAX_FORMAT) as output:
    member = tarfile.TarInfo('capability')
    member.size, member.uid, member.gid, member.mode = len(payload), 1001, 1001, 0o755
    member.pax_headers = {'SCHILY.xattr.security.capability': struct.pack('<IIIII', 0x02000001, 1 << 10, 0, 0, 0).decode('latin1')}
    output.addfile(member, io.BytesIO(payload))
def docker(*args, **kwargs):
    return subprocess.run(['docker', *args], check=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=45, **kwargs)
docker('create', '--name', 'hako-shared-proof', '--mount', 'type=bind,source=' + str(root) + ',target=/proof', BUSYBOX,
    'sh', '-c', 'test "$(cat /proof/runner)" = runner-to-docker && printf docker-to-runner > /proof/docker')
try:
    docker('cp', '-a', '-', 'hako-shared-proof:/proof', input=archive.getvalue())
    docker('start', '--attach', 'hako-shared-proof')
finally:
    docker('rm', '-f', 'hako-shared-proof')
assert (root / 'docker').read_text() == 'docker-to-runner'
assert os.getxattr(root / 'capability', 'security.capability').hex() == CAPABILITY
print(json.dumps({'runner_inode': (root / 'runner').stat().st_ino, 'capability_inode': (root / 'capability').stat().st_ino}))
"""

VERIFY_PROBE = """import json, os, pathlib, subprocess
root = pathlib.Path('/home/runner/.hakopod-shared-proof')
assert (root / 'runner').read_text() == 'runner-to-docker' and (root / 'docker').read_text() == 'docker-to-runner'
assert (root / 'capability').read_bytes() == b'capability-survives-sidecar-restart\\n'
assert os.getxattr(root / 'runner', 'user.hakopod-shared') == b'shared-metadata-v1'
assert os.getxattr(root / 'capability', 'security.capability').hex() == CAPABILITY
response = subprocess.run(['docker', 'run', '--rm', '--mount', 'type=bind,source=' + str(root) + ',target=/proof',
    BUSYBOX, 'sh', '-c', 'test "$(cat /proof/runner)" = runner-to-docker && '
    'test "$(cat /proof/docker)" = docker-to-runner && cat /proof/capability'],
    check=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=45)
assert response.stdout == b'capability-survives-sidecar-restart\\n'
print(json.dumps({'runner_inode': (root / 'runner').stat().st_ino, 'capability_inode': (root / 'capability').stat().st_ino}))
"""


class Observer:
    """Bounded host checks; callers supply their named-cluster command wrappers."""
    def __init__(self, command, kube, node, report, driver='vfs'):
        require(node == NODE, 'Shared workspace observer requires the named development node')
        require(driver in ('vfs', 'overlay2'), 'Unexpected development Docker storage driver')
        self.command, self.kube, self.node, self.report = command, kube, node, report
        self.driver = driver
        self.identities = {}
        self.removed_uids = set()
        self.disk_monitor = None
        self.deadline = 0

    def _timeout(self):
        remaining = self.deadline - time.monotonic()
        require(remaining > 0, 'Shared workspace checks exceeded their 210-second bound')
        return min(60, remaining)

    def runner(self, identity, code):
        result = self.kube(['-n', identity['namespace'], 'exec', 'actions-runtime-fixture', '-c', 'runner', '--',
            'python3', '-c', code], timeout=self._timeout())
        return result.stdout

    def host(self, args):
        return self.command(['docker', 'exec', self.node, *args], timeout=self._timeout()).stdout

    def verify_config(self):
        self.deadline = time.monotonic() + 60
        value = self.host(['cat', '/var/lib/rancher/k3s/agent/etc/containerd/config.toml'])
        self.report['runtime'] = verify_runtime_config(value)
        self.report['profile'] = verify_runtime_profile(self.host(['cat', '/opt/hakopod-actions-fixture/runsc-actions.toml']))

    def filestore_command(self, identity):
        # No recursive read and no file content: only the exact owned filestore
        # is allowed in this host directory, and every ancestor must be real.
        script = '''set -eu
directory="$1"
file="$directory/.gvisor.filestore.$2"
test "$(readlink -f "$directory")" = "$directory"
test ! -L "$directory"
test ! -L "$file"
test -f "$file"
count=0
for entry in "$directory"/* "$directory"/.[!.]* "$directory"/..?*; do
    if test -e "$entry" || test -L "$entry"; then
        count=$((count + 1))
        test "$entry" = "$file"
    fi
done
test "$count" = 1
stat -f -c '%T|%t' "$directory"
stat -c '%F|%h|%d|%i|%s|%b' "$file"
'''
        return ['sh', '-c', script, 'shared-workspace', identity['source'], identity['sandbox_id']]

    def filestore(self, identity):
        return verify_filestore(self.host(self.filestore_command(identity)))

    def sample(self, identity, observation=None):
        result = self.filestore(identity)
        if observation is not None:
            observation['physical'] = dict(result)
            observation['kubelet'] = {}
        stats = bounded_json(self.kube(['get', '--raw', f'/api/v1/nodes/{self.node}/proxy/stats/summary'], timeout=self._timeout()).stdout, 2 * MIB)
        result['kubelet_used_bytes'] = volume_accounting(stats, identity, observation['kubelet'] if observation is not None else None)
        return result

    def usage(self, stats):
        # Keep only bounded peaks. A kubelet eviction may remove the filestore
        # between an API sample and stat; the successful overage samples below
        # are required independently before the disk scenario can pass.
        previous = self.deadline
        self.deadline = min(previous, time.monotonic() + 20) if previous > time.monotonic() else time.monotonic() + 20
        try:
            for identity in self.identities.values():
                if identity['uid'] in self.removed_uids:
                    continue
                used = volume_accounting(stats, identity)
                peaks = self.report.setdefault('usage', {}).setdefault(identity['uid'], {'samples': 0, 'physical_bytes': 0, 'kubelet_bytes': 0, 'unavailable_samples': 0})
                peaks['samples'] += 1
                if used is not None:
                    peaks['kubelet_bytes'] = max(peaks['kubelet_bytes'], used)
                try:
                    physical = self.filestore(identity)
                except RuntimeError:
                    peaks['unavailable_samples'] += 1
                else:
                    peaks['physical_bytes'] = max(peaks['physical_bytes'], physical['allocated_bytes'])
        finally:
            self.deadline = previous

    def start_disk_monitor(self, identity, before):
        require(self.disk_monitor is None and identity['workspace_gib'] == 2, 'Only one original disk fixture may be monitored')
        stop = threading.Event()
        evidence = {'uid': identity['uid'], 'samples': 0, 'physical_bytes': before['allocated_bytes'], 'unavailable_samples': 0}
        def collect():
            # A kubelet scan can evict and delete the volume before the next
            # stats/summary response. Preserve bounded physical peaks while the
            # actual overage is written, independently of summary availability.
            deadline = time.monotonic() + 240
            for _ in range(480):
                if stop.is_set() or time.monotonic() >= deadline:
                    break
                try:
                    text = self.command(['docker', 'exec', self.node, *self.filestore_command(identity)], timeout=10).stdout
                except Exception as error:
                    evidence['unavailable_samples'] += 1
                    evidence['last_error_type'] = type(error).__name__
                else:
                    try:
                        observed = verify_filestore(text)
                        require((observed['device'], observed['inode']) == (before['device'], before['inode']),
                                'Disk fixture filestore identity changed')
                    except (RuntimeError, ValueError):
                        evidence['invalid_observation'] = True
                        break
                    else:
                        evidence['samples'] += 1
                        evidence['physical_bytes'] = max(evidence['physical_bytes'], observed['allocated_bytes'])
                if stop.wait(0.5):
                    break
        thread = threading.Thread(target=collect, daemon=True)
        self.disk_monitor = (stop, thread, evidence)
        self.report['disk_physical_monitor'] = evidence
        thread.start()

    def close(self):
        if self.disk_monitor:
            stop, thread, _ = self.disk_monitor
            stop.set()
            thread.join(timeout=12)
            require(not thread.is_alive(), 'Disk physical observer did not stop within its bound')

    def verify_eviction(self, uid, status):
        self.close()
        identity = self.identities.get(uid)
        require(identity is not None and identity['workspace_gib'] == 2, 'Eviction did not use the existing 2 GiB disk fixture')
        peaks = self.report.get('usage', {}).get(uid, {})
        monitor = self.report.get('disk_physical_monitor', {})
        require(not monitor.get('invalid_observation'), 'Disk filestore identity or observation changed')
        physical = max(peaks.get('physical_bytes', 0), monitor.get('physical_bytes', 0) if monitor.get('uid') == uid else 0)
        require(physical > 2 * GIB and status.get('reason') == 'Evicted' and
                re.search(r'\bEmptyDir volume "runner" exceeds the limit "2Gi"(?:\.|$)', status.get('message', '')) is not None,
                'Physical disk overage and original runner EmptyDir 2 GiB eviction were not both observed')
        self.report['eviction_usage'] = {**peaks, 'physical_bytes': physical, 'kubelet_evicted_for_runner_emptydir': True,
            'kubelet_overage_summary_observed': peaks.get('kubelet_bytes', 0) > 2 * GIB}

    def accounted(self, identity, phase='unspecified'):
        require(phase in ('before', 'after', 'unspecified'), 'Unexpected accounting probe phase')
        attempts = self.report.setdefault('accounting_attempts', [])
        require(len(attempts) < 6, 'Excessive workspace accounting attempts')
        started = time.monotonic()
        end = min(self.deadline, started + 85)
        attempt = {'uid': identity['uid'], 'phase': phase, 'tolerance_bytes': MIB,
                   'maximum_samples': 32, 'samples': [], 'status': 'running'}
        attempts.append(attempt)
        for index in range(32):
            if time.monotonic() >= end:
                break
            observation = {'index': index, 'elapsed_seconds': round(time.monotonic() - started, 3)}
            attempt['samples'].append(observation)
            try:
                value = self.sample(identity, observation)
            except Exception as error:
                observation['error_type'] = type(error).__name__
                attempt['status'] = 'observation-failed'
                raise
            observation.update(allocated_bytes=value['allocated_bytes'], logical_bytes=value.get('logical_bytes'),
                kubelet_used_bytes=value['kubelet_used_bytes'],
                kubelet_minus_physical_bytes=value['kubelet_used_bytes'] - value['allocated_bytes'] if value['kubelet_used_bytes'] is not None else None)
            if value['kubelet_used_bytes'] is not None and abs(value['kubelet_used_bytes'] - value['allocated_bytes']) <= MIB:
                attempt['status'] = 'matched'
                return value
            time.sleep(3)
        attempt['status'] = 'unmatched'
        raise RuntimeError('Kubelet runner volume usage did not match physical filestore allocation')

    def daemon_sharing(self, identity, inodes):
        output = self.kube(['-n', identity['namespace'], 'exec', 'actions-runtime-fixture', '-c', 'docker', '--', 'sh', '-c',
            'test "$(cat /home/runner/.hakopod-shared-prepare)" = shared-workspace-v1 && '
            'test "$(cat /home/runner/.hakopod-shared-proof/runner)" = runner-to-docker && '
            'stat -c "%i" /home/runner/.hakopod-shared-proof/runner /home/runner/.hakopod-shared-proof/capability'],
            timeout=self._timeout()).stdout
        require(output.splitlines() == [str(inodes['runner_inode']), str(inodes['capability_inode'])],
                'Docker sidecar does not share the runner workspace inodes')

    def prepare(self, pod, restart, full=True, disk=False):
        self.deadline = time.monotonic() + 210
        identity = pod_identity(pod, self.driver)
        require(identity['uid'] not in self.identities and len(self.identities) < 3, 'Repeated or excessive shared workspace Pods')
        inspected = bounded_json(self.host(['/bin/crictl', 'inspect', '-o', 'json', identity['container_id']]))
        identity['sandbox_id'] = verify_container_source(inspected, identity)
        sandboxes = bounded_json(self.host(['/bin/crictl', 'pods', '--name', 'actions-runtime-fixture', '--namespace', identity['namespace'], '-o', 'json']))
        verify_sandbox(sandboxes, identity, identity['sandbox_id'])
        mount = verify_mount(bounded_json(self.runner(identity, MOUNT_PROBE)), identity)
        self.identities[identity['uid']] = identity
        evidence = {'identity': identity, 'mount': mount, 'status': 'running', 'full_probe': full}
        self.report.setdefault('pods', []).append(evidence)
        evidence['docker'] = verify_docker(bounded_json(self.runner(identity, DOCKER_PROBE)), self.driver, empty=True)
        if not full:
            # Disk and replacement Pods retain their original five-minute
            # deadline and empty-daemon assertions. They never pull probe images.
            evidence.update(before=self.sample(identity), status='passed')
            if disk:
                self.start_disk_monitor(identity, evidence['before'])
            self.runner(identity, "from pathlib import Path; Path('/home/runner/.hakopod-shared-continue').touch(exist_ok=False)")
            return evidence
        constants = 'BUSYBOX = ' + repr(BUSYBOX) + '\nCAPABILITY = ' + repr(CAPABILITY) + '\n'
        inodes = bounded_json(self.runner(identity, constants + CREATE_PROBE), 1024)
        # The daemon directly reads the same init and runner files, in addition
        # to the nested Docker bind mount exercised by CREATE_PROBE.
        self.daemon_sharing(identity, inodes)
        before = self.accounted(identity, 'before')
        evidence['before'] = before
        self.runner(identity, '''import os, pathlib
with pathlib.Path('/home/runner/.hakopod-shared-proof/allocation').open('xb') as output:
    for _ in range(16):
        output.write(os.urandom(1024 * 1024))
    output.flush()
    os.fsync(output.fileno())
''')
        after = self.accounted(identity, 'after')
        evidence['after'] = after
        evidence.update(before=before, after=after, accounting=verify_growth(before, after))
        restart(timeout=self._timeout())
        require(bounded_json(self.runner(identity, constants + VERIFY_PROBE), 1024) == inodes,
                'Workspace files or metadata changed after the Docker sidecar restart')
        self.daemon_sharing(identity, inodes)
        evidence['docker_after_restart'] = verify_docker(bounded_json(self.runner(identity, DOCKER_PROBE)), self.driver)
        require(evidence['docker_after_restart'] == {**evidence['docker'], 'fresh_daemon_verified': False},
                'Docker daemon identity or storage driver changed after restart')
        restarted = self.sample(identity)
        require((restarted['device'], restarted['inode']) == (before['device'], before['inode']),
                'Docker sidecar restart replaced the shared disk filestore')
        evidence.update(after_restart=restarted, shared_inodes=inodes, capability_hex=CAPABILITY,
                        docker_archive_metadata=True, init_and_nested_bind_shared=True, status='passed')
        self.runner(identity, "from pathlib import Path; Path('/home/runner/.hakopod-shared-proof/allocation').unlink(); Path('/home/runner/.hakopod-shared-continue').touch(exist_ok=False)")
        return evidence

    def removed(self, uid, command=None):
        identity = self.identities.get(uid)
        require(identity is not None, 'Deleted workspace was never physically observed')
        require(re.fullmatch(r'[a-f0-9]{8}(?:-[a-f0-9]{4}){3}-[a-f0-9]{12}', uid) and
                identity['source'] == f'/var/lib/kubelet/pods/{uid}/volumes/kubernetes.io~empty-dir/runner',
                'Deleted workspace path differs from the observed Pod UID')
        self.deadline = time.monotonic() + 30
        arguments = ['sh', '-c', 'test ! -e "$1" && test ! -L "$1"', 'shared-workspace', identity['source']]
        if command:
            # Failure cleanup gets its own bounded budget after the workload
            # deadline. The callback still executes only this exact owned path.
            for attempt in range(15):
                result = command(['docker', 'exec', self.node, *arguments], timeout=self._timeout(), check=False)
                if result.returncode == 0:
                    break
                require(attempt < 14, 'Deleted Pod still has a shared disk workspace')
                time.sleep(2)
        else:
            self.host(arguments)
        self.removed_uids.add(uid)
        self.report.setdefault('removed_workspaces', []).append({'uid': uid, 'sandbox_id': identity['sandbox_id'], 'source_absent': True})
