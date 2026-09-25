"""Install the optional Actions runtime on an installer-owned K3s server.

This command restarts K3s, so it is an explicit operator maintenance action.
It never alters the default runtime or uses a customer's Docker socket.
"""
import hashlib
import json
import os
from pathlib import Path
import platform
import shutil
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


def runtime_section(root):
    return BEGIN + '''[plugins."io.containerd.cri.v1.runtime".containerd.runtimes.hakopod-actions]
  runtime_type = "io.containerd.runsc.v1"
  runtime_path = "ROOT/containerd-shim-runsc-v1"
[plugins."io.containerd.cri.v1.runtime".containerd.runtimes.hakopod-actions.options]
  TypeUrl = "io.containerd.runsc.v1.options"
  BinaryName = "ROOT/runsc"
  ConfigPath = "ROOT/runsc-actions.toml"
'''.replace('ROOT', str(root)) + END


def extend_template(current, root):
    section = runtime_section(root)
    if BEGIN in current:
        if current.count(BEGIN) != 1 or current.count(END) != 1 or section not in current:
            raise ValueError('Actions runtime configuration was changed; review it before upgrading')
        return current
    if 'hakopod-actions' in current or END in current:
        raise ValueError('An unowned Actions runtime configuration already exists')
    return current.rstrip() + '\n\n' + section


def atomic_write(path, value, mode=0o644):
    if path.is_symlink():
        raise ValueError('Refusing a symlink: ' + str(path))
    with tempfile.NamedTemporaryFile(dir=path.parent, delete=False) as temp:
        temp.write(value.encode())
        os.fchmod(temp.fileno(), mode)
        name = temp.name
    os.replace(name, path)


def install(config, marker, kube, read):
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
    existing = read('get', 'runtimeclass', 'hakopod-actions', '--ignore-not-found')
    if existing and existing.get('metadata', {}).get('labels', {}).get('hakopod.com/installation') != ident:
        raise ValueError('An unowned Actions RuntimeClass already exists')
    if TEMPLATE.is_symlink() or BASE.is_symlink():
        raise ValueError('Refusing a symlink in the runtime configuration')
    if BASE.exists() and (not (BASE / 'owner').is_file() or (BASE / 'owner').read_text() != ident):
        raise ValueError('An unowned Actions runtime directory already exists')
    root = BASE / VERSION
    original = TEMPLATE.read_text() if TEMPLATE.exists() else '{{ template "base" . }}\n'
    updated = extend_template(original, root)
    with tempfile.TemporaryDirectory(prefix='hakopod-actions-') as tmp:
        tmp = Path(tmp)
        archive = tmp / 'gvisor.tar.bz2'
        digest = hashlib.sha256()
        total = 0
        url = f'https://github.com/google/gvisor/releases/download/{VERSION}/gvisor-{platform.machine()}.tar.bz2'
        with urllib.request.urlopen(url, timeout=60) as response, archive.open('wb') as output:
            while chunk := response.read(1024 * 1024):
                total += len(chunk)
                if total > 180 * 1024 * 1024:
                    raise ValueError('Runtime download exceeded its size bound')
                digest.update(chunk)
                output.write(chunk)
        if digest.hexdigest() != DIGESTS[platform.machine()]:
            raise ValueError('Runtime checksum mismatch')
        extracted = tmp / 'extracted'
        with tarfile.open(archive) as tar:
            tar.extractall(extracted, filter='data')
        stage = tmp / 'runtime'
        stage.mkdir()
        for name in ('runsc', 'containerd-shim-runsc-v1', 'gvisor-bin'):
            matches = list(extracted.rglob(name))
            if len(matches) != 1:
                raise ValueError('Runtime archive is missing ' + name)
            if matches[0].is_dir():
                shutil.copytree(matches[0], stage / name, symlinks=True)
            else:
                shutil.copy2(matches[0], stage / name, follow_symlinks=False)
        (stage / 'runsc-actions.toml').write_text('[runsc_config]\n  platform = "systrap"\n  net-raw = "true"\n  allow-packet-socket-write = "true"\n  overlay2 = "root:memory,size=512m"\n')
        BASE.mkdir(exist_ok=True, mode=0o755)
        atomic_write(BASE / 'owner', ident)
        if not root.exists():
            shutil.copytree(stage, root, symlinks=True)
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
        if 'runtimes.hakopod-actions' not in generated.read_text():
            raise ValueError('K3s did not load the Actions runtime; no node was enabled')
        runtime = {'apiVersion': 'node.k8s.io/v1', 'kind': 'RuntimeClass', 'metadata': {'name': 'hakopod-actions', 'labels': {'app.kubernetes.io/managed-by': 'hakopod', 'hakopod.com/installation': ident}}, 'handler': 'hakopod-actions', 'scheduling': {'nodeSelector': {'hakopod.io/actions-runtime': 'ready'}}, 'overhead': {'podFixed': {'memory': '512Mi', 'cpu': '100m'}}}
        manifest = tmp / 'runtime.json'
        manifest.write_text(json.dumps(runtime))
        subprocess.run([*kube, 'apply', '--server-side', '--field-manager=hakopod-installer', '-f', str(manifest)], check=True, timeout=30)
        subprocess.run([*kube, 'label', 'node', config['node_name'], 'hakopod.io/actions-runtime=ready', '--overwrite'], check=True, timeout=30)
