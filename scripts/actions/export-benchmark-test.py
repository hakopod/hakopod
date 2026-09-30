#!/usr/bin/env python3
"""Pure parser regressions; run on the development VM with Python unittest.

These tests do not call Docker, create fixture data or measure performance.
The real benchmark must still pass inside a product runner pod.
"""
import importlib.util
import contextlib
import copy
import io
import json
from pathlib import Path
import stat
import tarfile
import tempfile
import unittest
import urllib.request
from unittest.mock import patch

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
    def test_buildkit_selection_defaults_to_stock_and_accepts_only_pinned_candidates(self):
        stock = benchmark.buildkit_selection()
        self.assertEqual(stock, benchmark.buildkit_selection(''))
        self.assertEqual(stock['reference'], benchmark.BUILDKIT)
        self.assertEqual(stock['kind'], 'upstream')
        self.assertEqual(stock['version'], 'v0.32.2')
        candidate = 'ghcr.io/hakopod/buildkit:v0.32.2-hakopod-' + 'a' * 40 + '@sha256:' + 'b' * 64
        selected = benchmark.buildkit_selection(candidate)
        self.assertEqual(selected['reference'], candidate)
        self.assertEqual(selected['kind'], 'candidate')
        self.assertEqual(selected['hakopod_revision'], 'a' * 40)
        self.assertEqual(selected['digest'], 'sha256:' + 'b' * 64)
        invalid = [None, candidate.split('@')[0], candidate.replace('ghcr.io/', 'ghcr.io.evil/'),
            candidate.replace('/hakopod/', '/other/'), candidate.replace('v0.32.2', 'v0.32.3'),
            candidate.replace('a' * 40, 'a' * 39), candidate.replace('b' * 64, 'B' * 64),
            ' ' + candidate, candidate + '\n', candidate + ',network=host',
            'docker.io/moby/buildkit:v0.32.2@sha256:' + 'b' * 64]
        for value in invalid:
            with self.subTest(value=value), self.assertRaises(RuntimeError):
                benchmark.buildkit_selection(value)

    def test_selected_builder_must_match_observed_image_version_architecture_and_mode(self):
        candidate = 'ghcr.io/hakopod/buildkit:v0.32.2-hakopod-' + 'a' * 40 + '@sha256:' + 'b' * 64
        for reference in (benchmark.BUILDKIT, candidate):
            selected = benchmark.buildkit_selection(reference)
            image = {'Id': 'sha256:' + 'c' * 64, 'Os': 'linux', 'Architecture': 'amd64'}
            settings = ['HAKOPOD_BUILDKIT_USERXATTR=true'] if selected['kind'] == 'candidate' else []
            container = {'Image': image['Id'], 'Config': {'Image': reference, 'Env': settings}}
            version = 'buildkitd github.com/moby/buildkit ' + selected['version'] + ' upstream-revision\n'
            result = benchmark.builder_image_identity(selected, container, image, version, 'amd64')
            self.assertEqual(result['image'], reference)
            self.assertEqual(result['image_id'], image['Id'])
            self.assertEqual(result['managed_userxattr'], selected['kind'] == 'candidate')
            changes = [
                lambda c, i: c.update(Image='sha256:' + 'd' * 64),
                lambda c, i: c['Config'].update(Image=reference.split('@')[0]),
                lambda c, i: c['Config'].update(Env=['HAKOPOD_BUILDKIT_USERXATTR=false']),
                lambda c, i: i.update(Architecture='arm64'),
                lambda c, i: i.update(Os='windows'),
            ]
            for change in changes:
                observed, metadata = copy.deepcopy(container), copy.deepcopy(image)
                change(observed, metadata)
                with self.subTest(reference=reference, change=change), self.assertRaises(RuntimeError):
                    benchmark.builder_image_identity(selected, observed, metadata, version, 'amd64')
            with self.assertRaises(RuntimeError):
                benchmark.builder_image_identity(selected, container, image, version.replace('v0.32.2', 'v0.32.20'), 'amd64')

    def test_native_artifact_wait_is_candidate_only_and_bounded(self):
        with self.assertRaisesRegex(RuntimeError, 'candidate'):
            benchmark.wait_for_integration(benchmark.buildkit_selection(), 'c' * 64, 'amd64')
        candidate = 'ghcr.io/hakopod/buildkit:v0.32.2-hakopod-' + 'a' * 40 + '@sha256:' + 'b' * 64
        with tempfile.TemporaryDirectory() as temp, patch.object(benchmark, 'INTEGRATION_DIRECTORY', Path(temp) / 'staged'), \
                patch.object(benchmark.time, 'monotonic', side_effect=[0, 121]), self.assertRaisesRegex(RuntimeError, 'timed out'):
            benchmark.wait_for_integration(benchmark.buildkit_selection(candidate), 'c' * 64, 'amd64')

    def test_staged_helper_is_not_executed_without_matching_checksum(self):
        candidate = 'ghcr.io/hakopod/buildkit:v0.32.2-hakopod-' + 'a' * 40 + '@sha256:' + 'b' * 64
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp) / 'staged'
            def staged(_seconds):
                (root / 'buildkit-integration.py').write_text("raise AssertionError('unverified helper executed')")
                (root / 'ready').write_bytes(b'ready\n')
            with patch.object(benchmark, 'INTEGRATION_DIRECTORY', root), patch.object(benchmark.time, 'sleep', side_effect=staged), \
                    self.assertRaisesRegex(RuntimeError, 'helper identity'):
                benchmark.wait_for_integration(benchmark.buildkit_selection(candidate), 'c' * 64, 'amd64')

    def test_native_artifact_wait_initializes_a_fresh_private_workspace(self):
        selected = benchmark.buildkit_selection('ghcr.io/hakopod/buildkit:v0.32.2-hakopod-' + 'a' * 40 + '@sha256:' + 'b' * 64)
        with tempfile.TemporaryDirectory() as temp:
            workspace = Path(temp) / '_work'
            root = workspace / 'buildkit-integration'
            with patch.object(benchmark, 'INTEGRATION_DIRECTORY', root), \
                    patch.object(benchmark.time, 'monotonic', side_effect=[0, 121]), \
                    self.assertRaisesRegex(RuntimeError, 'timed out'):
                benchmark.wait_for_integration(selected, 'c' * 64, 'amd64')
            self.assertTrue(root.is_dir())
            self.assertEqual(stat.S_IMODE(workspace.stat().st_mode), 0o700)
            self.assertEqual(stat.S_IMODE(root.stat().st_mode), 0o700)

    def test_native_artifact_wait_rejects_a_symlinked_workspace_before_staging(self):
        selected = benchmark.buildkit_selection('ghcr.io/hakopod/buildkit:v0.32.2-hakopod-' + 'a' * 40 + '@sha256:' + 'b' * 64)
        with tempfile.TemporaryDirectory() as temp:
            target = Path(temp) / 'outside'
            target.mkdir()
            workspace = Path(temp) / '_work'
            workspace.symlink_to(target, target_is_directory=True)
            with patch.object(benchmark, 'INTEGRATION_DIRECTORY', workspace / 'buildkit-integration'), \
                    self.assertRaisesRegex(RuntimeError, 'symlink'):
                benchmark.wait_for_integration(selected, 'c' * 64, 'amd64')
            self.assertEqual(list(target.iterdir()), [])

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

    def test_registry_failure_keeps_partial_measurement_without_comparison(self):
        runner = benchmark.Benchmark.__new__(benchmark.Benchmark)
        runner.integration = None
        runner.docker_storage = lambda *args: None
        runner.report = {'phases': [], 'variants': []}
        runner.boundary = runner.fixture = runner.registry = lambda: {}
        runner.builder = lambda variant: {'name': 'fixture-' + variant}
        runner.build = lambda *args: {'build_and_push_seconds': 49, 'phases': [
            {'name': 'exporting layers', 'duration_seconds': 40, 'cached': False}],
            'payload_and_mutation_cached': False}
        def fail(*args):
            raise RuntimeError('Registry rootfs filesystem mismatch: test fixture')
        runner.verify_registry = fail
        with contextlib.redirect_stdout(io.StringIO()), self.assertRaisesRegex(RuntimeError, 'Registry rootfs'):
            runner.execute()
        result = runner.report['variants'][0]
        self.assertEqual(result['status'], 'failed')
        self.assertEqual(result['cold']['build_and_push_seconds'], 49)
        self.assertFalse(result['cold']['registry_verified'])
        self.assertFalse(result['cold']['pullback_verified'])
        self.assertNotIn('warm', result)
        self.assertNotIn('comparison', runner.report)
        self.assertEqual(runner.report['phases'][-1]['phase'], 'gzip-default-cold-registry')
        self.assertEqual(runner.report['phases'][-1]['status'], 'failed')

    def test_storage_diagnostics_use_bounded_docker_accounting_without_new_containers(self):
        runner = benchmark.Benchmark.__new__(benchmark.Benchmark)
        runner.root = Path('/')
        runner.report = {'docker_storage_samples': []}
        calls = []
        def accounting(args, **options):
            calls.append((args, options))
            return json.dumps({'Type': 'Images', 'TotalCount': 3, 'Active': 1, 'Size': '1.5GB', 'Reclaimable': '0B'})
        runner.run = accounting
        with contextlib.redirect_stdout(io.StringIO()):
            runner.docker_storage('after-pull-buildkit')
        self.assertEqual(calls, [(['system', 'df', '--format', '{{json .}}'], {'timeout': 5, 'maximum': 16384})])
        sample = runner.report['docker_storage_samples'][0]
        self.assertEqual(sample['status'], 'available')
        self.assertEqual(sample['docker_accounting'][0]['Size'], '1.5GB')
        def unavailable(*args, **kwargs):
            raise RuntimeError('daemon unavailable')
        runner.run = unavailable
        with contextlib.redirect_stdout(io.StringIO()):
            runner.docker_storage('failed-command')
        self.assertEqual(runner.report['docker_storage_samples'][-1]['status'], 'unavailable')
        self.assertEqual(runner.report['docker_storage_samples'][-1]['error_type'], 'RuntimeError')


if __name__ == '__main__':
    unittest.main()
