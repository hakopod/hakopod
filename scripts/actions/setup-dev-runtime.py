#!/usr/bin/env python3
"""Install a pinned runner sandbox only in this CI job's disposable k3d cluster."""
import hashlib
import io
import os
from pathlib import Path
import subprocess
import tarfile
import tempfile
import urllib.request

if os.environ.get('GITHUB_ACTIONS') != 'true':
    raise SystemExit('This fixture only runs in an isolated GitHub Actions job')
node = 'k3d-hakopod-dev-server-0'
label = subprocess.check_output(['docker', 'inspect', '--format', '{{index .Config.Labels "k3d.cluster"}}', node], text=True).strip()
if label != 'hakopod-dev':
    raise SystemExit('Unexpected development container')
subprocess.run(['docker', 'exec', node, 'mkdir', '-p', '/usr/local/bin'], check=True)
arch = subprocess.check_output(['uname', '-m'], text=True).strip()
digests = {'x86_64': '81416511897ab8abd4e723d66823c5b0461a2ee3311cfa70d152404ef9b860cf', 'aarch64': '2b162adb35860f598ab2f89b9d752bff2c7ee6175c05d9cc532174a336cfb38c'}
if arch not in digests:
    raise SystemExit('Unsupported architecture')
with urllib.request.urlopen(f'https://github.com/google/gvisor/releases/download/release-20260907.0/gvisor-{arch}.tar.bz2', timeout=60) as response:
    data = response.read(180 * 1024 * 1024 + 1)
if hashlib.sha256(data).hexdigest() != digests[arch]:
    raise SystemExit('gVisor checksum mismatch')
with tempfile.TemporaryDirectory() as tmp:
    root = Path(tmp)
    with tarfile.open(fileobj=io.BytesIO(data)) as archive:
        archive.extractall(root, filter='data')
    for name in ('runsc', 'containerd-shim-runsc-v1', 'gvisor-bin'):
        matches = list(root.rglob(name))
        if len(matches) != 1 or not matches[0].is_file():
            raise SystemExit(f'Missing runtime file {name}')
        subprocess.run(['docker', 'cp', str(matches[0]), f'{node}:/usr/local/bin/{name}'], check=True)
    config = root / 'runsc-actions.toml'
    config.write_text('[runsc_config]\n  platform = "systrap"\n  net-raw = "true"\n  allow-packet-socket-write = "true"\n')
    template = root / 'config-v3.toml.tmpl'
    template.write_text('''{{ template "base" . }}
[plugins."io.containerd.cri.v1.runtime".containerd.runtimes.hakopod-actions]
  runtime_type = "io.containerd.runsc.v1"
[plugins."io.containerd.cri.v1.runtime".containerd.runtimes.hakopod-actions.options]
  TypeUrl = "io.containerd.runsc.v1.options"
  ConfigPath = "/etc/runsc-actions.toml"
''')
    subprocess.run(['docker', 'exec', node, 'test', '!', '-e', '/etc/runsc-actions.toml'], check=True)
    subprocess.run(['docker', 'cp', str(config), f'{node}:/etc/runsc-actions.toml'], check=True)
    subprocess.run(['docker', 'cp', str(template), f'{node}:/var/lib/rancher/k3s/agent/etc/containerd/config-v3.toml.tmpl'], check=True)
subprocess.run(['docker', 'restart', node], check=True)
print('Installed pinned gVisor in the disposable development node')
