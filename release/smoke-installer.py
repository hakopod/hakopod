#!/usr/bin/env python3
"""Disposable Linux artifact/permission smoke. Does not perform a K3s host install."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import platform
import subprocess
import urllib.request
import uuid

ROOT = Path(__file__).resolve().parents[1]
PINS = json.loads((ROOT / 'installer/pins.json').read_text())
IMAGE = 'debian:13-slim@sha256:d7e12182ce18b85b93007c1dedf31f2d29e01ccf3182cc4017c709b6259bc132'
IMAGES = {
    'amd64': 'debian@sha256:abc9cb88a5587630d7f915f47b23b0668fe250fbfc6457aa4d52b534c1bbf73f',
    'arm64': 'debian@sha256:7215f78f35ffe58fe13f244fac9c4f21326d55187271fbb3e1a8aa5cc7e387ab',
}

SCRIPT = r'''
set -eu
umask 077
export DEBIAN_FRONTEND=noninteractive
apt-get update -qq
apt-get install -y -qq --no-install-recommends python3 shellcheck systemd ca-certificates curl openssl kmod >/tmp/packages.log
mkdir -m 0755 /work
(cd /artifacts && sha256sum --check SHA256SUMS)
python3 /repo/release/bootstrap-version.py /artifacts "$VERSION"
python3 /repo/installer/host.py unpack --source "/artifacts/hakopod_${VERSION}_installer.tar.gz" --destination /work/kit --root "hakopod_${VERSION}_installer"
export KIT_ROOT="/work/kit/hakopod_${VERSION}_installer"
python3 -m unittest discover -s "$KIT_ROOT/installer" -p 'test_*.py'
shellcheck "$KIT_ROOT/scripts/install.sh" "$KIT_ROOT/scripts/installer.sh"
python3 - <<'PY'
import json,os
from pathlib import Path
c=json.loads((Path(os.environ['KIT_ROOT'])/'installer/example.json').read_text());c['version']=os.environ['VERSION'];Path('/work/config.json').write_text(json.dumps(c))
PY
bash "$KIT_ROOT/scripts/install.sh" --artifact-dir /artifacts --config /work/config.json --dry-run --arch "$ARCH"
# This fixture deliberately bypasses host preflight to test preparation/file modes
# in an ordinary isolated container. It never starts K3s or systemd services.
installation=$(python3 "$KIT_ROOT/installer/host.py" prepare --config /work/config.json --artifact-dir /artifacts --arch "$ARCH")
python3 "$KIT_ROOT/installer/host.py" render --config /work/config.json --arch "$ARCH" --id "$installation" --destination /work/rendered
useradd --system --home-dir /nonexistent --no-create-home --shell /usr/sbin/nologin hakopod-api
useradd --system --home-dir /nonexistent --no-create-home --shell /usr/sbin/nologin hakopod-dashboard
chown hakopod-api:hakopod-api /etc/hakopod/secrets/setup-token /etc/hakopod/secrets/auth-encryption-key
chmod 0400 /etc/hakopod/secrets/setup-token /etc/hakopod/secrets/auth-encryption-key
python3 "$KIT_ROOT/installer/host.py" unpack --source "/artifacts/hakopod_${VERSION}_linux_${ARCH}.tar.gz" --destination /work/server --root "hakopod_${VERSION}_linux_${ARCH}"
python3 "$KIT_ROOT/installer/host.py" unpack --source "/artifacts/hakopod_${VERSION}_dashboard.tar.gz" --destination /work/dashboard --root "hakopod_${VERSION}_dashboard"
python3 "$KIT_ROOT/installer/host.py" unpack --source /upstream/node.tar.gz --destination /work/node --root "$NODE_ROOT"
python3 "$KIT_ROOT/installer/host.py" unpack --source /upstream/helm.tar.gz --destination /work/helm --root "linux-$ARCH"
install -d -m 0755 /opt/hakopod/releases /opt/hakopod/tools
mv "/work/server/hakopod_${VERSION}_linux_${ARCH}" "/opt/hakopod/releases/$VERSION"
mv "/work/dashboard/hakopod_${VERSION}_dashboard" "/opt/hakopod/releases/$VERSION/dashboard"
mv "/work/node/$NODE_ROOT" /opt/hakopod/tools/node
install -m 0755 /upstream/k3s /opt/hakopod/tools/k3s
install -m 0755 "/work/helm/linux-$ARCH/helm" /opt/hakopod/tools/helm
ln -s "releases/$VERSION" /opt/hakopod/current
test "$(runuser -u hakopod-api -- /opt/hakopod/current/hakopod version)" = "$VERSION"
runuser -u hakopod-api -- sh -c 'test -r /etc/hakopod/secrets/setup-token && test -r /etc/hakopod/secrets/auth-encryption-key && ! test -r /etc/hakopod/secrets/session-secret'
runuser -u hakopod-dashboard -- sh -c 'test -r /opt/hakopod/current/dashboard/serve.mjs && ! test -r /etc/hakopod/secrets/setup-token'
/opt/hakopod/tools/k3s --version
/opt/hakopod/tools/helm version --short
/opt/hakopod/tools/node/bin/node --version
systemd-analyze verify /work/rendered/hakopod-k3s.service /work/rendered/hakopod-api.service /work/rendered/hakopod-dashboard.service
/opt/hakopod/tools/helm template hakopod-ingress "$KIT_ROOT/deploy/charts/hakopod-platform" --namespace haproxy-controller --kube-version v1.35.8 --values /work/rendered/haproxy.json > /work/ingress.yaml
python3 /repo/release/smoke-installer-runtime.py
'''

def download(pin, destination):
    if not destination.exists():
        partial = destination.with_suffix('.partial')
        with urllib.request.urlopen(pin['url'], timeout=60) as source, partial.open('wb') as target:
            size = 0
            while chunk := source.read(1024 * 1024):
                size += len(chunk)
                if size > 256 * 1024 * 1024: raise ValueError('Upstream artifact exceeded download bound')
                target.write(chunk)
        partial.replace(destination)
    h = hashlib.sha256()
    with destination.open('rb') as stream:
        for chunk in iter(lambda: stream.read(1024 * 1024), b''): h.update(chunk)
    if h.hexdigest() != pin['sha256']: raise ValueError('Pinned upstream checksum mismatch: ' + destination.name)

def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--version', default='0.1.0-dev')
    parser.add_argument('--arch', action='append', choices=('amd64', 'arm64'))
    args = parser.parse_args()
    architectures = args.arch or [('arm64' if platform.machine() in ('arm64', 'aarch64') else 'amd64')]
    artifact_dir = ROOT / '.local/installer-artifacts' / args.version
    if not (artifact_dir / 'SHA256SUMS').is_file(): raise SystemExit('Build installer artifacts first')
    artifact_hashes = {}
    for path in artifact_dir.glob('*.tar.gz'):
        h = hashlib.sha256()
        with path.open('rb') as stream:
            for chunk in iter(lambda: stream.read(1024 * 1024), b''): h.update(chunk)
        artifact_hashes[path.name] = h.hexdigest()
    report = dict(base_image=IMAGE, artifact_sha256=artifact_hashes, checks=[], limitations=['No full systemd host installation, K3s provisioning, reboot recovery, public DNS, or ACME issuance was exercised.'])
    for arch in architectures:
        upstream = ROOT / '.local/installer-smoke' / arch; upstream.mkdir(parents=True, exist_ok=True)
        for name in ('node', 'helm', 'k3s'):
            download(PINS[name][arch], upstream / (name + ('.tar.gz' if name != 'k3s' else '')))
        node_arch = 'x64' if arch == 'amd64' else 'arm64'
        name = 'hakopod-installer-smoke-' + arch + '-' + uuid.uuid4().hex[:8]
        command = ['docker', 'run', '--rm', '--name', name,
            '--label', 'com.hakopod.test=installer', '--memory=512m', '--cpus=2', '--pids-limit=256',
            '--platform', 'linux/' + arch, '--env', 'ARCH=' + arch, '--env', 'VERSION=' + args.version,
            '--env', 'NODE_ROOT=node-' + PINS['node']['version'] + '-linux-' + node_arch,
            '--env', 'GOMAXPROCS=2', '--env', 'GOMEMLIMIT=256MiB',
            '--mount', 'type=bind,src=' + str(ROOT) + ',dst=/repo,readonly',
            '--mount', 'type=bind,src=' + str(artifact_dir) + ',dst=/artifacts,readonly',
            '--mount', 'type=bind,src=' + str(upstream) + ',dst=/upstream,readonly',
            IMAGES[arch], 'bash', '-c', SCRIPT]
        print('Running disposable 512 MiB Linux/' + arch + ' installer artifact smoke', flush=True)
        try:
            result = subprocess.run(command, cwd=ROOT, capture_output=True, text=True, timeout=600)
        finally:
            subprocess.run(['docker', 'rm', '--force', name], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        (upstream / 'last-smoke.log').write_text(result.stdout + result.stderr)
        print(result.stdout, end=''); print(result.stderr, end='')
        if result.returncode: raise SystemExit('Linux smoke failed; inspect ' + str(upstream / 'last-smoke.log'))
        observation = next(json.loads(line) for line in result.stdout.splitlines() if line.startswith('{"result": "PASS"'))
        host_arch = 'arm64' if platform.machine() in ('arm64', 'aarch64') else 'amd64'
        report['checks'].append(dict(architecture=arch, execution='native' if arch == host_arch else 'emulated',
            passed=True, observed_dashboard_rss_kib=observation['dashboard_rss_kib'],
            tested=['strict inputs and malicious archives', 'dry-run without service mutation', 'umask077 with separate unprivileged users',
                    'actual Go CLI/K3s/Helm/Node binaries', 'systemd unit verification', 'HAProxy host-port template',
                    'packaged dashboard SSR/static/auth boundaries and observed RSS', 'resume preserves secrets and refuses missing established keys']))
    destination = artifact_dir / 'installer-smoke.json'; destination.write_text(json.dumps(report, indent=2) + '\n')
    # Keep transfer verification complete after adding evidence.
    manifest = artifact_dir / 'SHA256SUMS'
    lines = [line for line in manifest.read_text().splitlines() if not line.endswith('  installer-smoke.json')]
    lines.append(hashlib.sha256(destination.read_bytes()).hexdigest() + '  installer-smoke.json')
    manifest.write_text('\n'.join(sorted(lines, key=lambda line: line[66:])) + '\n')

if __name__ == '__main__': main()
