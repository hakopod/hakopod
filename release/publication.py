#!/usr/bin/env python3
"""Assemble verified release assets and checksums; never upload or tag anything."""
import argparse
import hashlib
import importlib.util
import json
from pathlib import Path
import re
import shutil
import subprocess
import tarfile

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


def builder_module(filename):
    return tooling_module(Path(__file__).with_name(filename))


def tooling_module(path):
    specification = importlib.util.spec_from_file_location('publication_' + path.stem.replace('-', '_'), path)
    module = importlib.util.module_from_spec(specification)
    specification.loader.exec_module(module)
    module.ROOT = ROOT
    return module


def validate_public_ui():
    # CI restores the public consumer files without initializing the gitlink.
    # Git's dirty status cannot authenticate those files; compare their exact
    # selected tree to the tagged, checksum-verified source bundle instead.
    ui = tooling_module(Path(__file__).resolve().parents[1] / 'scripts/ui-source.py')
    source, archive = ROOT / 'packages/ui', ROOT / 'third_party/ui/ui-source.tar.gz'
    checksum = archive.with_suffix('.gz.sha256').read_text().strip()
    if not re.fullmatch(r'[0-9a-f]{64}  ui-source.tar.gz', checksum) or digest(archive) != checksum[:64]:
        raise ValueError('Tracked public UI source bundle checksum differs')
    if archive.stat().st_size > ui.LIMIT:
        raise ValueError('Public UI source bundle exceeds its bound')
    expected, size = {}, 0
    with tarfile.open(archive, 'r:gz') as bundle:
        for member in bundle:
            size += member.size
            name = member.name.removeprefix('ui/')
            if (not member.isfile() or not member.name.startswith('ui/') or name in expected
                    or len(expected) >= 512 or size > ui.LIMIT):
                raise ValueError('Invalid public UI source bundle')
            expected[name] = hashlib.sha256(bundle.extractfile(member).read()).hexdigest()
    actual = {path.relative_to(source).as_posix(): digest(path) for path in ui.source_files(source)}
    if not expected or actual != expected:
        raise ValueError('Restored public UI source differs from the tagged consumer bundle')


def validate_sources(version, go, installer):
    revision = subprocess.check_output(['git', 'rev-parse', 'HEAD'], cwd=ROOT, text=True).strip()
    dirty = subprocess.check_output(['git', 'status', '--porcelain', '--ignore-submodules=all'], cwd=ROOT, text=True).strip()
    if (dirty or any(record.get('version') != version or record.get('source_revision') != revision
                     or record.get('source_dirty') is not False for record in (go, installer))
            or go.get('source_changed_during_build') is not False
            or installer.get('source_changed_during_packaging') is not False
            or installer.get('dashboard_source') != 'snapshot build; installed dependency closure'):
        raise ValueError('Publication requires both builders and the current tree to be clean at this exact revision and version')
    validate_public_ui()
    # These scopes differ: Go snapshots include cmd/internal/API and dependency
    # locks; installer snapshots include dashboard/UI, scripts and deployment inputs.
    if (go.get('source_fingerprint_sha256') != builder_module('build.py').fingerprint()[0]
            or installer.get('source_fingerprint_sha256') != builder_module('build-installer.py').source_fingerprint()):
        raise ValueError('Current source fingerprints differ from the built Go or installer snapshot')


def assemble(version):
    destination = ROOT / '.local/publication' / version
    if destination.exists():
        raise ValueError('Publication staging already exists; preserve or remove that generated directory before rebuilding')
    sources = [ROOT / '.local/releases' / version, ROOT / '.local/installer-artifacts' / version]
    for source in sources:
        verify(source)
    go = json.loads((sources[0] / 'provenance.json').read_text())
    installer = json.loads((sources[1] / 'installer-provenance.json').read_text())
    validate_sources(version, go, installer)
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
    validate_sources(version, go, installer)
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
        if (len(report['checks']) != 1 or report['checks'][0]['architecture'] != arch
                or not report['checks'][0]['passed'] or report['checks'][0].get('execution') != 'native'):
            raise ValueError('Missing successful native Linux/' + arch + ' smoke')
        if set(report['artifact_sha256']) != {path.name for path in directory.glob('*.tar.gz')}:
            raise ValueError('Smoke evidence does not cover every archive')
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
