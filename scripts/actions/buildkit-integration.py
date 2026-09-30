#!/usr/bin/env python3
"""Validate native test artifacts and run fixed suites in a candidate builder.

No host kernel tests are executed here. Artifact extraction is the only CLI
operation; run_kernel_tests requires the managed benchmark's Docker boundary.
"""
import argparse
import gzip
import hashlib
import json
import os
from pathlib import Path
import re
import stat
import tarfile
import time

MIB = 1024 * 1024
MAX_BINARY = 256 * MIB
MAX_BUNDLE = 512 * MIB
MAX_MANIFEST = 16 * 1024
DESTINATION = '/home/runner/_work/buildkit-integration'
UPSTREAM = '991535e0973488b6a429096d21fa13f81f2d89d8'
FILES = ('overlay.test', 'buildkitd.test', 'source.json')
SUITES = {
    'overlay.test': ('TestManagedUserXAttrSemanticProbe', 'TestManagedUserXAttrLayerImport'),
    'buildkitd.test': ('TestManagedSnapshotterPropagatesUserXAttrToReadOnlyMounts',),
}


def require(value, message):
    if not value:
        raise RuntimeError(message)


def private_path(path, directory=False):
    path = Path(path).absolute()
    require(path.resolve(strict=True) == path, 'Artifact path contains a symlink')
    info = path.lstat()
    require((stat.S_ISDIR(info.st_mode) if directory else stat.S_ISREG(info.st_mode))
            and not info.st_mode & 0o022, 'Artifact must be a regular path without shared write access')
    return path


def file_evidence(path, maximum):
    path = private_path(path)
    fd = os.open(path, os.O_RDONLY | os.O_NOFOLLOW)
    digest, size = hashlib.sha256(), 0
    with os.fdopen(fd, 'rb') as source:
        info = os.fstat(source.fileno())
        require(stat.S_ISREG(info.st_mode) and info.st_nlink == 1 and 0 < info.st_size <= maximum,
                'Artifact is empty, linked, or exceeds its size bound')
        while chunk := source.read(MIB):
            size += len(chunk)
            require(size <= maximum, 'Artifact grew beyond its size bound')
            digest.update(chunk)
        require(size == info.st_size, 'Artifact changed during validation')
    return {'bytes': size, 'sha256': digest.hexdigest()}


def candidate_revision(image):
    match = re.fullmatch(r'ghcr\.io/hakopod/buildkit:v0\.32\.2-hakopod-([a-f0-9]{40})@sha256:[a-f0-9]{64}', image)
    require(match is not None, 'Native tests require an explicitly pinned Hakopod candidate')
    return match[1]


def small_file(path, maximum):
    fd = os.open(private_path(path), os.O_RDONLY | os.O_NOFOLLOW)
    with os.fdopen(fd, 'rb') as source:
        require(stat.S_ISREG(os.fstat(source.fileno()).st_mode), 'Artifact changed file type')
        value = source.read(maximum + 1)
    require(0 < len(value) <= maximum, 'Artifact text exceeds its bound')
    return value


def validate_source(value, image, architecture, run_id=None):
    require(isinstance(value, dict) and set(value) == {'schema', 'repository', 'revision', 'run_id', 'run_attempt', 'architecture', 'upstream_revision', 'sha256'}
            and value.get('schema') == 1
            and value.get('repository') == 'hakopod/hakopod'
            and value.get('revision') == candidate_revision(image)
            and value.get('upstream_revision') == UPSTREAM
            and architecture in ('amd64', 'arm64') and value.get('architecture') == architecture,
            'Native test source or architecture differs from the selected candidate')
    require(isinstance(value.get('run_id'), str) and re.fullmatch(r'[1-9][0-9]{0,19}', value['run_id'])
            and (run_id is None or value['run_id'] == run_id)
            and type(value.get('run_attempt')) is int and 1 <= value['run_attempt'] <= 10000,
            'Native test workflow identity is invalid')
    checksums = value.get('sha256')
    require(isinstance(checksums, dict) and set(checksums) == {'buildkitd', 'userxattr.patch', *SUITES}
            and all(isinstance(v, str) and re.fullmatch(r'[a-f0-9]{64}', v) for v in checksums.values()),
            'Native test checksums are incomplete')
    return value


def load_bundle(directory, image, architecture, run_id=None, staged=False, ready=True):
    directory = private_path(directory, directory=True)
    names = []
    with os.scandir(directory) as entries:
        for entry in entries:
            names.append(entry.name)
            require(len(names) <= 5, 'Native test directory contains extra entries')
    expected = set(FILES) | ({'buildkit-integration.py'} if staged else set()) | ({'ready'} if staged and ready else set())
    require(set(names) == expected, 'Native test directory contains missing or unexpected files')
    files = {name: file_evidence(directory / name, MAX_MANIFEST if name == 'source.json' else MAX_BINARY) for name in FILES}
    require(sum(item['bytes'] for item in files.values()) <= MAX_BUNDLE, 'Native test bundle exceeds its bound')
    raw = small_file(directory / 'source.json', MAX_MANIFEST)
    require(hashlib.sha256(raw).hexdigest() == files['source.json']['sha256'], 'Native test manifest changed during validation')
    source = validate_source(json.loads(raw), image, architecture, run_id)
    require(all(files[name]['sha256'] == source['sha256'][name] for name in SUITES), 'Native test executable checksum mismatch')
    for name in SUITES:
        with (directory / name).open('rb') as binary:
            header = binary.read(20)
        machine = 62 if architecture == 'amd64' else 183
        require(header[:6] == b'\x7fELF\x02\x01' and len(header) == 20
                and int.from_bytes(header[18:20], 'little') == machine, 'Native test executable has the wrong ELF architecture')
    return {'source': source, 'files': files, 'total_bytes': sum(v['bytes'] for v in files.values()), 'directory': str(directory)}


class BoundedArchive:
    def __init__(self, source):
        self.source, self.count = source, 0

    def read(self, amount):
        require(0 <= amount <= MIB, 'Archive requested an oversized read')
        value = self.source.read(amount)
        self.count += len(value)
        require(self.count <= MAX_BUNDLE + MIB, 'Expanded artifact archive exceeds its bound')
        return value


def unpack_bundle(archive, destination, image, architecture, run_id):
    file_evidence(archive, MAX_BUNDLE)
    destination = Path(destination).absolute()
    require(destination.parent.resolve(strict=True) == destination.parent, 'Artifact destination parent contains a symlink')
    destination.mkdir(mode=0o700, exist_ok=False)
    names, size = set(), 0
    with Path(archive).open('rb') as compressed, gzip.GzipFile(fileobj=compressed) as expanded:
        with tarfile.open(fileobj=BoundedArchive(expanded), mode='r|') as bundle:
            for member in bundle:
                require(member.name in FILES and member.name not in names and member.isreg()
                        and not member.linkname and not member.pax_headers and not member.sparse,
                        'Artifact archive contains an unexpected entry')
                names.add(member.name)
                maximum = MAX_MANIFEST if member.name == 'source.json' else MAX_BINARY
                size += member.size
                require(0 < member.size <= maximum and size <= MAX_BUNDLE, 'Artifact archive member exceeds its bound')
                source = bundle.extractfile(member)
                require(source is not None, 'Artifact archive file is missing')
                fd = os.open(destination / member.name, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW,
                             0o600 if member.name == 'source.json' else 0o700)
                with os.fdopen(fd, 'wb') as output:
                    remaining = member.size
                    while remaining:
                        chunk = source.read(min(MIB, remaining))
                        require(chunk, 'Artifact archive file is truncated')
                        output.write(chunk)
                        remaining -= len(chunk)
    require(names == set(FILES), 'Artifact archive is incomplete')
    return load_bundle(destination, image, architecture, run_id)


def run_kernel_tests(benchmark, builder, bundle):
    container = 'buildx_buildkit_' + builder['name'] + '0'
    observed = benchmark.run(['exec', container, 'sha256sum', '/usr/bin/buildkitd'], maximum=4096).split()
    require(len(observed) == 2 and observed[0] == bundle['source']['sha256']['buildkitd'],
            'Candidate daemon checksum differs from the native test artifacts')
    root = '/var/lib/buildkit/hakopod-integration'
    benchmark.run(['exec', container, 'mkdir', '-m', '700', root])
    benchmark.run(['exec', container, 'mkdir', '-m', '700', root + '/tmp'])
    result = {'status': 'running', 'source': bundle['source'], 'files': bundle['files'], 'tests': []}
    benchmark.report['kernel_integration'] = result
    try:
        for name, tests in SUITES.items():
            benchmark.run(['cp', str(Path(bundle['directory']) / name), container + ':' + root + '/' + name], timeout=35)
            actual = benchmark.run(['exec', container, 'sha256sum', root + '/' + name], maximum=4096).split()
            require(len(actual) == 2 and actual[0] == bundle['files'][name]['sha256'], 'Copied native test checksum mismatch')
            started = time.monotonic()
            output = benchmark.run(['exec', '-e', 'HAKOPOD_BUILDKIT_TEST_OVERLAY=1', '-e', 'TMPDIR=' + root + '/tmp',
                container, root + '/' + name, '-test.v', '-test.run=^(' + '|'.join(tests) + ')$', '-test.timeout=60s'],
                timeout=70, maximum=MIB)
            require(not re.search(r'^\s*--- (SKIP|FAIL):', output, re.M), 'Native kernel test was skipped or failed')
            require(all(re.search(r'^--- PASS: ' + re.escape(test) + r' \(', output, re.M) for test in tests),
                    'Native kernel test did not report every required pass')
            result['tests'].append({'binary': name, 'tests': list(tests), 'status': 'passed',
                                    'duration_seconds': round(time.monotonic() - started, 3)})
        result['status'] = 'passed'
    finally:
        if result['status'] == 'running':
            result['status'] = 'failed'
    return result


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--archive', type=Path, required=True)
    parser.add_argument('--destination', type=Path, required=True)
    parser.add_argument('--image', required=True)
    parser.add_argument('--architecture', choices=('amd64', 'arm64'), required=True)
    parser.add_argument('--run-id', required=True)
    args = parser.parse_args()
    value = unpack_bundle(args.archive, args.destination, args.image, args.architecture, args.run_id)
    print(json.dumps({key: value[key] for key in ('source', 'files', 'total_bytes')}))


if __name__ == '__main__':
    main()
