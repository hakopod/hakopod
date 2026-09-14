import importlib.util
import json
from pathlib import Path
import tempfile
import unittest

spec = importlib.util.spec_from_file_location('probe_metadata', Path(__file__).with_name('probe-metadata.py'))
probe = importlib.util.module_from_spec(spec)
spec.loader.exec_module(probe)


class ProbeMetadataTest(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.directory = Path(self.temp.name)
        self.image = 'ghcr.io/hakopod/hakopod-probe'
        (self.directory / 'probe-image.txt').write_text(self.image + '@sha256:' + 'c' * 64)
        self.index = {'manifests': []}
        for arch, value in [('amd64', 'a'), ('arm64', 'b')]:
            digest = 'sha256:' + value * 64
            self.index['manifests'].append({'digest': digest, 'platform': {'os': 'linux', 'architecture': arch}})
            (self.directory / f'probe-{arch}.txt').write_text(self.image + '@' + digest)
            (self.directory / f'probe-smoke-{arch}.json').write_text(json.dumps(
                {'architecture': arch, 'execution': 'native', 'passed': True}))
        self.save_index()

    def save_index(self):
        (self.directory / 'probe-index.json').write_text(json.dumps(self.index))

    def run_assembly(self):
        return probe.assemble(self.directory, 'v0.1.0-alpha.5', 'd' * 40)

    def test_binds_both_platforms(self):
        result = self.run_assembly()
        self.assertEqual(set(result['platforms']), {'amd64', 'arm64'})
        self.assertEqual(result['image'], self.image + '@sha256:' + 'c' * 64)

    def test_rejects_replaced_image(self):
        self.index['manifests'][0]['digest'] = 'sha256:' + 'e' * 64
        self.save_index()
        with self.assertRaisesRegex(ValueError, 'tested image'):
            self.run_assembly()

    def test_rejects_missing_platform(self):
        self.index['manifests'].pop()
        self.save_index()
        with self.assertRaisesRegex(ValueError, 'exactly two'):
            self.run_assembly()

    def test_rejects_emulated_or_failed_smoke(self):
        for report in [{'architecture': 'arm64', 'execution': 'emulated', 'passed': True},
                       {'architecture': 'arm64', 'execution': 'native', 'passed': False}]:
            (self.directory / 'probe-smoke-arm64.json').write_text(json.dumps(report))
            with self.assertRaisesRegex(ValueError, 'native probe smoke'):
                self.run_assembly()

    def test_rejects_duplicate_platform(self):
        self.index['manifests'][1] = self.index['manifests'][0]
        self.save_index()
        with self.assertRaisesRegex(ValueError, 'duplicate'):
            self.run_assembly()
