from pathlib import Path
import runpy
import unittest

policy = runpy.run_path(str(Path(__file__).with_name('upgrade-paths.py')))


class UpgradePolicyTests(unittest.TestCase):
    def test_release_requires_explicit_policy(self):
        with self.assertRaisesRegex(ValueError, 'Declare upgrade source'):
            policy['sources']('0.1.0-alpha.999', required=True)
        self.assertEqual(policy['sources']('0.1.0-dev'), [])

    def test_every_declared_source_has_native_architecture_and_database_coverage(self):
        version = '0.1.0-alpha.10'
        sources = policy['sources'](version, required=True)
        self.assertIn('0.1.0-alpha.8', sources)
        cases = policy['matrix'](version)['include']
        for source in ['', *sources]:
            self.assertEqual({(c['arch'], c['mode']) for c in cases if c['source'] == source},
                             {('amd64', 'managed'), ('arm64', 'managed'), ('amd64', 'local'), ('amd64', 'external')})
