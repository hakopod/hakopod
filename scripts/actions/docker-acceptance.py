#!/usr/bin/env python3
"""Run product-built runner pods only in this CI job's disposable dev cluster."""
import json
from datetime import datetime
import os
from pathlib import Path
import platform
import subprocess
import time

ROOT = Path(__file__).resolve().parents[2]
OUTPUT = ROOT / '.local/actions-runtime-acceptance'
KUBE = ['kubectl', '--kubeconfig', str(ROOT / '.local/kubeconfig'), '--context', 'k3d-hakopod-dev']
NODE = 'k3d-hakopod-dev-server-0'
PEER_NAMESPACE = 'hakopod-actions-acceptance-peer'
STARTED = time.monotonic()
DEADLINE = STARTED + 25 * 60
REPORT = {'schema_version': 1, 'status': 'running', 'host_architecture': platform.machine(),
          'scope': 'disposable single-node product runner pod acceptance',
          'limits': {'node_memory': os.environ.get('HAKOPOD_DEV_SERVER_MEMORY', '6g'),
                     'runner_cpu': '2', 'runner_memory': '4Gi', 'workspace_gib': 8,
                     'concurrent_runner_workloads': 1, 'concurrent_build_requests': 2,
                     'buildkit_max_parallelism': 2, 'disk_probe_max_written_mib': 2304},
          'checks': [], 'samples': {}, 'coverage_limits': [
              'One runner pod at a time; two concurrent BuildKit requests are not fleet load evidence.',
              'No GitHub registration, control-plane failover, provider throttling, or multi-node failure in this runtime fixture.',
              'Resource peaks are sampled lower bounds; short bursts can occur between samples.',
              'Cross-architecture support covers BuildKit Dockerfile RUN only, not binfmt_misc or foreign docker run.']}
NAMESPACES = set()
RUNTIME_CREATED = False
OUTPUT_CREATED = False


def command(args, timeout=45, check=True, **kwargs):
    remaining = DEADLINE - time.monotonic()
    if remaining <= 0:
        raise TimeoutError('runtime acceptance exceeded its 25-minute budget')
    result = subprocess.run(args, text=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE,
                            timeout=min(timeout, remaining), **kwargs)
    if check and result.returncode:
        raise RuntimeError(f'{args[:4]} exited {result.returncode}: {result.stderr[-16000:]} {result.stdout[-16000:]}')
    return result


def kube(args, **kwargs):
    return command(KUBE + args, **kwargs)


def apply(value):
    kube(['apply', '-f', '-'], input=json.dumps(value))


def record(name, started, **details):
    check = {'name': name, 'status': 'passed', 'duration_seconds': round(time.monotonic() - started, 3), **details}
    REPORT['checks'].append(check)
    print(json.dumps(check), flush=True)


def save_report():
    REPORT['duration_seconds'] = round(time.monotonic() - STARTED, 3)
    (OUTPUT / 'report.json').write_text(json.dumps(REPORT, indent=2) + '\n')


def sample():
    response = kube(['get', '--raw', f'/api/v1/nodes/{NODE}/proxy/stats/summary'], check=False, timeout=15)
    if response.returncode:
        REPORT['resource_sample_error'] = response.stderr[-512:]
        return
    stats = json.loads(response.stdout)
    for pod in stats.get('pods', []):
        ref = pod.get('podRef', {})
        if ref.get('namespace') not in NAMESPACES:
            continue
        key = f'{ref["namespace"]}/{ref["name"]}'
        peak = REPORT['samples'].setdefault(key, {'count': 0, 'memory_working_set_bytes': None,
                                                  'cpu_nanocores': None, 'ephemeral_used_bytes': None})
        peak['count'] += 1
        for output, family, field in [('memory_working_set_bytes', 'memory', 'workingSetBytes'),
                                     ('cpu_nanocores', 'cpu', 'usageNanoCores'),
                                     ('ephemeral_used_bytes', 'ephemeral-storage', 'usedBytes')]:
            value = pod.get(family, {}).get(field)
            if value is not None:
                peak[output] = max(peak[output] or 0, value)


def state(namespace, name='actions-runtime-fixture'):
    return json.loads(kube(['-n', namespace, 'get', 'pod', name, '-o', 'json']).stdout)


def startup_timings(pod):
    status = pod.get('status', {})
    initialized = {item['name']: item.get('state', {}) for item in status.get('initContainerStatuses', [])}
    prepare = initialized.get('prepare', {}).get('terminated', {})
    daemon = initialized.get('docker', {}).get('running', {})
    runner = next((item.get('state', {}).get('running', {}) for item in status.get('containerStatuses', []) if item['name'] == 'runner'), {})
    pairs = {
        'create_to_prepare_start_seconds': (pod['metadata'].get('creationTimestamp'), prepare.get('startedAt')),
        'prepare_copy_seconds': (prepare.get('startedAt'), prepare.get('finishedAt')),
        'prepare_finished_to_daemon_start_seconds': (prepare.get('finishedAt'), daemon.get('startedAt')),
        'daemon_start_to_runner_start_seconds': (daemon.get('startedAt'), runner.get('startedAt')),
    }
    result = {}
    for name, (start, end) in pairs.items():
        if start and end:
            result[name] = round((datetime.fromisoformat(end.replace('Z', '+00:00')) -
                                  datetime.fromisoformat(start.replace('Z', '+00:00'))).total_seconds(), 3)
    return result


def logs(namespace, scenario):
    # kubectl and kubelet bound the read; artifacts only contain our fixtures.
    result = kube(['-n', namespace, 'logs', 'actions-runtime-fixture', '-c', 'runner', '--limit-bytes=4194304'], check=False)
    (OUTPUT / f'{scenario}.log').write_text(result.stdout + result.stderr)
    events = []
    for line in result.stdout.splitlines():
        if line.startswith('HAKOPOD_ACCEPTANCE '):
            events.append(json.loads(line[len('HAKOPOD_ACCEPTANCE '):]))
    return events


def wait_for(namespace, predicate, timeout=300):
    end = min(DEADLINE, time.monotonic() + timeout)
    while time.monotonic() < end:
        pod = state(namespace)
        if predicate(pod):
            return pod
        if pod.get('status', {}).get('phase') in ('Succeeded', 'Failed'):
            raise RuntimeError(f'runner exited before expected checkpoint: {pod.get("status", {})}')
        sample()
        time.sleep(5)
    raise TimeoutError('runner checkpoint timed out')


def exec_runner(namespace, code):
    return kube(['-n', namespace, 'exec', 'actions-runtime-fixture', '-c', 'runner', '--', 'python3', '-c', code], timeout=30)


def image_observation(expected):
    env = {**os.environ, 'HAKOPOD_ACTIONS_RUNTIME_IMAGE_STATE': expected,
           'HAKOPOD_TEST_KUBECONFIG': str(ROOT / '.local/kubeconfig')}
    result = command(['go', 'test', '-p=1', './internal/cluster', '-run', '^TestActionsRuntimeImageObservationLive$',
                      '-count=1', '-timeout=45s', '-v'], env=env, cwd=ROOT, timeout=90)
    (OUTPUT / f'image-{expected}.log').write_text(result.stdout + result.stderr)


def delete_pod(namespace):
    uid = state(namespace)['metadata']['uid']
    workspace = f'/var/lib/kubelet/pods/{uid}/volumes/kubernetes.io~empty-dir/runner'
    kube(['-n', namespace, 'delete', 'pod', 'actions-runtime-fixture', '--wait=true', '--timeout=60s'], timeout=75)
    kube(['-n', namespace, 'delete', 'secret', 'actions-runtime-fixture', '--wait=true'])
    started = time.monotonic()
    for _ in range(30):
        if command(['docker', 'exec', NODE, 'test', '!', '-e', workspace], check=False).returncode == 0:
            record('private-workspace-removed', started, pod_uid=uid)
            return
        time.sleep(2)
    raise RuntimeError('the deleted pod still has a private workspace on the disposable node')


def boundary_checks(namespace):
    started = time.monotonic()
    uid = state(namespace)['metadata']['uid']
    # Establish the actual volume location while the runner is alive; kubelet
    # can already remove a completed/evicted pod's volume before API deletion.
    command(['docker', 'exec', NODE, 'test', '-d', f'/var/lib/kubelet/pods/{uid}/volumes/kubernetes.io~empty-dir/runner'])
    apply({'apiVersion': 'v1', 'kind': 'Namespace', 'metadata': {'name': PEER_NAMESPACE,
           'labels': {'app.kubernetes.io/managed-by': 'hakopod-actions-development-fixture'}}})
    NAMESPACES.add(PEER_NAMESPACE)
    apply({'apiVersion': 'v1', 'kind': 'Pod', 'metadata': {'name': 'peer', 'namespace': PEER_NAMESPACE}, 'spec': {
        'automountServiceAccountToken': False, 'restartPolicy': 'Never', 'activeDeadlineSeconds': 300,
        'securityContext': {'runAsNonRoot': True, 'runAsUser': 1001, 'seccompProfile': {'type': 'RuntimeDefault'}},
        'containers': [{'name': 'peer', 'image': 'docker.io/library/busybox:1.37.0@sha256:9db7b59979c38555a39def84a31fb98b5296952f9e3afd4f6f11f05b07adfab0',
                        'command': ['sh', '-c', 'mkdir -p /tmp/www; echo peer-ready >/tmp/www/index.html; exec httpd -f -p 18083 -h /tmp/www'],
                        'securityContext': {'allowPrivilegeEscalation': False, 'capabilities': {'drop': ['ALL']}},
                        'resources': {'requests': {'cpu': '10m', 'memory': '16Mi'}, 'limits': {'cpu': '100m', 'memory': '64Mi', 'ephemeral-storage': '16Mi'}},
                        'readinessProbe': {'exec': {'command': ['wget', '-T', '2', '-qO-', 'http://127.0.0.1:18083/']}, 'periodSeconds': 2}}]}})
    kube(['-n', PEER_NAMESPACE, 'wait', '--for=condition=Ready', 'pod/peer', '--timeout=90s'], timeout=100)
    peer = state(PEER_NAMESPACE, 'peer')['status']['podIP']
    runner = state(namespace)['status']['podIP']
    # Positive controls ensure denial is not an unstarted server or a bad port.
    exec_runner(namespace, "import urllib.request; assert urllib.request.urlopen('http://127.0.0.1:18082/', timeout=3).status == 200")
    api = json.loads(kube(['-n', 'default', 'get', 'service', 'kubernetes', '-o', 'json']).stdout)['spec']['clusterIP']
    for address, port in [(peer, 18083), (api, 443)]:
        exec_runner(namespace, f'''import socket
try:
    connection = socket.create_connection(({address!r}, {port}), timeout=3)
except OSError:
    pass
else:
    connection.close()
    raise RuntimeError("private network boundary allowed the connection")
''')
    response = kube(['-n', PEER_NAMESPACE, 'exec', 'peer', '--', 'wget', '-T', '3', '-qO-', f'http://{runner}:18082/'], check=False)
    assert response.returncode != 0, 'peer reached the runner HTTP endpoint'
    kube(['delete', 'namespace', PEER_NAMESPACE, '--wait=true', '--timeout=60s'], timeout=75)
    NAMESPACES.remove(PEER_NAMESPACE)
    record('namespace-ingress-egress-and-api-isolation', started, positive_controls=True)

    started = time.monotonic()
    image_observation('running')
    record('running-image-observation', started)
    started = time.monotonic()
    before = state(namespace)
    restart_count = next(status.get('restartCount', 0) for status in before['status']['initContainerStatuses'] if status['name'] == 'docker')
    # Restart before any build is active; the runner retains its workspace.
    # dockerd installs a TERM handler. Linux protects namespace PID 1 from an
    # unhandled KILL sent by another process in the same PID namespace.
    kube(['-n', namespace, 'exec', 'actions-runtime-fixture', '-c', 'docker', '--', 'sh', '-c', 'kill -TERM 1'], check=False)
    wait_for(namespace, lambda pod: any(status['name'] == 'docker' and status.get('restartCount', 0) > restart_count
                                       and status.get('ready') for status in pod.get('status', {}).get('initContainerStatuses', [])), timeout=120)
    exec_runner(namespace, "import subprocess; subprocess.run(['docker', 'info'], check=True, stdout=subprocess.DEVNULL)")
    record('docker-sidecar-restart', started)
    exec_runner(namespace, "from pathlib import Path; Path('/home/runner/_work/acceptance/continue').touch()")


def main():
    global RUNTIME_CREATED, OUTPUT_CREATED
    if os.environ.get('GITHUB_ACTIONS') != 'true':
        raise SystemExit('This fixture only runs in an isolated GitHub Actions job')
    label = command(['docker', 'inspect', '--format', '{{index .Config.Labels "k3d.cluster"}}', NODE]).stdout.strip()
    assert label == 'hakopod-dev', 'unexpected development container'
    OUTPUT.mkdir(parents=True, exist_ok=False)
    OUTPUT_CREATED = True
    command(['go', 'test', '-p=1', './internal/cluster', '-run', '^TestActionsRuntimeAcceptanceFixtures$', '-count=1', '-timeout=60s'],
            env={**os.environ, 'HAKOPOD_ACTIONS_RUNTIME_FIXTURE_DIR': str(OUTPUT)}, cwd=ROOT, timeout=240)
    fixtures = {name: json.loads((OUTPUT / f'{name}.json').read_text()) for name in ['workload', 'disk', 'replacement']}
    for fixture in fixtures.values():
        namespace = next(item['metadata']['name'] for item in fixture['items'] if item['kind'] == 'Namespace')
        assert not kube(['get', 'namespace', namespace, '--ignore-not-found', '-o', 'name']).stdout.strip(), 'fixture namespace already exists'
    assert not kube(['get', 'namespace', PEER_NAMESPACE, '--ignore-not-found', '-o', 'name']).stdout.strip(), 'peer namespace already exists'
    assert not kube(['get', 'runtimeclass', 'hakopod-actions', '--ignore-not-found', '-o', 'name']).stdout.strip(), 'runtime class already exists'
    apply({'apiVersion': 'node.k8s.io/v1', 'kind': 'RuntimeClass', 'metadata': {'name': 'hakopod-actions',
           'labels': {'app.kubernetes.io/managed-by': 'hakopod'}}, 'handler': 'hakopod-actions',
           'scheduling': {'nodeSelector': {'hakopod.io/actions-runtime': 'ready'}},
           'overhead': {'podFixed': {'cpu': '100m', 'memory': '512Mi'}}})
    RUNTIME_CREATED = True
    kube(['label', 'node', NODE, 'hakopod.io/actions-runtime=ready'])
    workload_ns = next(item['metadata']['namespace'] for item in fixtures['workload']['items'] if item['kind'] == 'Pod')
    disk_ns = next(item['metadata']['namespace'] for item in fixtures['disk']['items'] if item['kind'] == 'Pod')
    NAMESPACES.add(workload_ns)
    started = time.monotonic()
    apply(fixtures['workload'])
    startup = wait_for(workload_ns, lambda pod: any(event['phase'] == 'sandbox-boundary' for event in logs(workload_ns, 'workload')), timeout=360)
    assert startup['spec'].get('nodeName') == NODE, 'runner did not use the explicitly selected node'
    record('runner-and-docker-ready', started, includes_pinned_image_pulls=True, pod_timings=startup_timings(startup))
    boundary_checks(workload_ns)
    started = time.monotonic()
    pod = wait_for(workload_ns, lambda pod: pod.get('status', {}).get('phase') in ('Succeeded', 'Failed'), timeout=1200)
    REPORT['workload_events'] = logs(workload_ns, 'workload')
    assert pod['status']['phase'] == 'Succeeded', pod['status']
    assert any(event['phase'] == 'complete' for event in REPORT['workload_events']), 'missing workload completion'
    record('compiled-multiarch-workload', started)
    started = time.monotonic()
    delete_pod(workload_ns)
    image_observation('absent')
    record('deleted-pod-image-observation', started)

    started = time.monotonic()
    NAMESPACES.add(disk_ns)
    apply(fixtures['disk'])
    disk_pod = wait_for(disk_ns, lambda pod: pod.get('status', {}).get('phase') == 'Failed', timeout=300)
    REPORT['disk_events'] = logs(disk_ns, 'disk')
    status = disk_pod['status']
    assert status.get('reason') == 'Evicted' and ('emptydir' in status.get('message', '').lower() or 'ephemeral-storage' in status.get('message', '').lower()), status
    record('bounded-workspace-eviction', started, reason=status['reason'], message=status.get('message'))
    old_uid = disk_pod['metadata']['uid']
    delete_pod(disk_ns)
    started = time.monotonic()
    apply(fixtures['replacement'])
    replacement = wait_for(disk_ns, lambda pod: pod.get('status', {}).get('phase') in ('Succeeded', 'Failed'), timeout=240)
    REPORT['replacement_events'] = logs(disk_ns, 'replacement')
    assert replacement['status']['phase'] == 'Succeeded', replacement['status']
    assert replacement['metadata']['uid'] != old_uid
    record('replacement-after-disk-eviction', started, previous_uid=old_uid, replacement_uid=replacement['metadata']['uid'])
    workload_samples = REPORT['samples'].get(f'{workload_ns}/actions-runtime-fixture', {})
    assert workload_samples.get('count', 0) > 0 and workload_samples.get('memory_working_set_bytes') is not None, 'kubelet did not provide runner resource measurements'
    REPORT['status'] = 'passed'


try:
    main()
except BaseException as error:
    REPORT['status'] = 'failed'
    REPORT['error'] = str(error)
    raise
finally:
    if OUTPUT_CREATED:
        for namespace in sorted(NAMESPACES):
            # Cleanup has its own bounded budget after the workload deadline.
            try:
                diagnostics = subprocess.run(KUBE + ['-n', namespace, 'describe', 'pods'], text=True,
                                             stdout=subprocess.PIPE, stderr=subprocess.STDOUT, timeout=30)
                (OUTPUT / f'{namespace}-pods.txt').write_text(diagnostics.stdout[-65536:])
                cleanup = subprocess.run(KUBE + ['delete', 'namespace', namespace, '--wait=true', '--timeout=60s'],
                                         text=True, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, timeout=75)
                if cleanup.returncode:
                    raise RuntimeError(cleanup.stdout[-2048:])
                verify = subprocess.run(KUBE + ['get', 'namespace', namespace, '--ignore-not-found', '-o', 'name'],
                                        text=True, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, timeout=20)
                if verify.returncode or verify.stdout.strip():
                    raise RuntimeError('fixture namespace still exists after cleanup: ' + verify.stdout[-2048:])
                REPORT.setdefault('deleted_namespaces', []).append(namespace)
            except Exception as error:
                REPORT['status'] = 'failed'
                REPORT.setdefault('cleanup_errors', []).append(str(error))
        if RUNTIME_CREATED:
            try:
                subprocess.run(KUBE + ['delete', 'runtimeclass', 'hakopod-actions', '--wait=true', '--timeout=15s'], check=True, timeout=20)
            except Exception as error:
                REPORT['status'] = 'failed'
                REPORT.setdefault('cleanup_errors', []).append(str(error))
        save_report()
        print(json.dumps(REPORT), flush=True)
        if REPORT['status'] != 'passed':
            raise SystemExit(1)
