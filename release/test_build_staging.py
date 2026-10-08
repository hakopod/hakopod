"""Keep local Go replacements in release snapshots and source provenance."""
import hashlib
import importlib.util
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location('release_build', Path(__file__).with_name('build.py'))
builder = importlib.util.module_from_spec(spec)
spec.loader.exec_module(builder)


class BuildStagingTest(unittest.TestCase):
    def test_vendored_replacement_is_fingerprinted_and_staged(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary) / 'repo'
            contents = {
                'go.mod': 'module fixture\nreplace github.com/sijms/go-ora/v3 => ./third_party/go-ora-v3\n',
                'go.sum': '', 'web/package.json': '{}', 'web/pnpm-lock.yaml': '',
                'LICENSE': 'license', 'NOTICE': 'notice',
                'third_party/go-ora-v3/go.mod': 'module github.com/sijms/go-ora/v3\n',
                'third_party/go-ora-v3/network/session.go': 'package network\n',
                'third_party/go-ora-v3/LICENSE': 'vendored license',
            }
            for name, content in contents.items():
                path = root / name
                path.parent.mkdir(parents=True, exist_ok=True)
                path.write_text(content)
            with patch.object(builder, 'ROOT', root):
                before, manifest = builder.fingerprint()
                stage = Path(temporary) / 'stage'
                builder.stage_source(stage, manifest)
                for name in contents:
                    self.assertEqual((stage / name).read_bytes(), (root / name).read_bytes())
                    self.assertEqual(manifest[name], hashlib.sha256((root / name).read_bytes()).hexdigest())
                (root / 'third_party/go-ora-v3/network/session.go').write_text('package network\n// updated\n')
                self.assertNotEqual(before, builder.fingerprint()[0])


if __name__ == '__main__':
    unittest.main()
