#!/usr/bin/env python3
"""Pure bounded qualification regressions; run on the development VM, not locally."""
import copy
import gzip
import importlib.util
from pathlib import Path
import unittest
from unittest.mock import patch


with patch('subprocess.Popen', side_effect=AssertionError('import launched a process')):
    spec = importlib.util.spec_from_file_location('buildkit_qualification', Path(__file__).with_name('buildkit-qualification.py'))
    qualification = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(qualification)


class QualificationTests(unittest.TestCase):
    def test_seed_preserves_binary_capabilities_xattrs_whiteouts_and_hardlinks(self):
        layers = qualification.fixture_layers()
        self.assertLess(sum(map(len, layers)), 64 * 1024)
        rootfs = {}
        for raw in layers:
            layer = qualification.read_metadata_layer(gzip.compress(raw, mtime=0))
            self.assertEqual(layer['diff_id'], qualification.digest(raw))
            qualification.benchmark.apply_layer(rootfs, layer)
        qualification.benchmark.verify_files(rootfs, qualification.expected_files(built=False), 'seed test')
        metadata = rootfs['fixture/metadata']
        self.assertEqual(bytes.fromhex(metadata['xattrs']['security.capability']), qualification.CAPABILITY)
        self.assertEqual(bytes.fromhex(metadata['xattrs']['user.hakopod-proof']), qualification.XATTRS['user.hakopod-proof'])
        self.assertEqual(len(qualification.CAPABILITY), 20)
        self.assertEqual(rootfs['fixture/hard-link']['type'], 'hardlink')
        self.assertNotIn('fixture/deleted', rootfs)
        self.assertNotIn('fixture/opaque/old', rootfs)

    def test_source_payload_and_decompression_are_bounded(self):
        with self.assertRaisesRegex(RuntimeError, '64 KiB'):
            qualification.tar_layer([('fixture/large', 'file', 0o644, b'x' * (64 * 1024), {})])
        data = gzip.compress(qualification.fixture_layers()[0], mtime=0)
        with patch.object(qualification, 'MAX_ARCHIVE', 1024):
            with self.assertRaisesRegex(RuntimeError, 'Uncompressed'):
                qualification.read_metadata_layer(data)

    def test_archive_traversal_and_duplicate_fixture_members_fail(self):
        for entries in ([('../outside', 'file', 0o644, b'bad', {})],
                        [('fixture/repeated', 'file', 0o644, b'one', {}),
                         ('fixture/repeated', 'file', 0o644, b'two', {})]):
            with self.subTest(entries=entries), self.assertRaises(RuntimeError):
                qualification.read_metadata_layer(gzip.compress(qualification.tar_layer(entries)))

    def test_registry_routes_cannot_escape_loopback_or_owned_repositories(self):
        base = 'http://127.0.0.1:23456'
        token = '1' * 8 + '-' + '2' * 4 + '-' + '3' * 4 + '-' + '4' * 4 + '-' + '5' * 12
        location = '/v2/qualify/seed/blobs/uploads/' + token + '?_state=abc_DEF-123&digest=sha256%3A' + 'a' * 64
        self.assertEqual(qualification.registry_url(base, location), base + location)
        self.assertEqual(qualification.registry_url(base, base + location), base + location)
        for bad in ('https://127.0.0.1:23456' + location, 'http://127.0.0.1:23457' + location,
                    'http://other.invalid' + location, '//other.invalid' + location,
                    '/v2/other/seed/manifests/seed', '/v2/qualify/seed/../result/manifests/cold',
                    '/v2/qualify/seed/manifests/seed?token=secret', location + '&digest=sha256%3A' + 'b' * 64,
                    location + '&redirect=elsewhere', location + '#fragment'):
            with self.subTest(value=bad), self.assertRaises(RuntimeError):
                qualification.registry_url(base, bad)

    def test_only_managed_digest_selection_can_start_qualification(self):
        for selected in ('', qualification.benchmark.BUILDKIT, 'ghcr.io/hakopod/buildkit:latest'):
            with self.subTest(selected=selected), self.assertRaises(RuntimeError):
                qualification.Qualification(False, selected)

    def test_restoration_requires_real_cache_hits_and_new_materialization(self):
        reference = '127.0.0.1:23456/qualify/cache:cache'
        steps = [{'name': '[stage 1/3] COPY --link --from=metadata /fixture/ /fixture/', 'cached': True},
                 {'name': "[stage 2/3] RUN printf 'original cache step\\n' > /fixture/run-proof", 'cached': True},
                 {'name': 'importing cache manifest from ' + reference, 'completed': '2026-09-30T00:00:01Z'},
                 {'name': '[stage 3/3] RUN chmod 750 /fixture/metadata && printf restored', 'completed': '2026-09-30T00:00:02Z'},
                 {'name': '[stage 1/3] LINK COPY --link --from=metadata /fixture/ /fixture/', 'cached': True}]
        result = qualification.check_cache_records(steps, True, reference)
        self.assertTrue(result['restore_copy_up_executed'])
        mutations = [lambda values: values[0].update(cached=False),
                     lambda values: values[1].update(cached=False),
                     lambda values: values[2].update(name='importing cache manifest from other'),
                     lambda values: values[2].pop('completed'),
                     lambda values: values[3].update(cached=True),
                     lambda values: values[3].pop('completed'),
                     lambda values: values[4].update(cached=False)]
        for mutate in mutations:
            changed = copy.deepcopy(steps)
            mutate(changed)
            with self.subTest(mutation=mutate), self.assertRaises(RuntimeError):
                qualification.check_cache_records(changed, True, reference)
        cold = [{**value, 'cached': False} for value in steps[:2] + steps[4:]]
        self.assertFalse(qualification.check_cache_records(cold, False, reference)['copy_link_cached'])

    def test_new_layer_must_carry_restored_capability_not_just_reference_old_blobs(self):
        expected = qualification.expected_files(restored=True)
        raw = qualification.tar_layer([
            ('fixture/metadata', 'file', 0o750, qualification.SEED_FILE, qualification.EXPORTED_XATTRS),
            ('fixture/restore-proof', 'file', 0o644, qualification.RESTORED_FILE, {}),
        ])
        layer = qualification.read_metadata_layer(gzip.compress(raw, mtime=0))
        self.assertEqual(layer['entries']['fixture/metadata'], expected['fixture/metadata'])
        changed = copy.deepcopy(expected)
        changed['fixture/metadata']['xattrs'].pop('security.capability')
        with self.assertRaisesRegex(RuntimeError, 'filesystem mismatch'):
            qualification.benchmark.verify_files(changed, expected, 'missing imported capability')

    def test_new_layers_match_upstream_export_scope_without_weakening_seed_checks(self):
        for restored in (False, True):
            self.assertEqual(qualification.expected_files(restored=restored)['fixture/metadata']['xattrs'],
                             {'security.capability': qualification.CAPABILITY.hex()})
        expected = qualification.expected_files(built=False)
        changed = copy.deepcopy(expected)
        changed['fixture/metadata']['xattrs'].pop('user.hakopod-proof')
        with self.assertRaisesRegex(RuntimeError, 'filesystem mismatch'):
            qualification.benchmark.verify_files(changed, expected, 'missing seed user attribute')


if __name__ == '__main__':
    unittest.main()
