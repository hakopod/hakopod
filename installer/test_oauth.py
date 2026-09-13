import contextlib
import importlib.util
import io
import json
import os
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location('oauth', Path(__file__).with_name('oauth.py'))
oauth = importlib.util.module_from_spec(spec)
spec.loader.exec_module(oauth)


class OAuthTests(unittest.TestCase):
    def test_provider_pair_validation_and_env(self):
        self.assertEqual(oauth.configured({}, {}), {})
        for name in oauth.PROVIDERS:
            prefix = 'HAKOPOD_' + name.upper()
            values = oauth.configured({}, {prefix + '_CLIENT_ID': 'configured-id', prefix + '_CLIENT_SECRET': 'private-value'})
            self.assertEqual(values[name]['client_secret_file'], '@environment')
            self.assertNotIn('private-value', json.dumps(values))
            for env in ({prefix + '_CLIENT_ID': 'id'}, {prefix + '_CLIENT_SECRET': 'secret'}, {prefix + '_CLIENT_ID': 'id', prefix + '_CLIENT_SECRET': 'secret', prefix + '_CLIENT_SECRET_FILE': '/secret'}):
                with self.assertRaises(ValueError):
                    oauth.configured({}, env)
        with self.assertRaises(ValueError):
            oauth.configured({'unknown': {}}, {})

    def test_private_file_handling_and_resume_preserves_rotation(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / 'secrets').mkdir()
            source = root / 'input'
            source.write_text('sensitive-value\n')
            source.chmod(0o600)
            values = {'google': {'client_id': 'client', 'client_secret_file': str(source)}}
            oauth.preserve(values, root, {})
            rendered = (root / 'oauth.env').read_text()
            self.assertIn('HAKOPOD_GOOGLE_CLIENT_SECRET_FILE=', rendered)
            self.assertNotIn('sensitive-value', rendered)
            target = root / 'secrets/oauth-google-secret'
            self.assertEqual(target.stat().st_mode & 0o777, 0o600)
            target.write_text('rotated\n')
            oauth.preserve(values, root, {})
            self.assertEqual(target.read_text(), 'rotated\n')
            source.chmod(0o644)
            with self.assertRaises(ValueError): oauth.secret_file(source)
            link = root / 'link'
            link.symlink_to(source)
            with self.assertRaises(ValueError): oauth.secret_file(link)

    def test_hidden_input_and_optional_providers(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / 'config.json'
            path.write_text(json.dumps({'dashboard_origin': 'https://admin.example.test'}))
            captured = io.StringIO()
            with patch.dict(os.environ, {}, clear=True), patch('builtins.input', side_effect=['yes', 'my-client', '', 'no', 'no']), patch('getpass.getpass', return_value='secret-never-print'), contextlib.redirect_stdout(captured):
                oauth.prompt(path, directory)
            self.assertNotIn('secret-never-print', captured.getvalue())
            self.assertNotIn('secret-never-print', path.read_text())
            self.assertIn('/auth/oauth/google/callback', captured.getvalue())
            values = json.loads(path.read_text())['oauth']
            self.assertEqual(set(values), {'google'})
            self.assertEqual(oauth.secret_file(values['google']['client_secret_file']), 'secret-never-print')

    def test_preconfigured_provider_is_not_prompted_or_overwritten(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / 'config.json'
            path.write_text(json.dumps({'dashboard_origin': 'http://localhost:3000'}))
            env = {'HAKOPOD_GOOGLE_CLIENT_ID': 'existing', 'HAKOPOD_GOOGLE_CLIENT_SECRET': 'hidden'}
            with patch.dict(os.environ, env, clear=True), patch('builtins.input', side_effect=['no', 'no']) as ask, contextlib.redirect_stdout(io.StringIO()):
                oauth.prompt(path, directory)
            self.assertEqual(ask.call_count, 2)
            self.assertNotIn('hidden', path.read_text())


if __name__ == '__main__': unittest.main()
