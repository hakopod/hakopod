#!/usr/bin/env python3
"""Credential-free workload injected into the real managed-runner pod by CI."""
from concurrent.futures import ThreadPoolExecutor
import hashlib
import importlib.util
import json
import math
import os
from pathlib import Path
import platform
import re
import socket
import subprocess
import sys
import tempfile
import time
import urllib.request

BUSYBOX = 'docker.io/library/busybox:1.37.0@sha256:9db7b59979c38555a39def84a31fb98b5296952f9e3afd4f6f11f05b07adfab0'
BUILDKIT = 'docker.io/moby/buildkit:v0.33.0@sha256:6c2fa84a6b61ccd72899dde4239f8d5717f05f9a8ca6f3cad185fb1a95a94de3'
GOLANG = 'docker.io/library/golang:1.26.8-alpine@sha256:ce864e7223ac17b1775e6fd0b4c0db580c2eb50e7953a427916379e4b92a1628'
REGISTRY = 'docker.io/library/registry:3.0.0@sha256:6c5666b861f3505b116bb9aa9b25175e71210414bd010d92035ff64018f9457e'
ROOT = Path('/home/runner/_work/acceptance')
PREFIX = 'HAKOPOD_ACCEPTANCE '
UPSTREAM_REVISION = 'dddd5621af04ea57823085c93a063383f71d3173'
CANDIDATE_UPSTREAM_REVISION = '991535e0973488b6a429096d21fa13f81f2d89d8'


def benchmark_helpers():
    # The product fixture injects the exact, bounded helper source in memory.
    # Host-side selection and pure tests load the same checked-in implementation.
    global _BENCHMARK_HELPERS
    if '_BENCHMARK_HELPERS' not in globals():
        spec = importlib.util.spec_from_file_location('runtime_buildkit_helpers', Path(__file__).with_name('export-benchmark.py'))
        module = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(module)
        _BENCHMARK_HELPERS = vars(module)
    return _BENCHMARK_HELPERS


def require(condition, message):
    if not condition:
        raise RuntimeError(message)


def runtime_buildkit_selection(image='', force=False):
    require(isinstance(force, bool), 'Runtime forced-overlay selection must be a boolean')
    if image == '':
        require(not force, 'Runtime forced-overlay mode requires an explicit candidate')
        selection = {'kind': 'upstream', 'reference': BUILDKIT, 'version': 'v0.33.0',
                     'digest': BUILDKIT.split('@', 1)[1], 'upstream_revision': UPSTREAM_REVISION}
    else:
        selection = benchmark_helpers()['buildkit_selection'](image)
        require(selection['kind'] == 'candidate', 'Runtime image must be an explicit digest-pinned Hakopod candidate')
        selection['upstream_revision'] = CANDIDATE_UPSTREAM_REVISION
    return {**selection, 'snapshotter': 'overlayfs' if selection['kind'] == 'candidate' else 'native',
            'force_overlay_diff': force}


def runtime_selection_from_environment(environment):
    image = environment.get('HAKOPOD_ACTIONS_RUNTIME_BUILDKIT_IMAGE', '')
    force = environment.get('HAKOPOD_ACTIONS_RUNTIME_FORCE_OVERLAY_DIFF', '0')
    require(force in ('0', '1'), 'Runtime forced-overlay flag must be 0 or 1')
    candidate_build = environment.get('HAKOPOD_ACTIONS_BUILDKIT_CANDIDATE') == '1'
    shared = environment.get('HAKOPOD_ACTIONS_SHARED_WORKSPACE', '0')
    require(shared in ('0', '1'), 'Shared workspace flag must be 0 or 1')
    require(not (shared == '1' and (candidate_build or environment.get('HAKOPOD_ACTIONS_PUBLISH_CANDIDATE') == '1')),
            'Shared workspace experiments must run separately from candidate builds and publication')
    export = environment.get('HAKOPOD_ACTIONS_EXPORT_BENCHMARK') == '1'
    qualification = environment.get('HAKOPOD_ACTIONS_BUILDKIT_QUALIFICATION') == '1'
    kernel = environment.get('HAKOPOD_ACTIONS_EXPORT_INTEGRATION_TESTS') == '1' or bool(environment.get('HAKOPOD_ACTIONS_EXPORT_TEST_RUN'))
    require(not (qualification and (candidate_build or export or kernel)), 'BuildKit qualification must run separately from other diagnostics')
    if image or force == '1':
        require(not (candidate_build or export or qualification or kernel or
                     environment.get('HAKOPOD_ACTIONS_PUBLISH_CANDIDATE') == '1' or
                     environment.get('HAKOPOD_ACTIONS_EXPORT_FORCE_OVERLAY_DIFF') == '1' or
                     environment.get('HAKOPOD_ACTIONS_EXPORT_BUILDKIT_IMAGE')),
                'Runtime candidate acceptance must run separately from candidate builds and export-only diagnostics')
    return runtime_buildkit_selection(image, force == '1')


def runtime_builder_identity(selection, container, image, version, architecture, workers):
    require(architecture in ('amd64', 'arm64'), 'Runtime host architecture is unsupported')
    identity = benchmark_helpers()['builder_image_identity'](selection, container, image, version, architecture)
    require(version.split() == ['buildkitd', 'github.com/moby/buildkit', selection['version'], selection['upstream_revision']],
            'Builder source revision differs from the selected runtime image')
    require(isinstance(workers, str) and len(workers) <= 16384, 'Unexpected builder worker output')
    snapshotters = re.findall(r'org\.mobyproject\.buildkit\.worker\.snapshotter:\s*(\S+)', workers)
    require(snapshotters == [selection['snapshotter']], 'Builder snapshotter differs from the selected runtime mode')
    forced = [value for value in container['Config']['Env'] if value.startswith('BUILDKIT_DEBUG_FORCE_OVERLAY_DIFF=')]
    require(forced == (['BUILDKIT_DEBUG_FORCE_OVERLAY_DIFF=true'] if selection['force_overlay_diff'] else []),
            'Builder forced-overlay setting differs from the selected runtime mode')
    return {**identity, 'snapshotter': snapshotters[0], 'force_overlay_diff': selection['force_overlay_diff'],
            'upstream_revision': selection['upstream_revision']}


def compiler_metrics(stream, architecture):
    result = []
    for vertex in benchmark_helpers()['progress_records'](stream):
        name = vertex.get('name', '')
        if not isinstance(name, str) or 'RUN go build' not in name:
            continue
        for arch in ('amd64', 'arm64'):
            if f'linux/{arch}' not in name:
                continue
            observed = {'architecture': arch, 'cached': bool(vertex.get('cached')), 'emulated': arch != architecture}
            duration = benchmark_helpers()['duration'](vertex)
            if duration is not None:
                observed['duration_seconds'] = duration
            result.append(observed)
    return result


def verify_compiler_metrics(vertices, architecture, *, cold):
    require(isinstance(vertices, list) and len(vertices) == 2 and
            {vertex.get('architecture') for vertex in vertices} == {'amd64', 'arm64'},
            'Missing native or emulated compiler evidence')
    for vertex in vertices:
        require(vertex.get('emulated') is (vertex['architecture'] != architecture), 'Compiler architecture evidence changed')
        if cold:
            duration = vertex.get('duration_seconds')
            require(vertex.get('cached') is False and isinstance(duration, (int, float)) and not isinstance(duration, bool) and
                    math.isfinite(duration) and duration > 0, 'Cold compiler must execute on both target architectures')
        else:
            require(vertex.get('cached') is True, 'Warm build did not reuse both compiler results')


def run(args, timeout=180, check=True, build_metrics=None, **kwargs):
    # Large tool output stays on the bounded workspace. Emit only the last 16 KiB
    # on failure; no runner/provider credentials are accepted by this fixture.
    with tempfile.TemporaryFile(dir=ROOT) as output:
        process = subprocess.run(args, stdout=output, stderr=subprocess.STDOUT,
                                 timeout=timeout, **kwargs)
        size = output.tell()
        if build_metrics is not None:
            output.seek(0)
            build_metrics.extend(compiler_metrics(output, {'x86_64': 'amd64', 'aarch64': 'arm64'}.get(platform.machine())))
        output.seek(max(0, size - 16384))
        result = output.read().decode('utf-8', errors='replace')
        if check and process.returncode != 0:
            raise RuntimeError(f'{args[:3]} exited {process.returncode}: {result}')
        return process.returncode, result.strip()


def phase(name, action):
    started = time.monotonic()
    print(f'::group::{name}', flush=True)
    result = action()
    print(PREFIX + json.dumps({'phase': name, 'duration_seconds': round(time.monotonic() - started, 3),
                             'status': 'passed', 'details': result}), flush=True)
    print('::endgroup::', flush=True)
    return result


def wait_http(url):
    for _ in range(30):
        try:
            with urllib.request.urlopen(url, timeout=2) as response:
                return response.read(1024).decode().strip()
        except OSError:
            time.sleep(1)
    raise RuntimeError(f'HTTP endpoint did not become ready: {url}')


def boundary():
    assert not Path('/var/run/secrets/kubernetes.io/serviceaccount/token').exists()
    assert not Path('/var/run/docker.sock').exists()
    assert not Path('/proc/sys/fs/binfmt_misc/register').exists()
    assert os.environ.get('DOCKER_HOST') == 'tcp://127.0.0.1:2375'
    _, root = run(['docker', 'info', '--format', '{{.DockerRootDir}}'])
    assert root == '/home/runner/.docker-data', root
    _, address = run(['hostname', '-i'])
    for candidate in address.split():
        try:
            connection = socket.create_connection((candidate, 2375), timeout=2)
        except OSError:
            continue
        connection.close()
        raise RuntimeError('Docker API is reachable through the pod address')
    # A nested privileged container receives the sandbox root, not a node mount.
    run(['docker', 'run', '--rm', '--privileged', '-v', '/:/sandbox', BUSYBOX, 'sh', '-c',
         'test ! -e /sandbox/var/lib/rancher/k3s/server/token && '
         'test ! -e /sandbox/var/run/secrets/kubernetes.io/serviceaccount/token && '
         'test ! -e /proc/sys/fs/binfmt_misc/register'])
    return {'host_architecture': platform.machine(), 'docker_data_root': root}


def containers():
    (ROOT / 'input').write_text('workspace-isolated\n')
    _, output = run(['docker', 'run', '--rm', '-v', f'{ROOT}:/work', BUSYBOX, 'cat', '/work/input'])
    assert output == 'workspace-isolated', output
    run(['docker', 'network', 'create', 'actions-job'])
    try:
        run(['docker', 'run', '-d', '--name', 'service', '--network', 'actions-job',
             '--network-alias', 'database', '-p', '127.0.0.1:18081:8080', BUSYBOX, 'sh', '-c',
             'mkdir -p /www; echo service-ready >/www/index.html; exec httpd -f -p 8080 -h /www'])
        assert wait_http('http://127.0.0.1:18081/') == 'service-ready'
        _, result = run(['docker', 'run', '--rm', '--network', 'actions-job', BUSYBOX,
                         'wget', '-T', '5', '-qO-', 'http://database:8080/'])
        assert result == 'service-ready', result
    finally:
        run(['docker', 'rm', '-f', 'service'], check=False)
        run(['docker', 'network', 'rm', 'actions-job'], check=False)
    return {'workspace_bind_mount': True, 'service_dns': True, 'loopback_port': True}


def builder_command(selection, config):
    command = ['docker', 'buildx', 'create', '--name', 'actions-multiarch', '--driver', 'docker-container',
               '--driver-opt', 'image=' + selection['reference'], '--driver-opt', 'network=host',
               '--buildkitd-config', str(config), '--use']
    if selection['force_overlay_diff']:
        command += ['--driver-opt', 'env.BUILDKIT_DEBUG_FORCE_OVERLAY_DIFF=true']
    return command


def compilation(selection):
    timings = {}
    architecture = {'x86_64': 'amd64', 'aarch64': 'arm64'}.get(platform.machine())
    source = ROOT / 'source'
    source.mkdir()
    # The compiler itself and the resulting binary run on each target architecture
    # inside BuildKit. GOARCH cross-compilation would not test emulator execution.
    (source / 'main.go').write_text('''package main
import ("crypto/sha256"; "fmt"; "runtime")
func main() {
    data := make([]byte, 65536)
    for i := range data { data[i] = byte(i % 251) }
    sum := sha256.Sum256(data)
    for i := 0; i < 100; i++ { sum = sha256.Sum256(sum[:]) }
    fmt.Printf("%s %x\\n", runtime.GOARCH, sum)
}
''')
    (source / 'Dockerfile').write_text(f'''FROM {GOLANG} AS compile
ENV GOMAXPROCS=2 CGO_ENABLED=0
WORKDIR /src
COPY main.go .
RUN go build -p=1 -trimpath -o /app main.go && /app > /proof
FROM {BUSYBOX}
COPY --from=compile /app /app
COPY --from=compile /proof /proof
CMD ["/app"]
''')
    config = ROOT / 'buildkitd.toml'
    config.write_text(f'''[worker.oci]
  max-parallelism = 2
  snapshotter = "{selection['snapshotter']}"
[worker.containerd]
  enabled = false
[registry."127.0.0.1:15000"]
  http = true
''')
    started = time.monotonic()
    run(['docker', 'run', '-d', '--name', 'registry', '-p', '127.0.0.1:15000:5000',
         '-e', 'REGISTRY_STORAGE_DELETE_ENABLED=true', REGISTRY])
    wait_http('http://127.0.0.1:15000/v2/')
    run(builder_command(selection, config))
    run(['docker', 'buildx', 'inspect', 'actions-multiarch', '--bootstrap'])
    container = 'buildx_buildkit_actions-multiarch0'
    _, workers = run(['docker', 'exec', container, 'buildctl', 'debug', 'workers', '--verbose'])
    _, version = run(['docker', 'exec', container, 'buildkitd', '--version'])
    _, actual = run(['docker', 'inspect', container, '--format', '{{json .}}'])
    _, image = run(['docker', 'image', 'inspect', selection['reference'], '--format', '{{json .}}'])
    identity = runtime_builder_identity(selection, json.loads(actual), json.loads(image), version, architecture, workers)
    print(PREFIX + json.dumps({'phase': 'runtime-builder-verified', 'status': 'passed',
                              'details': {'selection': selection, 'builder': identity}}), flush=True)
    timings['registry_and_builder_startup_seconds'] = round(time.monotonic() - started, 3)
    reference = '127.0.0.1:15000/acceptance/compiled:proof'
    build_command = ['docker', 'buildx', 'build', '--builder', 'actions-multiarch', '--platform', 'linux/amd64,linux/arm64',
                     '--progress=rawjson', '--provenance=false', '--push', '-t', reference, str(source)]
    cold_metrics = []
    started = time.monotonic()
    run(build_command, timeout=840, build_metrics=cold_metrics)
    timings['cold_multiarch_build_and_push_seconds'] = round(time.monotonic() - started, 3)
    timings['cold_compiler_vertices'] = cold_metrics
    verify_compiler_metrics(cold_metrics, architecture, cold=True)
    warm_metrics = []
    started = time.monotonic()
    run(build_command, timeout=180, build_metrics=warm_metrics)
    timings['warm_multiarch_build_and_push_seconds'] = round(time.monotonic() - started, 3)
    timings['warm_compiler_vertices'] = warm_metrics
    verify_compiler_metrics(warm_metrics, architecture, cold=False)
    # Read the actual registry manifest, not build output text.
    request = urllib.request.Request('http://127.0.0.1:15000/v2/acceptance/compiled/manifests/proof',
                                     headers={'Accept': 'application/vnd.oci.image.index.v1+json, application/vnd.docker.distribution.manifest.list.v2+json'})
    with urllib.request.urlopen(request, timeout=10) as response:
        digest = response.headers['Docker-Content-Digest']
        manifest = json.load(response)
    arches = sorted(entry['platform']['architecture'] for entry in manifest['manifests'])
    assert arches == ['amd64', 'arm64'], arches
    for arch in arches:
        run(['docker', 'pull', '--platform', f'linux/{arch}', reference])
        _, observed = run(['docker', 'image', 'inspect', '--format', '{{.Architecture}}', reference])
        assert observed == arch, observed
    verify = ROOT / 'verify'
    verify.mkdir()
    (verify / 'Dockerfile').write_text(f'''FROM 127.0.0.1:15000/acceptance/compiled@{digest} AS verify
ARG ACCEPTANCE_RUN
RUN /app > /actual && cmp /actual /proof && echo "$ACCEPTANCE_RUN" > /run
FROM scratch
COPY --from=verify /actual /actual
COPY --from=verify /run /run
''')
    expected = hashlib.sha256(bytes(i % 251 for i in range(65536))).digest()
    for _ in range(100):
        expected = hashlib.sha256(expected).digest()

    # Go supports native cross-compilation. This is a separate fast workflow
    # path: keep the compiler native and emulate only the final target program.
    # Base images are warm here; compare compiler work, not cold download time.
    fast = ROOT / 'native-cross-compile'
    fast.mkdir()
    (fast / 'main.go').write_text((source / 'main.go').read_text())
    (fast / 'Dockerfile').write_text(f'''FROM --platform=$BUILDPLATFORM {GOLANG} AS compile
ARG TARGETARCH
ENV GOMAXPROCS=2 CGO_ENABLED=0
WORKDIR /src
COPY main.go .
RUN GOARCH="$TARGETARCH" go build -p=1 -trimpath -o /app main.go
FROM {BUSYBOX} AS verify
COPY --from=compile /app /app
RUN /app > /actual
FROM scratch
COPY --from=verify /actual /actual
''')
    started = time.monotonic()
    fast_result = ROOT / 'native-cross-compiled-result'
    run(['docker', 'buildx', 'build', '--builder', 'actions-multiarch', '--platform', 'linux/amd64,linux/arm64',
         '--output', f'type=local,dest={fast_result}', str(fast)], timeout=240)
    timings['native_cross_compile_and_target_execution_seconds'] = round(time.monotonic() - started, 3)
    timings['native_cross_compile_base_images_already_cached'] = True
    for arch in arches:
        assert (fast_result / f'linux_{arch}' / 'actual').read_text().strip() == f'{arch} {expected.hex()}'

    def verify_push_pull(number):
        started = time.monotonic()
        destination = ROOT / f'verified-{number}'
        run(['docker', 'buildx', 'build', '--builder', 'actions-multiarch', '--platform', 'linux/amd64,linux/arm64',
             '--build-arg', f'ACCEPTANCE_RUN={number}', '--output', f'type=local,dest={destination}', str(verify)], timeout=300)
        for arch in arches:
            assert (destination / f'linux_{arch}' / 'actual').read_text().strip() == f'{arch} {expected.hex()}'
            assert (destination / f'linux_{arch}' / 'run').read_text().strip() == str(number)
        return {'request': number, 'duration_seconds': round(time.monotonic() - started, 3)}

    started = time.monotonic()
    with ThreadPoolExecutor(max_workers=2) as executor:
        results = list(executor.map(verify_push_pull, [1, 2]))
    timings['two_concurrent_readback_builds_seconds'] = round(time.monotonic() - started, 3)
    # A failed build must return promptly and leave the same builder usable.
    failure = ROOT / 'failed'
    failure.mkdir()
    (failure / 'Dockerfile').write_text(f'FROM {BUSYBOX}\nRUN exit 37\n')
    status, _ = run(['docker', 'buildx', 'build', '--builder', 'actions-multiarch', str(failure)], timeout=90, check=False)
    assert status != 0, 'intentionally failing build succeeded'
    verify_push_pull(3)
    result = {'platforms': arches, 'manifest_digest': digest, 'parallel_build_requests': results,
              'buildkit_selection': selection, 'builder': identity, 'pullback_execution_platforms': arches,
              'native_cross_compile_platforms': arches, 'post_failure_pullback_verified': True,
              'buildkit_max_parallelism': 2, 'failed_build_recovered': True,
              'native_and_emulated_compiler': True, 'timings': timings}
    print(PREFIX + json.dumps({'phase': 'build-results-verified', 'status': 'passed', 'details': result}), flush=True)
    started = time.monotonic()
    # Deleting thousands of cached compiler files through the nested Docker API
    # can exceed Buildx's request deadline. Stop the builder promptly and leave
    # its private state for the ephemeral pod's whole-workspace cleanup.
    run(['docker', 'buildx', 'rm', '--keep-state', 'actions-multiarch'])
    run(['docker', 'rm', '-f', 'registry'])
    timings['ephemeral_builder_stop_seconds'] = round(time.monotonic() - started, 3)
    return result


def disk():
    # A disk-backed emptyDir is evicted after kubelet measures its size. Write a
    # bounded 2.25 GiB beyond the 2 GiB volume limit; never fill the node device.
    (ROOT / 'previous-job-marker').write_text('must not survive replacement')
    started = time.monotonic()
    with (ROOT / 'bounded-disk-pressure').open('wb') as output:
        block = bytes(1024 * 1024)
        for _ in range(2304):
            output.write(block)
        output.flush()
        os.fsync(output.fileno())
    print(PREFIX + json.dumps({'phase': 'disk-limit-written', 'status': 'awaiting-eviction',
                             'bytes_written': 2304 * 1024 * 1024,
                             'duration_seconds': round(time.monotonic() - started, 3)}), flush=True)
    time.sleep(180)
    raise RuntimeError('the product workspace limit did not evict the runner within 180 seconds')


def main():
    selection = runtime_selection_from_environment(os.environ)
    if sys.argv[1:] == ['--validate-selection']:
        print(json.dumps(selection))
        return
    require(not sys.argv[1:], 'Unexpected runtime fixture arguments')
    scenario = os.environ['ACCEPTANCE_SCENARIO']
    require(scenario in ('disk', 'replacement', 'workload'), 'Unexpected runtime scenario')
    ROOT.mkdir(parents=True, exist_ok=True)
    if scenario == 'disk':
        disk()
    elif scenario == 'replacement':
        assert not (ROOT / 'previous-job-marker').exists()
        assert not (ROOT / 'bounded-disk-pressure').exists()
        _, containers_left = run(['docker', 'ps', '-aq'])
        _, images_left = run(['docker', 'images', '-q'])
        assert not containers_left and not images_left
        phase('replacement-workspace-clean', boundary)
    else:
        # The harness probes this known-live endpoint from a different namespace.
        endpoint = subprocess.Popen(['python3', '-m', 'http.server', '18082', '--bind', '0.0.0.0', '--directory', str(ROOT)],
                                    stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        try:
            wait_http('http://127.0.0.1:18082/')
            phase('sandbox-boundary', boundary)
            # The harness tests network isolation, image observation and sidecar
            # recovery while the workload waits at a deterministic checkpoint.
            for _ in range(300):
                if (ROOT / 'continue').exists():
                    break
                time.sleep(1)
            else:
                raise RuntimeError('runtime harness did not release the workload checkpoint')
            phase('containers-and-services', containers)
            phase('compiled-multiarch-registry-concurrency', lambda: compilation(selection))
        finally:
            endpoint.terminate()
            endpoint.wait(timeout=10)
    print(PREFIX + json.dumps({'phase': 'complete', 'status': 'passed'}), flush=True)


if __name__ == '__main__':
    main()
