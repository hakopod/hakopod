"""Focused source tests for the development Vitess S3 fixture helper."""
import importlib.util
from pathlib import Path
import sys
import time
import unittest

spec = importlib.util.spec_from_file_location('vitess_storage', Path(__file__).with_name('setup-development-vitess-storage.py'))
storage = importlib.util.module_from_spec(spec)
spec.loader.exec_module(storage)


class BoundedKubeTests(unittest.TestCase):
    def setUp(self):
        self.kube = storage.KUBE

    def tearDown(self):
        storage.KUBE = self.kube

    def test_output_limit_terminates_child(self):
        storage.KUBE = [sys.executable, '-c', 'import sys,time;sys.stdout.write("x"*4096);sys.stdout.flush();time.sleep(30)', '--']
        started = time.monotonic()
        with self.assertRaisesRegex(RuntimeError, 'output exceeded'):
            storage.kube([], timeout=2, output_limit=128)
        self.assertLess(time.monotonic()-started, 5)

    def test_timeout_terminates_child(self):
        storage.KUBE = [sys.executable, '-c', 'import time;time.sleep(30)', '--']
        started = time.monotonic()
        with self.assertRaisesRegex(RuntimeError, 'timed out'):
            storage.kube([], timeout=.1, output_limit=128)
        self.assertLess(time.monotonic()-started, 5)


class FixtureConstructionTests(unittest.TestCase):
    def test_service_selects_storage_component_only(self):
        service = storage.storage_service({'namespace':'fixture','labels':{'hakopod.io/development-fixture':storage.OWNER}})
        self.assertEqual(service['spec']['selector'], {
            'hakopod.io/development-fixture':storage.OWNER,
            'app.kubernetes.io/component':'s3-storage',
        })

    def test_fresh_run_uses_unique_ids_and_run_scoped_buckets(self):
        run_id = '0123456789abcdef0123456789abcdef'
        state, identities = storage.fresh_fixture_state(run_id)
        fixtures = list(state['fixtures'].values())
        self.assertEqual(len(fixtures), len(storage.NAMES))
        self.assertEqual(len({item['database_id'] for item in fixtures}), len(fixtures))
        self.assertEqual(len({item['destination']['id'] for item in fixtures}), len(fixtures))
        self.assertTrue(all(item['destination']['bucket'].startswith('hakopod-vitess-'+run_id[:12]+'-') for item in fixtures))
        self.assertEqual(len(identities), len(fixtures))


if __name__ == '__main__':
    unittest.main()
