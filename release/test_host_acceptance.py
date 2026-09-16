"""Release publication requires native host evidence for the exact shipped bytes."""
import copy
import hashlib
import json
from pathlib import Path
import runpy
import subprocess
import tempfile
import unittest
from unittest.mock import patch

MODULE = runpy.run_path(str(Path(__file__).with_name('host-acceptance.py')))


class HostEvidenceTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.directory = Path(self.temp.name)
        for name in ('release.tar.gz', 'installer.sh'):
            (self.directory / name).write_bytes(b'actual release bytes')
        hashes = {p.name: hashlib.sha256(p.read_bytes()).hexdigest() for p in self.directory.iterdir()}
        self.reports = [dict(status='passed', version='0.1.0', source_revision='commit',
                             artifact_sha256=hashes, machine=machine, mode=mode)
                        for machine, mode in [('x86_64', 'managed'), ('aarch64', 'managed'),
                                              ('x86_64', 'local'), ('x86_64', 'external')]]

    def verify(self, reports=None):
        MODULE['verify_reports'](self.directory, reports or self.reports, '0.1.0', 'commit')

    def test_all_modes_and_native_architectures_pass(self):
        self.verify()

    def test_declared_upgrade_requires_all_native_source_cases(self):
        (self.directory / 'upgrade.json').write_text(json.dumps({'from_versions': ['0.0.9']}))
        with self.assertRaisesRegex(ValueError, 'incomplete'): self.verify()
        upgrades = copy.deepcopy(self.reports)
        for report in upgrades:
            report.update(upgrade_from='0.0.9', upgrade_method='bootstrap', source_artifact_sha256={'source.tar.gz': 'a'*64})
        self.verify(self.reports + upgrades)
        for index in range(4):
            with self.assertRaisesRegex(ValueError, 'incomplete'):
                self.verify(self.reports + upgrades[:index] + upgrades[index+1:])

    def test_upgrade_requires_published_source_evidence(self):
        (self.directory / 'upgrade.json').write_text(json.dumps({'from_versions': ['0.0.9']}))
        upgrades = [dict(report, upgrade_from='0.0.9') for report in self.reports]
        with self.assertRaisesRegex(ValueError, 'published source'): self.verify(self.reports + upgrades)

    def test_modified_shipped_bytes_fail(self):
        (self.directory / 'release.tar.gz').write_bytes(b'changed after testing')
        with self.assertRaises(ValueError):
            self.verify()

    def test_missing_arm_or_database_mode_fails(self):
        for index in range(4):
            with self.subTest(index=index), self.assertRaises(ValueError):
                self.verify(self.reports[:index] + self.reports[index + 1:])

    def test_wrong_commit_version_or_failed_host_fails(self):
        for field, value in [('source_revision', 'other'), ('version', '0.2.0'), ('status', 'failed')]:
            reports = copy.deepcopy(self.reports)
            reports[0][field] = value
            with self.subTest(field=field), self.assertRaises(ValueError):
                self.verify(reports)

    def test_uncovered_installer_fails(self):
        reports = copy.deepcopy(self.reports)
        del reports[0]['artifact_sha256']['installer.sh']
        with self.assertRaises(ValueError):
            self.verify(reports)

    def test_developer_machine_guard_fails_before_host_changes(self):
        with patch.dict('os.environ', {}, clear=True), self.assertRaises(RuntimeError):
            MODULE['guard']()

    def test_unrelated_failure_cannot_prove_database_rejection(self):
        result = subprocess.CompletedProcess([], 1, '', 'apt-get update failed')
        with self.assertRaises(RuntimeError):
            MODULE['require_rejection'](result, 'requires an empty public schema', False)

    def test_rejection_requires_expected_diagnostic_and_no_mutation(self):
        result = subprocess.CompletedProcess([], 1, '', 'requires an empty public schema')
        MODULE['require_rejection'](result, 'requires an empty public schema', False)
        with self.assertRaises(RuntimeError):
            MODULE['require_rejection'](result, 'requires an empty public schema', True)


if __name__ == '__main__':
    unittest.main()
