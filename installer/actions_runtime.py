"""Install the optional Actions runtime on an installer-owned K3s node.

The installer's server uses modules.py. An additional K3s agent adopted by that
server runs this file's `agent` command on the agent host itself. Both restart
K3s, so they are explicit operator maintenance actions. Neither alters the
default runtime or uses a customer's Docker socket.
"""
import argparse
import fnmatch
import fcntl
import hashlib
import json
import os
from pathlib import Path, PurePosixPath
import platform
import shutil
import stat
import time
import subprocess
import tarfile
import tempfile
import urllib.request

VERSION = 'release-20260907.0'
DIGESTS = {
    'x86_64': '81416511897ab8abd4e723d66823c5b0461a2ee3311cfa70d152404ef9b860cf',
    'aarch64': '2b162adb35860f598ab2f89b9d752bff2c7ee6175c05d9cc532174a336cfb38c',
}
BEGIN = '# BEGIN HAKOPOD MANAGED ACTIONS\n'
END = '# END HAKOPOD MANAGED ACTIONS\n'
BASE = Path('/opt/hakopod/actions-runtime')
TEMPLATE = Path('/var/lib/hakopod/k3s/agent/etc/containerd/config-v3.toml.tmpl')
WORKSPACE_PROFILE = 'shared-overlay2-v1'
WORKSPACE_LABEL = 'hakopod.io/actions-workspace'
RUNTIME_LABEL = 'hakopod.io/actions-runtime'
WORKSPACE_KEYS = ['dev.gvisor.spec.mount.runner.' + name for name in ('type', 'share', 'options')]
MAINTENANCE_LOCK = Path('/run/lock/hakopod-actions-runtime.lock')
INSTALLATION_LABEL = 'hakopod.com/installation'
CONTROL_PLANE_LABEL = 'node-role.kubernetes.io/control-plane'
# Paths of a standard K3s agent installation (get.k3s.io, default data dir).
AGENT_TEMPLATE = Path('/var/lib/rancher/k3s/agent/etc/containerd/config-v3.toml.tmpl')
# The agent publishes only its own node's label with its kubelet identity;
# Kubernetes' NodeRestriction admission confines that identity to this node.
AGENT_KUBE = ['/usr/local/bin/k3s', 'kubectl', '--kubeconfig=/var/lib/rancher/k3s/agent/kubelet.kubeconfig',
              '--request-timeout=15s']
MACHINE_ID = Path('/etc/machine-id')


class RuntimeMaintenanceLock:
    """Serialize runtime mutation with qualification on this same host."""
    def __init__(self):
        self.fd = None
        directories = []
        try:
            if os.geteuid() != 0 or not MAINTENANCE_LOCK.is_absolute() or any(part in ('.', '..') for part in MAINTENANCE_LOCK.parts):
                raise ValueError('Actions runtime maintenance requires a root-owned absolute lock')
            flags = os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW | os.O_CLOEXEC
            directories.append(os.open('/', flags))
            for part in MAINTENANCE_LOCK.parts[1:-1]:
                info = os.fstat(directories[-1])
                if not stat.S_ISDIR(info.st_mode) or info.st_uid != 0 or (stat.S_IMODE(info.st_mode) & 0o022 and not info.st_mode & stat.S_ISVTX):
                    raise ValueError('Actions runtime lock directory is unsafe')
                directories.append(os.open(part, flags, dir_fd=directories[-1]))
            info = os.fstat(directories[-1])
            if not stat.S_ISDIR(info.st_mode) or info.st_uid != 0 or (stat.S_IMODE(info.st_mode) & 0o022 and not info.st_mode & stat.S_ISVTX):
                raise ValueError('Actions runtime lock directory is unsafe')
            self.fd = os.open(MAINTENANCE_LOCK.name, os.O_RDWR | os.O_CREAT | os.O_NOFOLLOW | os.O_CLOEXEC | os.O_NONBLOCK,
                              0o600, dir_fd=directories[-1])
            info = os.fstat(self.fd)
            if not stat.S_ISREG(info.st_mode) or info.st_uid != 0 or info.st_nlink != 1 or stat.S_IMODE(info.st_mode) != 0o600:
                raise ValueError('Actions runtime lock ownership or file type is unsafe')
            try:
                fcntl.flock(self.fd, fcntl.LOCK_EX | fcntl.LOCK_NB)
            except BlockingIOError as error:
                raise ValueError('Actions runtime maintenance or qualification is already active') from error
            current = os.stat(MAINTENANCE_LOCK.name, dir_fd=directories[-1], follow_symlinks=False)
            if (current.st_dev, current.st_ino) != (info.st_dev, info.st_ino) or not stat.S_ISREG(current.st_mode) or current.st_uid != 0 or current.st_nlink != 1 or stat.S_IMODE(current.st_mode) != 0o600:
                raise ValueError('Actions runtime lock changed while acquiring it')
        except BaseException:
            self.close()
            raise
        finally:
            for descriptor in reversed(directories):
                os.close(descriptor)

    def close(self):
        if self.fd is not None:
            descriptor, self.fd = self.fd, None
            os.close(descriptor)

    def __enter__(self):
        return self

    def __exit__(self, *_):
        self.close()


def runtime_section(root, workspace_profile='vfs'):
    if workspace_profile not in ('vfs', WORKSPACE_PROFILE):
        raise ValueError('Unknown Actions workspace profile')
    annotations = '  pod_annotations = ' + json.dumps(WORKSPACE_KEYS) + '\n' if workspace_profile == WORKSPACE_PROFILE else ''
    return BEGIN + '''[plugins."io.containerd.cri.v1.runtime".containerd.runtimes.hakopod-actions]
ANNOTATIONS  runtime_type = "io.containerd.runsc.v1"
  runtime_path = "ROOT/containerd-shim-runsc-v1"
[plugins."io.containerd.cri.v1.runtime".containerd.runtimes.hakopod-actions.options]
  TypeUrl = "io.containerd.runsc.v1.options"
  ConfigPath = "ROOT/runsc-actions.toml"
'''.replace('ROOT', str(root)).replace('ANNOTATIONS', annotations) + END


def runtime_profile(root):
    # Unlike runc, the runsc shim reads binary_name from this profile, not
    # BinaryName in containerd's options table.
    return ('binary_name = "' + str(root / 'runsc') + '"\n'
            '[runsc_config]\n  platform = "systrap"\n  net-raw = "true"\n'
            '  allow-packet-socket-write = "true"\n  overlay2 = "root:memory,size=512m"\n')


def extend_template(current, root, workspace_profile='vfs'):
    section = runtime_section(root, workspace_profile)
    if BEGIN in current:
        if current.count(BEGIN) != 1 or current.count(END) != 1:
            raise ValueError('Actions runtime configuration was changed; review it before upgrading')
        start, end = current.index(BEGIN), current.index(END) + len(END)
        existing = current[start:end]
        if existing not in (runtime_section(root, 'vfs'), runtime_section(root, WORKSPACE_PROFILE)):
            raise ValueError('Actions runtime configuration was changed; review it before upgrading')
        return current[:start] + section + current[end:]
    if 'hakopod-actions' in current or END in current:
        raise ValueError('An unowned Actions runtime configuration already exists')
    return current.rstrip() + '\n\n' + section


def verify_generated_config(text, root, workspace_profile):
    # All supported installer distributions ship Python 3.11 or newer.
    try:
        import tomllib
    except ImportError as error:
        raise ValueError('Managed Actions setup requires Python 3.11 or newer') from error
    if not isinstance(text, str) or not 0 < len(text) <= 262144:
        raise ValueError('Generated Actions runtime configuration exceeds its size bound')
    config = tomllib.loads(text)
    runtimes = config.get('plugins', {}).get('io.containerd.cri.v1.runtime', {}).get('containerd', {}).get('runtimes', {})
    runtime = runtimes.get('hakopod-actions', {})
    expected = {'runtime_type': 'io.containerd.runsc.v1',
                'runtime_path': str(root / 'containerd-shim-runsc-v1'),
                'options': {'TypeUrl': 'io.containerd.runsc.v1.options',
                            'ConfigPath': str(root / 'runsc-actions.toml')}}
    if workspace_profile == WORKSPACE_PROFILE:
        expected['pod_annotations'] = WORKSPACE_KEYS
    elif workspace_profile != 'vfs':
        raise ValueError('Unknown Actions workspace profile')
    # Containerd may supply unrelated runtime defaults; every security-sensitive
    # selection and the complete options table must match the owned stanza.
    if any(runtime.get(key) != value for key, value in expected.items()) or runtime.get('pod_annotations', []) != expected.get('pod_annotations', []):
        raise ValueError('K3s did not load the exact Actions runtime configuration')
    for name, other in runtimes.items():
        if name == 'hakopod-actions':
            continue
        if any(fnmatch.fnmatchcase(key, pattern) for pattern in other.get('pod_annotations', []) for key in WORKSPACE_KEYS):
            raise ValueError('Another runtime forwards the Actions workspace annotations')


def runtime_tree(root):
    pending, paths = [root], []
    while pending:
        path = pending.pop()
        paths.append(path)
        if len(paths) > 256:
            raise ValueError('Actions runtime tree exceeds its file bound')
        if stat.S_ISDIR(path.lstat().st_mode):
            with os.scandir(path) as entries:
                for entry in entries:
                    if len(paths) + len(pending) >= 256:
                        raise ValueError('Actions runtime tree exceeds its file bound')
                    pending.append(Path(entry.path))
    return paths


def runtime_digest(path):
    if path.stat().st_size > 512 * 1024 * 1024:
        raise ValueError('Actions runtime file exceeds its size bound')
    result = hashlib.sha256()
    with path.open('rb') as source:
        while chunk := source.read(1024 * 1024):
            result.update(chunk)
    return result.digest()


def verify_installed_runtime(root, expected):
    paths = runtime_tree(root)
    if root.resolve(strict=True) != root:
        raise ValueError('Installed Actions runtime tree is invalid')
    actual = set()
    for path in paths:
        info = path.lstat()
        if info.st_uid != 0 or stat.S_IMODE(info.st_mode) & 0o022 or not (stat.S_ISDIR(info.st_mode) or stat.S_ISREG(info.st_mode)):
            raise ValueError('Installed Actions runtime ownership or file type changed')
        relative = path.relative_to(root)
        source = expected / relative
        if not source.exists() or stat.S_IFMT(info.st_mode) != stat.S_IFMT(source.lstat().st_mode) or stat.S_IMODE(info.st_mode) != stat.S_IMODE(source.lstat().st_mode):
            raise ValueError('Installed Actions runtime paths or modes changed')
        actual.add(str(relative))
        if stat.S_ISREG(info.st_mode):
            if info.st_nlink != 1 or not source.is_file() or stat.S_IMODE(info.st_mode) != stat.S_IMODE(source.stat().st_mode) or runtime_digest(path) != runtime_digest(source):
                raise ValueError('Installed Actions runtime differs from its verified archive')
    wanted = {str(path.relative_to(expected)) for path in runtime_tree(expected)}
    if actual != wanted:
        raise ValueError('Installed Actions runtime files are missing or unexpected')
    result = subprocess.run([str(root / 'runsc'), '--version'], capture_output=True, timeout=15, check=True)
    if VERSION.encode() not in result.stdout or len(result.stdout) > 65536 or len(result.stderr) > 65536:
        raise ValueError('Installed Actions runtime version probe failed')


def read_runtime_text(path, maximum):
    info = path.lstat()
    if path.resolve(strict=True) != path or not stat.S_ISREG(info.st_mode) or info.st_uid != 0 or info.st_nlink != 1 or not 0 < info.st_size <= maximum or stat.S_IMODE(info.st_mode) & 0o022:
        raise ValueError('Actions runtime configuration ownership or size changed')
    with os.fdopen(os.open(path, os.O_RDONLY | os.O_NOFOLLOW), 'rb') as source:
        actual = os.fstat(source.fileno())
        if (actual.st_dev, actual.st_ino) != (info.st_dev, info.st_ino):
            raise ValueError('Actions runtime configuration changed while reading')
        data = source.read(maximum + 1)
    if len(data) > maximum:
        raise ValueError('Actions runtime configuration exceeds its size bound')
    return data.decode('utf-8')


def atomic_write(path, value, mode=0o644):
    if path.is_symlink():
        raise ValueError('Refusing a symlink: ' + str(path))
    with tempfile.NamedTemporaryFile(dir=path.parent, delete=False) as temp:
        temp.write(value.encode())
        os.fchmod(temp.fileno(), mode)
        name = temp.name
    os.replace(name, path)


def unpack_runtime(source, destination):
    """Extract the verified flat runtime bundle on Python 3.10 and later."""
    destination = Path(destination)
    if destination.exists() and any(destination.iterdir()):
        raise ValueError('Runtime extraction destination must be empty')
    with tarfile.open(source) as archive:
        members = archive.getmembers()
        if len(members) > 256 or sum(m.size for m in members) > 1024 ** 3:
            raise ValueError('Runtime archive exceeds extraction bounds')
        paths = set()
        for member in members:
            path = PurePosixPath(member.name)
            if (path.is_absolute() or '..' in path.parts or not path.parts
                    or '\\' in member.name or str(path) in paths
                    or path.parts[0] not in ('runsc', 'containerd-shim-runsc-v1', 'gvisor-bin')
                    or not (member.isfile() or member.isdir())):
                raise ValueError('Unsafe runtime archive entry')
            paths.add(str(path))
        for member in members:
            path = destination / member.name
            path.parent.mkdir(parents=True, exist_ok=True)
            if member.isdir():
                path.mkdir(exist_ok=True)
            else:
                with archive.extractfile(member) as src, path.open('xb') as dst:
                    shutil.copyfileobj(src, dst, 1024 * 1024)
                path.chmod(0o755 if member.mode & 0o111 else 0o644)
        for path in [destination, *destination.rglob('*')]:
            if path.is_dir():
                path.chmod(0o755)


def runtime_class(ident):
    return {'apiVersion': 'node.k8s.io/v1', 'kind': 'RuntimeClass', 'metadata': {'name': 'hakopod-actions', 'labels': {'app.kubernetes.io/managed-by': 'hakopod', INSTALLATION_LABEL: ident}}, 'handler': 'hakopod-actions', 'scheduling': {'nodeSelector': {RUNTIME_LABEL: 'ready'}}, 'overhead': {'podFixed': {'memory': '512Mi', 'cpu': '100m'}}}


def verify_runtime_class(installed, ident):
    runtime = runtime_class(ident)
    labels = installed.get('metadata', {}).get('labels', {}) if installed else {}
    if labels.get(INSTALLATION_LABEL) != ident or labels.get('app.kubernetes.io/managed-by') != 'hakopod' or any(installed.get(key) != runtime[key] for key in ('handler', 'scheduling', 'overhead')):
        raise ValueError('Actions RuntimeClass verification failed')


def refuse_unowned_published_nodes(nodes, ident):
    # The RuntimeClass admits every node carrying the runtime label. A node
    # published outside this installation could receive jobs on an unverified
    # runtime, so the class is not created or refreshed while one exists.
    for node in nodes:
        labels = node['metadata'].get('labels', {})
        if (RUNTIME_LABEL in labels or WORKSPACE_LABEL in labels) and labels.get(INSTALLATION_LABEL) != ident:
            raise ValueError('Node ' + node['metadata']['name'] + ' publishes Actions eligibility without this installation\'s ownership label')


def apply_runtime_class(ident, kube, read, tmp):
    manifest = Path(tmp) / 'runtime.json'
    manifest.write_text(json.dumps(runtime_class(ident)))
    subprocess.run([*kube, 'apply', '--server-side', '--field-manager=hakopod-installer', '-f', str(manifest)], check=True, timeout=30)
    verify_runtime_class(read('get', 'runtimeclass', 'hakopod-actions'), ident)


def publish_runtime_class(marker, kube, read):
    """Create the cluster's RuntimeClass without preparing the server node.

    Used when only adopted agents run jobs, so the server's K3s keeps running.
    """
    with RuntimeMaintenanceLock():
        ident = marker['id']
        existing = read('get', 'runtimeclass', 'hakopod-actions', '--ignore-not-found')
        if existing and existing.get('metadata', {}).get('labels', {}).get(INSTALLATION_LABEL) != ident:
            raise ValueError('An unowned Actions RuntimeClass already exists')
        refuse_unowned_published_nodes(read('get', 'nodes')['items'], ident)
        with tempfile.TemporaryDirectory(prefix='hakopod-actions-') as tmp:
            apply_runtime_class(ident, kube, read, tmp)


def install(config, marker, kube, read, workspace_profile=None):
    with RuntimeMaintenanceLock() as maintenance:
        return install_locked(config, marker, kube, read, workspace_profile, maintenance)


def install_agent(node_name, ident, kube, read, workspace_profile=None):
    if workspace_profile not in (None, 'vfs'):
        # The workspace probe needs the server's administrator credential and
        # binary on the probed host; agents are not given either.
        raise ValueError('An adopted agent supports only the VFS workspace profile')
    with RuntimeMaintenanceLock() as maintenance:
        return install_locked({'node_name': node_name}, {'id': ident}, kube, read, 'vfs', maintenance, agent=True)


def install_locked(config, marker, kube, read, workspace_profile, maintenance, agent=False):
    if platform.system() != 'Linux' or platform.machine() not in DIGESTS:
        raise ValueError('Actions requires Linux AMD64 or ARM64')
    template = AGENT_TEMPLATE if agent else TEMPLATE
    unit_name = 'k3s-agent' if agent else 'hakopod-k3s'
    if not template.parent.is_dir():
        raise ValueError('The K3s containerd directory is missing')
    ident = marker['id']
    if agent:
        # An agent changes only its own host. The server operator adopts the
        # node first; the machine ID proves this host is that node.
        node = read('get', 'node', config['node_name'])
        machine = MACHINE_ID.read_text().strip() if MACHINE_ID.is_file() else ''
        if not machine or node.get('status', {}).get('nodeInfo', {}).get('machineID') != machine:
            raise ValueError('Run agent setup on the host of the named node')
        if CONTROL_PLANE_LABEL in node['metadata'].get('labels', {}):
            raise ValueError('Agent setup applies only to K3s agents; use modules.py on the server')
        unit = subprocess.check_output(['systemctl', 'show', unit_name, '--property=ExecStart', '--value'], text=True, timeout=15)
        if '/usr/local/bin/k3s' not in unit or ' agent' not in unit:
            raise ValueError('The K3s agent unit could not be verified')
    else:
        nodes = read('get', 'nodes')['items']
        own = [item for item in nodes if item['metadata']['name'] == config['node_name']]
        if len(own) != 1:
            raise ValueError('Actions setup requires the installer-owned server node')
        node = own[0]
        refuse_unowned_published_nodes(nodes, ident)
        unit = subprocess.check_output(['systemctl', 'show', unit_name, '--property=ExecStart', '--value'], text=True, timeout=15)
        if '/opt/hakopod/tools/k3s' not in unit or '/etc/hakopod/k3s.yaml' not in unit:
            raise ValueError('The installer-owned K3s unit could not be verified')
    if node['metadata'].get('labels', {}).get(INSTALLATION_LABEL) != ident:
        raise ValueError('Actions node belongs to another installation')
    if 'hakopod.io/actions-workspace-probe' in node['metadata'].get('annotations', {}):
        raise ValueError('Finish the existing Actions workspace probe before runtime maintenance')
    if template.is_symlink() or BASE.is_symlink():
        raise ValueError('Refusing a symlink in the runtime configuration')
    profile_path = BASE / 'workspace-profile'
    if workspace_profile is None:
        workspace_profile = 'vfs'
        if profile_path.exists() or profile_path.is_symlink():
            info = profile_path.lstat()
            if not stat.S_ISREG(info.st_mode) or info.st_uid != 0 or info.st_nlink != 1 or stat.S_IMODE(info.st_mode) != 0o600 or info.st_size > 64:
                raise ValueError('Saved Actions workspace profile has invalid ownership')
            workspace_profile = read_runtime_text(profile_path, 64).strip()
    if workspace_profile not in ('vfs', WORKSPACE_PROFILE):
        raise ValueError('Unknown Actions workspace profile')
    existing = read('get', 'runtimeclass', 'hakopod-actions', '--ignore-not-found')
    if existing and existing.get('metadata', {}).get('labels', {}).get(INSTALLATION_LABEL) != ident:
        raise ValueError('An unowned Actions RuntimeClass already exists')
    if agent:
        # The agent's kubelet identity cannot create the class; the server's
        # module publishes it before any agent is prepared.
        verify_runtime_class(existing, ident)
    if BASE.exists():
        info = BASE.lstat()
        if not stat.S_ISDIR(info.st_mode) or info.st_uid != 0 or stat.S_IMODE(info.st_mode) & 0o022 or read_runtime_text(BASE / 'owner', 128) != ident:
            raise ValueError('An unowned Actions runtime directory already exists')
    root = BASE / VERSION
    if root.exists() or root.is_symlink():
        info = root.lstat()
        if root.resolve(strict=True) != root or not stat.S_ISDIR(info.st_mode) or info.st_uid != 0:
            raise ValueError('Installed Actions runtime directory has invalid ownership')
    original = read_runtime_text(template, 262144) if template.exists() else '{{ template "base" . }}\n'
    updated = extend_template(original, root, workspace_profile)
    verify_generated_config(runtime_section(root, workspace_profile), root, workspace_profile)
    withdraw = [name + '-' for name in (RUNTIME_LABEL, WORKSPACE_LABEL) if name in node['metadata'].get('labels', {})]
    if withdraw:
        subprocess.run([*kube, 'label', 'node', config['node_name'], *withdraw,
                        '--resource-version=' + node['metadata']['resourceVersion']], check=True, timeout=30)
    with tempfile.TemporaryDirectory(prefix='hakopod-actions-') as tmp:
        tmp = Path(tmp)
        archive = tmp / 'gvisor.tar.bz2'
        digest = hashlib.sha256()
        total = 0
        url = f'https://github.com/google/gvisor/releases/download/{VERSION}/gvisor-{platform.machine()}.tar.bz2'
        deadline = time.monotonic() + 120
        with urllib.request.urlopen(url, timeout=60) as response, archive.open('wb') as output:
            while chunk := response.read(1024 * 1024):
                total += len(chunk)
                if total > 180 * 1024 * 1024 or time.monotonic() > deadline:
                    raise ValueError('Runtime download exceeded its size bound')
                digest.update(chunk)
                output.write(chunk)
        if digest.hexdigest() != DIGESTS[platform.machine()]:
            raise ValueError('Runtime checksum mismatch')
        extracted = tmp / 'extracted'
        unpack_runtime(archive, extracted)
        stage = tmp / 'runtime'
        stage.mkdir()
        stage.chmod(0o755)
        for name in ('runsc', 'containerd-shim-runsc-v1', 'gvisor-bin'):
            matches = list(extracted.rglob(name))
            if len(matches) != 1:
                raise ValueError('Runtime archive is missing ' + name)
            if matches[0].is_dir():
                shutil.copytree(matches[0], stage / name, symlinks=True)
            else:
                shutil.copy2(matches[0], stage / name, follow_symlinks=False)
        (stage / 'runsc-actions.toml').write_text(runtime_profile(root))
        (stage / 'runsc-actions.toml').chmod(0o644)
        current = read('get', 'node', config['node_name'])
        if current['metadata']['uid'] != node['metadata']['uid'] or current['metadata'].get('labels', {}).get(INSTALLATION_LABEL) != ident or 'hakopod.io/actions-workspace-probe' in current['metadata'].get('annotations', {}):
            raise ValueError('Actions node ownership changed before runtime maintenance')
        if any(name in current['metadata'].get('labels', {}) for name in (RUNTIME_LABEL, WORKSPACE_LABEL)):
            raise ValueError('Actions eligibility changed during runtime maintenance')
        BASE.mkdir(exist_ok=True, mode=0o755)
        # Module setup uses umask077. The sandbox drops privileges before
        # starting its helper, so every owned parent must remain traversable.
        BASE.chmod(0o755)
        atomic_write(BASE / 'owner', ident)
        if not root.exists():
            shutil.copytree(stage, root, symlinks=True)
        root.chmod(0o755)
        # Older installs wrote this non-secret profile with umask077. Preserve
        # its contents while making the exact runtime profile readable.
        profile = root / 'runsc-actions.toml'
        if profile.is_symlink() or read_runtime_text(profile, 8192) != runtime_profile(root):
            raise ValueError('Installed Actions runtime profile was changed')
        profile.chmod(0o644)
        verify_installed_runtime(root, stage)
        if updated != original:
            backup = BASE / 'containerd-template.before-actions'
            if not backup.exists():
                atomic_write(backup, original, 0o600)
            atomic_write(template, updated)
        subprocess.run(['systemctl', 'restart', unit_name], check=True, timeout=300)
        subprocess.run([*kube, 'wait', '--for=condition=Ready', 'node/' + config['node_name'], '--timeout=180s'], check=True, timeout=200)
        # Publish eligibility only after the generated containerd config contains
        # our exact runtime, keeping unsandboxed fallback impossible.
        generated = template.parent / 'config.toml'
        verify_generated_config(read_runtime_text(generated, 262144), root, workspace_profile)
        verify_installed_runtime(root, stage)
        if agent:
            verify_runtime_class(read('get', 'runtimeclass', 'hakopod-actions'), ident)
        else:
            apply_runtime_class(ident, kube, read, tmp)
        current = read('get', 'node', config['node_name'])
        if current['metadata']['uid'] != node['metadata']['uid'] or current['metadata'].get('labels', {}).get(INSTALLATION_LABEL) != ident or 'hakopod.io/actions-workspace-probe' in current['metadata'].get('annotations', {}) or any(name in current['metadata'].get('labels', {}) for name in (RUNTIME_LABEL, WORKSPACE_LABEL)):
            raise ValueError('Actions node eligibility changed before runtime verification completed')
        subprocess.run([*kube, 'label', 'node', config['node_name'], RUNTIME_LABEL + '=ready',
                        '--resource-version=' + current['metadata']['resourceVersion']], check=True, timeout=30)
        if workspace_profile == WORKSPACE_PROFILE:
            context = subprocess.check_output([*kube, 'config', 'current-context'], text=True, timeout=15).strip()
            # The ordinary probe acquires the same local lock itself. Keep the
            # outer installation lock while handing off this narrower lock.
            maintenance.close()
            subprocess.run(['/opt/hakopod/current/hakopod-server', 'actions-workspace-probe',
                            '--kubeconfig', '/etc/hakopod/admin-kubeconfig', '--context', context,
                            '--node', config['node_name'], '--installation-id', ident,
                            '--workspace-profile', workspace_profile], check=True, timeout=1000)
        atomic_write(profile_path, workspace_profile + '\n', 0o600)


def agent_cli(argv=None):
    parser = argparse.ArgumentParser(description='Prepare this adopted K3s agent for Managed Actions.')
    parser.add_argument('command', choices=['agent'])
    parser.add_argument('--node', required=True, help='Kubernetes node name of this host')
    parser.add_argument('--installation-id', required=True, help='ID from the server\'s /etc/hakopod/installation.json')
    parser.add_argument('--workspace-profile', choices=['vfs'])
    args = parser.parse_args(argv)
    if os.geteuid() != 0:
        raise ValueError('Run agent setup as root')
    os.umask(0o077)

    def read(*command):
        raw = subprocess.check_output([*AGENT_KUBE, *command, '-o', 'json'], timeout=20, text=True)
        return json.loads(raw) if raw.strip() else {}
    install_agent(args.node, args.installation_id, AGENT_KUBE, read, args.workspace_profile)
    print('Managed Actions runtime is ready on ' + args.node + '.')


if __name__ == '__main__':
    agent_cli()
