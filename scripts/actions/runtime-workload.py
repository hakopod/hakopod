#!/usr/bin/env python3
"""Credential-free workload injected into the real managed-runner pod by CI."""
from concurrent.futures import ThreadPoolExecutor
from datetime import datetime
import hashlib
import json
import os
from pathlib import Path
import platform
import socket
import subprocess
import tempfile
import time
import urllib.request

BUSYBOX = 'docker.io/library/busybox:1.37.0@sha256:9db7b59979c38555a39def84a31fb98b5296952f9e3afd4f6f11f05b07adfab0'
BUILDKIT = 'docker.io/moby/buildkit:v0.33.0@sha256:6c2fa84a6b61ccd72899dde4239f8d5717f05f9a8ca6f3cad185fb1a95a94de3'
GOLANG = 'docker.io/library/golang:1.26.8-alpine@sha256:ce864e7223ac17b1775e6fd0b4c0db580c2eb50e7953a427916379e4b92a1628'
REGISTRY = 'docker.io/library/registry:3.0.0@sha256:6c5666b861f3505b116bb9aa9b25175e71210414bd010d92035ff64018f9457e'
ROOT = Path('/home/runner/_work/acceptance')
ROOT.mkdir(parents=True, exist_ok=True)
PREFIX = 'HAKOPOD_ACCEPTANCE '


def run(args, timeout=180, check=True, build_metrics=None, **kwargs):
    # Large tool output stays on the bounded workspace. Emit only the last 16 KiB
    # on failure; no runner/provider credentials are accepted by this fixture.
    with tempfile.TemporaryFile(dir=ROOT) as output:
        process = subprocess.run(args, stdout=output, stderr=subprocess.STDOUT,
                                 timeout=timeout, **kwargs)
        size = output.tell()
        if build_metrics is not None:
            output.seek(0)
            vertices = {}
            for raw in output:
                try:
                    event = json.loads(raw)
                except (ValueError, UnicodeDecodeError):
                    continue
                if not isinstance(event, dict):
                    continue
                for update in event.get('vertexes', []):
                    if isinstance(update, dict) and update.get('digest'):
                        vertices.setdefault(update['digest'], {}).update(update)
            for vertex in vertices.values():
                name = vertex.get('name', '')
                if 'RUN go build' not in name:
                    continue
                for arch in ['amd64', 'arm64']:
                    if f'linux/{arch}' not in name:
                        continue
                    observed = {'architecture': arch, 'cached': bool(vertex.get('cached')),
                                'emulated': arch != {'x86_64': 'amd64', 'aarch64': 'arm64'}.get(platform.machine())}
                    if vertex.get('started') and vertex.get('completed'):
                        observed['duration_seconds'] = round((datetime.fromisoformat(vertex['completed'].replace('Z', '+00:00')) -
                                                              datetime.fromisoformat(vertex['started'].replace('Z', '+00:00'))).total_seconds(), 3)
                    build_metrics.append(observed)
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


def compilation():
    timings = {}
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
    config.write_text('''[worker.oci]
  max-parallelism = 2
  snapshotter = "native"
[registry."127.0.0.1:15000"]
  http = true
''')
    started = time.monotonic()
    run(['docker', 'run', '-d', '--name', 'registry', '-p', '127.0.0.1:15000:5000',
         '-e', 'REGISTRY_STORAGE_DELETE_ENABLED=true', REGISTRY])
    wait_http('http://127.0.0.1:15000/v2/')
    run(['docker', 'buildx', 'create', '--name', 'actions-multiarch', '--driver', 'docker-container',
         '--driver-opt', f'image={BUILDKIT}', '--driver-opt', 'network=host',
         '--buildkitd-config', str(config), '--use'])
    run(['docker', 'buildx', 'inspect', 'actions-multiarch', '--bootstrap'])
    timings['registry_and_builder_startup_seconds'] = round(time.monotonic() - started, 3)
    reference = '127.0.0.1:15000/acceptance/compiled:proof'
    build_command = ['docker', 'buildx', 'build', '--builder', 'actions-multiarch', '--platform', 'linux/amd64,linux/arm64',
                     '--progress=rawjson', '--provenance=false', '--push', '-t', reference, str(source)]
    cold_metrics = []
    started = time.monotonic()
    run(build_command, timeout=840, build_metrics=cold_metrics)
    timings['cold_multiarch_build_and_push_seconds'] = round(time.monotonic() - started, 3)
    timings['cold_compiler_vertices'] = cold_metrics
    assert {vertex['architecture'] for vertex in cold_metrics if 'duration_seconds' in vertex} == {'amd64', 'arm64'}, 'missing compiler timings from BuildKit'
    warm_metrics = []
    started = time.monotonic()
    run(build_command, timeout=180, build_metrics=warm_metrics)
    timings['warm_multiarch_build_and_push_seconds'] = round(time.monotonic() - started, 3)
    timings['warm_compiler_vertices'] = warm_metrics
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


scenario = os.environ['ACCEPTANCE_SCENARIO']
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
        phase('compiled-multiarch-registry-concurrency', compilation)
    finally:
        endpoint.terminate()
        endpoint.wait(timeout=10)
print(PREFIX + json.dumps({'phase': 'complete', 'status': 'passed'}), flush=True)
