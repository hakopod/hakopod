from pathlib import Path
import json
import runpy
import unittest
from unittest.mock import patch

policy = runpy.run_path(str(Path(__file__).with_name('upgrade-paths.py')))


class UpgradePolicyTests(unittest.TestCase):
    def test_release_requires_explicit_policy(self):
        with self.assertRaisesRegex(ValueError, 'Declare upgrade source'):
            policy['sources']('0.1.0-alpha.999', required=True)
        self.assertEqual(policy['sources']('0.1.0-dev'), [])

    def test_every_declared_source_has_native_architecture_and_database_coverage(self):
        versions = json.loads(Path(__file__).with_name('upgrade-paths.json').read_text())
        for version in versions:
            with self.subTest(version=version):
                sources = policy['sources'](version, required=True)
                cases = policy['matrix'](version)['include']
                self.assertLessEqual(len(cases), 12)
                self.assertEqual(len(cases), 4 * (len(sources) + 1))
                for source in ['', *sources]:
                    self.assertEqual({(c['arch'], c['mode']) for c in cases if c['source'] == source},
                                     {('amd64', 'managed'), ('arm64', 'managed'), ('amd64', 'local'), ('amd64', 'external')})

    def test_alpha12_covers_published_upgrade_sources(self):
        self.assertEqual(policy['sources']('0.1.0-alpha.12', required=True),
                         ['0.1.0-alpha.9', '0.1.0-alpha.10'])

    def test_alpha13_covers_published_upgrade_sources(self):
        self.assertEqual(policy['sources']('0.1.0-alpha.13', required=True),
                         ['0.1.0-alpha.10', '0.1.0-alpha.12'])


    def test_alpha14_covers_published_upgrade_sources(self):
        self.assertEqual(policy['sources']('0.1.0-alpha.14', required=True),
                         ['0.1.0-alpha.12', '0.1.0-alpha.13'])

    def test_current_release_uses_last_two_published_versions(self):
        self.assertEqual(policy['sources']('0.1.0-alpha.29', required=True),
                         ['0.1.0-alpha.26', '0.1.0-alpha.27'])
        self.assertEqual(len(policy['matrix']('0.1.0-alpha.29')['include']), 12)
        with self.assertRaisesRegex(ValueError, 'Declare upgrade source'):
            policy['sources']('0.1.0-alpha.28', required=True)

    def test_rejects_accumulated_duplicate_and_self_upgrade_sources(self):
        for sources in [['one', 'two', 'three'], ['one', 'one'], ['candidate']]:
            with self.subTest(sources=sources), patch.object(Path, 'read_text', return_value=json.dumps({'candidate': sources})):
                with self.assertRaises(ValueError):
                    policy['sources']('candidate', required=True)
