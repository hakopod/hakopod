#!/usr/bin/env python3
"""Small artifact and invocation checks; no Docker or native tests execute."""
import copy
import hashlib
import importlib.util
import io
import json
from pathlib import Path
import tarfile
import tempfile
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location('buildkit_integration', Path(__file__).with_name('buildkit-integration.py'))
integration = importlib.util.module_from_spec(spec)
spec.loader.exec_module(integration)
IMAGE = 'ghcr.io/hakopod/buildkit:v0.32.2-hakopod-' + 'a' * 40 + '@sha256:' + 'b' * 64


def fixture(root):
    binary = bytearray(128)
    binary[:6] = b'\x7fELF\x02\x01'
    binary[18:20] = (62).to_bytes(2, 'little')
    digest = hashlib.sha256(binary).hexdigest()
    source = {'schema': 1, 'repository': 'hakopod/hakopod', 'revision': 'a' * 40,
        'run_id': '123', 'run_attempt': 1, 'architecture': 'amd64', 'upstream_revision': integration.UPSTREAM,
        'sha256': {'buildkitd': 'c' * 64, 'userxattr.patch': 'd' * 64, 'overlay.test': digest, 'buildkitd.test': digest}}
    for name in integration.SUITES:
        (root / name).write_bytes(binary)
        (root / name).chmod(0o700)
    (root / 'source.json').write_text(json.dumps(source))
    (root / 'source.json').chmod(0o600)
    return source


class ArtifactTests(unittest.TestCase):
    def test_exact_bundle_is_accepted(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            source = fixture(root)
            value = integration.load_bundle(root, IMAGE, 'amd64', '123')
            self.assertEqual(value['source'], source)
            self.assertEqual(value['files']['overlay.test']['bytes'], 128)
            self.assertEqual(set(value['files']), set(integration.FILES))

    def test_source_architecture_candidate_revision_and_run_are_bound(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            source = fixture(root)
            for key, value in [('repository', 'other/project'), ('revision', 'f' * 40), ('architecture', 'arm64'),
                               ('upstream_revision', 'f' * 40), ('run_id', '124'), ('run_attempt', 0)]:
                changed = {**source, key: value}
                with self.subTest(key=key), self.assertRaises(RuntimeError):
                    integration.validate_source(changed, IMAGE, 'amd64', '123')
            for image in (IMAGE.split('@')[0], IMAGE.replace('/hakopod/', '/other/'), 'docker.io/moby/buildkit:latest'):
                with self.subTest(image=image), self.assertRaises(RuntimeError):
                    integration.validate_source(source, image, 'amd64', '123')

    def test_binary_changes_wrong_elf_and_links_are_rejected(self):
        for mutation in ('checksum', 'architecture', 'symlink', 'hardlink', 'extra'):
            with self.subTest(mutation=mutation), tempfile.TemporaryDirectory() as temp:
                root = Path(temp)
                source = fixture(root)
                binary = root / 'overlay.test'
                if mutation == 'checksum':
                    binary.write_bytes(b'changed')
                elif mutation == 'architecture':
                    data = bytearray(binary.read_bytes())
                    data[18:20] = (183).to_bytes(2, 'little')
                    binary.write_bytes(data)
                    source['sha256']['overlay.test'] = hashlib.sha256(data).hexdigest()
                    (root / 'source.json').write_text(json.dumps(source))
                elif mutation in ('symlink', 'hardlink'):
                    binary.unlink()
                    if mutation == 'symlink':
                        binary.symlink_to(root / 'buildkitd.test')
                    else:
                        binary.hardlink_to(root / 'buildkitd.test')
                else:
                    (root / 'unexpected').touch()
                with self.assertRaises(RuntimeError):
                    integration.load_bundle(root, IMAGE, 'amd64', '123')

    def test_binary_and_manifest_bounds_are_enforced(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            fixture(root)
            with patch.object(integration, 'MAX_BINARY', 64), self.assertRaises(RuntimeError):
                integration.load_bundle(root, IMAGE, 'amd64')
            with patch.object(integration, 'MAX_MANIFEST', 16), self.assertRaises(RuntimeError):
                integration.load_bundle(root, IMAGE, 'amd64')

    def test_archive_unpacks_only_three_regular_owned_files(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            source = root / 'source'
            source.mkdir(mode=0o700)
            fixture(source)
            archive = root / 'bundle.tar.gz'
            with tarfile.open(archive, 'w:gz', format=tarfile.USTAR_FORMAT) as output:
                for name in integration.FILES:
                    output.add(source / name, arcname=name)
            result = integration.unpack_bundle(archive, root / 'unpacked', IMAGE, 'amd64', '123')
            self.assertEqual(set(result['files']), set(integration.FILES))
            self.assertEqual((root / 'unpacked/overlay.test').stat().st_mode & 0o777, 0o700)

    def test_archive_rejects_links_traversal_duplicates_and_extra_files(self):
        for name, kind, duplicate in [('../escape', tarfile.REGTYPE, False), ('overlay.test', tarfile.SYMTYPE, False),
                                      ('extra', tarfile.REGTYPE, False), ('overlay.test', tarfile.REGTYPE, True)]:
            with self.subTest(name=name, kind=kind), tempfile.TemporaryDirectory() as temp:
                root = Path(temp)
                archive = root / 'bundle.tar.gz'
                with tarfile.open(archive, 'w:gz') as output:
                    member = tarfile.TarInfo(name)
                    member.type, member.size = kind, 1 if kind == tarfile.REGTYPE else 0
                    if kind == tarfile.SYMTYPE:
                        member.linkname = '/etc/passwd'
                    output.addfile(member, io.BytesIO(b'x') if kind == tarfile.REGTYPE else None)
                    if duplicate:
                        output.addfile(member, io.BytesIO(b'x'))
                with self.assertRaises(RuntimeError):
                    integration.unpack_bundle(archive, root / 'unpacked', IMAGE, 'amd64', '123')
                self.assertFalse((root / 'escape').exists())


class KernelInvocationTests(unittest.TestCase):
    def run_fixture(self, skipped=False, daemon_mismatch=False, native_failure=False, missing_metadata=False, malformed_metadata=False):
        class Benchmark:
            report = {}
            calls = []
            def run(self, args, **kwargs):
                self.calls.append(args)
                if args[:2] == ['exec', 'buildx_buildkit_fixture0'] and 'sha256sum' in args:
                    return ('0' * 64 if daemon_mismatch and args[-1] == '/usr/bin/buildkitd' else 'c' * 64) + '  ' + args[-1]
                if '-test.v' in args:
                    tests = integration.SUITES[Path(args[-4]).name]
                    records = []
                    if Path(args[-4]).name == 'overlay.test':
                        checkpoints = integration.METADATA_CHECKPOINTS[1:] if missing_metadata else integration.METADATA_CHECKPOINTS
                        records = [{'checkpoint': value, 'status': 'passed'} for value in checkpoints]
                        if native_failure:
                            records[0].update(status='failed', operations={'set:security.capability': {'errno': 95, 'message': 'operation not supported'}})
                    output = '\n'.join('HAKOPOD_BUILDKIT_METADATA ' + json.dumps(value) for value in records) + '\n'
                    output += '\n'.join(('--- SKIP: ' if skipped else '--- PASS: ') + name + ' (0.01s)' for name in tests)
                    if native_failure:
                        if malformed_metadata:
                            output += '\nHAKOPOD_BUILDKIT_METADATA {'
                        output += '\nHAKOPOD_BUILDKIT_METADATA_FIRST_FAILURE ' + json.dumps(records[0])
                        raise RuntimeError('native test command failed:\n' + output)
                    return output
                return ''
        benchmark = Benchmark()
        bundle = {'directory': '/private/staged', 'source': {'sha256': {'buildkitd': 'c' * 64}},
                  'files': {name: {'sha256': 'c' * 64, 'bytes': 128} for name in integration.SUITES}}
        return benchmark, bundle

    def test_fixed_suites_use_only_the_selected_builder_and_explicit_gate(self):
        benchmark, bundle = self.run_fixture()
        result = integration.run_kernel_tests(benchmark, {'name': 'fixture'}, bundle)
        self.assertEqual(result['status'], 'passed')
        self.assertEqual(len(result['tests']), 2)
        self.assertEqual(len(result['tests'][0]['metadata_diagnostics']), len(integration.METADATA_CHECKPOINTS))
        self.assertIn('TestManagedUserXAttrMetadataImport', result['tests'][0]['tests'])
        for call in [v for v in benchmark.calls if '-test.v' in v]:
            self.assertIn('HAKOPOD_BUILDKIT_TEST_OVERLAY=1', call)
            self.assertIn('TMPDIR=/var/lib/buildkit/hakopod-integration/tmp', call)
            self.assertTrue(call[-2].startswith('-test.run=^(') and call[-2].endswith(')$'))
            self.assertEqual(call[-1], '-test.timeout=60s')

    def test_skips_or_mismatched_daemon_never_pass(self):
        for options in ({'skipped': True}, {'daemon_mismatch': True}):
            benchmark, bundle = self.run_fixture(**options)
            with self.subTest(options=options), self.assertRaises(RuntimeError):
                integration.run_kernel_tests(benchmark, {'name': 'fixture'}, bundle)

    def test_native_failure_retains_first_loss_and_later_metadata_checkpoints(self):
        benchmark, bundle = self.run_fixture(native_failure=True)
        with self.assertRaisesRegex(RuntimeError, 'native test command failed'):
            integration.run_kernel_tests(benchmark, {'name': 'fixture'}, bundle)
        result = benchmark.report['kernel_integration']
        self.assertEqual(result['status'], 'failed')
        self.assertEqual(result['tests'][0]['status'], 'failed')
        records = result['tests'][0]['metadata_diagnostics']
        self.assertEqual(len(records), len(integration.METADATA_CHECKPOINTS))
        self.assertEqual(records[0]['checkpoint'], 'plain-volume')
        self.assertEqual(records[0]['operations']['set:security.capability']['errno'], 95)
        self.assertEqual(records[-1]['checkpoint'], 'after-copy-up/metadata-layer')
        self.assertEqual(result['tests'][0]['metadata_first_failure'], [records[0]])
        tail = 'HAKOPOD_BUILDKIT_METADATA_FIRST_FAILURE ' + json.dumps(records[0])
        self.assertEqual(integration.metadata_diagnostics(tail, first_failure=True), [records[0]])

    def test_missing_first_metadata_checkpoint_cannot_pass_from_a_truncated_tail(self):
        benchmark, bundle = self.run_fixture(missing_metadata=True)
        with self.assertRaisesRegex(RuntimeError, 'checkpoints are incomplete'):
            integration.run_kernel_tests(benchmark, {'name': 'fixture'}, bundle)
        self.assertEqual(benchmark.report['kernel_integration']['status'], 'failed')

    def test_malformed_checkpoint_does_not_hide_the_original_error_or_first_failure(self):
        benchmark, bundle = self.run_fixture(native_failure=True, malformed_metadata=True)
        with self.assertRaisesRegex(RuntimeError, 'native test command failed'):
            integration.run_kernel_tests(benchmark, {'name': 'fixture'}, bundle)
        result = benchmark.report['kernel_integration']['tests'][0]
        self.assertEqual(result['metadata_diagnostics_error'], 'JSONDecodeError')
        self.assertEqual(result['metadata_first_failure'][0]['checkpoint'], 'plain-volume')

    def test_metadata_records_reject_unexpected_duplicate_or_oversized_proof(self):
        valid = 'HAKOPOD_BUILDKIT_METADATA ' + json.dumps({'checkpoint': 'plain-volume', 'status': 'passed'})
        for output in (valid + '\n' + valid, valid.replace('plain-volume', 'unknown'),
                       valid.replace('passed', 'skipped'), 'HAKOPOD_BUILDKIT_METADATA ' + 'x' * 4096):
            with self.subTest(output=output[:100]), self.assertRaises((RuntimeError, ValueError)):
                integration.metadata_diagnostics(output)


if __name__ == '__main__':
    unittest.main()
