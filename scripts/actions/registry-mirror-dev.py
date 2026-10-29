#!/usr/bin/env python3
"""Install the Actions registry mirror on the dev cluster and warm it.

The service pull benchmark then measures a pull served by a warm mirror. The
manifests are the installer's own, so this exercises what operators install.
"""
import contextlib
import importlib.util
import json
import os
import subprocess
import sys
import tempfile
import time
import urllib.request
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / 'installer'))
import actions_runtime  # noqa: E402

KUBE = ['kubectl', '--kubeconfig', str(ROOT / '.local/kubeconfig'), '--context', 'k3d-hakopod-dev']
ACCEPT = ', '.join(['application/vnd.oci.image.index.v1+json', 'application/vnd.docker.distribution.manifest.list.v2+json',
                   'application/vnd.oci.image.manifest.v1+json', 'application/vnd.docker.distribution.manifest.v2+json'])


def get(url, accept=ACCEPT):
    request = urllib.request.Request(url, headers={'Accept': accept})
    with urllib.request.urlopen(request, timeout=300) as response:
        return response.read()


def warm(base, reference, architecture):
    repository, digest = reference.removeprefix('docker.io/').rsplit('@', 1)
    index = json.loads(get(f'{base}/v2/{repository}/manifests/{digest}'))
    manifest = index
    if 'manifests' in index:
        chosen = [m for m in index['manifests'] if m.get('platform', {}).get('os') == 'linux' and m['platform'].get('architecture') == architecture]
        manifest = json.loads(get(f'{base}/v2/{repository}/manifests/{chosen[0]["digest"]}'))
    total = 0
    for blob in [manifest['config'], *manifest['layers']]:
        total += len(get(f'{base}/v2/{repository}/blobs/{blob["digest"]}', '*/*'))
    return total


def main():
    architecture = os.environ['ARCH']
    with tempfile.TemporaryDirectory() as tmp:
        read = lambda *args: json.loads(subprocess.check_output([*KUBE, *args, '-o', 'json'], timeout=30) or b'{}')
        actions_runtime.apply_registry_mirror('dev', KUBE, read, tmp)
    spec = importlib.util.spec_from_file_location('benchmark', ROOT / 'scripts/actions/service-pull-benchmark.py')
    benchmark = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(benchmark)
    forward = subprocess.Popen([*KUBE, '-n', actions_runtime.MIRROR_NAMESPACE, 'port-forward', 'service/registry-mirror', '15000:5000'],
                               stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    try:
        for _ in range(30):
            with contextlib.suppress(OSError):
                get('http://127.0.0.1:15000/v2/', '*/*')
                break
            time.sleep(1)
        for service in benchmark.SERVICES:
            started = time.monotonic()
            size = warm('http://127.0.0.1:15000', service['reference'], architecture)
            print(f'warmed {service["name"]}: {size} bytes in {time.monotonic() - started:.1f}s')
    finally:
        forward.terminate()


if __name__ == '__main__':
    main()
