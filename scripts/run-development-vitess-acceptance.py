"""Run one native Vitess acceptance case in the isolated development snapshot.

This helper neither builds images nor enables the shipping runtime gate. The
caller supplies the tested candidate image references after importing them into
the named development cluster, and coordinates resources before starting it.
"""
import argparse
import json
import os
from pathlib import Path
import platform
import re
import runpy
import selectors
import shutil
import signal
import subprocess
import sys
import time

CASES = {
    'lifecycle': 'TestManagedVitessLive',
    'recovery': 'TestManagedVitessRecoveryLive',
    'reseed': 'TestManagedVitessNativeReseedLive',
    'revocation': 'TestManagedVitessBackupRevocationLive',
}


MAX_LOG_BYTES = 64 * 1024 * 1024
MAX_LINE_BYTES = 2 * 1024 * 1024
MAX_EVENTS = 1024
MAX_RUN_SECONDS = 95 * 60
PACKAGE = 'github.com/hakopod/hakopod/internal/cluster'
NODES = ('k3d-hakopod-dev-server-0', 'k3d-hakopod-database-worker-0')
GIB = 1024 ** 3


def canonical_image(reference):
    repository, digest = reference.rsplit('@', 1)
    if repository.rfind(':') > repository.rfind('/'):
        repository = repository[:repository.rfind(':')]
    return repository + '@' + digest


def native_go_environment(root, kubeconfig, fixtures):
    env = os.environ.copy()
    for key in list(env):
        if key.startswith(('GO', 'CGO_', 'HAKOPOD_', 'AWS_')) or key in ('CC', 'CXX', 'FC', 'PKG_CONFIG'):
            env.pop(key)
    env.update(PATH=str(root/'bin')+':/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin',
        GOMAXPROCS='1', GOROOT='/opt/hakopod-build-go',
        GOENV='off', GOWORK='off', GOFLAGS='-mod=readonly', GOTOOLCHAIN='local', GO111MODULE='on',
        GOOS='linux', GOARCH='amd64', GOAMD64='v1', CGO_ENABLED='1', CC='/usr/bin/gcc', CXX='/usr/bin/g++',
        GOCACHE='/srv/hakopod-backup-scratch/managed-databases-20260927/go-cache',
        GOMODCACHE='/srv/hakopod-backup-scratch/managed-databases-20260927/go-mod', TMPDIR=str(root/'tmp'),
        PYTHONDONTWRITEBYTECODE='1', HAKOPOD_DATABASE_RECOVERY_TEST='1', HAKOPOD_DATABASE_VITESS_TEST='1',
        HAKOPOD_KEEP_DATABASE_FIXTURES='1', HAKOPOD_TEST_KUBECONFIG=str(kubeconfig),
        HAKOPOD_VITESS_NATIVE_FIXTURE_CONFIG=str(fixtures), HAKOPOD_DATABASE_FIXTURE_NODES=','.join(NODES))
    return env


def command_json(command):
    data = subprocess.check_output(command, timeout=20, stderr=subprocess.DEVNULL)
    if len(data) > 2 * 1024 * 1024:
        raise RuntimeError('development environment metadata exceeded its bound')
    return json.loads(data)


def native_environment(root, kube, inventory, images, case, fixture_budget_gib):
    """Read only the two approved nodes after cache warm-up and image imports."""
    if len(inventory) != len(NODES) or {node['metadata']['name'] for node in inventory} != set(NODES):
        raise RuntimeError('native acceptance requires both named development nodes')
    disk = shutil.disk_usage(root)
    report = {'schema_version': 1, 'case': case, 'minimum_free_bytes': 12 * GIB,
              'fixture_budget_bytes': fixture_budget_gib * GIB,
              'host_filesystem': {'capacity_bytes': disk.total, 'available_bytes': disk.free},
              'nodes': []}
    for node in sorted(inventory, key=lambda item: item['metadata']['name']):
        name = node['metadata']['name']
        summary = command_json(kube + ['get', '--raw', '/api/v1/nodes/' + name + '/proxy/stats/summary'])['node']
        config = command_json(kube + ['get', '--raw', '/api/v1/nodes/' + name + '/proxy/configz'])['kubeletconfig']
        info = node['status']['nodeInfo']
        filesystems = {'nodefs': summary['fs'], 'imagefs': summary['runtime']['imageFs']}
        image_inventory = command_json(['docker', 'exec', name, 'crictl', 'images', '-o', 'json']).get('images')
        if not isinstance(image_inventory, list) or len(image_inventory) > 1024:
            raise RuntimeError('development image inventory is missing or exceeded its bound')
        digests = set()
        for image in image_inventory:
            references = image.get('repoDigests') if isinstance(image, dict) else None
            if not isinstance(references, list) or any(not isinstance(reference, str) for reference in references):
                raise RuntimeError('development image digest inventory is malformed')
            digests.update(references)
        cached = [canonical_image(reference) for reference in images.values() if canonical_image(reference) in digests]
        report['nodes'].append({'name': name, 'architecture': info.get('architecture'),
            'operating_system': info.get('operatingSystem'),
            'schedulable': not node.get('spec', {}).get('unschedulable', False),
            'conditions': {item['type']: item['status'] for item in node['status'].get('conditions', [])
                           if item['type'] in ('Ready', 'DiskPressure', 'MemoryPressure', 'PIDPressure')},
            'image_gc_high_threshold_percent': config.get('imageGCHighThresholdPercent'),
            'filesystems': {kind: {'capacity_bytes': item.get('capacityBytes'),
                                   'available_bytes': item.get('availableBytes')}
                            for kind, item in filesystems.items()},
            'cached_images': sorted(cached)})
    return report


def stop_group(process):
    try:
        os.killpg(process.pid, signal.SIGTERM)
    except ProcessLookupError:
        pass
    try:
        process.wait(timeout=10)
    except subprocess.TimeoutExpired:
        os.killpg(process.pid, signal.SIGKILL)
        process.wait(timeout=10)


def run_bounded(command, source, env, log):
    descriptor = os.open(log, os.O_CREAT | os.O_EXCL | os.O_WRONLY, 0o600)
    limit_error = ''
    with os.fdopen(descriptor, 'wb') as output:
        process = subprocess.Popen(command, cwd=source, env=env, stdout=subprocess.PIPE,
                                   stderr=subprocess.STDOUT, start_new_session=True)
        deadline = time.monotonic() + MAX_RUN_SECONDS
        size = 0
        try:
            with selectors.DefaultSelector() as selector:
                selector.register(process.stdout, selectors.EVENT_READ)
                while selector.get_map():
                    if time.monotonic() >= deadline:
                        limit_error = 'timeout'
                        break
                    for key, _ in selector.select(timeout=1):
                        block = os.read(key.fileobj.fileno(), 64 * 1024)
                        if not block:
                            selector.unregister(key.fileobj)
                            continue
                        if size + len(block) > MAX_LOG_BYTES:
                            limit_error = 'log_size'
                            break
                        output.write(block)
                        output.flush()
                        size += len(block)
                    if limit_error:
                        break
                if limit_error:
                    stop_group(process)
                else:
                    try:
                        process.wait(timeout=max(1, deadline - time.monotonic()))
                    except subprocess.TimeoutExpired:
                        limit_error = 'timeout'
                        stop_group(process)
        except BaseException:
            stop_group(process)
            raise
        finally:
            process.stdout.close()
    return process.returncode, limit_error


def structural_events(log):
    events = []
    valid = True
    with log.open('rb') as source:
        while line := source.readline(MAX_LINE_BYTES + 1):
            if len(line) > MAX_LINE_BYTES:
                return events, False
            try:
                event = json.loads(line)
            except (json.JSONDecodeError, UnicodeDecodeError):
                valid = False
                continue
            if not isinstance(event, dict):
                valid = False
                continue
            if event.get('Action') in ('run', 'pass', 'fail', 'skip') and event.get('Test'):
                if len(events) >= MAX_EVENTS:
                    return events, False
                events.append({key: event.get(key) for key in ('Action', 'Package', 'Test')})
    return events, valid


def passed_events(events, prefix='TestManagedVitess'):
    running, passed = set(), set()
    for event in events:
        name, action = event['Test'], event['Action']
        if event['Package'] != PACKAGE or not isinstance(name, str) or not re.fullmatch(re.escape(prefix)+r'[A-Za-z0-9_/.-]{0,160}', name):
            return set(), False
        if action == 'run' and name not in running:
            running.add(name)
        elif action == 'pass' and name in running:
            running.remove(name)
            passed.add(name)
        else:
            return set(), False
    return passed, not running


def main():
    parser=argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--root',type=Path,default=Path('/srv/hakopod-backup-scratch/database-cockpit-20260929'))
    parser.add_argument('--engine-image',required=True)
    parser.add_argument('--operator-image',required=True)
    parser.add_argument('--case',choices=CASES,required=True)
    parser.add_argument('--attempt',type=int,required=True)
    parser.add_argument('--fixture-budget-gib',type=int,required=True,
                        help='additional measured or conservative disk budget for this case, beyond the 12 GiB reserve')
    args=parser.parse_args()
    root=args.root.resolve();source=root/'vitess-source-check'
    if args.attempt<1 or args.attempt>100:
        raise RuntimeError('native attempt number must be between 1 and 100')
    if args.fixture_budget_gib < 1 or args.fixture_budget_gib > 64:
        raise RuntimeError('native fixture disk budget must be between 1 and 64 GiB')
    image=re.compile(r'^[A-Za-z0-9._:/-]+@sha256:[a-f0-9]{64}$')
    if not image.fullmatch(args.engine_image) or not image.fullmatch(args.operator_image):
        raise RuntimeError('candidate images must have immutable registry digest references')
    runtime=(source/'internal/cluster/database_vitess.go').read_text()
    for name,want in [('vitessServerImage',args.engine_image),('vitessOperatorImage',args.operator_image)]:
        match=re.search(r'\b'+name+r'\s*=\s*"([^"]+)"',runtime)
        if not match or match.group(1)!=want:
            raise RuntimeError('candidate runtime image differs from the requested acceptance image')
    if 'managed Vitess is unavailable: native replication verification and recovery acceptance are incomplete' in runtime:
        raise RuntimeError('isolated candidate still has its runtime gate closed')
    kubeconfig=root/'development-kubeconfig'
    kube=[str(root/'bin/kubectl'),'--kubeconfig',str(kubeconfig),'--context','k3d-hakopod-dev']
    context=subprocess.check_output(kube+['config','current-context'],timeout=10,text=True).strip()
    if context!='k3d-hakopod-dev':
        raise RuntimeError('native acceptance requires the named development cluster')
    if platform.system()!='Linux' or platform.machine() not in ('x86_64','amd64'):
        raise RuntimeError('native acceptance requires an amd64 Linux executor')
    nodes=subprocess.check_output(kube+['get','nodes',*NODES,'-o','json'],timeout=15)
    if len(nodes)>128*1024:
        raise RuntimeError('development node inventory exceeded its bound')
    inventory=json.loads(nodes).get('items',[])
    if len(inventory)!=len(NODES) or {node['metadata']['name'] for node in inventory}!=set(NODES):
        raise RuntimeError('native acceptance requires both named development nodes')
    fixtures=root/'vitess-native-fixture.json'
    if fixtures.is_symlink() or not fixtures.is_file() or fixtures.stat().st_mode&0o077 or fixtures.stat().st_size>256*1024:
        raise RuntimeError('protected native backup fixture configuration is required')
    stem='vitess-native-'+args.case+'-v'+str(args.attempt)
    log=root/(stem+'.jsonl');evidence=root/(stem+'.evidence.json');preflight=root/(stem+'.preflight.json')
    if log.exists() or evidence.exists() or preflight.exists():
        raise RuntimeError('acceptance evidence for this attempt already exists')
    verifier=runpy.run_path(str(source/'release/verify-vitess-runtime.py'))
    before=verifier['source_files'](source)
    environment=native_environment(root,kube,inventory,
        {'runtime':args.engine_image,'operator':args.operator_image,'etcd':verifier['source_constant'](source,'vitessEtcdImage')},
        args.case,args.fixture_budget_gib)
    descriptor=os.open(preflight,os.O_CREAT|os.O_EXCL|os.O_WRONLY,0o600)
    with os.fdopen(descriptor,'w') as output:json.dump(environment,output,indent=2)
    print('Protected native environment evidence:',preflight,flush=True)
    try:
        verifier['validate_native_environment'](environment,args.case,
            [args.engine_image,args.operator_image,verifier['source_constant'](source,'vitessEtcdImage')])
    except ValueError as error:
        print('Native Vitess environment refused:',str(error),flush=True)
        return 1
    env=native_go_environment(root,kubeconfig,fixtures)
    test=CASES[args.case]
    started=time.time()
    exit_code,limit_error=run_bounded(['/usr/local/bin/go','test','-p','1','./internal/cluster','-run','^'+test+'$','-count=1','-timeout=90m','-json'],source,env,log)
    after=verifier['source_files'](source)
    events,valid_log=structural_events(log)
    passed_tests,valid_events=passed_events(events)
    expected={test}
    if args.case=='lifecycle':expected.update(test+'/'+mode for mode in ['standalone','cluster'])
    passed=exit_code==0 and not limit_error and before==after and valid_log and valid_events and expected.issubset(passed_tests)
    report={'schema_version':1,'case':args.case,'test':test,'context':'k3d-hakopod-dev','execution':'native','platform':'linux/amd64',
        'images':{'runtime':args.engine_image,'operator':args.operator_image},'source_files':before,'source_files_after':after,
        'log_sha256':verifier['file_hash'](log,MAX_LOG_BYTES),'runner_sha256':verifier['file_hash'](Path(__file__)),
        'verifier_sha256':verifier['file_hash'](source/'release/verify-vitess-runtime.py'),
        'environment':environment,
        'exit_code':exit_code,'limit_error':limit_error,'elapsed_seconds':round(time.time()-started,3),
        'test_events':events,'passed_tests':sorted(passed_tests),'failed_tests':sorted({event['Test'] for event in events if event['Action'] in ['fail','skip']}),'passed':passed}
    descriptor=os.open(evidence,os.O_CREAT|os.O_EXCL|os.O_WRONLY,0o600)
    with os.fdopen(descriptor,'w') as output:json.dump(report,output,indent=2)
    print('Native Vitess case:',args.case,'passed:',passed,'exit:',exit_code,flush=True)
    print('Protected event log:',log,flush=True)
    print('Protected structural evidence:',evidence,flush=True)
    return 0 if passed else 1


if __name__=='__main__':
    try: sys.exit(main())
    except Exception as error:
        print('Native Vitess acceptance setup failed:',type(error).__name__,flush=True)
        sys.exit(1)
