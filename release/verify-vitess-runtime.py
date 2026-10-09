#!/usr/bin/env python3
"""Verify the qualified Vitess images without rebuilding or running them."""
import argparse
import hashlib
import json
import os
from pathlib import Path, PurePosixPath
import re
import runpy
import shutil
import subprocess
import tempfile


ROOT = Path(__file__).resolve().parent.parent
DIGEST = re.compile(r'[0-9a-f]{64}')
CHAIN = runpy.run_path(str(ROOT / 'release/runtime-source-compatibility.py'))
MAX_SOURCE_FILES = 4096
MAX_SOURCE_BYTES = 128 * 1024 * 1024
REQUIRED_TESTS = {
    'TestManagedVitessLive/standalone',
    'TestManagedVitessLive/cluster',
    'TestManagedVitessRecoveryLive',
    'TestManagedVitessNativeReseedLive',
    'TestManagedVitessBackupRevocationLive',
    'TestManagedVitessScaleLive',
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
    for name in ('auth', 'cmd/hakopod-server', 'internal', 'templates'):
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
        'release/runtime-source-compatibility.py',
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


def canonical_hash(value):
    return hashlib.sha256(json.dumps(value, sort_keys=True, separators=(',', ':')).encode()).hexdigest()


def _compatibility_object(pairs):
    result = {}
    for key, value in pairs:
        if key in result:
            raise ValueError('Duplicate Vitess compatibility review field')
        result[key] = value
    return result


def load_source_compatibility(root):
    path = Path(root) / 'release/managed-vitess/source-compatibility.json'
    if not path.exists():
        raise ValueError('Vitess source changed without an exact compatibility review')
    file_hash(path, 512 * 1024)
    try:
        record = json.loads(path.read_text(), object_pairs_hook=_compatibility_object)
    except (json.JSONDecodeError, UnicodeDecodeError) as error:
        raise ValueError('Invalid Vitess compatibility review') from error
    version = record.get('schema_version') if isinstance(record, dict) else None
    if version == 3:
        CHAIN['chain_header'](root, 'vitess', record)
        return record
    identity_field = 'qualified_candidate' if version == 2 else 'qualified_release'
    fields = {'schema_version', identity_field, 'reviewed_release', 'control_plane_commit',
              'regression_evidence', 'scopes'}
    if (not isinstance(record, dict) or set(record) != fields
            or type(version) is not int or version not in (1, 2)):
        raise ValueError('Invalid Vitess compatibility review schema')
    baseline = record[identity_field]
    hashes = {'native_manifest_sha256', 'http_evidence_sha256', 'http_report_sha256'}
    identity_fields = {'tag', 'commit'} if version == 1 else {'version', 'native_commit', 'http_commit'}
    if not isinstance(baseline, dict) or set(baseline) != hashes | identity_fields:
        raise ValueError('Vitess compatibility review lacks its release and evidence identities')
    if version == 1:
        valid_identity = (isinstance(baseline['tag'], str)
                          and re.fullmatch(r'v[0-9]+\.[0-9]+\.[0-9]+(?:-[A-Za-z0-9.-]+)?', baseline['tag'])
                          and isinstance(baseline['commit'], str)
                          and re.fullmatch(r'[0-9a-f]{40}', baseline['commit']))
    else:
        # This identifies unpublished evidence, never a published baseline tag.
        valid_identity = (isinstance(baseline['version'], str)
                          and re.fullmatch(r'[0-9]+\.[0-9]+\.[0-9]+-alpha\.[0-9]+', baseline['version'])
                          and all(isinstance(baseline[key], str) and re.fullmatch(r'[0-9a-f]{40}', baseline[key])
                                  for key in ('native_commit', 'http_commit'))
                          and record['reviewed_release'] == 'v' + baseline['version'])
    if (not valid_identity
            or any(not isinstance(baseline.get(key), str) or not DIGEST.fullmatch(baseline[key]) for key in hashes)
            or not isinstance(record['reviewed_release'], str)
            or not re.fullmatch(r'v[0-9]+\.[0-9]+\.[0-9]+(?:-[A-Za-z0-9.-]+)?', record['reviewed_release'])
            or not isinstance(record['control_plane_commit'], str)
            or not re.fullmatch(r'[0-9a-f]{40}', record['control_plane_commit'])):
        raise ValueError('Vitess compatibility review lacks its release and evidence identities')
    regression = record['regression_evidence']
    if (not isinstance(regression, dict) or set(regression) != {'url', 'tests'}
            or not isinstance(regression['url'], str)
            or not re.fullmatch(r'https://github\.com/hakopod/hakopod/actions/runs/[0-9]+', regression['url'])
            or not isinstance(regression['tests'], list) or not 1 <= len(regression['tests']) <= 32
            or any(not isinstance(name, str) or not re.fullmatch(r'Test[A-Za-z0-9_/.-]{1,200}', name)
                   for name in regression['tests'])
            or len(set(regression['tests'])) != len(regression['tests'])):
        raise ValueError('Vitess compatibility review lacks its control-plane regression evidence')
    if not isinstance(record['scopes'], dict) or set(record['scopes']) != {'runtime', 'http_harness'}:
        raise ValueError('Vitess compatibility review requires both source scopes')
    for delta in record['scopes'].values():
        if (not isinstance(delta, dict) or set(delta) != {'baseline_sha256', 'current_sha256', 'changes'}
                or any(not isinstance(delta[key], str) or not DIGEST.fullmatch(delta[key])
                       for key in ('baseline_sha256', 'current_sha256'))
                or not isinstance(delta['changes'], list) or len(delta['changes']) > 1024):
            raise ValueError('Invalid Vitess compatibility source scope')
        for change in delta['changes']:
            if (not isinstance(change, dict) or set(change) != {'path', 'before', 'after', 'reason'}
                    or not isinstance(change['reason'], str) or not 20 <= len(change['reason'].strip()) <= 500):
                raise ValueError('Vitess compatibility changes require individual review reasons')
    return record


def compatibility_baseline(record):
    return record['qualified_candidate' if record['schema_version'] == 2 else 'qualified_release']


def _protected_vitess_source(path):
    return (path in {'go.mod', 'go.sum', 'Dockerfile.vitess-runtime', 'Dockerfile.vitess-operator',
                     'scripts/apply-managed-vitess-patches.py', 'scripts/build-managed-vitess.sh',
                     'installer/vitess_controller.py', 'release/verify-vitess-runtime.py'}
            or path.startswith('auth/') or path.startswith('patches/vitess')
            or path.startswith('internal/') and 'vitess' in path.lower())


def _alpha59_go_mod_reclassification(review, recorded, current, path, before, after):
    return (review['reviewed_release'] == 'v0.1.0-alpha.59' and path == 'go.mod'
            and before == 'cdd09b01abe6d0371b89d566f77ae1b87ebbd3f74bf5c801a6c591898162b174'
            and after == 'a5ad64b13b23e366dccc9d560cbecb97cb6ab32ab8f7e3385171a8f89e96c010'
            and recorded.get('go.sum') == current.get('go.sum'))


def _alpha59_verifier_bootstrap(review, path, before):
    expected = {
        'tag': 'v0.1.0-alpha.58',
        'commit': '2040ac12564eb4d8d769463fb9cd3e5d989b2c48',
        'native_manifest_sha256': '64a9a969ff473e14fc25f345a380fdb27defed9d0ef1cee6df59048007805b1f',
        'http_evidence_sha256': '7970da3efa8ce9b714438ba06e1d849fd4d837676032176edbe208556747f545',
        'http_report_sha256': '7c2a438ac4cb1aa772ccda6459c459e7984f9c08b0f37ced7d4de2dd863fb274',
    }
    return (review['reviewed_release'] == 'v0.1.0-alpha.59'
            and review['qualified_release'] == expected
            and review['control_plane_commit'] == 'e38c24b1c09c3d035f77128b1555c513602e9ec8'
            and path == 'release/verify-vitess-runtime.py'
            and before == '8a0299b42411f8f13e06308393cf42a5cd087aba33b46db5402e246b7b7ab08c')


def validate_source_compatibility(root, scope, recorded, current, artifact_sha256=None):
    """Reuse historical evidence only for an explicitly reviewed, exact source delta."""
    if recorded == current:
        return False
    review = load_source_compatibility(root)
    if review['schema_version'] == 3:
        if scope not in ('runtime', 'http_harness'):
            raise ValueError('Unknown Vitess compatibility source scope')
        return CHAIN['validate_chain'](root, 'vitess', review, recorded, current, _protected_vitess_source, artifact_sha256, scope)
    if scope not in ('runtime', 'http_harness'):
        raise ValueError('Unknown Vitess compatibility source scope')
    if artifact_sha256 is not None and artifact_sha256 != compatibility_baseline(review)['native_manifest_sha256']:
        raise ValueError('Vitess compatibility review belongs to another native manifest')
    for inventory in (recorded, current):
        if (not isinstance(inventory, dict) or not 1 <= len(inventory) <= MAX_SOURCE_FILES
                or any(not isinstance(path, str) or not isinstance(digest, str) or not DIGEST.fullmatch(digest)
                       for path, digest in inventory.items())):
            raise ValueError('Invalid Vitess compatibility source inventory')
    delta = review['scopes'][scope]
    if (not isinstance(delta, dict) or set(delta) != {'baseline_sha256', 'current_sha256', 'changes'}
            or delta['baseline_sha256'] != canonical_hash(recorded)
            or delta['current_sha256'] != canonical_hash(current)
            or not isinstance(delta['changes'], list) or not 1 <= len(delta['changes']) <= 1024):
        raise ValueError('Vitess source changed outside the reviewed source delta')
    expected, seen = dict(recorded), set()
    for change in delta['changes']:
        if not isinstance(change, dict) or set(change) != {'path', 'before', 'after', 'reason'}:
            raise ValueError('Invalid Vitess compatibility source change')
        path, before, after, reason = (change[key] for key in ('path', 'before', 'after', 'reason'))
        if (not isinstance(path, str) or not path or path == '.' or len(path) > 512
                or any(ord(character) < 32 for character in path)
                or PurePosixPath(path).is_absolute() or '..' in PurePosixPath(path).parts
                or str(PurePosixPath(path)) != path or '\\' in path or path in seen
                or any(value is not None and (not isinstance(value, str) or not DIGEST.fullmatch(value))
                       for value in (before, after)) or before == after
                or not isinstance(reason, str) or not 20 <= len(reason.strip()) <= 500):
            raise ValueError('Invalid, duplicate or unsafe Vitess compatibility source change')
        if (_protected_vitess_source(path)
                and not _alpha59_go_mod_reclassification(review, recorded, current, path, before, after)
                and not _alpha59_verifier_bootstrap(review, path, before)):
            raise ValueError('Vitess runtime, dependency or controller changes require new qualification')
        if expected.get(path) != before:
            raise ValueError('Vitess compatibility change differs from its qualified baseline')
        seen.add(path)
        if after is None:
            del expected[path]
        else:
            expected[path] = after
    if expected != current:
        raise ValueError('Vitess source changed outside the reviewed source delta')
    return True


def validate_http_compatibility(root, report, runtime_sources, harness_sources):
    review = load_source_compatibility(root)
    if canonical_hash(report) != compatibility_baseline(review)['http_report_sha256']:
        raise ValueError('Vitess compatibility review belongs to another historical HTTP report')
    validate_source_compatibility(root, 'runtime', report['runtime_source_files'], runtime_sources)
    validate_source_compatibility(root, 'http_harness', report['http_harness_source_files'], harness_sources)
    return True


def source_constant(root, name):
    text = (root / 'internal/cluster/database_vitess.go').read_text()
    match = re.search(r'^\s*' + re.escape(name) + r'\s*=\s*"([^"]+)"', text, re.MULTILINE)
    if not match:
        raise ValueError('Missing Vitess runtime source constant: ' + name)
    return match[1]


def source_boolean(root, name):
    text = (root / 'internal/cluster/database_vitess.go').read_text()
    match = re.search(r'^\s*' + re.escape(name) + r'\s*=\s*(true|false)\s*$', text, re.MULTILINE)
    if not match:
        raise ValueError('Missing Vitess release qualification constant: ' + name)
    return match[1] == 'true'


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
            'schema_version', 'case', 'minimum_free_bytes', 'fixture_budget_bytes', 'required_cpu_milli',
            'required_memory_bytes', 'host_cpu_milli', 'cpu_shortfall_milli', 'memory_shortfall_bytes',
            'host_filesystem', 'nodes', 'cluster'}:
        raise ValueError('Vitess native environment evidence is missing or malformed')
    if type(environment['schema_version']) is not int or environment['schema_version'] != 1 or environment['case'] != case:
        raise ValueError('Vitess native environment evidence belongs to another case or schema')
    minimum, budget = environment['minimum_free_bytes'], environment['fixture_budget_bytes']
    if type(minimum) is not int or minimum != 12 * 1024 ** 3:
        raise ValueError('Vitess native acceptance requires a 12 GiB filesystem reserve')
    if type(budget) is not int or not 1024 ** 3 <= budget <= 67 * 1024 ** 3 or case == 'scale' and budget != 67 * 1024 ** 3:
        raise ValueError('Vitess native acceptance requires a separate fixture disk budget')
    expected_cpu = {'lifecycle': 8850, 'recovery': 17700, 'reseed': 6550, 'revocation': 5100, 'scale': 45450}
    expected_memory = 97050 * 1024 ** 2 if case == 'scale' else 0
    if (environment['required_cpu_milli'] != expected_cpu.get(case)
            or environment['required_memory_bytes'] != expected_memory
            or type(environment['host_cpu_milli']) is not int
            or environment['host_cpu_milli'] < 1
            or case == 'scale' and environment['host_cpu_milli'] < environment['required_cpu_milli']
            or environment['cpu_shortfall_milli'] != 0 or environment['memory_shortfall_bytes'] != 0):
        raise ValueError('Vitess native acceptance lacks the fixed case CPU envelope')

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
    approved_node_sets = (
        {'k3d-hakopod-dev-server-0', 'k3d-hakopod-database-worker-0', 'k3d-hakopod-database-worker-1'},
        {'k3d-hakopod-vitess-worker-0', 'k3d-hakopod-vitess-worker-1', 'k3d-hakopod-vitess-worker-2'},
    )
    if not isinstance(nodes, list) or len(nodes) not in (2, 3) or any(not isinstance(node, dict) for node in nodes):
        raise ValueError('Vitess native evidence requires two or three development nodes')
    node_names = [node.get('name') for node in nodes]
    if any(not isinstance(name, str) or not name for name in node_names) or len(set(node_names)) != len(node_names):
        raise ValueError('Vitess native evidence contains an empty, duplicate, or foreign node')
    matching_node_sets = [allowed for index, allowed in enumerate(approved_node_sets)
                          if set(node_names) <= allowed and (index == 0 or set(node_names) == allowed)]
    if len(matching_node_sets) != 1:
        raise ValueError('Vitess native evidence contains an empty, duplicate, or foreign node')
    allowed = matching_node_sets[0]
    cluster = environment['cluster']
    expected_crds = {
        'etcdlockservers.planetscale.com', 'vitessbackups.planetscale.com',
        'vitessbackupschedules.planetscale.com', 'vitessbackupstorages.planetscale.com',
        'vitesscells.planetscale.com', 'vitessclusters.planetscale.com',
        'vitesskeyspaces.planetscale.com', 'vitessshards.planetscale.com',
    }
    if (not isinstance(cluster, dict) or set(cluster) != {'uid', 'node_uids', 'vitess_crds', 'receipt_sha256'}
            or not isinstance(cluster['uid'], str) or not cluster['uid']
            or not isinstance(cluster['receipt_sha256'], str) or not DIGEST.fullmatch(cluster['receipt_sha256'])
            or not isinstance(cluster['node_uids'], dict) or set(cluster['node_uids']) != allowed or not set(node_names) <= set(cluster['node_uids'])
            or any(not isinstance(uid, str) or not uid for uid in cluster['node_uids'].values())
            or not isinstance(cluster['vitess_crds'], list)
            or any(not isinstance(name, str) for name in cluster['vitess_crds'])
            or set(cluster['vitess_crds']) != expected_crds or len(cluster['vitess_crds']) != len(expected_crds)):
        raise ValueError('Vitess native evidence is not bound to the exact development cluster and CRDs')
    expected_images = set()
    for reference in images:
        repository, digest = reference.rsplit('@', 1)
        if repository.rfind(':') > repository.rfind('/'):
            repository = repository[:repository.rfind(':')]
        expected_images.add(repository + '@' + digest)
    for node in nodes:
        if set(node) != {'name', 'architecture', 'operating_system', 'schedulable', 'conditions',
                         'image_gc_high_threshold_percent', 'filesystems', 'cached_images',
                         'allocatable_cpu_milli', 'requested_cpu_milli', 'available_cpu_milli',
                         'worker_cpu_limit_milli', 'worker_memory_limit_bytes', 'allocatable_memory_bytes',
                         'requested_memory_bytes', 'available_memory_bytes'}:
            raise ValueError('Vitess native node evidence is malformed')
        if node['architecture'] != 'amd64' or node['operating_system'] != 'linux' or node['schedulable'] is not True:
            raise ValueError('Vitess native acceptance requires schedulable Linux amd64 nodes')
        if (type(node['allocatable_cpu_milli']) is not int or type(node['requested_cpu_milli']) is not int
                or type(node['available_cpu_milli']) is not int or node['allocatable_cpu_milli'] < 1
                or node['requested_cpu_milli'] < 0 or node['available_cpu_milli'] != node['allocatable_cpu_milli'] - node['requested_cpu_milli']):
            raise ValueError('Vitess native CPU capacity evidence is invalid')
        if (type(node['worker_cpu_limit_milli']) is not int or node['worker_cpu_limit_milli'] < node['allocatable_cpu_milli']
                or type(node['worker_memory_limit_bytes']) is not int or node['worker_memory_limit_bytes'] < node['allocatable_memory_bytes']
                or type(node['allocatable_memory_bytes']) is not int or type(node['requested_memory_bytes']) is not int
                or type(node['available_memory_bytes']) is not int or node['requested_memory_bytes'] < 0
                or node['available_memory_bytes'] != node['allocatable_memory_bytes'] - node['requested_memory_bytes']):
            raise ValueError('Vitess native worker cgroup or memory capacity evidence is invalid')
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
            raise ValueError('Vitess native acceptance requires every canonical image digest cached on every selected node')
    if sum(node['available_cpu_milli'] for node in nodes) < environment['required_cpu_milli']:
        raise ValueError('Vitess native acceptance lacks selected-node CPU capacity')
    if sum(node['available_memory_bytes'] for node in nodes) < environment['required_memory_bytes']:
        raise ValueError('Vitess native acceptance lacks selected-node memory capacity')


def validate_metadata(directory, root=ROOT):
    manifest = read_json(directory / 'manifest.json')
    if not isinstance(manifest, dict) or type(manifest.get('schema_version')) is not int or manifest['schema_version'] != 1 or manifest.get('platform') != 'linux/amd64':
        raise ValueError('Vitess qualification requires schema 1 and native linux/amd64')
    validate_source_compatibility(root, 'runtime', manifest.get('source_files'), source_files(root),
                                  artifact_sha256=file_hash(directory / 'manifest.json'))
    if not source_boolean(root, 'vitessReleaseQualified'):
        raise ValueError('Vitess shipping admission remains closed')
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
    cases = {'lifecycle', 'recovery', 'reseed', 'revocation', 'scale'}
    if not isinstance(attempts, list) or len(attempts) != len(cases) or any(not isinstance(attempt, dict) for attempt in attempts):
        raise ValueError('Vitess native acceptance requires environment evidence for all five cases')
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
