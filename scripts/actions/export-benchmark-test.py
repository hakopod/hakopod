#!/usr/bin/env python3
"""Pure parser regressions; run on the development VM with Python unittest.

These tests do not call Docker, create fixture data or measure performance.
The real benchmark must still pass inside a product runner pod.
"""
import importlib.util
import io
import json
from pathlib import Path
import tarfile
import unittest
import urllib.request

spec = importlib.util.spec_from_file_location('export_benchmark', Path(__file__).with_name('export-benchmark.py'))
benchmark = importlib.util.module_from_spec(spec)
spec.loader.exec_module(benchmark)


def archive(entries):
    output = io.BytesIO()
    with tarfile.open(fileobj=output, mode='w') as stream:
        for name, kind, content, mode in entries:
            member = tarfile.TarInfo(name)
            member.type, member.mode = kind, mode
            if kind == tarfile.REGTYPE:
                member.size = len(content)
                stream.addfile(member, io.BytesIO(content))
            else:
                if kind in (tarfile.LNKTYPE, tarfile.SYMTYPE):
                    member.linkname = content
                stream.addfile(member)
    output.seek(0)
    return output


class ExportProofTests(unittest.TestCase):
    def test_child_environment_does_not_inherit_credentials_or_proxy_settings(self):
        result = benchmark.benchmark_environment({'PATH': '/usr/bin', 'HOME': '/home/runner', 'LANG': 'C',
            'GITHUB_TOKEN': 'fixture-secret', 'AWS_SECRET_ACCESS_KEY': 'fixture-secret', 'HTTP_PROXY': 'http://proxy.invalid',
            'DOCKER_HOST': 'tcp://remote.invalid:2375', 'DOCKER_AUTH_CONFIG': 'fixture-secret'}, Path('/private/benchmark/home'))
        self.assertEqual(result, {'PATH': '/usr/bin', 'HOME': '/private/benchmark/home', 'LANG': 'C', 'DOCKER_HOST': 'tcp://127.0.0.1:2375'})
        self.assertNotEqual(result['HOME'], '/home/runner')

    def test_registry_requests_are_loopback_only_and_redirects_fail_closed(self):
        self.assertEqual(benchmark.loopback_url('http://127.0.0.1:15000', '/v2/'), 'http://127.0.0.1:15000/v2/')
        for base in ['http://example.test:15000', 'http://user@127.0.0.1:15000', 'https://127.0.0.1:15000', 'http://127.0.0.1:15000/']:
            with self.assertRaises(RuntimeError):
                benchmark.loopback_url(base, '/v2/')
        for path in ['//example.test/v2/', '/v2/../secrets', '/v2/?token=fixture', '/v2/export/gzip-default/blobs/sha256:bad']:
            with self.assertRaises(RuntimeError):
                benchmark.loopback_url('http://127.0.0.1:15000', path)
        handler = benchmark.NoRegistryRedirects()
        for destination in ['http://example.test/', 'http://127.0.0.1:15000/v2/']:
            with self.assertRaisesRegex(RuntimeError, 'redirects are forbidden'):
                handler.redirect_request(urllib.request.Request('http://127.0.0.1:15000/v2/'), None, 302, 'Found', {}, destination)

    def test_whiteouts_remove_only_lower_entries_and_preserve_new_directory_contents(self):
        lower = benchmark.read_layer(archive([
            ('fixture', tarfile.DIRTYPE, '', 0o755),
            ('fixture/deleted', tarfile.REGTYPE, b'old', 0o644),
            ('fixture/opaque', tarfile.DIRTYPE, '', 0o755),
            ('fixture/opaque/stale', tarfile.REGTYPE, b'old', 0o644),
        ]))
        upper = benchmark.read_layer(archive([
            # New content can precede an opaque marker in the tar stream.
            ('fixture/opaque/new', tarfile.REGTYPE, b'new', 0o640),
            ('fixture/opaque/.wh..wh..opq', tarfile.REGTYPE, b'', 0),
            ('fixture/.wh.deleted', tarfile.REGTYPE, b'', 0),
        ]))
        rootfs = {}
        benchmark.apply_layer(rootfs, lower)
        benchmark.apply_layer(rootfs, upper)
        self.assertEqual(set(rootfs), {'fixture', 'fixture/opaque', 'fixture/opaque/new'})
        self.assertEqual(rootfs['fixture/opaque/new']['mode'], 0o640)
        self.assertEqual(upper['opaque'], ['fixture/opaque'])
        self.assertEqual(upper['removed'], ['fixture/deleted'])

    def test_hardlinks_resolve_to_content_while_symlinks_remain_links(self):
        layer = benchmark.read_layer(archive([
            ('fixture/source', tarfile.REGTYPE, b'same content', 0o751),
            ('fixture/hard', tarfile.LNKTYPE, 'fixture/source', 0o751),
            ('fixture/symbolic', tarfile.SYMTYPE, 'source', 0o777),
        ]))
        files = benchmark.normalized_files(layer['entries'])
        self.assertEqual(files['fixture/hard'], files['fixture/source'])
        self.assertEqual(files['fixture/hard']['mode'], 0o751)
        self.assertEqual(files['fixture/symbolic']['type'], 'symlink')
        self.assertEqual(files['fixture/symbolic']['target'], 'source')
        with self.assertRaisesRegex(RuntimeError, 'Invalid hardlink'):
            benchmark.normalized_files({'fixture/link': {'type': 'hardlink', 'target': 'fixture/missing'}})

    def test_unsafe_paths_special_files_and_oversized_reads_fail(self):
        for path in ['../fixture/file', 'fixture/../../file', 'fixture\\file']:
            with self.assertRaises(RuntimeError):
                benchmark.clean_path(path)
        with self.assertRaisesRegex(RuntimeError, 'member type'):
            benchmark.read_layer(archive([('fixture/device', tarfile.CHRTYPE, '', 0o600)]))
        with self.assertRaisesRegex(RuntimeError, 'Uncompressed layer'):
            benchmark.BoundedReader(io.BytesIO(b'12345'), 4).read()

    def test_direct_and_wrapped_progress_updates_keep_cache_and_phase_evidence(self):
        events = [
            {'id': 'copy', 'name': '[2/4] COPY payload/ /fixture/', 'started': '2026-09-30T00:00:00Z'},
            {'id': 'copy', 'cached': True, 'completed': '2026-09-30T00:00:01Z'},
            {'statuses': [{'id': 'exporting layers', 'vertex': 'sha256:export', 'current': 0,
                           'timestamp': '2026-09-30T00:00:01Z', 'started': '2026-09-30T00:00:01Z'}]},
            {'statuses': [{'id': 'exporting layers', 'vertex': 'sha256:export', 'current': 0,
                           'timestamp': '2026-09-30T00:00:03Z', 'started': '2026-09-30T00:00:01Z',
                           'completed': '2026-09-30T00:00:03Z'}]},
        ]
        records = benchmark.progress_records(io.BytesIO(('\n'.join(json.dumps(value) for value in events) + '\n').encode()))
        self.assertEqual(len(records), 2)
        self.assertTrue(records[0]['cached'])
        self.assertEqual(benchmark.duration(records[0]), 1)
        self.assertEqual(records[1]['name'], 'exporting layers')
        self.assertEqual(benchmark.duration(records[1]), 2)

    def test_phase_label_fallback_is_limited_to_vertex_statuses(self):
        events = [
            {'id': 'sha256:unlabeled', 'started': '2026-09-30T00:00:00Z'},
            {'vertexes': [{'digest': 'sha256:vertex', 'started': '2026-09-30T00:00:00Z'}]},
            {'id': 'exporting config sha256:config', 'vertex': 'sha256:export'},
            {'statuses': [{'id': 'sha256:layer', 'vertex': 'sha256:export', 'name': 'pushing layer'}]},
        ]
        records = benchmark.progress_records(io.BytesIO(('\n'.join(json.dumps(value) for value in events) + '\n').encode()))
        self.assertNotIn('name', records[0])
        self.assertNotIn('name', records[1])
        self.assertEqual(records[2]['name'], 'exporting config sha256:config')
        self.assertEqual(records[3]['name'], 'pushing layer')


if __name__ == '__main__':
    unittest.main()
