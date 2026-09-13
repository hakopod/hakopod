#!/usr/bin/env python3
"""Assemble verified release assets and checksums; never upload or tag anything."""
import argparse
import hashlib
import json
from pathlib import Path
import re
import shutil
import subprocess

ROOT = Path(__file__).resolve().parents[1]


def digest(path):
    value = hashlib.sha256()
    with path.open('rb') as stream:
        for chunk in iter(lambda: stream.read(1024 * 1024), b''):
            value.update(chunk)
    return value.hexdigest()


def manifest(directory):
    return ''.join(digest(path) + '  ' + path.name + '\n' for path in sorted(directory.iterdir())
        if path.is_file() and not path.name.startswith('.') and path.name not in ('SHA256SUMS', 'build-provenance.intoto.jsonl'))


def verify(directory):
    seen = set()
    for line in (directory / 'SHA256SUMS').read_text().splitlines():
        match = re.fullmatch(r'([0-9a-f]{64})  ([A-Za-z0-9][A-Za-z0-9._-]*)', line)
        if not match or match[2] in seen:
            raise ValueError('Invalid checksum manifest')
        seen.add(match[2])
        path = directory / match[2]
        if path.is_symlink() or not path.is_file() or digest(path) != match[1]:
            raise ValueError('Checksum failed: ' + match[2])
    if not seen or (directory / 'SHA256SUMS').read_text() != manifest(directory):
        raise ValueError('Checksum manifest does not cover the complete release directory')


def release_version(tag):
    if not re.fullmatch(r'v(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)(?:-[A-Za-z0-9]+(?:[.-][A-Za-z0-9]+)*)?', tag) or len(tag) > 65:
        raise ValueError('Release tag must be vMAJOR.MINOR.PATCH with an optional prerelease suffix')
    return tag[1:]


def assemble(version):
    destination = ROOT / '.local/publication' / version
    if destination.exists():
        raise ValueError('Publication staging already exists; preserve or remove that generated directory before rebuilding')
    sources = [ROOT / '.local/releases' / version, ROOT / '.local/installer-artifacts' / version]
    for source in sources:
        verify(source)
    go = json.loads((sources[0] / 'provenance.json').read_text())
    installer = json.loads((sources[1] / 'installer-provenance.json').read_text())
    revision = subprocess.check_output(['git', 'rev-parse', 'HEAD'], cwd=ROOT, text=True).strip()
    if (go['version'] != version or go['source_revision'] != revision or go['source_dirty'] or go['source_changed_during_build']
            or installer['version'] != version or installer['source_changed_during_packaging']
            or installer['dashboard_source'] != 'snapshot build; installed dependency closure'):
        raise ValueError('Publication requires an unchanged source build of this exact revision and version')
    destination.mkdir(parents=True)
    for source in sources:
        for path in sorted(source.iterdir()):
            if not path.is_file() or path.name.startswith('.') or path.name == 'SHA256SUMS':
                continue
            target = destination / path.name
            if target.exists() and digest(target) != digest(path):
                raise ValueError('Builders disagree on artifact bytes: ' + path.name)
            shutil.copyfile(path, target)
    (destination / 'SHA256SUMS').write_text(manifest(destination))
    verify(destination)
    return destination


def finalize(directory):
    # Verify original build assets before adding architecture smoke evidence.
    original = (directory / 'SHA256SUMS').read_text()
    for line in original.splitlines():
        expected, name = line.split('  ', 1)
        if not re.fullmatch(r'[A-Za-z0-9][A-Za-z0-9._-]*', name) or digest(directory / name) != expected:
            raise ValueError('Original build artifact changed before publication')
    for arch in ('amd64', 'arm64'):
        report = json.loads((directory / ('installer-smoke-' + arch + '.json')).read_text())
        if len(report['checks']) != 1 or report['checks'][0]['architecture'] != arch or not report['checks'][0]['passed']:
            raise ValueError('Missing successful native Linux/' + arch + ' smoke')
        for name, expected in report['artifact_sha256'].items():
            if not re.fullmatch(r'[A-Za-z0-9][A-Za-z0-9._-]*', name) or digest(directory / name) != expected:
                raise ValueError('Smoke evidence does not match the published artifact bytes')
    (directory / 'SHA256SUMS').write_text(manifest(directory))
    verify(directory)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('action', choices=('version', 'assemble', 'finalize', 'verify'))
    parser.add_argument('value', help='Tag for version/assemble, directory for finalize/verify')
    args = parser.parse_args()
    if args.action == 'version': print(release_version(args.value))
    elif args.action == 'assemble': print(assemble(release_version(args.value)))
    elif args.action == 'finalize': finalize(Path(args.value))
    else: verify(Path(args.value))


if __name__ == '__main__':
    main()
