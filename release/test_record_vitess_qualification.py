import copy
import importlib.util
import json
from pathlib import Path
import unittest

import test_verify_vitess_runtime as verification_fixture


spec = importlib.util.spec_from_file_location('vitess_record', Path(__file__).with_name('record-vitess-qualification.py'))
record = importlib.util.module_from_spec(spec)
spec.loader.exec_module(record)


class VitessQualificationRecordTest(unittest.TestCase):
    def setUp(self):
        fixture = verification_fixture.VitessReleaseVerificationTest('test_accepts_exact_native_source_and_binaries')
        fixture.setUp()
        self.addCleanup(fixture.doCleanups)
        self.fixture = fixture
        self.sources = fixture.manifest['source_files']
        self.images = fixture.acceptance['images']
        self.verifier_hash = 'a' * 64
        self.runner_hash = 'b' * 64
        self.reports = []
        for case, name in record.CASES.items():
            names = [name]
            if case == 'lifecycle':
                names += [name + '/standalone', name + '/cluster']
            events = [{'Action': action, 'Package': 'github.com/hakopod/hakopod/internal/cluster', 'Test': test}
                      for test in names for action in ('run', 'pass')]
            self.reports.append({'schema_version': 1, 'case': case, 'test': name,
                'context': 'k3d-hakopod-dev', 'execution': 'native', 'platform': 'linux/amd64',
                'passed': True, 'exit_code': 0, 'limit_error': '', 'source_files': self.sources,
                'source_files_after': self.sources, 'images': self.images,
                'log_sha256': 'c' * 64, 'runner_sha256': self.runner_hash,
                'verifier_sha256': self.verifier_hash, 'elapsed_seconds': 120.25,
                'environment': verification_fixture.environment_fixture(case, self.images),
                'test_events': events, 'failed_tests': [], 'passed_tests': sorted(names),
                'raw_log': 'private fixture diagnostic must not be copied'})

    def collect(self, reports=None):
        return record.collect_reports(self.reports if reports is None else reports,
            self.sources, self.images, self.verifier_hash, self.runner_hash, verification_fixture.ETCD_IMAGE)

    def test_aggregates_each_case_and_only_public_evidence(self):
        result = self.collect()
        self.assertTrue(result['passed'])
        self.assertEqual({item['case'] for item in result['attempts']}, set(record.CASES))
        self.assertTrue(record.VERIFIER['REQUIRED_TESTS'].issubset(result['passed_tests']))
        self.assertNotIn('private fixture diagnostic', json.dumps(result))
        self.assertTrue(all(set(event) == {'Action', 'Package', 'Test'} for event in result['test_events']))
        self.assertEqual(record.CASES['scale'], 'TestManagedVitessScaleLive')

    def test_rejects_failed_bounded_foreign_or_changed_runs(self):
        changes = [{'schema_version': 2}, {'passed': False}, {'exit_code': 1}, {'exit_code': False},
                   {'limit_error': 'timeout'}, {'context': 'operator-cluster'}, {'execution': 'emulated'},
                   {'platform': 'linux/arm64'}, {'source_files': {}}, {'source_files_after': {}},
                   {'images': {}}, {'verifier_sha256': 'd' * 64}, {'runner_sha256': 'd' * 64},
                   {'environment': None},
                   {'failed_tests': ['TestManagedVitessLive']}, {'log_sha256': 'invalid'},
                   {'elapsed_seconds': None}, {'elapsed_seconds': True}, {'elapsed_seconds': -1},
                   {'elapsed_seconds': float('nan')}, {'elapsed_seconds': float('inf')},
                   {'elapsed_seconds': 95 * 60 + 1}]
        for change in changes:
            with self.subTest(change=change):
                reports = copy.deepcopy(self.reports)
                reports[0].update(change)
                with self.assertRaises(ValueError):
                    self.collect(reports)

    def test_rejects_missing_duplicated_and_malformed_cases(self):
        for reports in (self.reports[:-1], self.reports + [self.reports[0]],
                        [self.reports[0], *self.reports[:-1]],
                        [None, *self.reports[1:]],
                        [dict(self.reports[0], case=[]), *self.reports[1:]]):
            with self.subTest(reports=reports):
                with self.assertRaises(ValueError):
                    self.collect(reports)

    def test_rejects_missing_failure_duplicate_or_raw_test_events(self):
        events = self.reports[0]['test_events']
        variants = [[], [event for event in events if event['Action'] == 'pass'], events[:-1],
                    [dict(events[0], Action='fail'), *events[1:]],
                    [dict(events[0], Action='skip'), *events[1:]],
                    [*events, events[0], events[1]],
                    [dict(events[0], Output='private log'), *events[1:]],
                    [dict(events[0], Package='other/package'), *events[1:]]]
        for bad in variants:
            with self.subTest(events=bad):
                reports = copy.deepcopy(self.reports)
                reports[0]['test_events'] = bad
                with self.assertRaises(ValueError):
                    self.collect(reports)

    def test_rejects_aggregate_success_assigned_to_wrong_case(self):
        reports = copy.deepcopy(self.reports)
        reports[0]['test_events'], reports[1]['test_events'] = reports[1]['test_events'], reports[0]['test_events']
        with self.assertRaises(ValueError):
            self.collect(reports)
        for change in ({'test': 'TestManagedVitessRecoveryLive'}, {'passed_tests': []}):
            reports = copy.deepcopy(self.reports)
            reports[0].update(change)
            with self.assertRaises(ValueError):
                self.collect(reports)

    def test_assembles_and_revalidates_exact_source_patches_and_binaries(self):
        source = self.fixture.root
        build = source.parent / 'build'
        output = source.parent / 'recorded-qualification'
        for kind, paths in record.VERIFIER['BINARIES'].items():
            directory = build / ('runtime' if kind == 'runtime' else 'controller')
            (directory / 'bin').mkdir(parents=True)
            for path in paths:
                (directory / 'bin' / Path(path).name).write_bytes(self.fixture.binary_data[path])
            patch = self.fixture.directory / ('runtime-upstream.patch' if kind == 'runtime' else 'operator-upstream.patch')
            (directory / 'upstream.patch').write_bytes(patch.read_bytes())
        (build / 'binary-sha256.txt').write_bytes((self.fixture.directory / 'binary-sha256.txt').read_bytes())
        reports = []
        for report in self.reports:
            report = copy.deepcopy(report)
            report['verifier_sha256'] = record.VERIFIER['file_hash'](source / 'release/verify-vitess-runtime.py')
            report['runner_sha256'] = record.VERIFIER['file_hash'](source / 'scripts/run-development-vitess-acceptance.py')
            path = source.parent / (report['case'] + '.json')
            path.write_text(json.dumps(report))
            reports.append(path)
        manifest = record.assemble(source, build, reports, output)
        self.assertEqual(record.VERIFIER['validate_metadata'](output, source), manifest)
        self.assertNotIn('private fixture diagnostic', (output / 'native-acceptance.json').read_text())
        self.assertEqual(set(path.name for path in output.iterdir()), {'manifest.json', *manifest['files']})
        with self.assertRaisesRegex(ValueError, 'fresh'):
            record.assemble(source, build, reports, output)


if __name__ == '__main__':
    unittest.main()
