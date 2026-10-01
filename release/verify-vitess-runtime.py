#!/usr/bin/env python3
"""Verify the qualified Vitess images without rebuilding or running them."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import tempfile


ROOT = Path(__file__).resolve().parent.parent
DIGEST = re.compile(r'[0-9a-f]{64}')
MAX_SOURCE_FILES = 4096
MAX_SOURCE_BYTES = 128 * 1024 * 1024
REQUIRED_TESTS = {
    'TestManagedVitessLive/standalone',
    'TestManagedVitessLive/cluster',
    'TestManagedVitessRecoveryLive',
    'TestManagedVitessNativeReseedLive',
    'TestManagedVitessBackupRevocationLive',
}
PACKAGES = {
    'runtime': 'ghcr.io/hakopod/managed-vitess-runtime',
    'operator': 'ghcr.io/hakopod/managed-vitess-operator',
}
BINARIES = {
    'runtime': {f'/vt/bin/{name}' for name in (
        'vtctld', 'vtctldclient', 'vtgate', 'vttablet', 'vtorc', 'vtbackup', 'mysqlctld')},
    'operator': {'/usr/local/bin/vitess-operator'},
}
SOURCE_REPOSITORIES = {
    'runtime': 'https://github.com/vitessio/vitess',
    'operator': 'https://github.com/planetscale/vitess-operator',
}
RUNTIME_CONFIG = {
    'User': 'vitess',
    'Env': ['PATH=/vt/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin',
            'VTROOT=/vt', 'VTDATAROOT=/vt/vtdataroot'],
    'Cmd': ['bash'],
    'Volumes': {'/vt/vtdataroot': {}},
}


def file_hash(path, limit=64 * 1024 * 1024):
    if path.is_symlink() or not path.is_file() or path.stat().st_size > limit:
        raise ValueError('Missing, symbolic or oversized qualification artifact: ' + path.name)
    digest = hashlib.sha256()
    with path.open('rb') as source:
        for block in iter(lambda: source.read(1024 * 1024), b''):
            digest.update(block)
    return digest.hexdigest()


def read_json(path):
    file_hash(path, 2 * 1024 * 1024)
    return json.loads(path.read_text())


def source_files(root):
    paths = [root / 'go.mod', root / 'go.sum']
    # Include transitive packages and embedded data. Keeping the entire internal
    # tree also catches future imports without maintaining another Go parser.
    for name in ('internal', 'templates'):
        directory = root / name
        if directory.is_symlink() or not directory.is_dir():
            raise ValueError('Missing or symbolic qualification source directory: ' + name)
        pending = [directory]
        entries = 0
        while pending:
            with os.scandir(pending.pop()) as children:
                for child in children:
                    if child.name in ('.git', '.DS_Store', '__pycache__') or (
                            child.name.startswith('.') and child.name.endswith('.shipping')):
                        continue
                    entries += 1
                    if entries > MAX_SOURCE_FILES or len(paths) >= MAX_SOURCE_FILES:
                        raise ValueError('Qualification source traversal exceeded its file limit')
                    if child.is_symlink():
                        raise ValueError('Symbolic qualification source is not permitted: ' + child.name)
                    if child.is_dir():
                        pending.append(Path(child.path))
                    elif child.is_file():
                        paths.append(Path(child.path))
    paths.extend((root / 'patches').glob('vitess*.go.txt'))
    paths.extend(root / name for name in (
        'scripts/apply-managed-vitess-patches.py', 'scripts/build-managed-vitess.sh',
        'scripts/run-development-vitess-acceptance.py',
        'release/verify-vitess-runtime.py', 'release/record-vitess-qualification.py',
        'installer/vitess_controller.py',
        'Dockerfile.vitess-runtime', 'Dockerfile.vitess-operator'))
    paths = sorted(set(paths))
    if len(paths) > MAX_SOURCE_FILES:
        raise ValueError('Qualification source inventory exceeded its file limit')
    total = 0
    result = {}
    for path in paths:
        if any((root / parent).is_symlink() for parent in path.relative_to(root).parents):
            raise ValueError('Symbolic qualification source directory is not permitted')
        digest = file_hash(path)
        total += path.stat().st_size
        if total > MAX_SOURCE_BYTES:
            raise ValueError('Qualification source inventory exceeded its byte limit')
        result[path.relative_to(root).as_posix()] = digest
    return result


def source_constant(root, name):
    text = (root / 'internal/cluster/database_vitess.go').read_text()
    match = re.search(r'^\s*' + re.escape(name) + r'\s*=\s*"([^"]+)"', text, re.MULTILINE)
    if not match:
        raise ValueError('Missing Vitess runtime source constant: ' + name)
    return match[1]


def accepted_test_events(events, required_tests=REQUIRED_TESTS):
    if not isinstance(events, list) or not events or len(events) > 1024:
        raise ValueError('Vitess native acceptance requires bounded Go test events')
    running, passed = set(), set()
    for event in events:
        if not isinstance(event, dict) or set(event) != {'Action', 'Package', 'Test'}:
            raise ValueError('Vitess native evidence must contain only structural Go test events')
        name, action = event['Test'], event['Action']
        if event['Package'] != 'github.com/hakopod/hakopod/internal/cluster' or not isinstance(name, str) or not re.fullmatch(r'TestManagedVitess[A-Za-z0-9_/.-]{0,160}', name):
            raise ValueError('Vitess native acceptance contains another test package or name')
        if action == 'run' and name not in running and name not in passed:
            running.add(name)
        elif action == 'pass' and name in running:
            running.remove(name)
            passed.add(name)
        else:
            raise ValueError('Vitess native acceptance contains failed, skipped, duplicate or incomplete test events')
    if running or not required_tests.issubset(passed):
        raise ValueError('Vitess native lifecycle, security and recovery events are incomplete')
    return passed


def validate_native_environment(environment, case, images):
    if not isinstance(environment, dict) or set(environment) != {
            'schema_version', 'case', 'minimum_free_bytes', 'fixture_budget_bytes', 'host_filesystem', 'nodes'}:
        raise ValueError('Vitess native environment evidence is missing or malformed')
    if type(environment['schema_version']) is not int or environment['schema_version'] != 1 or environment['case'] != case:
        raise ValueError('Vitess native environment evidence belongs to another case or schema')
    minimum, budget = environment['minimum_free_bytes'], environment['fixture_budget_bytes']
    if type(minimum) is not int or minimum != 12 * 1024 ** 3:
        raise ValueError('Vitess native acceptance requires a 12 GiB filesystem reserve')
    if type(budget) is not int or not 1024 ** 3 <= budget <= 64 * 1024 ** 3:
        raise ValueError('Vitess native acceptance requires a separate fixture disk budget')

    def capacity(filesystem, high_water=85):
        if not isinstance(filesystem, dict) or set(filesystem) != {'capacity_bytes', 'available_bytes'}:
            raise ValueError('Vitess native filesystem evidence is missing or malformed')
        total, free = filesystem['capacity_bytes'], filesystem['available_bytes']
        if type(total) is not int or type(free) is not int or not 0 <= free <= total or total <= 0:
            raise ValueError('Vitess native filesystem capacity is invalid')
        # Preserve the larger reserve when the observed kubelet high-water mark
        # starts image collection before this VM reaches the fixed 12 GiB floor.
        reserve = max(minimum, (total * (100 - high_water) + 99) // 100)
        if free < reserve + budget:
            raise ValueError('Vitess native filesystem lacks its reserve plus the fixture disk budget')

    capacity(environment['host_filesystem'])
    nodes = environment['nodes']
    names = {'k3d-hakopod-dev-server-0', 'k3d-hakopod-database-worker-0'}
    if not isinstance(nodes, list) or len(nodes) != 2 or any(not isinstance(node, dict) for node in nodes):
        raise ValueError('Vitess native evidence requires both development nodes')
    if any(not isinstance(node.get('name'), str) for node in nodes) or {node['name'] for node in nodes} != names:
        raise ValueError('Vitess native evidence contains another node')
    expected_images = set()
    for reference in images:
        repository, digest = reference.rsplit('@', 1)
        if repository.rfind(':') > repository.rfind('/'):
            repository = repository[:repository.rfind(':')]
        expected_images.add(repository + '@' + digest)
    for node in nodes:
        if set(node) != {'name', 'architecture', 'operating_system', 'schedulable', 'conditions',
                         'image_gc_high_threshold_percent', 'filesystems', 'cached_images'}:
            raise ValueError('Vitess native node evidence is malformed')
        if node['architecture'] != 'amd64' or node['operating_system'] != 'linux' or node['schedulable'] is not True:
            raise ValueError('Vitess native acceptance requires schedulable Linux amd64 nodes')
        if node['conditions'] != {'Ready': 'True', 'DiskPressure': 'False', 'MemoryPressure': 'False', 'PIDPressure': 'False'}:
            raise ValueError('Vitess native acceptance requires healthy development nodes without pressure')
        high_water = node['image_gc_high_threshold_percent']
        if type(high_water) is not int or not 1 <= high_water <= 99:
            raise ValueError('Vitess native image collection threshold is invalid')
        if not isinstance(node['filesystems'], dict) or set(node['filesystems']) != {'nodefs', 'imagefs'}:
            raise ValueError('Vitess native evidence requires nodefs and imagefs measurements')
        for filesystem in node['filesystems'].values():
            capacity(filesystem, high_water)
        cached = node['cached_images']
        if not isinstance(cached, list) or len(cached) != len(expected_images) or any(not isinstance(item, str) for item in cached) or set(cached) != expected_images:
            raise ValueError('Vitess native acceptance requires every canonical image digest cached on both nodes')


def validate_metadata(directory, root=ROOT):
    manifest = read_json(directory / 'manifest.json')
    if not isinstance(manifest, dict) or type(manifest.get('schema_version')) is not int or manifest['schema_version'] != 1 or manifest.get('platform') != 'linux/amd64':
        raise ValueError('Vitess qualification requires schema 1 and native linux/amd64')
    if manifest.get('source_files') != source_files(root):
        raise ValueError('Vitess source changed after native qualification')
    expected_files = {'runtime-upstream.patch', 'operator-upstream.patch', 'native-acceptance.json', 'binary-sha256.txt'}
    if set(manifest.get('files', {})) != expected_files:
        raise ValueError('Vitess qualification must include patches, binary hashes and native acceptance')
    for name, digest in manifest['files'].items():
        if not isinstance(digest, str) or not DIGEST.fullmatch(digest) or file_hash(directory / name) != digest:
            raise ValueError('Vitess qualification artifact checksum changed: ' + name)
    if set(manifest.get('images', {})) != set(PACKAGES) or set(manifest.get('sources', {})) != set(PACKAGES):
        raise ValueError('Both Vitess runtime and namespace operator must be qualified')
    expected_checksums = {}
    for kind, package in PACKAGES.items():
        item = manifest['images'][kind]
        image = item.get('reference', '')
        if not re.fullmatch(re.escape(package) + r'(?:[:][A-Za-z0-9_.-]+)?@sha256:[0-9a-f]{64}', image):
            raise ValueError('Vitess image must use its qualified GHCR package and immutable digest')
        symbol = 'vitessServer' if kind == 'runtime' else 'vitessOperator'
        if source_constant(root, symbol + 'Image') != image:
            raise ValueError('Vitess source image differs from its qualified digest')
        source = manifest['sources'][kind]
        if source != {'repository': SOURCE_REPOSITORIES[kind], 'revision': source_constant(root, symbol + 'Source')}:
            raise ValueError('Vitess official source provenance changed')
        base = (root / ('Dockerfile.vitess-' + kind)).read_text().splitlines()[0]
        if base != 'FROM ' + item.get('base_image', ''):
            raise ValueError('Vitess pinned upstream base changed')
        binaries = item.get('binaries', {})
        if set(binaries) != BINARIES[kind] or any(not isinstance(value, str) or not DIGEST.fullmatch(value) for value in binaries.values()):
            raise ValueError('Vitess binary hash inventory is incomplete')
        for path, digest in binaries.items():
            expected_checksums[kind + '/bin/' + Path(path).name] = digest
    checksums = {}
    for line in (directory / 'binary-sha256.txt').read_text().splitlines():
        match = re.fullmatch(r'([0-9a-f]{64})  ((?:runtime|controller)/bin/[a-z0-9-]+)', line)
        if not match:
            raise ValueError('Invalid Vitess binary checksum manifest')
        name = match[2].replace('controller/', 'operator/', 1)
        if name in checksums:
            raise ValueError('Duplicate Vitess binary checksum')
        checksums[name] = match[1]
    if checksums != expected_checksums:
        raise ValueError('Vitess binary checksum manifest disagrees with qualified images')
    acceptance = read_json(directory / 'native-acceptance.json')
    expected_images = {kind: item['reference'] for kind, item in manifest['images'].items()}
    if not isinstance(acceptance, dict) or type(acceptance.get('schema_version')) is not int or acceptance['schema_version'] != 1:
        raise ValueError('Vitess native acceptance requires schema 1')
    if acceptance.get('context') != 'k3d-hakopod-dev' or acceptance.get('execution') != 'native' or acceptance.get('platform') != 'linux/amd64' or acceptance.get('passed') is not True:
        raise ValueError('Vitess requires successful native named-development-cluster acceptance')
    if acceptance.get('images') != expected_images or acceptance.get('source_files') != manifest['source_files']:
        raise ValueError('Vitess native evidence belongs to another image or source snapshot')
    passed = accepted_test_events(acceptance.get('test_events'))
    if set(acceptance.get('passed_tests', [])) != passed or acceptance.get('failed_tests') != []:
        raise ValueError('Vitess native lifecycle, security and recovery acceptance is incomplete')
    attempts = acceptance.get('attempts')
    cases = {'lifecycle', 'recovery', 'reseed', 'revocation'}
    if not isinstance(attempts, list) or len(attempts) != len(cases) or any(not isinstance(attempt, dict) for attempt in attempts):
        raise ValueError('Vitess native acceptance requires environment evidence for all four cases')
    if any(not isinstance(attempt.get('case'), str) for attempt in attempts) or {attempt['case'] for attempt in attempts} != cases:
        raise ValueError('Vitess native acceptance environment cases are missing or duplicated')
    for attempt in attempts:
        validate_native_environment(attempt.get('environment'), attempt['case'],
            [*expected_images.values(), source_constant(root, 'vitessEtcdImage')])
    return manifest


def docker(args, config):
    environment = dict(os.environ, DOCKER_CONFIG=str(config))
    result = subprocess.run(['docker', *args], check=True, capture_output=True, text=True,
                            timeout=600, env=environment)
    if len(result.stdout) > 2 * 1024 * 1024:
        raise ValueError('Docker metadata exceeded its bound')
    return result.stdout


def verify_images(manifest, runner=docker):
    with tempfile.TemporaryDirectory(prefix='vitess-release-verification-') as temporary:
        directory = Path(temporary)
        config = directory / 'anonymous'; config.mkdir()
        for kind, item in manifest['images'].items():
            reference = item['reference']
            runner(['pull', '--platform', 'linux/amd64', reference], config)
            values = json.loads(runner(['image', 'inspect', reference], config))
            if len(values) != 1:
                raise ValueError('Unexpected Vitess image inventory')
            image = values[0]
            repo_digest = PACKAGES[kind] + '@' + reference.rsplit('@', 1)[1]
            labels = image.get('Config', {}).get('Labels', {}) or {}
            source_label = 'io.hakopod.vitess.upstream' if kind == 'runtime' else 'io.hakopod.vitess.operator-upstream'
            if image.get('Os') != 'linux' or image.get('Architecture') != 'amd64' or repo_digest not in image.get('RepoDigests', []):
                raise ValueError('Vitess image platform or pulled digest changed')
            if labels.get(source_label) != manifest['sources'][kind]['revision'] or labels.get('org.opencontainers.image.source') != 'https://github.com/hakopod/hakopod':
                raise ValueError('Vitess image source labels changed')
            if kind == 'runtime':
                runtime_config = image.get('Config', {})
                # Linux starts an unset working directory at /. ArgsEscaped is
                # legacy Windows metadata and does not change Linux execution.
                if (any(runtime_config.get(key) != value for key, value in RUNTIME_CONFIG.items())
                        or runtime_config.get('WorkingDir') not in (None, '', '/')
                        or type(runtime_config.get('ArgsEscaped')) not in (type(None), bool)
                        or any(runtime_config.get(key) for key in ('Entrypoint', 'ExposedPorts', 'Healthcheck', 'StopSignal', 'OnBuild'))):
                    raise ValueError('Vitess image runtime configuration differs from its pinned upstream base')
            container = runner(['create', '--network', 'none', '--entrypoint', '/bin/false', reference], config).strip()
            if not re.fullmatch(r'[0-9a-f]{64}', container):
                raise ValueError('Invalid verification container identity')
            try:
                for number, (path, digest) in enumerate(sorted(item['binaries'].items())):
                    target = directory / f'{kind}-{number}'
                    runner(['cp', container + ':' + path, str(target)], config)
                    if file_hash(target, 512 * 1024 * 1024) != digest:
                        raise ValueError('Vitess image binary differs from native qualification: ' + path)
            finally:
                runner(['rm', '-v', container], config)


def verify(directory, output, root=ROOT):
    manifest = validate_metadata(directory, root)
    verify_images(manifest)
    if output.exists():
        raise ValueError('Use a fresh Vitess verification output directory')
    output.mkdir(parents=True)
    for name in ['manifest.json', *manifest['files']]:
        shutil.copyfile(directory / name, output / name)
    report = {'schema_version': 1, 'platform': 'linux/amd64', 'anonymous_pull_verified': True,
              'image_binary_hashes_verified': True, 'native_acceptance_reused': True,
              'images': {kind: item['reference'] for kind, item in manifest['images'].items()}}
    (output / 'release-verification.json').write_text(json.dumps(report, indent=2) + '\n')


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--qualification', type=Path, default=ROOT / 'release/managed-vitess')
    parser.add_argument('--output', type=Path, required=True)
    arguments = parser.parse_args()
    verify(arguments.qualification, arguments.output)
