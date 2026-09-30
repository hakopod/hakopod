#!/usr/bin/env python3
"""Opt-in metadata and registry-cache proof inside a disposable managed runner.

Run beside export-benchmark.py only through the named development harness. This
uses that helper's pinned images, verified candidate builder, loopback registry,
private Docker configuration, bounded commands and owned-resource cleanup. No
provider credential, host Docker socket, package installation or extra image is
needed. This script does not run when imported and is not a performance test.

Two synthetic OCI layers carry fewer than 64 KiB of fixture payload. They include
a valid Linux file capability, a user xattr, hardlinks, permissions and whiteouts.
COPY --link must import and merge that filesystem correctly. A separate fresh
builder imports the first builder's registry cache, proves COPY and RUN cache
hits, then changes a metadata file's mode. Its new layer must contain the exact
xattrs, proving snapshot materialization and copy-up rather than blob reuse.

OCI xattrs are verified byte-for-byte. Docker pullback additionally checks actual
content, permissions and hardlink inodes. It does not inspect Docker's private
storage or claim to verify getxattr after Docker unpack; the native integration
suite must cover that separately. Both builders and the registry remain inside
one disposable product pod. Cross-job credentials and fleet load are not tested.
"""
import argparse
import gzip
import hashlib
import importlib.util
import io
import json
import os
from pathlib import Path
import re
import signal
import struct
import tarfile
import time
import urllib.parse
import urllib.request
import uuid


def load_benchmark():
    location = Path(__file__).resolve().with_name('export-benchmark.py')
    spec = importlib.util.spec_from_file_location('qualification_export_helpers', location)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


benchmark = load_benchmark()
require = benchmark.require
MIB = 1024 * 1024
MAX_PAYLOAD = 64 * 1024
MAX_ARCHIVE = 32 * MIB
MAX_JSON = MIB
MAX_MEMBERS = 4096
CAPABILITY = struct.pack('<IIIII', 0x02000001, 1 << 10, 0, 0, 0)
XATTRS = {'security.capability': CAPABILITY, 'user.hakopod-proof': b'metadata-roundtrip-v1'}
SEED_FILE = b'#!/bin/sh\nprintf "metadata proof\\n"\n'
HARD_FILE = b'hardlink proof\n'
NEW_FILE = b'opaque replacement\n'
RUN_FILE = b'original cache step\n'
RESTORED_FILE = b'fresh builder materialized\n'
OCI_MANIFEST = 'application/vnd.oci.image.manifest.v1+json'
OCI_CONFIG = 'application/vnd.oci.image.config.v1+json'
OCI_LAYER = 'application/vnd.oci.image.layer.v1.tar+gzip'
CACHE_CONFIG = 'application/vnd.buildkit.cacheconfig.v0'


def digest(data):
    return 'sha256:' + hashlib.sha256(data).hexdigest()


def encoded(value):
    return json.dumps(value, sort_keys=True, separators=(',', ':')).encode()


def tar_layer(entries):
    """Construct metadata without granting the test process file capabilities."""
    output = io.BytesIO()
    total = 0
    with tarfile.open(fileobj=output, mode='w', format=tarfile.PAX_FORMAT) as archive:
        for name, kind, mode, content, attrs in entries:
            total += len(content)
            require(total < MAX_PAYLOAD, 'Fixture payload must remain below 64 KiB')
            member = tarfile.TarInfo(name)
            member.mode, member.uid, member.gid, member.mtime = mode, 0, 0, 1700000000
            member.pax_headers = {'SCHILY.xattr.' + key: value.decode('utf-8') for key, value in attrs.items()}
            if kind == 'directory':
                member.type = tarfile.DIRTYPE
            elif kind in ('hardlink', 'symlink'):
                member.type = tarfile.LNKTYPE if kind == 'hardlink' else tarfile.SYMTYPE
                member.linkname = content.decode()
            else:
                require(kind == 'file', 'Unexpected fixture member kind')
                member.size = len(content)
            archive.addfile(member, io.BytesIO(content) if member.isfile() else None)
    data = output.getvalue()
    require(len(data) < MAX_PAYLOAD, 'Fixture tar must remain below 64 KiB')
    return data


def fixture_layers():
    first = tar_layer([
        ('fixture', 'directory', 0o755, b'', {}),
        ('fixture/metadata', 'file', 0o751, SEED_FILE, XATTRS),
        ('fixture/hard-source', 'file', 0o640, HARD_FILE, {}),
        ('fixture/hard-link', 'hardlink', 0o640, b'fixture/hard-source', {}),
        ('fixture/symlink', 'symlink', 0o777, b'hard-source', {}),
        ('fixture/deleted', 'file', 0o644, b'delete me\n', {}),
        ('fixture/opaque', 'directory', 0o755, b'', {}),
        ('fixture/opaque/old', 'file', 0o644, b'old child\n', {}),
    ])
    second = tar_layer([
        ('fixture/.wh.deleted', 'file', 0o000, b'', {}),
        ('fixture/opaque/.wh..wh..opq', 'file', 0o000, b'', {}),
        ('fixture/opaque/new', 'file', 0o644, NEW_FILE, {}),
    ])
    require(len(first) + len(second) < MAX_PAYLOAD, 'Combined fixture tar exceeds 64 KiB')
    return [first, second]


def file_entry(content, mode=0o644, attrs=None):
    return {'type': 'file', 'mode': mode, 'uid': 0, 'gid': 0, 'size': len(content),
            'sha256': hashlib.sha256(content).hexdigest(),
            'xattrs': {key: value.hex() for key, value in (attrs or {}).items()}}


def expected_files(restored=False, built=True):
    directory = {'type': 'directory', 'mode': 0o755, 'uid': 0, 'gid': 0, 'xattrs': {}}
    result = {'fixture': dict(directory), 'fixture/opaque': dict(directory),
              'fixture/metadata': file_entry(SEED_FILE, 0o750 if restored else 0o751, XATTRS),
              'fixture/hard-source': file_entry(HARD_FILE, 0o640),
              'fixture/hard-link': file_entry(HARD_FILE, 0o640),
              'fixture/symlink': {'type': 'symlink', 'mode': 0o777, 'uid': 0, 'gid': 0,
                                  'target': 'hard-source', 'xattrs': {}},
              'fixture/opaque/new': file_entry(NEW_FILE)}
    if built:
        result['fixture/run-proof'] = file_entry(RUN_FILE)
    if restored:
        result['fixture/restore-proof'] = file_entry(RESTORED_FILE)
    return result


def read_metadata_layer(data):
    """Read bounded tar metadata without ever extracting untrusted paths."""
    require(len(data) <= MAX_ARCHIVE, 'Compressed archive exceeds 32 MiB')
    entries, removed, opaque = {}, [], []
    payload = 0
    with gzip.GzipFile(fileobj=io.BytesIO(data)) as expanded:
        uncompressed = expanded.read(MAX_ARCHIVE + 1)
        require(len(uncompressed) <= MAX_ARCHIVE, 'Uncompressed archive exceeds 32 MiB')
        with tarfile.open(fileobj=io.BytesIO(uncompressed), mode='r|') as archive:
            for index, member in enumerate(archive):
                require(index < MAX_MEMBERS, 'Archive member limit exceeded')
                path = benchmark.clean_path(member.name)
                if path != 'fixture' and not path.startswith('fixture/'):
                    continue
                parent, _, name = path.rpartition('/')
                if name == '.wh..wh..opq':
                    require(member.isfile() and member.size == 0, 'Invalid opaque marker')
                    opaque.append(parent)
                    continue
                if name.startswith('.wh.'):
                    require(member.isfile() and member.size == 0, 'Invalid whiteout')
                    removed.append(parent + '/' + name[4:])
                    continue
                require(path not in entries, 'Duplicate fixture member')
                require(not any(key.startswith('LIBARCHIVE.xattr.') for key in member.pax_headers),
                        'Unexpected alternate xattr encoding')
                attrs = {key[len('SCHILY.xattr.'):]: value.encode('utf-8', 'surrogateescape').hex()
                         for key, value in member.pax_headers.items() if key.startswith('SCHILY.xattr.')}
                require(len(attrs) <= 8 and sum(len(key) + len(value) for key, value in attrs.items()) < 4096,
                        'Fixture xattrs exceed their bound')
                entry = {'mode': member.mode & 0o7777, 'uid': member.uid, 'gid': member.gid, 'xattrs': attrs}
                if member.isdir():
                    entry['type'] = 'directory'
                elif member.issym():
                    entry.update(type='symlink', target=member.linkname)
                elif member.islnk():
                    entry.update(type='hardlink', target=benchmark.clean_path(member.linkname))
                else:
                    require(member.isfile() and 0 <= member.size < MAX_PAYLOAD, 'Unexpected fixture member type or size')
                    payload += member.size
                    require(payload < MAX_PAYLOAD, 'Fixture payload exceeds 64 KiB')
                    source = archive.extractfile(member)
                    require(source is not None, 'Missing fixture member contents')
                    content = source.read(MAX_PAYLOAD)
                    require(len(content) == member.size, 'Truncated fixture member')
                    entry.update(type='file', size=len(content), sha256=hashlib.sha256(content).hexdigest())
                entries[path] = entry
    return {'entries': entries, 'removed': removed, 'opaque': opaque, 'diff_id': digest(uncompressed)}


def registry_url(base, path):
    benchmark.loopback_url(base, '/v2/')
    require(isinstance(path, str) and len(path) <= 4096, 'Registry URL exceeds its bound')
    parsed = urllib.parse.urlsplit(path)
    if parsed.scheme or parsed.netloc:
        require(parsed.scheme == 'http' and parsed.netloc == urllib.parse.urlsplit(base).netloc,
                'Upload location must remain on the exact loopback registry')
    require(not parsed.fragment and not parsed.username and not parsed.password, 'Unexpected registry URL credentials or fragment')
    route = parsed.path
    allowed = (route == '/v2/' or re.fullmatch(
        r'/v2/qualify/(?:seed|result|cache)/(?:manifests/(?:seed|cold|restored|cache|sha256:[a-f0-9]{64})|blobs/(?:sha256:[a-f0-9]{64}|uploads/(?:[a-f0-9-]{36})?))', route))
    require(allowed, 'Unexpected qualification registry path')
    query = urllib.parse.parse_qs(parsed.query, strict_parsing=True)
    require(not query or '/blobs/uploads/' in route, 'Query is only valid for an owned blob upload')
    require(set(query) <= {'_state', 'digest'} and all(len(values) == 1 for values in query.values()), 'Unexpected registry upload query')
    if '_state' in query:
        require(re.fullmatch(r'[A-Za-z0-9_=-]{1,3072}', query['_state'][0]), 'Unexpected upload state encoding')
    if 'digest' in query:
        require(re.fullmatch(r'sha256:[a-f0-9]{64}', query['digest'][0]), 'Invalid upload digest')
    return base + route + ('?' + parsed.query if parsed.query else '')


def check_cache_records(records, restored, cache_reference):
    require(len(records) <= 256, 'Qualification progress exceeds 256 records')
    copied = [item for item in records if 'COPY --link' in item.get('name', '')
              and 'LINK COPY --link' not in item['name'] and '/fixture/' in item['name']]
    merged = [item for item in records if 'LINK COPY --link' in item.get('name', '') and '/fixture/' in item['name']]
    original = [item for item in records if 'RUN printf' in item.get('name', '') and 'original cache step' in item['name']]
    require(len(copied) == len(merged) == len(original) == 1, 'Missing COPY --link, merge or original RUN cache evidence')
    require(all(item.get('cached', False) is restored for item in copied + merged + original), 'Unexpected cross-builder cache-hit evidence')
    if restored:
        imported = [item for item in records if item.get('name') == 'importing cache manifest from ' + cache_reference]
        materialized = [item for item in records if 'RUN chmod 750 /fixture/metadata' in item.get('name', '')]
        require(imported and any(item.get('completed') for item in imported), 'Registry cache import was not completed')
        require(len(materialized) == 1 and not materialized[0].get('cached', False)
                and materialized[0].get('completed'), 'Restored snapshot was not materialized by a new RUN')
    return {'copy_link_cached': restored, 'merge_cached': restored, 'original_run_cached': restored,
            'registry_cache_import_completed': restored, 'restore_copy_up_executed': restored}


class Qualification(benchmark.Benchmark):
    def __init__(self, force, image):
        selection = benchmark.buildkit_selection(image)
        require(selection['kind'] == 'candidate', 'Qualification requires an explicitly pinned managed candidate')
        super().__init__(force, image)
        self.deadline = self.started + 420
        self.report.update(scope='Disposable managed sandbox metadata and registry-cache qualification',
                           scenario='buildkit-metadata-and-registry-cache', metadata_cache_checks_passed=False,
                           coverage_limits=[
                               'OCI import, COPY --link, cache restoration and copy-up xattrs are checked byte-for-byte.',
                               'Docker pullback checks content, mode and inodes, not getxattr after Docker unpack.',
                               'One native architecture per run; no provider credential, fleet load or speed claim.'])
        self.report['limits'].update(workload_seconds=420, fixture_payload_bytes=MAX_PAYLOAD,
                                     temporary_archive_bytes=MAX_ARCHIVE, command_output_bytes=MIB)
        self.report['checks'] = []

    def run(self, args, timeout=120, maximum=MIB, progress=False, archive=False):
        return super().run(args, timeout=timeout, maximum=maximum, progress=progress, archive=archive)

    def request(self, method, path, maximum=MAX_JSON, body=None, content_type='application/octet-stream'):
        require(method in ('GET', 'POST', 'PUT'), 'Unexpected registry method')
        require(body is None or len(body) <= MAX_ARCHIVE, 'Registry request exceeds its bound')
        timeout = min(15, self.deadline - time.monotonic())
        require(timeout > 0, 'Registry operation exceeded the qualification deadline')
        request = urllib.request.Request(registry_url(self.base, path), data=body, method=method,
            headers={'Accept': OCI_MANIFEST + ', application/vnd.docker.distribution.manifest.v2+json',
                     'Content-Type': content_type})
        with self.opener.open(request, timeout=timeout) as response:
            data = response.read(maximum + 1)
            require(len(data) <= maximum, 'Registry response exceeds its bound')
            return data, response.headers, response.status

    def fetch(self, path, maximum):
        data, headers, status = self.request('GET', path, maximum)
        require(status == 200, 'Registry GET did not succeed')
        return data, headers

    def upload_blob(self, data):
        _, headers, status = self.request('POST', '/v2/qualify/seed/blobs/uploads/', body=b'')
        require(status == 202, 'Registry did not start the owned blob upload')
        location = headers.get('Location', '')
        registry_url(self.base, location)
        parsed = urllib.parse.urlsplit(location)
        require(parsed.path.startswith('/v2/qualify/seed/blobs/uploads/') and not 'digest' in urllib.parse.parse_qs(parsed.query),
                'Registry returned an unexpected upload location')
        target = location + ('&' if parsed.query else '?') + urllib.parse.urlencode({'digest': digest(data)})
        _, finished, status = self.request('PUT', target, body=data)
        require(status == 201 and finished.get('Docker-Content-Digest') == digest(data), 'Registry did not confirm the uploaded blob digest')
        return {'digest': digest(data), 'size': len(data)}

    def seed(self):
        layers = fixture_layers()
        descriptors = []
        for layer in layers:
            descriptors.append({'mediaType': OCI_LAYER, **self.upload_blob(gzip.compress(layer, mtime=0))})
        config = encoded({'architecture': self.architecture, 'os': 'linux', 'config': {},
                          'rootfs': {'type': 'layers', 'diff_ids': [digest(layer) for layer in layers]},
                          'history': [{'created_by': 'credential-free metadata fixture'} for _ in layers]})
        config_descriptor = {'mediaType': OCI_CONFIG, **self.upload_blob(config)}
        manifest = encoded({'schemaVersion': 2, 'mediaType': OCI_MANIFEST, 'config': config_descriptor, 'layers': descriptors})
        _, headers, status = self.request('PUT', '/v2/qualify/seed/manifests/seed', body=manifest, content_type=OCI_MANIFEST)
        require(status == 201 and headers.get('Docker-Content-Digest') == digest(manifest), 'Seed manifest was not acknowledged by digest')
        self.seed_digest = digest(manifest)
        self.seed_reference = f'127.0.0.1:{self.port}/qualify/seed@{self.seed_digest}'
        self.verify_image('seed', 'seed', expected_files(built=False), self.seed_digest)
        return {'manifest_digest': self.seed_digest, 'payload_tar_bytes': sum(map(len, layers)),
                'capability': 'Linux v2 effective CAP_NET_BIND_SERVICE', 'xattr_names': sorted(XATTRS)}

    def build_image(self, builder, restored):
        source = self.root / ('restore-source' if restored else 'cold-source')
        source.mkdir(mode=0o700)
        dockerfile = f'''FROM {self.seed_reference} AS metadata
FROM {benchmark.BUSYBOX}
COPY --link --from=metadata /fixture/ /fixture/
RUN printf 'original cache step\\n' > /fixture/run-proof
'''
        if restored:
            dockerfile += "RUN chmod 750 /fixture/metadata && printf 'fresh builder materialized\\n' > /fixture/restore-proof\n"
        require(len(dockerfile.encode()) < MAX_PAYLOAD, 'Dockerfile exceeds its bound')
        (source / 'Dockerfile').write_text(dockerfile)
        tag = 'restored' if restored else 'cold'
        cache = f'127.0.0.1:{self.port}/qualify/cache:cache'
        image = f'127.0.0.1:{self.port}/qualify/result:{tag}'
        args = ['buildx', 'build', '--builder', builder['name'], '--platform', 'linux/' + self.architecture,
                '--progress=rawjson', '--provenance=false',
                '--output', f'type=image,name={image},push=true,oci-mediatypes=true,compression=gzip']
        args += ['--cache-from', 'type=registry,ref=' + cache] if restored else [
            '--cache-to', f'type=registry,ref={cache},mode=max,oci-mediatypes=true,image-manifest=true,compression=gzip']
        records = self.run(args + [str(source)], timeout=120, maximum=MIB, progress=True)
        return {'image_tag': tag, **check_cache_records(records, restored, cache)}

    def read_descriptor(self, repository, descriptor, maximum):
        require(isinstance(descriptor, dict) and re.fullmatch(r'sha256:[a-f0-9]{64}', descriptor.get('digest', '')),
                'Registry descriptor has no valid digest')
        require(type(descriptor.get('size')) is int and 0 < descriptor['size'] <= maximum, 'Registry descriptor size exceeds its bound')
        data, _ = self.fetch(f'/v2/qualify/{repository}/blobs/{descriptor["digest"]}', maximum)
        require(len(data) == descriptor['size'] and digest(data) == descriptor['digest'], 'Registry blob size or digest mismatch')
        return data

    def manifest(self, repository, tag):
        data, headers = self.fetch(f'/v2/qualify/{repository}/manifests/{tag}', MAX_JSON)
        require(headers.get('Docker-Content-Digest') == digest(data), 'Registry manifest digest mismatch')
        value = json.loads(data)
        require(value.get('schemaVersion') == 2 and value.get('mediaType') == OCI_MANIFEST
                and isinstance(value.get('layers'), list) and 1 <= len(value['layers']) <= 16,
                'Expected one bounded OCI image manifest')
        return value, digest(data)

    def verify_cache(self):
        manifest, reference = self.manifest('cache', 'cache')
        require(manifest['config'].get('mediaType') == CACHE_CONFIG, 'Registry cache has the wrong config type')
        config = json.loads(self.read_descriptor('cache', manifest['config'], MAX_JSON))
        require(isinstance(config.get('records'), list) and 1 <= len(config['records']) <= 64,
                'Registry cache has no bounded build records')
        total = 0
        for descriptor in manifest['layers']:
            data = self.read_descriptor('cache', descriptor, MAX_ARCHIVE)
            total += len(data)
            require(total <= 64 * MIB, 'Registry cache exceeds 64 MiB')
        return {'manifest_digest': reference, 'records': len(config['records']), 'layers': len(manifest['layers']),
                'all_cache_blobs_verified': True}

    def verify_image(self, repository, tag, expected, required_digest=None, copied_up=False):
        manifest, reference = self.manifest(repository, tag)
        require(required_digest is None or reference == required_digest, 'Selected manifest changed')
        require(manifest['config'].get('mediaType') == OCI_CONFIG, 'Image has the wrong config type')
        config = json.loads(self.read_descriptor(repository, manifest['config'], MAX_JSON))
        require(config.get('architecture') == self.architecture and config.get('os') == 'linux', 'Image architecture differs from the native runner')
        rootfs, parsed, total = {}, [], 0
        for descriptor in manifest['layers']:
            require(descriptor.get('mediaType') == OCI_LAYER, 'Expected gzip OCI layers')
            data = self.read_descriptor(repository, descriptor, MAX_ARCHIVE)
            total += len(data)
            require(total <= 64 * MIB, 'Fixture image exceeds 64 MiB')
            layer = read_metadata_layer(data)
            parsed.append(layer)
            benchmark.apply_layer(rootfs, layer)
        require(config.get('rootfs') == {'type': 'layers', 'diff_ids': [layer['diff_id'] for layer in parsed]},
                'Image rootfs diff IDs differ from the actual uncompressed layers')
        benchmark.verify_files(rootfs, expected, 'Qualification OCI rootfs')
        if copied_up:
            require(parsed[-1]['entries'].get('fixture/metadata') == expected['fixture/metadata'],
                    'Final restore layer did not re-export exact capability and user xattr after copy-up')
            require(parsed[-1]['entries'].get('fixture/restore-proof') == expected['fixture/restore-proof'],
                    'Final restore layer did not execute its new step')
        return {'manifest_digest': reference, 'layer_digests': [item['digest'] for item in manifest['layers']],
                'fixture_entries': len(expected), 'content_modes_links_xattrs': True,
                'restored_copy_up_layer_verified': copied_up}

    def pullback_image(self, tag, checked):
        reference = f'127.0.0.1:{self.port}/qualify/result@{checked["manifest_digest"]}'
        self.run(['pull', reference], timeout=60, maximum=MIB)
        self.images.add(reference)
        container = self.name + '-metadata-readback-' + uuid.uuid4().hex[:6]
        self.containers.append(container)
        self.run(['create', '--name', container, '--network', 'none', reference, 'true'], maximum=MIB)
        observed = self.run(['export', container], timeout=60, maximum=MAX_ARCHIVE, archive=True)
        expected = {path: {key: value for key, value in entry.items() if key != 'xattrs'}
                    for path, entry in expected_files(restored=tag == 'restored').items()}
        benchmark.verify_files(observed['entries'], expected, 'Qualification Docker pullback')
        command = ('test "$(stat -c %i /fixture/hard-source)" = "$(stat -c %i /fixture/hard-link)"; '
                   'test "$(stat -c %h /fixture/hard-source)" -eq 2; '
                   'test "$(stat -c %a /fixture/hard-source)" = 640; '
                   'test "$(readlink /fixture/symlink)" = hard-source; '
                   'test ! -e /fixture/deleted; test ! -e /fixture/opaque/old; '
                   'test "$(stat -c %a /fixture/metadata)" = ' + ('750' if tag == 'restored' else '751'))
        self.run(['run', '--rm', '--network', 'none', '--read-only', '--cap-drop', 'ALL', reference, 'sh', '-ec', command],
                 timeout=30, maximum=MIB)
        self.run(['rm', container], maximum=MIB)
        self.containers.remove(container)
        return {'manifest_digest': checked['manifest_digest'], 'content_modes_hardlink_inodes': True,
                'docker_unpack_xattrs_verified': False}

    def execute(self):
        self.phase('sandbox-boundary', self.boundary)
        self.phase('loopback-registry', self.registry)
        self.report['fixture'] = self.phase('seed-oci-metadata', self.seed)
        first = self.phase('cold-builder', lambda: self.builder('metadata-cold'))
        self.report['cold_builder'] = first
        self.report['cold_build'] = self.phase('cold-build-cache-export', lambda: self.build_image(first, False))
        cold = self.phase('cold-oci-verification', lambda: self.verify_image('result', 'cold', expected_files()))
        self.report['cold_image'] = cold
        self.report['cache'] = self.phase('registry-cache-verification', self.verify_cache)
        self.phase('cold-docker-pullback', lambda: self.pullback_image('cold', cold))
        self.phase('cold-builder-removal', lambda: self.run(['buildx', 'rm', '--keep-state', first['name']], timeout=20, maximum=MIB))
        self.builders.remove(first['name'])
        require(self.run(['ps', '-a', '--filter', 'name=^/buildx_buildkit_' + first['name'] + '0$', '--format', '{{.ID}}'], maximum=MIB) == '',
                'First builder container survived removal')
        second = self.phase('fresh-builder', lambda: self.builder('metadata-restored'))
        require(first['name'] != second['name'], 'Cache restoration reused the first builder')
        self.report['restored_builder'] = second
        self.report['restored_build'] = self.phase('fresh-build-cache-import', lambda: self.build_image(second, True))
        restored = self.phase('restored-oci-verification', lambda: self.verify_image(
            'result', 'restored', expected_files(restored=True), copied_up=True))
        self.report['restored_image'] = restored
        require(cold['layer_digests'] == restored['layer_digests'][:-1], 'Fresh builder did not reuse the original image layers')
        require(restored['layer_digests'][-1] not in cold['layer_digests'], 'Restore copy-up did not emit a new layer')
        self.phase('restored-docker-pullback', lambda: self.pullback_image('restored', restored))
        self.phase('bounded-storage', self.storage)
        self.report['metadata_cache_checks_passed'] = True


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--buildkit-image', required=True)
    parser.add_argument('--force-overlay-diff', action='store_true')
    args = parser.parse_args()
    require(os.environ.get('HAKOPOD_EXPORT_BENCHMARK_DEV_CONTEXT') == benchmark.CONTEXT,
            'Run only through the named development-cluster harness')
    runner = Qualification(args.force_overlay_diff, args.buildkit_image)
    def expired(_signal, _frame):
        raise TimeoutError('BuildKit qualification deadline exceeded')
    signal.signal(signal.SIGALRM, expired)
    signal.setitimer(signal.ITIMER_REAL, 420)
    try:
        runner.execute()
        runner.report['status'] = 'passed'
    except BaseException as error:
        runner.report.update(status='failed', metadata_cache_checks_passed=False, error=str(error)[-17000:])
    finally:
        signal.setitimer(signal.ITIMER_REAL, 45)
        try:
            if not runner.cleanup():
                runner.report.update(status='failed', metadata_cache_checks_passed=False)
        except BaseException as error:
            runner.report.update(status='failed', metadata_cache_checks_passed=False,
                                 cleanup={'status': 'failed', 'error': str(error)[-1024:]})
        signal.setitimer(signal.ITIMER_REAL, 0)
        runner.report['duration_seconds'] = benchmark.elapsed(runner.started)
        target = runner.root / 'qualification-report.json'
        target.write_text(json.dumps(runner.report, indent=2) + '\n')
        print('HAKOPOD_BUILDKIT_QUALIFICATION ' + json.dumps(runner.report, separators=(',', ':')), flush=True)
    if runner.report['status'] != 'passed':
        raise SystemExit(1)


if __name__ == '__main__':
    main()
