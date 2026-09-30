#!/usr/bin/env python3
"""Credential-free image export diagnostic for a disposable product runner pod.

The outer harness must verify the named development cluster, create the product
runner pod with its normal CPU/memory/workspace limits, inject this file, and set
HAKOPOD_EXPORT_BENCHMARK_DEV_CONTEXT=k3d-hakopod-dev. Run with Python 3 inside the
runner, never on a host or against a host Docker socket. No provider credentials,
registry credentials, Kubernetes access, or privileged host mounts are used.

Two fresh BuildKit builders use overlayfs, the same pinned images, 4,096 small
files and one deterministic 64 MiB binary. Each exports a cold image, then a warm
image after changing only marker.txt. The first uses gzip's default level; the
second uses gzip level 1 with force-compression=false. A loopback registry supplies
the actual manifests and blobs. Both OCI layers and Docker pull-back filesystems
must agree on content, deletions, permissions, symlinks and hardlinks. Replacing a
directory checks opaque-directory semantics; either an opaque marker or explicit
child whiteouts is valid OCI encoding. Cache reuse and unchanged layer digests are
required. Timing ratios are emitted only after all correctness checks pass.

--force-overlay-diff is an explicit diagnostic: it sets the builder container's
BUILDKIT_DEBUG_FORCE_OVERLAY_DIFF=true. Failure is reported, with no snapshotter
fallback. It does not change product defaults or prove that this path is faster.
The default is the affected workflow's pinned stock BuildKit v0.32.2 image.
--buildkit-image explicitly selects a digest-pinned Hakopod v0.32.2 candidate;
the selected reference, actual container image ID and version are recorded.
It never changes product defaults or silently replaces the stock benchmark. Extended
attributes are not verified here, so these checks cannot qualify an overlay-diff
patch for production even when the recorded filesystem assertions pass.

The workload has a 520-second deadline and cleanup has a separate 45-second
deadline. Generated files and command output are bounded. Fixture storage is
sampled after builds: source, owned registry/builder volumes and a conservative
sum of pulled image sizes plus 128 MiB temporary-output reserve must stay below
2 GiB. Pinned infrastructure images and pre-existing pod data are excluded from
that fixture estimate. The outer pod's workspace limit remains the hard disk
boundary. Cleanup stops only resources created here and leaves their private
volumes to the disposable pod's whole-workspace cleanup, avoiding an unbounded
per-file cache deletion. Do not reuse this pod for another job.

Output: HAKOPOD_EXPORT_BENCHMARK JSON records and export-benchmark-*/report.json
under /home/runner/_work. A failed or timed-out measurement exits nonzero and
never emits a speed comparison. No local execution is needed to prepare this
fixture; its results must come from the real managed sandbox.
"""
import argparse
from datetime import datetime
import gzip
import hashlib
import json
import os
from pathlib import Path, PurePosixPath
import platform
import re
import shutil
import signal
import socket
import stat
import subprocess
import tarfile
import tempfile
import time
import urllib.request
import urllib.parse
import uuid

# Keep these immutable references aligned with runtime-workload.py. That module
# has executable top-level scenarios, so importing it would start another test.
BUSYBOX = 'docker.io/library/busybox:1.37.0@sha256:9db7b59979c38555a39def84a31fb98b5296952f9e3afd4f6f11f05b07adfab0'
BUILDKIT = 'docker.io/moby/buildkit:v0.32.2@sha256:28a898719c18a33f4e8000685287fa36fd0dd9560c6440227d3a732d79bb41d8'
REGISTRY = 'docker.io/library/registry:3.0.0@sha256:6c5666b861f3505b116bb9aa9b25175e71210414bd010d92035ff64018f9457e'
MIB = 1024 * 1024
MAX_FIXTURE_BYTES = 2 * 1024 * MIB
MAX_TAR_BYTES = 128 * MIB
PREFIX = 'HAKOPOD_EXPORT_BENCHMARK '
CONTEXT = 'k3d-hakopod-dev'
INTEGRATION_DIRECTORY = Path('/home/runner/_work/buildkit-integration')


def buildkit_selection(image=BUILDKIT):
    require(isinstance(image, str), 'BuildKit image must be an immutable allowed reference')
    if image == '' or image == BUILDKIT:
        return {'kind': 'upstream', 'reference': BUILDKIT, 'version': 'v0.32.2',
                'digest': BUILDKIT.split('@', 1)[1]}
    candidate = re.fullmatch(r'ghcr\.io/hakopod/buildkit:(v0\.32\.2-hakopod-([a-f0-9]{40}))@(sha256:[a-f0-9]{64})', image)
    require(candidate is not None, 'BuildKit image must be the stock pin or a digest-pinned Hakopod v0.32.2 candidate')
    return {'kind': 'candidate', 'reference': image, 'version': candidate[1],
            'hakopod_revision': candidate[2], 'digest': candidate[3]}


def builder_image_identity(selection, container, image, version, architecture):
    require(container.get('Config', {}).get('Image') == selection['reference'], 'Builder did not use the selected image reference')
    image_id = image.get('Id', '')
    require(re.fullmatch(r'sha256:[a-f0-9]{64}', image_id) and container.get('Image') == image_id,
            'Builder container image ID differs from the selected image')
    require(image.get('Os') == 'linux' and image.get('Architecture') == architecture, 'Builder image is not the native Linux architecture')
    require(isinstance(version, str) and len(version) <= 512 and selection['version'] in version.split(),
            'Builder version differs from the selected image')
    settings = container.get('Config', {}).get('Env', [])
    require(isinstance(settings, list) and all(isinstance(value, str) for value in settings), 'Unexpected builder environment')
    mode = [value for value in settings if value.startswith('HAKOPOD_BUILDKIT_USERXATTR=')]
    require(mode == (['HAKOPOD_BUILDKIT_USERXATTR=true'] if selection['kind'] == 'candidate' else []),
            'Builder overlay namespace differs from the selected image')
    return {'image': selection['reference'], 'image_id': image_id, 'version': version.strip(),
            'architecture': architecture, 'managed_userxattr': selection['kind'] == 'candidate'}


def wait_for_integration(selection, helper_sha256, architecture):
    require(selection['kind'] == 'candidate' and re.fullmatch(r'[a-f0-9]{64}', helper_sha256 or ''),
            'Native tests require a candidate and an exact helper checksum')
    root = INTEGRATION_DIRECTORY
    require(root.parent.resolve(strict=True) == root.parent, 'Integration workspace parent contains a symlink')
    root.mkdir(mode=0o700, exist_ok=False)
    deadline = time.monotonic() + 120
    while not (root / 'ready').exists():
        require(time.monotonic() < deadline, 'Native test artifact staging timed out')
        time.sleep(0.25)
    require(root.resolve(strict=True) == root and not root.is_symlink(), 'Integration workspace changed')
    values = {}
    for name, maximum in [('ready', 16), ('buildkit-integration.py', 128 * 1024)]:
        fd = os.open(root / name, os.O_RDONLY | os.O_NOFOLLOW)
        with os.fdopen(fd, 'rb') as source:
            require(stat.S_ISREG(os.fstat(source.fileno()).st_mode), 'Integration helper is not a regular file')
            values[name] = source.read(maximum + 1)
        require(0 < len(values[name]) <= maximum, 'Integration helper exceeds its bound')
    require(values['ready'] == b'ready\n' and hashlib.sha256(values['buildkit-integration.py']).hexdigest() == helper_sha256,
            'Native test helper identity mismatch')
    module = {'__name__': 'managed_buildkit_integration'}
    exec(compile(values['buildkit-integration.py'], 'buildkit-integration.py', 'exec'), module)
    bundle = module['load_bundle'](root, selection['reference'], architecture, staged=True)
    return {'module': module, 'bundle': bundle, 'helper_bytes': len(values['buildkit-integration.py']), 'helper_sha256': helper_sha256}


def benchmark_environment(source, private_home):
    allowed = {'PATH', 'TMPDIR', 'TMP', 'TEMP', 'LANG', 'LANGUAGE', 'LC_ALL', 'LC_CTYPE'}
    return {**{key: value for key, value in source.items() if key in allowed},
            'HOME': str(private_home), 'DOCKER_HOST': 'tcp://127.0.0.1:2375'}


def loopback_url(base, path):
    parsed = urllib.parse.urlsplit(base)
    require(parsed.scheme == 'http' and parsed.hostname == '127.0.0.1' and parsed.username is None and
            parsed.password is None and parsed.port is not None and parsed.path == '' and not parsed.query and
            not parsed.fragment and base == f'http://127.0.0.1:{parsed.port}', 'Registry base must be exact loopback HTTP')
    require(path == '/v2/' or re.fullmatch(r'/v2/export/gzip-(?:default|level1)/(?:manifests/(?:cold|warm)|blobs/sha256:[a-f0-9]{64})', path),
            'Unexpected registry request path')
    return base + path


class NoRegistryRedirects(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, request, response, code, message, headers, newurl):
        raise RuntimeError('Registry redirects are forbidden in this benchmark')


def registry_opener():
    return urllib.request.build_opener(urllib.request.ProxyHandler({}), NoRegistryRedirects())


def require(condition, message):
    if not condition:
        raise RuntimeError(message)


def emit(event):
    print(PREFIX + json.dumps(event, separators=(',', ':')), flush=True)


def elapsed(start):
    return round(time.monotonic() - start, 3)


def duration(record):
    if not record.get('started') or not record.get('completed'):
        return None
    return round((datetime.fromisoformat(record['completed'].replace('Z', '+00:00')) -
                  datetime.fromisoformat(record['started'].replace('Z', '+00:00'))).total_seconds(), 3)


def progress_records(stream):
    records = {}
    for line in stream:
        require(len(line) <= MIB, 'BuildKit progress line exceeded 1 MiB')
        try:
            event = json.loads(line)
        except (ValueError, UnicodeDecodeError):
            continue
        if not isinstance(event, dict):
            continue
        # Buildx emits individual vertex/status records. Older progress adapters
        # wrap the same records in vertexes/statuses arrays.
        updates = [event] + event.get('vertexes', []) + event.get('statuses', [])
        for update in updates:
            if not isinstance(update, dict):
                continue
            identity = update.get('digest') or update.get('id')
            if not identity:
                continue
            key = (update.get('vertex', ''), identity)
            record = records.setdefault(key, {})
            record.update(update)
            # BuildKit's OneOff statuses put "exporting layers" in ID and
            # omit Name. Only statuses with a parent vertex use that fallback;
            # an ordinary vertex's digest is not a human-readable phase.
            if not record.get('name') and record.get('vertex') and isinstance(record.get('id'), str):
                record['name'] = record['id']
            require(len(records) <= 4096, 'BuildKit progress record limit exceeded')
    return list(records.values())


class BoundedReader:
    def __init__(self, source, maximum):
        self.source, self.maximum, self.count = source, maximum, 0

    def read(self, size=-1):
        size = min(size if size >= 0 else self.maximum + 1, self.maximum - self.count + 1)
        value = self.source.read(size)
        self.count += len(value)
        require(self.count <= self.maximum, 'Uncompressed layer exceeded the 128 MiB limit')
        return value


def clean_path(name):
    require('\x00' not in name and '\\' not in name, 'Unexpected archive path')
    path = PurePosixPath(name.lstrip('/'))
    require('..' not in path.parts, 'Archive path escapes the image root')
    return str(path).removeprefix('./').rstrip('/')


def read_layer(stream):
    entries, removed, opaque = {}, [], []
    with tarfile.open(fileobj=stream, mode='r|') as archive:
        count = 0
        for member in archive:
            count += 1
            require(count <= 16384, 'Archive member limit exceeded')
            path = clean_path(member.name)
            if path != 'fixture' and not path.startswith('fixture/'):
                continue
            base = PurePosixPath(path).name
            parent = str(PurePosixPath(path).parent)
            if base == '.wh..wh..opq':
                opaque.append(parent)
                continue
            if base.startswith('.wh.'):
                removed.append(f'{parent}/{base[4:]}')
                continue
            require(path not in entries, f'Duplicate layer path: {path}')
            entry = {'mode': member.mode & 0o7777, 'uid': member.uid, 'gid': member.gid}
            if member.isdir():
                entry['type'] = 'directory'
            elif member.issym():
                entry.update(type='symlink', target=member.linkname)
            elif member.islnk():
                entry.update(type='hardlink', target=clean_path(member.linkname))
            elif member.isfile():
                require(member.size <= 64 * MIB, f'Unexpected fixture file size: {path}')
                source = archive.extractfile(member)
                require(source is not None, f'Missing archive file: {path}')
                digest, total = hashlib.sha256(), 0
                while chunk := source.read(MIB):
                    digest.update(chunk)
                    total += len(chunk)
                require(total == member.size, f'Truncated file: {path}')
                entry.update(type='file', size=total, sha256=digest.hexdigest())
            else:
                raise RuntimeError(f'Unexpected fixture archive member type: {path}')
            entries[path] = entry
    return {'entries': entries, 'removed': removed, 'opaque': opaque}


def apply_layer(rootfs, layer):
    # Whiteouts affect lower layers, never new entries in this layer.
    for target in layer['removed']:
        for path in list(rootfs):
            if path == target or path.startswith(target + '/'):
                del rootfs[path]
    for target in layer['opaque']:
        for path in list(rootfs):
            if path.startswith(target + '/'):
                del rootfs[path]
    rootfs.update(layer['entries'])


def normalized_files(rootfs):
    result = {}
    for path, entry in rootfs.items():
        value, seen = entry, {path}
        while value['type'] == 'hardlink':
            target = value['target']
            require(target not in seen and target in rootfs, f'Invalid hardlink at {path}')
            seen.add(target)
            value = rootfs[target]
        result[path] = value
    return result


def verify_files(rootfs, expected, context):
    observed = normalized_files(rootfs)
    if observed != expected:
        missing = sorted(expected.keys() - observed.keys())
        extra = sorted(observed.keys() - expected.keys())
        changed = sorted(path for path in observed.keys() & expected.keys() if observed[path] != expected[path])
        raise RuntimeError(f'{context} filesystem mismatch: missing={missing[:8]}, extra={extra[:8]}, '
                           f'changed={[(path, observed[path], expected[path]) for path in changed[:4]]}')


class Benchmark:
    def __init__(self, force, buildkit_image=BUILDKIT, integration=None):
        self.buildkit = buildkit_selection(buildkit_image)
        self.integration = integration
        self.started = time.monotonic()
        self.deadline = self.started + 520
        self.force = force
        self.architecture = {'aarch64': 'arm64', 'arm64': 'arm64', 'x86_64': 'amd64', 'amd64': 'amd64'}.get(platform.machine())
        require(self.architecture is not None, 'Benchmark requires native AMD64 or ARM64')
        self.name = 'hako-export-' + uuid.uuid4().hex[:10]
        self.root = Path('/home/runner/_work') / self.name
        self.root.mkdir(mode=0o700, parents=True, exist_ok=False)
        self.config = self.root / 'docker-config'
        self.config.mkdir(mode=0o700)
        private_home = self.root / 'home'
        private_home.mkdir(mode=0o700)
        self.env = benchmark_environment(os.environ, private_home)
        self.opener = registry_opener()
        self.builders, self.containers, self.volumes, self.images = [], [], set(), set()
        self.layers = {}
        self.report = {'schema_version': 1, 'status': 'running', 'context': CONTEXT,
                       'scope': 'disposable managed runner; diagnostic, not fleet performance evidence',
                       'architecture': self.architecture, 'snapshotter': 'overlayfs',
                       'force_overlay_diff': force, 'buildkit_selection': self.buildkit,
                       'images': {'buildkit': self.buildkit['reference'], 'busybox': BUSYBOX, 'registry': REGISTRY},
                       'limits': {'workload_seconds': 520, 'cleanup_seconds': 45, 'fixture_bytes': MAX_FIXTURE_BYTES,
                                  'command_output_bytes': 8 * MIB, 'temporary_archive_bytes': MAX_TAR_BYTES,
                                  'buildkit_max_parallelism': 1, 'cpu_memory': 'outer product runner pod limits'},
                       'coverage_limits': ['Pinned BuildKit v0.32.2; other versions require their own measurements.',
                                           'Extended attributes are not checked; this benchmark cannot qualify an overlay-diff patch.',
                                           'Sequential single-sandbox measurements are not fleet-scale evidence.'],
                       'phases': [], 'variants': [], 'fixture_storage_samples': []}
        if integration:
            self.report['integration_artifacts'] = {'source': integration['bundle']['source'],
                'files': integration['bundle']['files'], 'helper_sha256': integration['helper_sha256'],
                'staging_limit_seconds': 120}

    def run(self, args, timeout=120, maximum=8 * MIB, progress=False, archive=False):
        remaining = min(timeout, self.deadline - time.monotonic())
        require(remaining > 0, 'Export benchmark workload deadline exceeded')
        output = tempfile.TemporaryFile(dir=self.root)
        errors = tempfile.TemporaryFile(dir=self.root) if archive else None
        process = subprocess.Popen(['docker', '--config', str(self.config), *args], env=self.env,
                                   stdout=output, stderr=errors if archive else subprocess.STDOUT,
                                   start_new_session=True)
        end = time.monotonic() + remaining
        failure = None
        try:
            while process.poll() is None:
                if time.monotonic() >= end:
                    failure = f'Command exceeded its {round(remaining, 1)}-second budget'
                    break
                if output.tell() > maximum:
                    failure = f'Command output exceeded {maximum} bytes'
                    break
                if errors and errors.tell() > MIB:
                    failure = 'Command error output exceeded 1 MiB'
                    break
                time.sleep(0.1)
            if failure:
                os.killpg(process.pid, signal.SIGTERM)
                try:
                    process.wait(timeout=2)
                except subprocess.TimeoutExpired:
                    os.killpg(process.pid, signal.SIGKILL)
                    process.wait(timeout=2)
            size = output.tell()
            require(size <= maximum, f'Command output exceeded {maximum} bytes')
            output.seek(max(0, size - 16384))
            if errors:
                errors.seek(max(0, errors.tell() - 16384))
                tail = errors.read().decode(errors='replace')
            else:
                tail = output.read().decode(errors='replace')
            require(not failure and process.returncode == 0,
                    f'{args[:3]} failed: {failure or process.returncode}: {tail}')
            output.seek(0)
            if archive:
                return read_layer(output)
            return progress_records(output) if progress else tail.strip()
        finally:
            if process.poll() is None:
                os.killpg(process.pid, signal.SIGKILL)
                process.wait(timeout=2)
            output.close()
            if errors:
                errors.close()

    def phase(self, name, action):
        start = time.monotonic()
        print(f'::group::{name}', flush=True)
        try:
            result = action()
            event = {'phase': name, 'status': 'passed', 'duration_seconds': elapsed(start), 'details': result}
            self.report['phases'].append(event)
            emit(event)
            return result
        except BaseException as error:
            event = {'phase': name, 'status': 'failed', 'duration_seconds': elapsed(start), 'error': str(error)[-17000:]}
            self.report['phases'].append(event)
            emit(event)
            raise
        finally:
            print('::endgroup::', flush=True)

    def boundary(self):
        require(os.environ.get('HAKOPOD_EXPORT_BENCHMARK_DEV_CONTEXT') == CONTEXT, 'Outer development-context gate is missing')
        require(os.environ.get('DOCKER_HOST') == 'tcp://127.0.0.1:2375', 'Only the product loopback Docker API is allowed')
        require(not Path('/var/run/docker.sock').exists(), 'A host Docker socket must not be mounted')
        require(not Path('/var/run/secrets/kubernetes.io/serviceaccount/token').exists(), 'Runner must not receive Kubernetes credentials')
        require(self.run(['info', '--format', '{{.DockerRootDir}}']) == '/home/runner/.docker-data', 'Unexpected Docker data root')
        require(shutil.disk_usage(self.root).free >= MAX_FIXTURE_BYTES, 'At least 2 GiB of free runner workspace is required')
        return {'loopback_docker': True, 'credential_free_docker_config': True}

    def fixture(self):
        source = self.root / 'source'
        payload = source / 'payload'
        (payload / 'many').mkdir(parents=True)
        (payload / 'opaque').mkdir()
        (payload / 'opaque' / 'old-one').write_text('remove old one\n')
        (payload / 'opaque' / 'old-two').write_text('remove old two\n')
        (payload / 'deleted.txt').write_text('remove this file\n')
        (payload / 'hard-source.txt').write_text('same inode after pull\n')
        (payload / 'executable').write_text('#!/bin/sh\necho export-proof\n')
        for index in range(4096):
            (payload / 'many' / f'{index:04d}.dat').write_bytes(hashlib.shake_256(f'hakopod-small-{index}'.encode()).digest(1024))
        with (payload / 'binary.bin').open('wb') as output:
            for index in range(64):
                output.write(hashlib.shake_256(f'hakopod-binary-{index}'.encode()).digest(MIB))
        for path in [payload, *payload.rglob('*')]:
            path.chmod(0o755 if path.is_dir() else 0o644)
            os.utime(path, (1_700_000_000, 1_700_000_000))
        (source / 'Dockerfile').write_text(f'''FROM {BUSYBOX}
COPY payload/ /fixture/
RUN rm /fixture/deleted.txt && rm -rf /fixture/opaque && mkdir /fixture/opaque && printf 'replacement directory\\n' > /fixture/opaque/new && chmod 751 /fixture/executable && chmod 640 /fixture/hard-source.txt && ln /fixture/hard-source.txt /fixture/hard-link && ln -s hard-source.txt /fixture/symlink
COPY marker.txt /fixture/marker.txt
''')
        (source / 'marker.txt').write_text('cold\n')
        (source / 'marker.txt').chmod(0o644)
        expected = {}
        for path in [payload, *payload.rglob('*')]:
            key = 'fixture' + ('/' + path.relative_to(payload).as_posix() if path != payload else '')
            if key == 'fixture/deleted.txt' or key.startswith('fixture/opaque/'):
                continue
            entry = {'type': 'directory' if path.is_dir() else 'file', 'mode': 0o755 if path.is_dir() else 0o644, 'uid': 0, 'gid': 0}
            if path.is_file():
                with path.open('rb') as data:
                    entry.update(size=path.stat().st_size, sha256=hashlib.file_digest(data, 'sha256').hexdigest())
            expected[key] = entry
        expected['fixture/executable']['mode'] = 0o751
        expected['fixture/hard-source.txt']['mode'] = 0o640
        expected['fixture/hard-link'] = dict(expected['fixture/hard-source.txt'])
        expected['fixture/symlink'] = {'type': 'symlink', 'target': 'hard-source.txt', 'mode': 0o777, 'uid': 0, 'gid': 0}
        value = b'replacement directory\n'
        expected['fixture/opaque/new'] = {'type': 'file', 'mode': 0o644, 'uid': 0, 'gid': 0, 'size': len(value), 'sha256': hashlib.sha256(value).hexdigest()}
        self.expected, self.source = expected, source
        return {'small_files': 4096, 'small_file_bytes': 1024, 'binary_bytes': 64 * MIB,
                'binary_sha256': expected['fixture/binary.bin']['sha256'], 'only_warm_change': 'marker.txt: cold newline -> warm newline'}

    def registry(self):
        for image in [BUSYBOX, self.buildkit['reference'], REGISTRY]:
            self.run(['pull', image], timeout=120)
        with socket.socket() as probe:
            probe.bind(('127.0.0.1', 0))
            self.port = probe.getsockname()[1]
        name = self.name + '-registry'
        volume = name + '-data'
        self.containers.append(name)
        self.volumes.add(volume)
        self.run(['run', '-d', '--name', name, '-p', f'127.0.0.1:{self.port}:5000',
                  '-v', f'{volume}:/var/lib/registry', '-e', 'REGISTRY_LOG_LEVEL=error', REGISTRY])
        self.base = f'http://127.0.0.1:{self.port}'
        for _ in range(30):
            try:
                self.fetch('/v2/', MIB)
                return {'port': self.port, 'public_images_prefetched': True}
            except OSError:
                time.sleep(0.25)
        raise RuntimeError('Loopback registry did not become ready')

    def fetch(self, path, maximum):
        timeout = min(15, self.deadline - time.monotonic())
        require(timeout > 0, 'Registry request exceeded workload deadline')
        request = urllib.request.Request(loopback_url(self.base, path), headers={'Accept': 'application/vnd.oci.image.manifest.v1+json, application/vnd.docker.distribution.manifest.v2+json'})
        with self.opener.open(request, timeout=timeout) as response:
            value = response.read(maximum + 1)
            require(len(value) <= maximum, 'Registry response exceeded its bound')
            return value, response.headers

    def builder(self, variant):
        name = self.name + '-' + variant
        self.builders.append(name)
        config = self.root / (variant + '.toml')
        config.write_text(f'''[worker.oci]
  max-parallelism = 1
  snapshotter = "overlayfs"
[worker.containerd]
  enabled = false
[registry."127.0.0.1:{self.port}"]
  http = true
''')
        command = ['buildx', 'create', '--name', name, '--driver', 'docker-container',
                   '--driver-opt', 'image=' + self.buildkit['reference'], '--driver-opt', 'network=host', '--buildkitd-config', str(config)]
        if self.force:
            command += ['--driver-opt', 'env.BUILDKIT_DEBUG_FORCE_OVERLAY_DIFF=true']
        self.run(command)
        self.run(['buildx', 'inspect', name, '--bootstrap'], timeout=75)
        container = 'buildx_buildkit_' + name + '0'
        workers = self.run(['exec', container, 'buildctl', 'debug', 'workers', '--verbose'])
        require(re.search(r'org\.mobyproject\.buildkit\.worker\.snapshotter:\s*overlayfs\b', workers), 'BuildKit is not using overlayfs')
        version = self.run(['exec', container, 'buildkitd', '--version'])
        actual = json.loads(self.run(['inspect', container, '--format', '{{json .}}']))
        image = json.loads(self.run(['image', 'inspect', self.buildkit['reference'], '--format', '{{json .}}']))
        identity = builder_image_identity(self.buildkit, actual, image, version, self.architecture)
        settings = actual['Config']['Env']
        forced = [value for value in settings if value.startswith('BUILDKIT_DEBUG_FORCE_OVERLAY_DIFF=')]
        require(forced == (['BUILDKIT_DEBUG_FORCE_OVERLAY_DIFF=true'] if self.force else []), 'Unexpected forced-overlay diagnostic configuration')
        mounts = json.loads(self.run(['inspect', container, '--format', '{{json .Mounts}}']))
        for mount in mounts:
            if mount.get('Type') == 'volume':
                require(mount['Name'].startswith('buildx_buildkit_' + name), 'Builder mounted an unowned volume')
                self.volumes.add(mount['Name'])
        return {'name': name, **identity, 'snapshotter': 'overlayfs', 'force_overlay_diff': self.force}

    def build(self, builder, variant, temperature):
        (self.source / 'marker.txt').write_text(temperature + '\n')
        reference = f'127.0.0.1:{self.port}/export/{variant}:{temperature}'
        options = f'type=image,name={reference},push=true,oci-mediatypes=true,compression=gzip'
        if variant == 'gzip-level1':
            options += ',compression-level=1,force-compression=false'
        start = time.monotonic()
        records = self.run(['buildx', 'build', '--builder', builder, '--platform', f'linux/{self.architecture}', '--progress=rawjson', '--provenance=false',
                            '--output', options, str(self.source)], timeout=180, progress=True)
        timings = [{'name': record.get('name', ''), 'duration_seconds': duration(record), 'cached': bool(record.get('cached'))}
                   for record in records if record.get('name') and duration(record) is not None]
        require(len(timings) <= 128, 'Unexpected number of build timing phases')
        require(any('exporting' in item['name'] for item in timings), 'BuildKit did not report export phase timings')
        heavy = [record for record in records if 'COPY payload/' in record.get('name', '') or 'RUN rm /fixture/deleted.txt' in record.get('name', '')]
        require(len(heavy) == 2, 'Missing payload or filesystem mutation cache evidence')
        require(all(bool(item.get('cached')) == (temperature == 'warm') for item in heavy), 'Unexpected cold/warm payload cache state')
        return {'reference': reference, 'build_and_push_seconds': elapsed(start), 'phases': timings,
                'payload_and_mutation_cached': temperature == 'warm'}

    def verify_registry(self, variant, temperature):
        repository = f'export/{variant}'
        raw, headers = self.fetch(f'/v2/{repository}/manifests/{temperature}', MIB)
        digest = 'sha256:' + hashlib.sha256(raw).hexdigest()
        require(headers.get('Docker-Content-Digest') == digest, 'Registry manifest digest mismatch')
        manifest = json.loads(raw)
        require(manifest.get('schemaVersion') == 2 and isinstance(manifest.get('layers'), list), 'Expected one native image manifest')
        require(3 <= len(manifest['layers']) <= 8, 'Unexpected fixture layer count')
        rootfs, encodings, sizes = {}, [], []
        for descriptor in manifest['layers']:
            require(descriptor['mediaType'].endswith('tar+gzip') or descriptor['mediaType'].endswith('tar.gzip'), 'Expected gzip layers')
            size, layer_digest = descriptor['size'], descriptor['digest']
            require(0 < size <= MAX_TAR_BYTES, 'Compressed layer size exceeds fixture limit')
            sizes.append(size)
            if layer_digest not in self.layers:
                raw_layer, _ = self.fetch(f'/v2/{repository}/blobs/{layer_digest}', MAX_TAR_BYTES)
                require(len(raw_layer) == size and 'sha256:' + hashlib.sha256(raw_layer).hexdigest() == layer_digest, 'Registry layer digest/size mismatch')
                with tempfile.TemporaryFile(dir=self.root) as layer_file:
                    layer_file.write(raw_layer)
                    del raw_layer
                    layer_file.seek(0)
                    with gzip.GzipFile(fileobj=layer_file) as expanded:
                        self.layers[layer_digest] = read_layer(BoundedReader(expanded, MAX_TAR_BYTES))
            layer = self.layers[layer_digest]
            encodings.extend(layer['opaque'])
            apply_layer(rootfs, layer)
        require(sum(sizes) <= MAX_TAR_BYTES, 'Total compressed fixture layers exceed 128 MiB')
        config_raw, _ = self.fetch(f'/v2/{repository}/blobs/{manifest["config"]["digest"]}', MIB)
        require(len(config_raw) == manifest['config']['size'] and 'sha256:' + hashlib.sha256(config_raw).hexdigest() == manifest['config']['digest'], 'Image config digest/size mismatch')
        config = json.loads(config_raw)
        require(config.get('architecture') == self.architecture, 'Exported platform is not native')
        expected = dict(self.expected)
        marker = (temperature + '\n').encode()
        expected['fixture/marker.txt'] = {'type': 'file', 'mode': 0o644, 'uid': 0, 'gid': 0, 'size': len(marker), 'sha256': hashlib.sha256(marker).hexdigest()}
        verify_files(rootfs, expected, 'Registry rootfs')
        require(any('fixture/deleted.txt' in self.layers[item['digest']]['removed'] for item in manifest['layers']), 'Missing file whiteout evidence')
        require('fixture/opaque' in encodings or all(any(f'fixture/opaque/{old}' in self.layers[item['digest']]['removed'] for item in manifest['layers']) for old in ['old-one', 'old-two']), 'Missing opaque-directory replacement evidence')
        return {'manifest_digest': digest, 'manifest_bytes': len(raw), 'config_bytes': len(config_raw),
                'layer_bytes': sizes, 'compressed_layer_bytes': sum(sizes), 'layer_digests': [item['digest'] for item in manifest['layers']],
                'opaque_directory_encoding': 'opaque-marker' if 'fixture/opaque' in encodings else 'explicit-child-whiteouts',
                'expected_rootfs': expected}

    def pullback(self, variant, checked):
        reference = f'127.0.0.1:{self.port}/export/{variant}@{checked["manifest_digest"]}'
        self.run(['pull', reference], timeout=90)
        self.images.add(reference)
        container = self.name + '-readback-' + uuid.uuid4().hex[:6]
        self.containers.append(container)
        self.run(['create', '--name', container, '--network', 'none', reference, 'true'])
        layer = self.run(['export', container], maximum=MAX_TAR_BYTES, timeout=90, archive=True)
        verify_files(layer['entries'], checked['expected_rootfs'], 'Docker pull-back')
        self.run(['run', '--rm', '--network', 'none', '--read-only', '--cap-drop', 'ALL', reference, 'sh', '-ec',
                  'test "$(stat -c %i /fixture/hard-source.txt)" = "$(stat -c %i /fixture/hard-link)"; '
                  'test "$(stat -c %h /fixture/hard-source.txt)" -eq 2; '
                  'test "$(readlink /fixture/symlink)" = hard-source.txt; '
                  'test ! -e /fixture/deleted.txt; test ! -e /fixture/opaque/old-one; test ! -e /fixture/opaque/old-two; '
                  'test "$(stat -c %a /fixture/executable)" = 751; test "$(stat -c %a /fixture/hard-source.txt)" = 640'])
        self.run(['rm', container])
        self.containers.remove(container)
        return {'pull_by_digest': reference, 'all_fixture_entries_verified': len(checked['expected_rootfs']),
                'hardlink_inode_and_link_count': True, 'content_modes_symlinks_deletions': True}

    def storage(self):
        total = sum(path.stat().st_size for path in self.root.rglob('*') if path.is_file()) + MAX_TAR_BYTES
        volumes = {}
        for volume in sorted(self.volumes):
            value = self.run(['run', '--rm', '--network', 'none', '--read-only', '-v', f'{volume}:/state:ro', BUSYBOX, 'du', '-sk', '/state'])
            volumes[volume] = int(value.split()[0]) * 1024
        pulled = sum(int(self.run(['image', 'inspect', '--format', '{{.Size}}', image])) for image in sorted(self.images))
        staged = self.integration['bundle']['total_bytes'] + self.integration['helper_bytes'] if self.integration else 0
        total += sum(volumes.values()) + pulled + staged
        sample = {'estimated_fixture_bytes': total, 'owned_volume_bytes': volumes, 'pulled_image_size_sum': pulled,
                  'temporary_output_reserve_bytes': MAX_TAR_BYTES}
        if self.integration:
            sample['staged_integration_bytes'] = staged
        self.report['fixture_storage_samples'].append(sample)
        require(total < MAX_FIXTURE_BYTES, 'Fixture storage estimate exceeded its 2 GiB bound')
        return sample

    def execute(self):
        self.phase('sandbox-boundary', self.boundary)
        self.report['fixture'] = self.phase('deterministic-fixture', self.fixture)
        self.phase('prefetch-and-loopback-registry', self.registry)
        for variant in ['gzip-default', 'gzip-level1']:
            builder = self.phase(variant + '-builder-startup', lambda: self.builder(variant))
            result = {'name': variant, 'builder': builder, 'status': 'running'}
            self.report['variants'].append(result)
            try:
                if self.integration and variant == 'gzip-default':
                    self.phase('candidate-kernel-integration', lambda: self.integration['module']['run_kernel_tests'](self, builder, self.integration['bundle']))
                for temperature in ['cold', 'warm']:
                    key = variant + '-' + temperature
                    measured = self.phase(key + '-build-and-push', lambda: self.build(builder['name'], variant, temperature))
                    measured.update(registry_verified=False, pullback_verified=False)
                    # Keep timing evidence even when a later correctness check
                    # fails. A measurement is not a successful verification.
                    result[temperature] = measured
                    checked = {}
                    def verify():
                        checked.update(self.verify_registry(variant, temperature))
                        return {name: value for name, value in checked.items() if name != 'expected_rootfs'}
                    summary = self.phase(key + '-registry', verify)
                    measured.update(summary, registry_verified=True)
                    self.phase(key + '-pullback', lambda: self.pullback(variant, checked))
                    measured['pullback_verified'] = True
                    self.phase(key + '-storage', self.storage)
                require(result['cold']['layer_digests'][:-1] == result['warm']['layer_digests'][:-1], 'Warm build re-exported unchanged layers with different digests')
                require(result['cold']['layer_digests'][-1] != result['warm']['layer_digests'][-1], 'Warm marker change was not exported')
                result['status'] = 'passed'
            finally:
                if result['status'] == 'running':
                    result['status'] = 'failed'
            self.run(['buildx', 'stop', builder['name']], timeout=20)
        baseline, candidate = self.report['variants']
        self.report['comparison'] = {temperature: {'default_gzip_seconds': baseline[temperature]['build_and_push_seconds'],
            'gzip_level1_seconds': candidate[temperature]['build_and_push_seconds'],
            'default_over_level1_ratio': round(baseline[temperature]['build_and_push_seconds'] / max(candidate[temperature]['build_and_push_seconds'], 0.001), 3),
            'default_compressed_bytes': baseline[temperature]['compressed_layer_bytes'],
            'level1_compressed_bytes': candidate[temperature]['compressed_layer_bytes']}
            for temperature in ['cold', 'warm']}
        self.report['comparison']['interpretation'] = 'One sequential pair in this sandbox; startup and pull-back are separate. Ratios are observations, not a speed guarantee.'

    def cleanup(self):
        self.deadline = time.monotonic() + 45
        errors = []
        for builder in reversed(self.builders):
            try:
                self.run(['buildx', 'rm', '--keep-state', builder], timeout=12)
            except BaseException as error:
                errors.append(str(error)[-1024:])
        for container in reversed(self.containers):
            try:
                self.run(['rm', '-f', container], timeout=8)
            except BaseException as error:
                errors.append(str(error)[-1024:])
        self.report['cleanup'] = {'status': 'failed' if errors else 'passed', 'errors': errors,
                                  'private_volumes_require_disposable_pod_cleanup': sorted(self.volumes)}
        return not errors


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--force-overlay-diff', action='store_true', help='Diagnostic builder env only; unsupported paths fail without fallback')
    parser.add_argument('--buildkit-image', default=BUILDKIT, help='Exact stock pin or digest-pinned ghcr.io/hakopod/buildkit:v0.32.2-hakopod-<revision> candidate')
    parser.add_argument('--integration-tests', action='store_true', help='Wait for explicitly staged native candidate test artifacts')
    parser.add_argument('--integration-helper-sha256', default='')
    args = parser.parse_args()
    require(os.environ.get('HAKOPOD_EXPORT_BENCHMARK_DEV_CONTEXT') == CONTEXT, 'Run only through the named development-cluster harness')
    selected = buildkit_selection(args.buildkit_image)
    architecture = {'aarch64': 'arm64', 'arm64': 'arm64', 'x86_64': 'amd64', 'amd64': 'amd64'}.get(platform.machine())
    require(args.integration_tests or not args.integration_helper_sha256, 'Native helper checksum requires the explicit test option')
    integration = wait_for_integration(selected, args.integration_helper_sha256, architecture) if args.integration_tests else None
    benchmark = Benchmark(args.force_overlay_diff, args.buildkit_image, integration)
    failure = None
    def deadline(_signal, _frame):
        raise TimeoutError('Export benchmark wall-clock deadline exceeded')
    signal.signal(signal.SIGALRM, deadline)
    signal.setitimer(signal.ITIMER_REAL, 520)
    try:
        benchmark.execute()
        benchmark.report['status'] = 'passed'
    except BaseException as error:
        failure = error
        benchmark.report['status'] = 'failed'
        benchmark.report['error'] = str(error)[-17000:]
        benchmark.report.pop('comparison', None)
    finally:
        signal.setitimer(signal.ITIMER_REAL, 45)
        try:
            cleaned = benchmark.cleanup()
        except BaseException as error:
            cleaned = False
            benchmark.report['cleanup'] = {'status': 'failed', 'error': str(error)[-1024:],
                                          'private_volumes_require_disposable_pod_cleanup': sorted(benchmark.volumes)}
        signal.setitimer(signal.ITIMER_REAL, 0)
        if not cleaned:
            benchmark.report['status'] = 'failed'
            benchmark.report.pop('comparison', None)
        benchmark.report['duration_seconds'] = elapsed(benchmark.started)
        (benchmark.root / 'report.json').write_text(json.dumps(benchmark.report, indent=2) + '\n')
        emit({'phase': 'complete', 'status': benchmark.report['status'], 'report': str(benchmark.root / 'report.json'),
              'duration_seconds': benchmark.report['duration_seconds'], 'comparison': benchmark.report.get('comparison'),
              'error': benchmark.report.get('error'), 'cleanup': benchmark.report['cleanup']})
    if failure or benchmark.report['status'] != 'passed':
        raise SystemExit(1)


if __name__ == '__main__':
    main()
