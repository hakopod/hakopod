import hashlib
import importlib.util
import json
from pathlib import Path
import tempfile
import unittest

spec = importlib.util.spec_from_file_location('publication', Path(__file__).with_name('publication.py'))
publication = importlib.util.module_from_spec(spec); spec.loader.exec_module(publication)


class PublicationTest(unittest.TestCase):
    def test_release_tags_are_explicit_safe_versions(self):
        self.assertEqual(publication.release_version('v0.1.0-alpha.1'), '0.1.0-alpha.1')
        for tag in ('0.1.0', 'vlatest', 'v1.0', 'v1.0.0/../main', 'v1.0.0\n', 'v01.0.0'):
            with self.assertRaises(ValueError): publication.release_version(tag)

    def test_manifest_rejects_missing_extra_changed_and_symlink_assets(self):
        with tempfile.TemporaryDirectory() as temporary:
            directory = Path(temporary)
            asset = directory / 'artifact.tar.gz'; asset.write_bytes(b'fixture')
            (directory / 'SHA256SUMS').write_text(publication.manifest(directory))
            publication.verify(directory)
            asset.write_bytes(b'changed')
            with self.assertRaises(ValueError): publication.verify(directory)
            asset.write_bytes(b'fixture')
            (directory / 'extra').write_bytes(b'extra')
            with self.assertRaises(ValueError): publication.verify(directory)
            (directory / 'extra').unlink()
            asset.unlink(); asset.symlink_to('/dev/null')
            with self.assertRaises(ValueError): publication.verify(directory)

    def test_smoke_reports_must_match_both_architectures_and_exact_bytes(self):
        with tempfile.TemporaryDirectory() as temporary:
            directory = Path(temporary)
            (directory / 'artifact.tar.gz').write_bytes(b'fixture')
            (directory / 'SHA256SUMS').write_text(publication.manifest(directory))
            report = {'artifact_sha256': {'artifact.tar.gz': hashlib.sha256(b'fixture').hexdigest()}}
            for arch in ('amd64', 'arm64'):
                report['checks'] = [{'architecture': arch, 'passed': True}]
                (directory / ('installer-smoke-' + arch + '.json')).write_text(json.dumps(report))
            publication.finalize(directory)
            publication.verify(directory)
            report['artifact_sha256']['artifact.tar.gz'] = '0' * 64
            (directory / 'installer-smoke-arm64.json').write_text(json.dumps(report))
            # Reflect the report in the manifest so the report-to-artifact binding
            # itself, rather than the ordinary checksum test, must reject it.
            (directory / 'SHA256SUMS').write_text(publication.manifest(directory))
            with self.assertRaisesRegex(ValueError, 'Smoke evidence'):
                publication.finalize(directory)


if __name__ == '__main__':
    unittest.main()
