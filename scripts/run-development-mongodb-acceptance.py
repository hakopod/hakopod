#!/usr/bin/env python3
"""Run one MongoDB native acceptance case in an isolated development snapshot.

Run this helper inside a bounded systemd unit on the development VM, after
coordinating the database test lane. It does not install a controller or build
images. Every case starts fresh fixtures on the two database development nodes.
"""
import argparse
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import platform
import re
import shutil
import subprocess
import sys
import time


CASES = {
    'standalone': ('TestManagedMongoDBLive', 'standalone'),
    'cluster': ('TestManagedMongoDBLive', 'cluster'),
    'scaling': ('TestManagedMongoDBScalingLive', ''),
    'recovery': ('TestManagedMongoDBRecoveryLive', ''),
    'binding': ('TestManagedMongoDBBindingLive', ''),
}
IMAGE_NAMES = ('Controller', 'Server', 'Agent', 'Readiness', 'Upgrade')


def file_hash(path):
    if path.is_symlink() or not path.is_file() or path.stat().st_size > 64 * 1024 * 1024:
        raise ValueError('qualification file is missing, symbolic or oversized')
    digest = hashlib.sha256()
    with path.open('rb') as source:
        for block in iter(lambda: source.read(1024 * 1024), b''):
            digest.update(block)
    return digest.hexdigest()


def source_files(root):
    files = set()
    for directory in ('internal', 'cmd', 'auth'):
        for suffix in ('*.go', '*.sql', '*.sh', '*.py', '*.toml'):
            files.update((root / directory).rglob(suffix))
    files.update(root / name for name in ('go.mod', 'go.sum',
        'scripts/development-database-quorum-fault.py', 'installer/mongodb-controller-values.json'))
    return {path.relative_to(root).as_posix(): file_hash(path) for path in sorted(files)}


def kube_json(kube, args):
    data = subprocess.check_output(kube + args + ['-o', 'json'], timeout=15, stderr=subprocess.DEVNULL)
    if len(data) > 2 * 1024 * 1024:
        raise ValueError('development inventory exceeded its bound')
    return json.loads(data)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--root', type=Path, default=Path('/srv/hakopod-backup-scratch/database-cockpit-20260929'))
    parser.add_argument('--source', type=Path)
    parser.add_argument('--case', choices=CASES, required=True)
    parser.add_argument('--attempt', type=int, required=True)
    args = parser.parse_args()
    root = args.root.resolve()
    source = (args.source or root / 'mongodb-source-check').resolve()
    if not source.is_relative_to(root) or source == root or args.attempt < 1 or args.attempt > 100:
        raise ValueError('native run requires an isolated scratch snapshot and attempt 1 through 100')
    if platform.system() != 'Linux' or platform.machine() not in ('x86_64', 'amd64'):
        raise ValueError('native run requires a Linux amd64 executor')
    # Leave room for missing images, seven test members and compiler caches.
    if shutil.disk_usage(root).free < 16 * 1024 ** 3:
        raise ValueError('native MongoDB acceptance requires 16 GiB of free scratch storage and an exclusive database test lane')
    spec = importlib.util.spec_from_file_location('bounded_acceptance', Path(__file__).with_name('run-development-vitess-acceptance.py'))
    bounded = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(bounded)
    bounded.MAX_RUN_SECONDS = 60 * 60
    kubeconfig = root / 'development-kubeconfig'
    kube = [str(root / 'bin/kubectl'), '--kubeconfig', str(kubeconfig), '--context', 'k3d-hakopod-dev']
    if subprocess.check_output(kube + ['config', 'current-context'], timeout=10, text=True).strip() != 'k3d-hakopod-dev':
        raise ValueError('named development context is required')
    nodes = kube_json(kube, ['get', 'nodes', *bounded.NODES]).get('items', [])
    if len(nodes) != 2 or {node['metadata']['name'] for node in nodes} != set(bounded.NODES):
        raise ValueError('both database development nodes are required')
    for node in nodes:
        info = node['status']['nodeInfo']
        conditions = {item['type']: item['status'] for item in node['status']['conditions']}
        if info.get('architecture') != 'amd64' or info.get('operatingSystem') != 'linux' or node.get('spec', {}).get('unschedulable'):
            raise ValueError('development nodes must be schedulable Linux amd64')
        if conditions.get('Ready') != 'True' or any(conditions.get(name) != 'False' for name in ('DiskPressure', 'MemoryPressure', 'PIDPressure')):
            raise ValueError('development nodes are not healthy enough for native acceptance')
    fixture = (source / 'internal/cluster/live_database_mongodb_test.go').read_text()
    if 'HAKOPOD_DATABASE_FIXTURE_NODES' not in fixture or any(name not in fixture for name in bounded.NODES):
        raise ValueError('MongoDB fixture must restrict placement to the two development nodes')
    runtime = (source / 'internal/cluster/database_mongodb.go').read_text()
    images = {}
    for name in IMAGE_NAMES:
        match = re.search(r'\bmongodb' + name + r'Image\s*=\s*"([^"]+@sha256:[a-f0-9]{64})"', runtime)
        if not match:
            raise ValueError('MongoDB runtime requires every pinned image')
        images[name.lower()] = match[1]
    controller = kube_json(kube, ['get', 'deployment', 'mongodb-kubernetes-operator', '-n', 'mongodb-system'])
    containers = controller['spec']['template']['spec']['containers']
    if len(containers) != 1 or containers[0]['image'] != images['controller'] or controller.get('status', {}).get('availableReplicas', 0) != 1:
        raise ValueError('the pinned MongoDB controller must be available')
    stem = 'mongodb-native-' + args.case + '-v' + str(args.attempt)
    log, evidence = root / (stem + '.jsonl'), root / (stem + '.evidence.json')
    if log.exists() or evidence.exists():
        raise ValueError('acceptance evidence for this attempt already exists')
    before = source_files(source)
    env = {key: value for key, value in os.environ.items() if not key.startswith(('HAKOPOD_', 'AWS_'))}
    env.update(PATH=str(root / 'bin') + ':' + env.get('PATH', ''), GOMAXPROCS='1', GOROOT='/opt/hakopod-build-go',
        GOCACHE='/srv/hakopod-backup-scratch/managed-databases-20260927/go-cache',
        GOMODCACHE='/srv/hakopod-backup-scratch/managed-databases-20260927/go-mod', TMPDIR=str(root / 'tmp'),
        HAKOPOD_DATABASE_RECOVERY_TEST='1', HAKOPOD_DATABASE_MONGODB_TEST='1', HAKOPOD_KEEP_DATABASE_FIXTURES='1',
        HAKOPOD_TEST_KUBECONFIG=str(kubeconfig), HAKOPOD_DATABASE_FIXTURE_NODES=','.join(bounded.NODES),
        HAKOPOD_MONGODB_FAULT_HELPER=str(source / 'scripts/development-database-quorum-fault.py'))
    test, subtest = CASES[args.case]
    pattern = '^' + test + '$' + ('/^' + subtest + '$' if subtest else '')
    expected = {test, test + '/' + subtest} if subtest else {test}
    started = time.monotonic()
    exit_code, limit_error = bounded.run_bounded(['/usr/local/bin/go', 'test', '-p', '1', './internal/cluster',
        '-run', pattern, '-count=1', '-timeout=55m', '-json'], source, env, log)
    after = source_files(source)
    events, valid_log = bounded.structural_events(log)
    passed_tests, valid_events = bounded.passed_events(events, 'TestManagedMongoDB')
    passed = exit_code == 0 and not limit_error and before == after and valid_log and valid_events and expected.issubset(passed_tests)
    report = {'schema_version': 1, 'case': args.case, 'context': 'k3d-hakopod-dev', 'execution': 'native',
        'platform': 'linux/amd64', 'images': images, 'source_files': before, 'source_files_after': after,
        'runner_sha256': file_hash(Path(__file__)), 'bounded_runner_sha256': file_hash(Path(bounded.__file__)),
        'log_sha256': file_hash(log), 'exit_code': exit_code, 'limit_error': limit_error,
        'elapsed_seconds': round(time.monotonic() - started, 3), 'test_events': events,
        'passed_tests': sorted(passed_tests), 'failed_tests': sorted({item['Test'] for item in events if item['Action'] in ('fail', 'skip')}), 'passed': passed}
    descriptor = os.open(evidence, os.O_CREAT | os.O_EXCL | os.O_WRONLY, 0o600)
    with os.fdopen(descriptor, 'w') as output:
        json.dump(report, output, indent=2)
    print('Native MongoDB case:', args.case, 'passed:', passed, 'exit:', exit_code, flush=True)
    print('Protected event log:', log, flush=True)
    print('Protected structural evidence:', evidence, flush=True)
    return 0 if passed else 1


if __name__ == '__main__':
    try:
        sys.exit(main())
    except Exception as error:
        print('Native MongoDB acceptance setup failed:', type(error).__name__, flush=True)
        sys.exit(1)
