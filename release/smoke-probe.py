#!/usr/bin/env python3
"""Verify the packaged probe and init-copy path on a native Docker architecture."""
import argparse
import json
from pathlib import Path
import subprocess
import tempfile

ROOT = Path(__file__).resolve().parents[1]


def command(*args, check=True):
    return subprocess.run(args, text=True, capture_output=True, check=check, timeout=60)


def smoke(image, arch):
    metadata = json.loads(command('docker', 'image', 'inspect', image).stdout)[0]
    engine = command('docker', 'info', '--format', '{{.Architecture}}').stdout.strip()
    native = {'x86_64': 'amd64', 'aarch64': 'arm64', 'amd64': 'amd64', 'arm64': 'arm64'}.get(engine)
    if native != arch or metadata['Architecture'] != arch or metadata['Os'] != 'linux':
        raise ValueError('Probe smoke must execute natively on the requested Linux architecture')
    config = metadata['Config']
    if config['User'] != '65532:65532' or config['Entrypoint'] != ['/hakopod-probe']:
        raise ValueError('Probe must use the nonroot entrypoint')
    image_id = metadata['Id']
    bounds = ['--network', 'none', '--read-only', '--memory', '64m', '--cpus', '1',
              '--pids-limit', '32', '--cap-drop', 'ALL', '--security-opt', 'no-new-privileges']
    container = None
    try:
        with tempfile.TemporaryDirectory(prefix='hakopod-probe-') as name:
            directory = Path(name)
            # The actual init container receives a writable fsGroup-owned emptyDir.
            directory.chmod(0o755)
            target = directory / 'installed'
            target.mkdir(mode=0o777)
            target.chmod(0o777)
            container = command('docker', 'create', *bounds, '--mount',
                                f'type=bind,src={target},dst=/probe', image_id, 'install').stdout.strip()
            command('docker', 'start', '--attach', container)
            state = json.loads(command('docker', 'inspect', '--format', '{{json .State}}', container).stdout)
            if state['ExitCode'] != 0 or state['OOMKilled']:
                raise ValueError('Probe init installation failed')
            # Read from the exact container that performed installation. Some local
            # engines inject an additional trust root when a container starts.
            for source, filename in [('/hakopod-probe', 'original'),
                                     ('/etc/ssl/certs/ca-certificates.crt', 'roots'),
                                     ('/licenses/hakopod/LICENSE', 'LICENSE'),
                                     ('/licenses/hakopod/NOTICE', 'NOTICE')]:
                command('docker', 'cp', container + ':' + source, str(directory / filename))
            for filename in ('LICENSE', 'NOTICE'):
                if (directory / filename).read_bytes() != (ROOT / filename).read_bytes():
                    raise ValueError('Probe license files differ from source')
            for installed, original in [('hakopod-probe', 'original'), ('ca-certificates.crt', 'roots')]:
                path = target / installed
                if path.read_bytes() != (directory / original).read_bytes() or path.stat().st_mode & 0o777 != 0o555:
                    raise ValueError(f'Probe init copy differs or has incorrect permissions: {installed}, mode={path.stat().st_mode & 0o777:o}, bytes_match={path.read_bytes() == (directory / original).read_bytes()}')
            if b'-----BEGIN CERTIFICATE-----' not in (target / 'ca-certificates.crt').read_bytes():
                raise ValueError('Probe image has no public trust bundle')
            result = command('docker', 'run', '--rm', *bounds, '--mount',
                             f'type=bind,src={target},dst=/probe,readonly', '--entrypoint',
                             '/probe/hakopod-probe', image_id, '--protocol', 'invalid', check=False)
            if result.returncode != 1 or result.stderr.strip() != 'invalid readiness configuration':
                raise ValueError('Copied probe did not execute or failed to reject invalid input')
    finally:
        if container:
            command('docker', 'rm', '-f', container, check=False)
    return {'architecture': arch, 'execution': 'native', 'image_id': image_id, 'passed': True,
            'checks': ['nonroot', 'read-only-root', 'init-copy', 'trust-bundle', 'licenses', 'copied-binary-execution', 'invalid-config-rejected']}


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--image', required=True)
    parser.add_argument('--arch', required=True, choices=['amd64', 'arm64'])
    parser.add_argument('--output', type=Path, required=True)
    args = parser.parse_args()
    report = smoke(args.image, args.arch)
    args.output.parent.mkdir(parents=True, exist_ok=True)
    args.output.write_text(json.dumps(report, indent=2) + '\n')
    print('Native probe image smoke passed:', args.arch)
