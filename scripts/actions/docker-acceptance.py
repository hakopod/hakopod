#!/usr/bin/env python3
"""Run product-built runner pods only in this CI job's disposable dev cluster.

HAKOPOD_ACTIONS_EXPORT_BENCHMARK=1 selects the export-only diagnostic instead of
the normal runtime/disk suite. It does not add a long benchmark to default runs.
HAKOPOD_ACTIONS_EXPORT_FORCE_OVERLAY_DIFF=1 additionally selects its explicit
forced-overlay diagnostic. Both modes retain the named-cluster and isolation
checks. HAKOPOD_ACTIONS_EXPORT_BUILDKIT_IMAGE selects a digest-pinned Hakopod
candidate only for this benchmark; an empty value retains the stock image.
HAKOPOD_ACTIONS_RUNTIME_BUILDKIT_IMAGE independently selects a candidate for the
full normal runtime suite. HAKOPOD_ACTIONS_RUNTIME_FORCE_OVERLAY_DIFF=1 requires
that explicit candidate. Neither input may overlap an export-only diagnostic.
An owned VM wrapper can generate the same fake-client fixture with:

  HAKOPOD_ACTIONS_EXPORT_BENCHMARK=1 HAKOPOD_ACTIONS_RUNTIME_FIXTURE_DIR=<directory>
  go test -p=1 ./internal/cluster -run '^TestActionsExportBenchmarkFixture$' -count=1

Apply only to the verified named development cluster. Capture actual outer CPU
limits when using a worker different from this disposable CI server node.
"""
import json
from datetime import datetime
import importlib.util
import math
import os
from pathlib import Path
import platform
import re
import selectors
import signal
import subprocess
import threading
import time

ROOT = Path(__file__).resolve().parents[2]
OUTPUT = ROOT / '.local/actions-runtime-acceptance'
KUBE = ['kubectl', '--kubeconfig', str(ROOT / '.local/kubeconfig'), '--context', 'k3d-hakopod-dev']
NODE = 'k3d-hakopod-dev-server-0'
PEER_NAMESPACE = 'hakopod-actions-acceptance-peer'
STARTED = time.monotonic()
EXPORT_BENCHMARK = os.environ.get('HAKOPOD_ACTIONS_EXPORT_BENCHMARK') == '1'
BUILDKIT_QUALIFICATION = os.environ.get('HAKOPOD_ACTIONS_BUILDKIT_QUALIFICATION') == '1'
SERVICE_PULL_BENCHMARK = os.environ.get('HAKOPOD_ACTIONS_SERVICE_PULL_BENCHMARK') == '1'
DEADLINE = STARTED + (32 if SERVICE_PULL_BENCHMARK else 25) * 60
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
SHARED_WORKSPACE = None
LOG_LIMIT = 4 * 1024 * 1024


class BoundedLogCapture:
    """Persist the live fixture stream before kubelet can remove an evicted Pod."""
    def __init__(self, namespace, scenario):
        assert scenario in ('export-benchmark', 'buildkit-qualification', 'service-pull-benchmark'), 'unexpected diagnostic log name'
        self.namespace, self.scenario = namespace, scenario
        self.stop = threading.Event()
        self.process = self.thread = None
        self.result = {'started': False, 'stdout_bytes': 0, 'stderr_bytes': 0, 'truncated': False}
        self.streams = {}
        try:
            for name, suffix in [('stdout', '.log'), ('stderr', '-capture.log')]:
                fd = os.open(OUTPUT / (scenario + suffix), os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600)
                self.streams[name] = os.fdopen(fd, 'wb', buffering=0)
        except OSError:
            for stream in self.streams.values():
                stream.close()
            raise

    def observe(self, pod):
        if self.result['started']:
            return
        statuses = pod.get('status', {}).get('containerStatuses', [])
        runner = next((item.get('state', {}) for item in statuses if item['name'] == 'runner'), {})
        if not (runner.get('running') or runner.get('terminated')):
            return
        self.result['started'] = True
        try:
            self.process = subprocess.Popen(KUBE + ['-n', self.namespace, 'logs', 'actions-runtime-fixture',
                '-c', 'runner', '--follow', '--tail=-1'],
                stdin=subprocess.DEVNULL, stdout=subprocess.PIPE, stderr=subprocess.PIPE, start_new_session=True)
            self.thread = threading.Thread(target=self._collect, daemon=True)
            self.thread.start()
        except OSError as error:
            self.result['error_type'] = type(error).__name__

    def _collect(self):
        selector = selectors.DefaultSelector()
        terminated_at = None
        try:
            for name in ('stdout', 'stderr'):
                stream = getattr(self.process, name)
                os.set_blocking(stream.fileno(), False)
                selector.register(stream, selectors.EVENT_READ, name)
            while selector.get_map():
                if time.monotonic() >= DEADLINE:
                    self.result['deadline_reached'] = True
                    self.stop.set()
                if self.stop.is_set():
                    if terminated_at is None:
                        self._signal_group(signal.SIGTERM)
                        terminated_at = time.monotonic()
                    elif time.monotonic() - terminated_at >= 2:
                        self._signal_group(signal.SIGKILL)
                        if time.monotonic() - terminated_at >= 2.5:
                            self.result['drain_deadline_reached'] = True
                            break
                for key, _ in selector.select(0.1):
                    chunk = os.read(key.fd, 16384)
                    if not chunk:
                        selector.unregister(key.fileobj)
                        continue
                    name = key.data
                    limit = LOG_LIMIT if name == 'stdout' else 65536
                    remaining = max(0, limit - self.result[name + '_bytes'])
                    self.streams[name].write(chunk[:remaining])
                    self.result[name + '_bytes'] += min(len(chunk), remaining)
                    if len(chunk) > remaining:
                        self.result['truncated'] = True
                        self.stop.set()
            self.result['exit_code'] = self.process.wait(timeout=2)
        except Exception as error:
            self.result['error_type'] = type(error).__name__
            self._signal_group(signal.SIGKILL)
            if self.process.poll() is None:
                self.process.wait(timeout=2)
        finally:
            selector.close()
            for name in ('stdout', 'stderr'):
                getattr(self.process, name).close()

    def _signal_group(self, value):
        # The follower starts its own session. Its subprocesses must not retain
        # pipes or survive cancellation after kubectl itself has already exited.
        try:
            os.killpg(self.process.pid, value)
        except ProcessLookupError:
            pass

    def close(self):
        if self.thread:
            # A terminal Pod usually closes the follower itself. Drain those
            # final bytes before interrupting a still-open stream.
            self.thread.join(timeout=1)
            if self.thread.is_alive():
                self.stop.set()
                self.thread.join(timeout=4)
            if self.thread.is_alive():
                self._signal_group(signal.SIGKILL)
                self.thread.join(timeout=2)
                self.result['error_type'] = 'CollectorDidNotStop'
        for stream in self.streams.values():
            stream.close()
        return dict(self.result)


def captured_records(scenario, prefix):
    with (OUTPUT / (scenario + '.log')).open('rb') as source:
        data = source.read(LOG_LIMIT + 1)
    assert len(data) <= LOG_LIMIT, 'captured diagnostic log exceeds its bound'
    records, malformed = [], 0
    for line in data.splitlines(keepends=True):
        if not line.startswith(prefix.encode()):
            continue
        if not line.endswith(b'\n') or len(line) > 1048576:
            malformed += 1
            continue
        try:
            value = json.loads(line[len(prefix):])
            assert isinstance(value, dict)
        except (ValueError, AssertionError):
            malformed += 1
            continue
        records.append(value)
        assert len(records) <= 4096, 'captured diagnostic event count exceeds its bound'
    return records, malformed


def verify_log_capture(result):
    assert result.get('started') and result.get('stdout_bytes', 0) > 0, 'diagnostic live log capture did not start'
    assert result.get('exit_code') == 0, 'diagnostic log follower did not exit cleanly'
    assert not any(result.get(key) for key in ('truncated', 'deadline_reached', 'drain_deadline_reached',
        'error_type', 'capture_error_type', 'malformed_events', 'malformed_reports')), 'diagnostic log capture is incomplete or malformed'


def export_buildkit_selection(image=''):
    spec = importlib.util.spec_from_file_location('selected_export_benchmark', ROOT / 'scripts/actions/export-benchmark.py')
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module.buildkit_selection(image)


def runtime_helpers():
    spec = importlib.util.spec_from_file_location('selected_runtime_workload', ROOT / 'scripts/actions/runtime-workload.py')
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def shared_workspace_helpers():
    spec = importlib.util.spec_from_file_location('dev_shared_workspace', ROOT / 'scripts/actions/shared-workspace.py')
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def verify_runtime_report(events, selected, architecture):
    helpers = runtime_helpers()
    assert architecture in ('amd64', 'arm64'), 'unsupported runtime architecture'
    assert isinstance(events, list) and len(events) <= 4096, 'runtime event bound exceeded'
    def details(phase):
        matching = [event for event in events if event.get('phase') == phase]
        assert len(matching) == 1 and matching[0].get('status') == 'passed', 'missing or repeated runtime phase: ' + phase
        return matching[0].get('details', {})
    initial = details('runtime-builder-verified')
    result = details('compiled-multiarch-registry-concurrency')
    verified = details('build-results-verified')
    assert {key: value for key, value in result.items() if key != 'timings'} == {key: value for key, value in verified.items() if key != 'timings'}, 'runtime build evidence changed during cleanup'
    assert {key: value for key, value in result.get('timings', {}).items() if key != 'ephemeral_builder_stop_seconds'} == verified.get('timings'), 'runtime timing evidence changed during cleanup'
    details('complete')
    details('sandbox-boundary')
    details('containers-and-services')
    assert initial.get('selection') == selected and result.get('buildkit_selection') == selected, 'runtime BuildKit selection changed'
    identity = initial.get('builder', {})
    assert identity == result.get('builder'), 'runtime builder identity changed after compilation'
    assert re.fullmatch(r'sha256:[a-f0-9]{64}', identity.get('image_id', '')), 'runtime builder image ID is invalid'
    expected = {'image': selected['reference'], 'image_id': identity['image_id'],
                'version': 'buildkitd github.com/moby/buildkit ' + selected['version'] + ' ' + selected['upstream_revision'],
                'architecture': architecture, 'managed_userxattr': selected['kind'] == 'candidate',
                'snapshotter': selected['snapshotter'], 'force_overlay_diff': selected['force_overlay_diff'],
                'upstream_revision': selected['upstream_revision']}
    assert identity == expected, 'runtime builder identity differs from the explicitly selected mode'
    for key in ('platforms', 'pullback_execution_platforms', 'native_cross_compile_platforms'):
        assert result.get(key) == ['amd64', 'arm64'], 'runtime did not verify both platforms: ' + key
    assert re.fullmatch(r'sha256:[a-f0-9]{64}', result.get('manifest_digest', '')), 'runtime manifest digest is invalid'
    assert result.get('native_and_emulated_compiler') is True and result.get('failed_build_recovered') is True and result.get('post_failure_pullback_verified') is True, 'runtime compiler or recovery checks are incomplete'
    assert result.get('buildkit_max_parallelism') == 2, 'runtime compiler concurrency changed'
    requests = result.get('parallel_build_requests', [])
    assert len(requests) == 2 and [item.get('request') for item in requests] == [1, 2], 'runtime did not verify concurrent build requests'
    timings = result.get('timings', {})
    helpers.verify_compiler_metrics(timings.get('cold_compiler_vertices'), architecture, cold=True)
    helpers.verify_compiler_metrics(timings.get('warm_compiler_vertices'), architecture, cold=False)
    return identity


def export_integration_bundle(directory, image, architecture, run_id=None):
    path = ROOT / 'scripts/actions/buildkit-integration.py'
    spec = importlib.util.spec_from_file_location('native_buildkit_integration', path)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    helper = module.file_evidence(path, 128 * 1024)
    bundle = module.load_bundle(directory, image, architecture, run_id)
    return {'module': module, 'bundle': bundle, 'helper': helper, 'helper_path': str(path), 'image': image,
            'architecture': architecture, 'run_id': run_id}


def stage_export_integration(call, namespace, integration):
    """Copy only verified artifacts into the already-running private workspace."""
    started = time.monotonic()
    module, bundle = integration['module'], integration['bundle']
    ready = f'''from pathlib import Path
import time
root = Path({module.DESTINATION!r})
deadline = time.monotonic() + 10
while not root.is_dir():
    assert time.monotonic() < deadline, 'native test workspace was not created'
    time.sleep(0.1)
assert root.resolve(strict=True) == root and not (root / 'ready').exists()
'''
    call(['-n', namespace, 'exec', 'actions-runtime-fixture', '-c', 'runner', '--', 'python3', '-c', ready], timeout=15)
    for name in (*module.FILES, 'buildkit-integration.py'):
        remaining = 110 - (time.monotonic() - started)
        assert remaining > 0, 'native test artifact staging exceeded its bound'
        path = integration['helper_path'] if name == 'buildkit-integration.py' else str(Path(bundle['directory']) / name)
        call(['cp', path, f'{namespace}/actions-runtime-fixture:{module.DESTINATION}/{name}', '-c', 'runner'], timeout=min(35, remaining))
    code = f'''import hashlib, os
from pathlib import Path
root = Path({module.DESTINATION!r})
assert root.resolve(strict=True) == root
fd = os.open(root / 'buildkit-integration.py', os.O_RDONLY | os.O_NOFOLLOW)
with os.fdopen(fd, 'rb') as source:
    data = source.read(131073)
assert len(data) <= 131072 and hashlib.sha256(data).hexdigest() == {integration['helper']['sha256']!r}
module = {{'__name__': 'native_buildkit_staging'}}
exec(compile(data, 'buildkit-integration.py', 'exec'), module)
module['load_bundle'](root, {integration['image']!r}, {integration['architecture']!r}, {integration['run_id']!r}, staged=True, ready=False)
fd = os.open(root / 'ready.pending', os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600)
with os.fdopen(fd, 'wb') as output:
    output.write(b'ready\\n')
    output.flush()
    os.fsync(output.fileno())
os.rename(root / 'ready.pending', root / 'ready')
'''
    remaining = 110 - (time.monotonic() - started)
    assert remaining > 0, 'native test artifact staging exceeded its bound'
    call(['-n', namespace, 'exec', 'actions-runtime-fixture', '-c', 'runner', '--', 'python3', '-c', code], timeout=min(30, remaining))
    return {'source': bundle['source'], 'files': bundle['files'], 'helper': integration['helper'],
            'duration_seconds': round(time.monotonic() - started, 3)}


if EXPORT_BENCHMARK or BUILDKIT_QUALIFICATION:
    REPORT['scenario'] = 'opt-in-buildkit-qualification' if BUILDKIT_QUALIFICATION else 'opt-in-export-benchmark'
    REPORT['limits'].update(workspace_gib=4, concurrent_build_requests=1, buildkit_max_parallelism=1)
    REPORT['limits'].pop('disk_probe_max_written_mib')
    REPORT['coverage_limits'] = [
        'Two compression settings in one disposable product sandbox; no fleet or provider lifecycle qualification.',
        'Pinned BuildKit v0.32.2 diagnostics do not qualify other versions or an overlay patch.',
        'Resource peaks are sampled lower bounds; the pod and outer worker limits are reported separately.']
    if BUILDKIT_QUALIFICATION:
        REPORT['coverage_limits'][0] = 'Small metadata and fresh-builder registry-cache fixture; no fleet or provider lifecycle qualification.'


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
    if SHARED_WORKSPACE:
        SHARED_WORKSPACE.usage(stats)
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


def logs(namespace, scenario, prefix='HAKOPOD_ACCEPTANCE '):
    # kubectl and kubelet bound the read; artifacts only contain our fixtures.
    result = kube(['-n', namespace, 'logs', 'actions-runtime-fixture', '-c', 'runner', '--limit-bytes=4194304'], check=False)
    (OUTPUT / f'{scenario}.log').write_text(result.stdout + result.stderr)
    events = []
    for line in result.stdout.splitlines():
        if line.startswith(prefix):
            events.append(json.loads(line[len(prefix):]))
    return events


def wait_for(namespace, predicate, timeout=300, capture=None):
    end = min(DEADLINE, time.monotonic() + timeout)
    while time.monotonic() < end:
        pod = state(namespace)
        if capture:
            capture.observe(pod)
        if predicate(pod):
            if SHARED_WORKSPACE:
                sample()
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


def delete_pod(namespace, credential_config=True):
    uid = state(namespace)['metadata']['uid']
    workspace = f'/var/lib/kubelet/pods/{uid}/volumes/kubernetes.io~empty-dir/runner'
    kube(['-n', namespace, 'delete', 'pod', 'actions-runtime-fixture', '--wait=true', '--timeout=60s'], timeout=75)
    if credential_config:
        kube(['-n', namespace, 'delete', 'secret', 'actions-runtime-fixture', '--wait=true'])
    started = time.monotonic()
    for _ in range(30):
        if command(['docker', 'exec', NODE, 'test', '!', '-e', workspace], check=False).returncode == 0:
            if SHARED_WORKSPACE:
                SHARED_WORKSPACE.removed(uid)
            record('private-workspace-removed', started, pod_uid=uid)
            return
        time.sleep(2)
    raise RuntimeError('the deleted pod still has a private workspace on the disposable node')


def restart_docker(namespace, timeout=120, capture=None):
    before = state(namespace)
    restart_count = next(status.get('restartCount', 0) for status in before['status']['initContainerStatuses'] if status['name'] == 'docker')
    # dockerd installs a TERM handler. Linux protects namespace PID 1 from an
    # unhandled KILL sent by another process in the same PID namespace.
    kube(['-n', namespace, 'exec', 'actions-runtime-fixture', '-c', 'docker', '--', 'sh', '-c', 'kill -TERM 1'], check=False)
    wait_for(namespace, lambda pod: any(status['name'] == 'docker' and status.get('restartCount', 0) > restart_count
        and status.get('ready') for status in pod.get('status', {}).get('initContainerStatuses', [])), timeout=timeout, capture=capture)
    exec_runner(namespace, "import subprocess; subprocess.run(['docker', 'info'], check=True, stdout=subprocess.DEVNULL)")


def prepare_shared_workspace(namespace, capture=None, full=True, disk=False):
    if not SHARED_WORKSPACE:
        return
    def ready(pod):
        running = any(item['name'] == 'runner' and item.get('state', {}).get('running')
            for item in pod.get('status', {}).get('containerStatuses', []))
        return running and kube(['-n', namespace, 'exec', 'actions-runtime-fixture', '-c', 'runner', '--',
            'test', '-f', '/home/runner/.hakopod-shared-ready'], check=False, timeout=10).returncode == 0
    pod = wait_for(namespace, ready, timeout=360, capture=capture)
    started = time.monotonic()
    SHARED_WORKSPACE.prepare(pod, lambda timeout: restart_docker(namespace, timeout=timeout, capture=capture), full=full, disk=disk)
    record('development-shared-disk-workspace', started, pod_uid=pod['metadata']['uid'])


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
    # Restart before any build is active; the runner retains its workspace.
    restart_docker(namespace)
    record('docker-sidecar-restart', started)
    exec_runner(namespace, "from pathlib import Path; Path('/home/runner/_work/acceptance/continue').touch()")


def export_fixture(fixture):
    return diagnostic_fixture(fixture)


def diagnostic_fixture(fixture, service_pull=False):
    marker = 'actions-service-pull-benchmark' if service_pull else 'actions-export-benchmark'
    context_env = 'HAKOPOD_SERVICE_PULL_BENCHMARK_DEV_CONTEXT' if service_pull else 'HAKOPOD_EXPORT_BENCHMARK_DEV_CONTEXT'
    items = fixture['items']
    assert all(item['kind'] in {'Namespace', 'NetworkPolicy', 'ResourceQuota', 'Pod'} for item in items), 'export fixture must not include credentials or extra infrastructure'
    assert all(item['metadata'].get('labels', {}).get('hakopod.io/development-fixture') == marker for item in items), 'unmarked diagnostic fixture object'
    namespaces = [item for item in items if item['kind'] == 'Namespace']
    pods = [item for item in items if item['kind'] == 'Pod']
    assert len(namespaces) == len(pods) == 1, 'expected one private namespace and one runner pod'
    namespace, pod = namespaces[0]['metadata']['name'], pods[0]
    assert namespace.startswith('hp-') and pod['metadata']['namespace'] == namespace, 'unexpected product namespace'
    assert pod['metadata']['name'] == 'actions-runtime-fixture', 'unexpected benchmark pod name'
    assert all(item['metadata'].get('namespace', namespace) == namespace for item in items), 'fixture crosses namespace boundaries'
    policies = {item['metadata']['name']: item['spec'] for item in items if item['kind'] == 'NetworkPolicy'}
    deny = policies.get('hakopod-default-deny', {})
    assert deny.get('podSelector') == {} and set(deny.get('policyTypes', [])) == {'Ingress', 'Egress'} and not deny.get('ingress') and not deny.get('egress'), 'product default-deny policy is missing'
    assert 'hakopod-service-runner' in policies, 'product runner egress policy is missing'
    config = pod['spec']
    assert config.get('automountServiceAccountToken') is False and config.get('runtimeClassName') == 'hakopod-actions', 'sandbox or service-account boundary changed'
    assert not any(config.get(key) for key in ['hostNetwork', 'hostPID', 'hostIPC']), 'host namespace requested'
    volumes = config.get('volumes', [])
    assert len(volumes) == 1 and set(volumes[0]) == {'name', 'emptyDir'} and volumes[0]['name'] == 'runner' and volumes[0]['emptyDir'].get('sizeLimit') == '4Gi', 'benchmark must only use its bounded 4 GiB workspace'
    assert not config.get('imagePullSecrets'), 'benchmark must not receive registry credentials'
    allowed_env = {'DOCKER_HOST', 'ACTIONS_RUNNER_PRINT_LOG_TO_STDOUT', context_env}
    if service_pull:
        allowed_env.add('HAKOPOD_ACTIONS_DOCKER_STORAGE_DRIVER')
        assert config.get('activeDeadlineSeconds') == 30 * 60, 'service pull Pod deadline changed'
    containers = config['initContainers'] + config['containers']
    for container in containers:
        assert '@sha256:' in container['image'], 'benchmark image is not pinned'
        assert not container.get('envFrom') and not container.get('securityContext', {}).get('privileged'), 'unexpected credentials or privilege'
        assert all(value['name'] in allowed_env and 'valueFrom' not in value for value in container.get('env', [])), 'unexpected environment injection'
        assert all(mount['name'] == 'runner' for mount in container.get('volumeMounts', [])), 'unexpected credential or host mount'
    runner_env = {value['name']: value.get('value') for value in config['containers'][0]['env']}
    assert runner_env.get('DOCKER_HOST') == 'tcp://127.0.0.1:2375' and runner_env.get(context_env) == 'k3d-hakopod-dev', 'benchmark entry environment changed'
    if service_pull:
        assert runner_env.get('HAKOPOD_ACTIONS_DOCKER_STORAGE_DRIVER') == shared_workspace_helpers().storage_driver(os.environ), 'service pull driver differs from selection'
    return namespace


def verify_service_pull_report(value, architecture, driver):
    spec = importlib.util.spec_from_file_location('service_pull_evidence', ROOT / 'scripts/actions/service-pull-benchmark.py')
    benchmark = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(benchmark)
    assert architecture in ('amd64', 'arm64') and driver in ('vfs', 'overlay2'), 'unsupported service pull selection'
    assert value.get('schema_version') == 1 and value.get('scenario') == 'cold-service-image-pulls' and value.get('status') == 'passed', 'service pull benchmark did not pass'
    assert value.get('context') == 'k3d-hakopod-dev' and value.get('architecture') == architecture and value.get('storage_driver') == driver, 'service pull context, architecture, or driver changed'
    assert value.get('limits') == {'images': 2, 'seconds_per_image': benchmark.MAX_IMAGE_SECONDS,
                                   'stream_bytes_per_image': benchmark.MAX_STREAM_BYTES, 'events_per_image': benchmark.MAX_EVENTS}, 'service pull bounds changed'
    services = value.get('services', [])
    assert isinstance(services, list) and len(services) == 2, 'service pull results are incomplete'
    for measured, service in zip(services, benchmark.SERVICES):
        expected = service['platforms'][architecture]
        image = measured.get('image', {})
        assert measured.get('service') == service['name'], 'service pull order or identity changed'
        assert image == {'config_digest': expected['config_digest'], 'platform': 'linux/' + architecture,
                         'repository_digest': service['reference'].rsplit('@', 1)[1]}, 'service pull image identity changed'
        assert measured.get('target_image_absent_before') is True and measured.get('target_config_absent_before') is True, 'service pull was not cold'
        assert measured.get('cleanup') == {'status': 'passed', 'removed_config_digest': expected['config_digest']}, 'service pull cleanup did not pass'
        version = measured.get('version')
        assert isinstance(version, str) and 0 < len(version) <= 256 and '\n' not in version, 'service binary execution evidence is missing'
        pull = measured.get('pull', {})
        total = pull.get('total_seconds')
        assert type(total) in (int, float) and math.isfinite(total) and 0 <= total <= benchmark.MAX_IMAGE_SECONDS, 'service pull duration is invalid'
        assert type(pull.get('event_count')) is int and 2 * len(expected['layer_prefixes']) <= pull['event_count'] <= benchmark.MAX_EVENTS, 'service pull event count is invalid'
        assert type(pull.get('stream_bytes')) is int and 0 < pull['stream_bytes'] <= benchmark.MAX_STREAM_BYTES, 'service pull byte count is invalid'
        layers = pull.get('layers', {})
        assert isinstance(layers, dict) and set(layers) == set(expected['layer_prefixes']), 'service pull layer evidence is incomplete'
        for timing in layers.values():
            assert set(timing) == {'Download complete', 'Pull complete', 'extraction_registration_queue_seconds'}, 'service layer timing evidence changed'
            download, complete, elapsed = (timing[key] for key in ('Download complete', 'Pull complete', 'extraction_registration_queue_seconds'))
            assert all(type(item) in (int, float) and math.isfinite(item) for item in (download, complete, elapsed)), 'service layer timing is invalid'
            assert 0 <= download <= complete <= total and elapsed == round(complete - download, 3), 'service layer timing order is invalid'


def run_service_pull_benchmark(fixture):
    scenario = 'service-pull-benchmark'
    namespace = diagnostic_fixture(fixture, service_pull=True)
    NAMESPACES.add(namespace)
    REPORT['scenario'] = scenario
    REPORT['limits'].update(workspace_gib=4, concurrent_build_requests=0, buildkit_max_parallelism=0,
                            disk_probe_max_written_mib=0, service_pull_seconds_per_image=600)
    started = time.monotonic()
    apply(fixture)
    capture = BoundedLogCapture(namespace, scenario)
    failure = None
    try:
        prepare_shared_workspace(namespace, capture)
        pod = wait_for(namespace, lambda value: value.get('status', {}).get('phase') in ('Succeeded', 'Failed'), timeout=1800, capture=capture)
    except BaseException as error:
        failure = error
        raise
    finally:
        try:
            REPORT['service_pull_log_capture'] = capture.close()
            events, malformed = captured_records(scenario, 'HAKOPOD_SERVICE_PULL_EVENT ')
            REPORT['service_pull_log_capture']['malformed_events'] = malformed
            REPORT['service_pull_events'] = events
            saved, malformed = captured_records(scenario, 'HAKOPOD_SERVICE_PULL ')
            REPORT['service_pull_log_capture']['malformed_reports'] = malformed
            if len(saved) == 1:
                REPORT['service_pull_benchmark'] = saved[0]
                (OUTPUT / (scenario + '-report.json')).write_text(json.dumps(saved[0], indent=2) + '\n')
        except Exception as error:
            REPORT.setdefault('service_pull_log_capture', {})['capture_error_type'] = type(error).__name__
            if failure is None:
                raise
    REPORT['service_pull_pod'] = {
        'namespace': namespace, 'name': pod['metadata']['name'], 'node': pod['spec'].get('nodeName'),
        'phase': pod.get('status', {}).get('phase'), 'reason': pod.get('status', {}).get('reason'),
        'message': pod.get('status', {}).get('message'), 'overhead': pod['spec'].get('overhead', {}),
        'startup_timings': startup_timings(pod),
        'containers': [{'name': item['name'], 'resources': item['resources']} for item in pod['spec']['initContainers'] + pod['spec']['containers']]}
    outer = command(['docker', 'inspect', NODE, '--format', '[{{json .HostConfig.NanoCpus}},{{json .HostConfig.CpuQuota}},{{json .HostConfig.CpuPeriod}},{{json .HostConfig.Memory}},{{json .HostConfig.CpusetCpus}}]']).stdout
    limits = json.loads(outer)
    assert len(limits) == 5, 'outer worker limits could not be observed'
    REPORT['service_pull_outer_worker'] = dict(zip(['nano_cpus', 'cpu_quota', 'cpu_period', 'memory_bytes', 'cpuset_cpus'], limits))
    assert len(saved) == 1, 'service pull did not preserve exactly one JSON report'
    verify_log_capture(REPORT['service_pull_log_capture'])
    assert pod['spec'].get('nodeName') == NODE and pod.get('status', {}).get('phase') == 'Succeeded', 'service pull Pod failed or ran on an unexpected node'
    architecture = {'x86_64': 'amd64', 'aarch64': 'arm64', 'amd64': 'amd64', 'arm64': 'arm64'}.get(platform.machine())
    verify_service_pull_report(saved[0], architecture, shared_workspace_helpers().storage_driver(os.environ))
    assert events == saved[0]['services'], 'service pull completion differs from its captured per-service evidence'
    record('opt-in-service-pull-benchmark', started, report=scenario + '-report.json')
    delete_pod(namespace, credential_config=False)


def verify_qualification_report(value, selected, architecture, force):
    assert selected.get('kind') == 'candidate', 'qualification requires an explicitly selected managed candidate'
    assert value.get('schema_version') == 1 and value.get('scenario') == 'buildkit-metadata-and-registry-cache', 'unexpected qualification scenario'
    assert value.get('status') == 'passed' and value.get('metadata_cache_checks_passed') is True, 'metadata and registry-cache qualification did not pass'
    assert value.get('context') == 'k3d-hakopod-dev' and value.get('architecture') == architecture, 'qualification ran in an unexpected context or architecture'
    assert value.get('snapshotter') == 'overlayfs' and value.get('force_overlay_diff') is force, 'qualification changed its selected overlay configuration'
    assert value.get('buildkit_selection') == selected and value.get('images', {}).get('buildkit') == selected['reference'], 'qualification image identity changed'
    assert value.get('cleanup', {}).get('status') == 'passed', 'qualification cleanup did not pass'
    builders = [value.get(name, {}) for name in ('cold_builder', 'restored_builder')]
    for builder, suffix in zip(builders, ('cold', 'restored')):
        assert builder.get('image') == selected['reference'] and builder.get('architecture') == architecture, 'qualification builder image differs from selection'
        assert isinstance(builder.get('version'), str) and selected['version'] in builder['version'].split(), 'qualification builder version differs from selection'
        assert re.fullmatch(r'sha256:[a-f0-9]{64}', builder.get('image_id', '')), 'qualification builder lacks an observed image ID'
        assert builder.get('snapshotter') == 'overlayfs' and builder.get('managed_userxattr') is True and builder.get('force_overlay_diff') is force, 'qualification builder namespace differs from selection'
        assert re.fullmatch(r'hako-export-[a-f0-9]{10}-metadata-' + suffix, builder.get('name', '')), 'qualification builder name is unexpected'
    assert builders[0]['name'] != builders[1]['name'] and builders[0]['image_id'] == builders[1]['image_id'], 'qualification did not use two fresh builders of the same image'


def run_export_benchmark(fixture, integration=None, qualification=False):
    scenario = 'buildkit-qualification' if qualification else 'export-benchmark'
    key = 'buildkit_qualification' if qualification else 'export_benchmark'
    capture_key = 'qualification_log_capture' if qualification else 'export_log_capture'
    selected = export_buildkit_selection(os.environ.get('HAKOPOD_ACTIONS_EXPORT_BUILDKIT_IMAGE', ''))
    REPORT['qualification_buildkit_selection' if qualification else 'export_buildkit_selection'] = selected
    namespace = export_fixture(fixture)
    NAMESPACES.add(namespace)
    started = time.monotonic()
    apply(fixture)
    capture = BoundedLogCapture(namespace, scenario)
    failure = None
    try:
        prepare_shared_workspace(namespace, capture)
        if integration:
            wait_for(namespace, lambda value: any(item['name'] == 'runner' and item.get('state', {}).get('running')
                for item in value.get('status', {}).get('containerStatuses', [])), timeout=360, capture=capture)
            REPORT['native_test_staging'] = stage_export_integration(kube, namespace, integration)
        pod = wait_for(namespace, lambda value: value.get('status', {}).get('phase') in ('Succeeded', 'Failed'), timeout=900, capture=capture)
    except BaseException as error:
        failure = error
        raise
    finally:
        try:
            REPORT[capture_key] = capture.close()
            events, malformed = captured_records(scenario, 'HAKOPOD_EXPORT_BENCHMARK ')
            REPORT[capture_key]['malformed_events'] = malformed
            REPORT[key + '_events'] = events
            saved, malformed = captured_records(scenario, 'HAKOPOD_QUALIFICATION_REPORT ' if qualification else 'HAKOPOD_EXPORT_REPORT ')
            REPORT[capture_key]['malformed_reports'] = malformed
            # Persist any complete result before checking terminal status. A
            # failed wait must not discard source evidence already captured.
            if len(saved) == 1:
                REPORT[key] = saved[0]
                (OUTPUT / (scenario + '-report.json')).write_text(json.dumps(saved[0], indent=2) + '\n')
        except Exception as error:
            REPORT.setdefault(capture_key, {})['capture_error_type'] = type(error).__name__
            if failure is None:
                raise
    REPORT[key + '_pod'] = {
        'namespace': namespace, 'name': pod['metadata']['name'], 'node': pod['spec'].get('nodeName'),
        'phase': pod.get('status', {}).get('phase'), 'reason': pod.get('status', {}).get('reason'),
        'message': pod.get('status', {}).get('message'),
        'overhead': pod['spec'].get('overhead', {}), 'startup_timings': startup_timings(pod),
        'containers': [{'name': item['name'], 'resources': item['resources']} for item in pod['spec']['initContainers'] + pod['spec']['containers']]}
    outer = command(['docker', 'inspect', NODE, '--format', '[{{json .HostConfig.NanoCpus}},{{json .HostConfig.CpuQuota}},{{json .HostConfig.CpuPeriod}},{{json .HostConfig.Memory}},{{json .HostConfig.CpusetCpus}}]']).stdout
    limits = json.loads(outer)
    assert len(limits) == 5, 'outer worker limits could not be observed'
    REPORT[key + '_outer_worker'] = dict(zip(['nano_cpus', 'cpu_quota', 'cpu_period', 'memory_bytes', 'cpuset_cpus'], limits))
    assert len(saved) == 1, 'benchmark did not preserve exactly one JSON report'
    verify_log_capture(REPORT[capture_key])
    assert pod['spec'].get('nodeName') == NODE, 'benchmark did not run on the named development node'
    assert pod['status']['phase'] == 'Succeeded' and saved[0].get('status') == 'passed', 'export benchmark failed; see retained JSON and log'
    assert saved[0].get('context') == 'k3d-hakopod-dev' and saved[0].get('snapshotter') == 'overlayfs', 'benchmark used an unexpected context or snapshotter'
    assert saved[0].get('buildkit_selection') == selected and saved[0].get('images', {}).get('buildkit') == selected['reference'], 'benchmark image identity differs from the explicit selection'
    assert saved[0].get('force_overlay_diff') == (os.environ.get('HAKOPOD_ACTIONS_EXPORT_FORCE_OVERLAY_DIFF') == '1'), 'forced-diff diagnostic did not match the explicit selection'
    if qualification:
        architecture = {'x86_64': 'amd64', 'aarch64': 'arm64', 'amd64': 'amd64', 'arm64': 'arm64'}.get(platform.machine())
        verify_qualification_report(saved[0], selected, architecture, os.environ.get('HAKOPOD_ACTIONS_EXPORT_FORCE_OVERLAY_DIFF') == '1')
        completed, malformed = captured_records(scenario, 'HAKOPOD_BUILDKIT_QUALIFICATION ')
        assert not malformed and completed == saved, 'qualification completion differs from its saved report'
    else:
        assert any(event.get('phase') == 'complete' and event.get('status') == 'passed' for event in events), 'benchmark completion was not observed'
        assert len(saved[0].get('variants', [])) == 2 and saved[0].get('comparison'), 'benchmark comparison is incomplete'
    if integration:
        assert saved[0].get('kernel_integration', {}).get('status') == 'passed', 'native kernel verification did not pass'
    record('opt-in-' + scenario, started, report=scenario + '-report.json')
    delete_pod(namespace, credential_config=False)


def main():
    global RUNTIME_CREATED, OUTPUT_CREATED, SHARED_WORKSPACE
    if os.environ.get('GITHUB_ACTIONS') != 'true':
        raise SystemExit('This fixture only runs in an isolated GitHub Actions job')
    runtime_selection = runtime_helpers().runtime_selection_from_environment(os.environ)
    assert not (EXPORT_BENCHMARK and BUILDKIT_QUALIFICATION), 'select one diagnostic workload per disposable Pod'
    if os.environ.get('HAKOPOD_ACTIONS_EXPORT_BUILDKIT_IMAGE'):
        assert EXPORT_BENCHMARK or BUILDKIT_QUALIFICATION, 'select a diagnostic when selecting a BuildKit image'
        export_buildkit_selection(os.environ['HAKOPOD_ACTIONS_EXPORT_BUILDKIT_IMAGE'])
    integration = None
    if os.environ.get('HAKOPOD_ACTIONS_EXPORT_INTEGRATION_TESTS') == '1':
        assert EXPORT_BENCHMARK and not BUILDKIT_QUALIFICATION, 'native tests require the explicit export benchmark'
        architecture = {'x86_64': 'amd64', 'aarch64': 'arm64'}.get(platform.machine())
        integration = export_integration_bundle(os.environ['HAKOPOD_ACTIONS_EXPORT_TESTS_DIR'],
            os.environ['HAKOPOD_ACTIONS_EXPORT_BUILDKIT_IMAGE'], architecture, os.environ.get('HAKOPOD_ACTIONS_EXPORT_TEST_RUN') or None)
    label = command(['docker', 'inspect', '--format', '{{index .Config.Labels "k3d.cluster"}}', NODE]).stdout.strip()
    assert label == 'hakopod-dev', 'unexpected development container'
    shared = shared_workspace_helpers()
    if shared.enabled(os.environ):
        REPORT['shared_workspace'] = {'scope': 'development-only disk-backed shared filesystem experiment',
            'coverage_limits': ['Docker archive capability checks are not BuildKit import/export or registry-cache qualification.',
                                'Disk eviction and replacement are checked only by the full runtime scenario.']}
        REPORT['shared_workspace']['docker_storage_driver'] = shared.storage_driver(os.environ)
        SHARED_WORKSPACE = shared.Observer(command, kube, NODE, REPORT['shared_workspace'], driver=shared.storage_driver(os.environ))
    OUTPUT.mkdir(parents=True, exist_ok=False)
    OUTPUT_CREATED = True
    if SHARED_WORKSPACE:
        SHARED_WORKSPACE.verify_config()
    fixture_test = ('^TestActionsServicePullBenchmarkFixture$' if SERVICE_PULL_BENCHMARK else
                    '^TestActionsBuildkitQualificationFixture$' if BUILDKIT_QUALIFICATION else
                    '^TestActionsExportBenchmarkFixture$' if EXPORT_BENCHMARK else '^TestActionsRuntimeAcceptanceFixtures$')
    command(['go', 'test', '-p=1', './internal/cluster', '-run', fixture_test, '-count=1', '-timeout=60s'],
            env={**os.environ, 'HAKOPOD_ACTIONS_RUNTIME_FIXTURE_DIR': str(OUTPUT)}, cwd=ROOT, timeout=240)
    fixture_names = (['service-pull-benchmark'] if SERVICE_PULL_BENCHMARK else ['buildkit-qualification'] if BUILDKIT_QUALIFICATION else
                     ['export-benchmark'] if EXPORT_BENCHMARK else ['workload', 'disk', 'replacement'])
    fixtures = {name: json.loads((OUTPUT / f'{name}.json').read_text()) for name in fixture_names}
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
    if SERVICE_PULL_BENCHMARK:
        run_service_pull_benchmark(fixtures[fixture_names[0]])
        REPORT['status'] = 'passed'
        return
    if EXPORT_BENCHMARK or BUILDKIT_QUALIFICATION:
        run_export_benchmark(fixtures[fixture_names[0]], integration, BUILDKIT_QUALIFICATION)
        REPORT['status'] = 'passed'
        return
    REPORT['scenario'] = 'candidate-full-runtime' if runtime_selection['kind'] == 'candidate' else 'normal-runtime'
    REPORT['runtime_buildkit_selection'] = runtime_selection
    workload_ns = next(item['metadata']['namespace'] for item in fixtures['workload']['items'] if item['kind'] == 'Pod')
    disk_ns = next(item['metadata']['namespace'] for item in fixtures['disk']['items'] if item['kind'] == 'Pod')
    NAMESPACES.add(workload_ns)
    started = time.monotonic()
    apply(fixtures['workload'])
    prepare_shared_workspace(workload_ns)
    startup = wait_for(workload_ns, lambda pod: any(event['phase'] == 'sandbox-boundary' for event in logs(workload_ns, 'workload')), timeout=360)
    assert startup['spec'].get('nodeName') == NODE, 'runner did not use the explicitly selected node'
    record('runner-and-docker-ready', started, includes_pinned_image_pulls=True, pod_timings=startup_timings(startup))
    boundary_checks(workload_ns)
    started = time.monotonic()
    pod = wait_for(workload_ns, lambda pod: pod.get('status', {}).get('phase') in ('Succeeded', 'Failed'), timeout=1200)
    REPORT['workload_events'] = logs(workload_ns, 'workload')
    assert pod['status']['phase'] == 'Succeeded', pod['status']
    assert any(event['phase'] == 'complete' for event in REPORT['workload_events']), 'missing workload completion'
    REPORT['runtime_builder'] = verify_runtime_report(REPORT['workload_events'], runtime_selection,
        {'x86_64': 'amd64', 'aarch64': 'arm64'}.get(platform.machine()))
    record('compiled-multiarch-workload', started)
    started = time.monotonic()
    delete_pod(workload_ns)
    image_observation('absent')
    record('deleted-pod-image-observation', started)

    started = time.monotonic()
    NAMESPACES.add(disk_ns)
    apply(fixtures['disk'])
    prepare_shared_workspace(disk_ns, full=False, disk=True)
    disk_pod = wait_for(disk_ns, lambda pod: pod.get('status', {}).get('phase') == 'Failed', timeout=300)
    REPORT['disk_events'] = logs(disk_ns, 'disk')
    status = disk_pod['status']
    assert status.get('reason') == 'Evicted' and ('emptydir' in status.get('message', '').lower() or 'ephemeral-storage' in status.get('message', '').lower()), status
    if SHARED_WORKSPACE:
        SHARED_WORKSPACE.verify_eviction(disk_pod['metadata']['uid'], status)
    record('bounded-workspace-eviction', started, reason=status['reason'], message=status.get('message'))
    old_uid = disk_pod['metadata']['uid']
    delete_pod(disk_ns)
    started = time.monotonic()
    apply(fixtures['replacement'])
    prepare_shared_workspace(disk_ns, full=False)
    replacement = wait_for(disk_ns, lambda pod: pod.get('status', {}).get('phase') in ('Succeeded', 'Failed'), timeout=240)
    REPORT['replacement_events'] = logs(disk_ns, 'replacement')
    assert replacement['status']['phase'] == 'Succeeded', replacement['status']
    assert replacement['metadata']['uid'] != old_uid
    record('replacement-after-disk-eviction', started, previous_uid=old_uid, replacement_uid=replacement['metadata']['uid'])
    if SHARED_WORKSPACE:
        delete_pod(disk_ns)
        REPORT['shared_workspace']['eviction_and_clean_replacement_verified'] = True
    workload_samples = REPORT['samples'].get(f'{workload_ns}/actions-runtime-fixture', {})
    assert workload_samples.get('count', 0) > 0 and workload_samples.get('memory_working_set_bytes') is not None, 'kubelet did not provide runner resource measurements'
    REPORT['status'] = 'passed'


if __name__ == "__main__":
    try:
        main()
    except BaseException as error:
        REPORT['status'] = 'failed'
        REPORT['error'] = str(error)
        raise
    finally:
        if OUTPUT_CREATED:
            if SHARED_WORKSPACE:
                try:
                    SHARED_WORKSPACE.close()
                except Exception as error:
                    REPORT['status'] = 'failed'
                    REPORT.setdefault('cleanup_errors', []).append(str(error))
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
                    if SHARED_WORKSPACE:
                        def cleanup_workspace(args, **kwargs):
                            return subprocess.run(args, text=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE, **kwargs)
                        for uid, identity in SHARED_WORKSPACE.identities.items():
                            if identity['namespace'] == namespace and uid not in SHARED_WORKSPACE.removed_uids:
                                SHARED_WORKSPACE.removed(uid, command=cleanup_workspace)
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
