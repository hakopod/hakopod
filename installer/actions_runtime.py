"""Install the optional Actions runtime on an installer-owned K3s server.

This command restarts K3s, so it is an explicit operator maintenance action.
It never alters the default runtime or uses a customer's Docker socket.
"""
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


def install(config, marker, kube, read, workspace_profile=None):
    with RuntimeMaintenanceLock() as maintenance:
        return install_locked(config, marker, kube, read, workspace_profile, maintenance)


def install_locked(config, marker, kube, read, workspace_profile, maintenance):
    if platform.system() != 'Linux' or platform.machine() not in DIGESTS:
        raise ValueError('Actions requires Linux AMD64 or ARM64')
    if not TEMPLATE.parent.is_dir():
        raise ValueError('The installer-owned K3s containerd directory is missing')
    # Only this installation's single local server is changed. An external or
    # multi-node cluster needs its own reviewed node runtime configuration.
    nodes = read('get', 'nodes')['items']
    if len(nodes) != 1 or nodes[0]['metadata']['name'] != config['node_name']:
        raise ValueError('Automatic Actions setup requires the installer-owned single server')
    unit = subprocess.check_output(['systemctl', 'show', 'hakopod-k3s', '--property=ExecStart', '--value'], text=True, timeout=15)
    if '/opt/hakopod/tools/k3s' not in unit or '/etc/hakopod/k3s.yaml' not in unit:
        raise ValueError('The installer-owned K3s unit could not be verified')
    ident = marker['id']
    if nodes[0]['metadata'].get('labels', {}).get('hakopod.com/installation') != ident:
        raise ValueError('Actions node belongs to another installation')
    if 'hakopod.io/actions-workspace-probe' in nodes[0]['metadata'].get('annotations', {}):
        raise ValueError('Finish the existing Actions workspace probe before runtime maintenance')
    if TEMPLATE.is_symlink() or BASE.is_symlink():
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
    if existing and existing.get('metadata', {}).get('labels', {}).get('hakopod.com/installation') != ident:
        raise ValueError('An unowned Actions RuntimeClass already exists')
    if BASE.exists():
        info = BASE.lstat()
        if not stat.S_ISDIR(info.st_mode) or info.st_uid != 0 or stat.S_IMODE(info.st_mode) & 0o022 or read_runtime_text(BASE / 'owner', 128) != ident:
            raise ValueError('An unowned Actions runtime directory already exists')
    root = BASE / VERSION
    if root.exists() or root.is_symlink():
        info = root.lstat()
        if root.resolve(strict=True) != root or not stat.S_ISDIR(info.st_mode) or info.st_uid != 0:
            raise ValueError('Installed Actions runtime directory has invalid ownership')
    original = read_runtime_text(TEMPLATE, 262144) if TEMPLATE.exists() else '{{ template "base" . }}\n'
    updated = extend_template(original, root, workspace_profile)
    verify_generated_config(runtime_section(root, workspace_profile), root, workspace_profile)
    withdraw = [name + '-' for name in (RUNTIME_LABEL, WORKSPACE_LABEL) if name in nodes[0]['metadata'].get('labels', {})]
    if withdraw:
        subprocess.run([*kube, 'label', 'node', config['node_name'], *withdraw,
                        '--resource-version=' + nodes[0]['metadata']['resourceVersion']], check=True, timeout=30)
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
        if current['metadata']['uid'] != nodes[0]['metadata']['uid'] or current['metadata'].get('labels', {}).get('hakopod.com/installation') != ident or 'hakopod.io/actions-workspace-probe' in current['metadata'].get('annotations', {}):
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
            atomic_write(TEMPLATE, updated)
        subprocess.run(['systemctl', 'restart', 'hakopod-k3s'], check=True, timeout=300)
        subprocess.run([*kube, 'wait', '--for=condition=Ready', 'node/' + config['node_name'], '--timeout=180s'], check=True, timeout=200)
        # Publish eligibility only after the generated containerd config contains
        # our exact runtime, keeping unsandboxed fallback impossible.
        generated = TEMPLATE.parent / 'config.toml'
        verify_generated_config(read_runtime_text(generated, 262144), root, workspace_profile)
        verify_installed_runtime(root, stage)
        runtime = {'apiVersion': 'node.k8s.io/v1', 'kind': 'RuntimeClass', 'metadata': {'name': 'hakopod-actions', 'labels': {'app.kubernetes.io/managed-by': 'hakopod', 'hakopod.com/installation': ident}}, 'handler': 'hakopod-actions', 'scheduling': {'nodeSelector': {'hakopod.io/actions-runtime': 'ready'}}, 'overhead': {'podFixed': {'memory': '512Mi', 'cpu': '100m'}}}
        manifest = tmp / 'runtime.json'
        manifest.write_text(json.dumps(runtime))
        subprocess.run([*kube, 'apply', '--server-side', '--field-manager=hakopod-installer', '-f', str(manifest)], check=True, timeout=30)
        installed = read('get', 'runtimeclass', 'hakopod-actions')
        if installed.get('metadata', {}).get('labels', {}).get('hakopod.com/installation') != ident or installed.get('metadata', {}).get('labels', {}).get('app.kubernetes.io/managed-by') != 'hakopod' or any(installed.get(key) != runtime[key] for key in ('handler', 'scheduling', 'overhead')):
            raise ValueError('Actions RuntimeClass verification failed')
        current = read('get', 'node', config['node_name'])
        if current['metadata']['uid'] != nodes[0]['metadata']['uid'] or current['metadata'].get('labels', {}).get('hakopod.com/installation') != ident or 'hakopod.io/actions-workspace-probe' in current['metadata'].get('annotations', {}) or any(name in current['metadata'].get('labels', {}) for name in (RUNTIME_LABEL, WORKSPACE_LABEL)):
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
