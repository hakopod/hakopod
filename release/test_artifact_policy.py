"""Public artifacts must retain the closed signup build policy."""
import importlib.util
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[1]


def module(name):
    spec = importlib.util.spec_from_file_location(name, ROOT / 'release' / (name + '.py'))
    value = importlib.util.module_from_spec(spec); spec.loader.exec_module(value)
    return value


build = module('build')
verify = module('verify-archives')


class ArtifactPolicyTests(unittest.TestCase):
    def test_public_build_explicitly_overrides_ambient_cloud_tag(self):
        with patch.dict(os.environ, GOFLAGS='-tags=hakopod_cloud'):
            for command in ('hakopod', 'hakopod-server'):
                args = build.build_command(command, '/tmp/fixture', '0.1.0-alpha.2')
                self.assertIn('-tags=hakopod_selfhosted', args)
                self.assertNotIn('-tags=hakopod_cloud', args)

    def test_binary_metadata_requires_exact_public_policy(self):
        result = verify.selfhosted_build_settings('fixture: go1.26\n\tbuild\t-tags=hakopod_selfhosted\n')
        self.assertFalse(result['public_signup'])
        for tags in ('', 'hakopod_cloud', 'hakopod_selfhosted,hakopod_cloud', 'unrelated'):
            with self.subTest(tags=tags), self.assertRaises(ValueError):
                verify.selfhosted_build_settings('\tbuild\t-tags=' + tags + '\n')

    @unittest.skipUnless(shutil.which('go'), 'Go compiler required to inspect a real build')
    def test_go_records_explicit_tag_despite_cloud_goflags(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            (root / 'go.mod').write_text('module fixture.example/policy\n\ngo 1.24\n')
            (root / 'main.go').write_text('package main\nfunc main() {}\n')
            binary = root / 'fixture'
            env = dict(os.environ, GOFLAGS='-tags=hakopod_cloud', GOWORK='off', CGO_ENABLED='0', GOMAXPROCS='2', GOMEMLIMIT='256MiB')
            args = build.build_command('hakopod-server', binary, '0.1.0-alpha.2')
            args[-1] = '.'
            subprocess.run(args, cwd=root, env=env, check=True, capture_output=True, timeout=90)
            metadata = subprocess.check_output(['go', 'version', '-m', str(binary)], env=env, text=True, timeout=10)
            self.assertFalse(verify.selfhosted_build_settings(metadata)['public_signup'])


if __name__ == '__main__': unittest.main()
