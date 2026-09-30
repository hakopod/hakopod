#!/usr/bin/env python3
"""Compare a flattened candidate with the pinned upstream without starting it."""
import argparse
import copy
import hashlib
import json
from pathlib import Path, PurePosixPath
import re
import signal
import subprocess
import tarfile

UPSTREAM = 'docker.io/moby/buildkit:v0.32.2@sha256:28a898719c18a33f4e8000685287fa36fd0dd9560c6440227d3a732d79bb41d8'
UPSTREAM_REVISION = '991535e0973488b6a429096d21fa13f81f2d89d8'
MIB = 1024 * 1024
MAX_EXPORT = 2048 * MIB
GENERATED = {'etc/hosts', 'etc/hostname', 'etc/resolv.conf'}
DAEMON = 'usr/bin/buildkitd'
LICENSE = 'usr/share/licenses/hakopod-buildkit/LICENSE'
LICENSE_PARENTS = {'usr', 'usr/share', 'usr/share/licenses', 'usr/share/licenses/hakopod-buildkit'}


def require(condition, message):
    if not condition:
        raise RuntimeError(message)


def capture(arguments, maximum=MIB):
    process = subprocess.Popen(['docker', *arguments], stdout=subprocess.PIPE, stderr=subprocess.DEVNULL)
    try:
        output = process.stdout.read(maximum + 1)
        require(len(output) <= maximum, 'Docker response exceeded its bound')
        require(process.wait(timeout=20) == 0, 'Docker command failed: ' + arguments[0])
        return output
    finally:
        process.stdout.close()
        if process.poll() is None:
            process.kill()
            process.wait(timeout=10)


def inspect(image):
    return json.loads(capture(['image', 'inspect', '--format', '{{json .}}', image]))


def bounded_names(names):
    return json.dumps([name[:256] for name in sorted(names)[:20]])


def differing_names(expected, actual):
    return bounded_names(name for name in expected.keys() | actual.keys()
                         if name not in expected or name not in actual or expected[name] != actual[name])


def config_proof(upstream, candidate, architecture, revision):
    for image in (upstream, candidate):
        require(image.get('Os') == 'linux' and image.get('Architecture') == architecture,
                'Image architecture differs from the native CI job')
    require(candidate.get('RootFS', {}).get('Type') == 'layers'
            and len(candidate['RootFS'].get('Layers', [])) == 1, 'Candidate must contain exactly one filesystem layer')
    expected = copy.deepcopy(upstream['Config'])
    expected['Env'] = [*expected.get('Env', []), 'HAKOPOD_BUILDKIT_USERXATTR=true']
    expected['Labels'] = {**(expected.get('Labels') or {}),
        'org.opencontainers.image.source': 'https://github.com/hakopod/hakopod',
        'org.opencontainers.image.licenses': 'Apache-2.0',
        'io.hakopod.buildkit.upstream-revision': UPSTREAM_REVISION,
        'org.opencontainers.image.revision': revision}
    require(candidate['Config'] == expected, 'Flattening changed upstream runtime configuration fields: '
            + differing_names(expected, candidate['Config']))


class BoundedExport:
    def __init__(self, source):
        self.source, self.bytes = source, 0

    def read(self, amount):
        require(0 <= amount <= MIB, 'Export requested an oversized read')
        data = self.source.read(amount)
        self.bytes += len(data)
        require(self.bytes <= MAX_EXPORT, 'Filesystem export exceeded 2 GiB')
        return data


def normalized(name):
    require(len(name.encode('utf-8', 'surrogateescape')) <= 4096 and '\x00' not in name,
            'Filesystem path exceeded its bound')
    path = PurePosixPath(name)
    require('..' not in path.parts, 'Filesystem export contains a parent path')
    return str(path).removeprefix('/').removeprefix('./')


def filesystem(container):
    process = subprocess.Popen(['docker', 'export', container], stdout=subprocess.PIPE, stderr=subprocess.DEVNULL)
    records, link_targets = {}, {}
    try:
        stream = BoundedExport(process.stdout)
        with tarfile.open(fileobj=stream, mode='r|') as archive:
            for item in archive:
                name = normalized(item.name)
                require(name not in records and len(records) < 50000, 'Duplicate or excessive filesystem entries')
                require(not item.sparse and sum(len(k) + len(v) for k, v in item.pax_headers.items()) <= 65536,
                        'Unsupported or oversized filesystem metadata')
                attrs = {k: v for k, v in item.pax_headers.items()
                         if k.startswith(('SCHILY.xattr.', 'LIBARCHIVE.xattr.', 'SCHILY.acl.'))}
                record = {'mode': item.mode, 'uid': item.uid, 'gid': item.gid, 'xattrs': attrs}
                if item.isreg():
                    require(0 <= item.size <= 512 * MIB, 'Filesystem file exceeds 512 MiB')
                    source, remaining, digest = archive.extractfile(item), item.size, hashlib.sha256()
                    require(source is not None, 'Filesystem file is missing')
                    while remaining:
                        data = source.read(min(MIB, remaining))
                        require(data, 'Filesystem file is truncated')
                        digest.update(data)
                        remaining -= len(data)
                    record.update(type='file', size=item.size, sha256=digest.hexdigest())
                elif item.islnk():
                    record = {'type': 'hardlink', 'target': normalized(item.linkname)}
                elif item.issym():
                    require(len(item.linkname) <= 4096, 'Symbolic link target exceeds its bound')
                    record.update(type='symlink', target=item.linkname)
                elif item.isdir():
                    record.update(type='directory')
                elif item.ischr() or item.isblk():
                    record.update(type='character' if item.ischr() else 'block', major=item.devmajor, minor=item.devminor)
                elif item.isfifo():
                    record.update(type='fifo')
                else:
                    raise RuntimeError('Unsupported filesystem entry type')
                records[name] = record
        while stream.read(MIB):
            pass
        require(process.wait(timeout=20) == 0, 'Docker filesystem export failed')
    finally:
        process.stdout.close()
        if process.poll() is None:
            process.kill()
            process.wait(timeout=10)
    for name, record in records.items():
        if record['type'] not in ('file', 'hardlink'):
            continue
        target, seen = name, set()
        while records[target]['type'] == 'hardlink':
            require(target not in seen and len(seen) < 32, 'Filesystem hardlink cycle or excessive depth')
            seen.add(target)
            target = records[target]['target']
            require(target in records, 'Filesystem hardlink target is missing')
        require(records[target]['type'] == 'file', 'Filesystem hardlink does not target a regular file')
        link_targets.setdefault(target, []).append(name)
    resolved = dict(records)
    for target, names in link_targets.items():
        record = {**records[target], 'links': sorted(names)}
        for name in names:
            resolved[name] = record
    return resolved, stream.bytes


def local_file(path):
    digest, size = hashlib.sha256(), 0
    with path.open('rb') as source:
        while data := source.read(MIB):
            size += len(data)
            require(size <= 512 * MIB, 'Expected source file exceeds its bound')
            digest.update(data)
    return {'size': size, 'sha256': digest.hexdigest()}


def compare_files(upstream, candidate, binary, license_file):
    require(DAEMON in upstream and DAEMON in candidate, 'BuildKit daemon is missing')
    require(candidate[DAEMON] == {**upstream[DAEMON], **local_file(binary)},
            'Candidate daemon differs from the compiled binary or its upstream file metadata')
    require(LICENSE not in upstream and candidate.get(LICENSE) == {
        'type': 'file', 'mode': 0o644, 'uid': 0, 'gid': 0, 'xattrs': {}, 'links': [LICENSE], **local_file(license_file)},
        'Candidate license differs from its source or expected metadata')
    additions = set(candidate) - set(upstream)
    require(additions <= LICENSE_PARENTS | {LICENSE}, 'Flattening added unexpected filesystem entries: '
            + bounded_names(additions - LICENSE_PARENTS - {LICENSE}))
    for name in additions - {LICENSE}:
        require(candidate[name] == {'type': 'directory', 'mode': 0o755, 'uid': 0, 'gid': 0, 'xattrs': {}},
                'Added license directory has unexpected metadata: ' + bounded_names([name]))
    ignored = GENERATED | {DAEMON}
    expected = {name: record for name, record in upstream.items() if name not in ignored}
    actual = {name: record for name, record in candidate.items() if name not in ignored and name not in additions}
    require(actual == expected, 'Flattening changed upstream file content or metadata at paths: '
            + differing_names(expected, actual))
    return len(expected)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--candidate', required=True)
    parser.add_argument('--architecture', choices=('amd64', 'arm64'), required=True)
    parser.add_argument('--revision', required=True)
    parser.add_argument('--output', type=Path, required=True)
    args = parser.parse_args()
    require(re.fullmatch(r'[a-f0-9]{40}', args.revision), 'Invalid candidate source revision')
    require(re.fullmatch(r'ghcr\.io/hakopod/buildkit:' + args.revision + r'-[1-9][0-9]*-[1-9][0-9]*-' + args.architecture,
                         args.candidate), 'Unexpected local candidate reference')
    containers = []
    def expired(_signal, _frame):
        raise TimeoutError('Runtime identity proof exceeded five minutes')
    signal.signal(signal.SIGALRM, expired)
    signal.alarm(300)
    try:
        upstream, candidate = inspect(UPSTREAM), inspect(args.candidate)
        config_proof(upstream, candidate, args.architecture, args.revision)
        roots, sizes = [], []
        for image in (UPSTREAM, args.candidate):
            container = capture(['create', '--network=none', '--read-only', '--cap-drop=ALL',
                '--security-opt=no-new-privileges', image], maximum=256).decode().strip()
            require(re.fullmatch(r'[a-f0-9]{64}', container), 'Docker did not return an exact container ID')
            containers.append(container)
            root, size = filesystem(container)
            roots.append(root)
            sizes.append(size)
        source = Path(__file__).resolve().parent
        count = compare_files(*roots, source / 'buildkitd', source / 'LICENSE.upstream')
        report = {'schema': 1, 'status': 'passed', 'architecture': args.architecture, 'revision': args.revision,
            'upstream': UPSTREAM, 'candidate': args.candidate, 'candidate_image_id': candidate['Id'],
            'filesystem_layers': 1, 'upstream_runtime_configuration_preserved': True,
            'unchanged_filesystem_entries': count, 'export_bytes': sizes,
            'ignored_container_files': sorted(GENERATED), 'containers_started': False}
    finally:
        signal.alarm(0)
        cleanup_errors = []
        for container in reversed(containers):
            try:
                subprocess.run(['docker', 'rm', '--volumes', container], check=True, timeout=30,
                               stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
            except (subprocess.SubprocessError, OSError):
                cleanup_errors.append(container)
        require(not cleanup_errors, 'Disposable runtime proof container cleanup failed')
    args.output.write_text(json.dumps(report, indent=2) + '\n')
    print(json.dumps(report))


if __name__ == '__main__':
    main()
