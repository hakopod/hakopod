#!/usr/bin/env python3
"""Installer/database integration with every installation path redirected to fixtures."""

import contextlib
import hashlib
import importlib.util
import io
import json
import os
from pathlib import Path
import stat
import sys
import tempfile
import unittest
from unittest.mock import patch

HERE = Path(__file__).resolve().parent
spec = importlib.util.spec_from_file_location('database_integration_host', HERE / 'host.py')
host = importlib.util.module_from_spec(spec)
sys.modules[spec.name] = host
spec.loader.exec_module(host)

PASSWORD = 'fixture-password:for-integration-only'
ENCODED_PASSWORD = 'fixture-password%3Afor-integration-only'
LOCAL_URL = 'postgresql://hakopod:' + ENCODED_PASSWORD + '@127.0.0.1:5432/hakopod'
EXTERNAL_URL = 'postgresql://hakopod:' + ENCODED_PASSWORD + '@db.example.test:5432/hakopod?sslmode=verify-full'
INSTALLATION_ID = 'a' * 32


class DatabaseIntegrationTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name).resolve()
        self.host_root = self.root / 'host'
        for parent in ('etc', 'opt', 'var/lib'):
            (self.host_root / parent).mkdir(parents=True, exist_ok=True)
        self.url_file = self.root / 'input-url'
        self.url_file.write_text(LOCAL_URL + '\n')
        self.url_file.chmod(0o600)
        self.config = dict(host.DEFAULTS, app_domain='apps.example.test', node_ip='192.0.2.10',
                           supervisor_host='192.0.2.10', acme='off', database_mode='local',
                           database_url_file=str(self.url_file))
        self.artifacts = self.root / 'artifacts'
        self.artifacts.mkdir()
        checksums = []
        for suffix in ('linux_arm64', 'dashboard'):
            name = 'hakopod_0.1.0-dev_' + suffix + '.tar.gz'
            content = ('isolated fixture ' + suffix).encode()
            (self.artifacts / name).write_bytes(content)
            checksums.append(hashlib.sha256(content).hexdigest() + '  ' + name + '\n')
        (self.artifacts / 'SHA256SUMS').write_text(''.join(checksums))

        # The real write/digest/permission functions run against temporary files.
        # No /etc, /opt, /var/lib, service accounts or processes are modified.
        actual_open, actual_chmod = os.open, os.chmod

        def fixture_open(path, flags, *args, **kwargs):
            target = self.map_path(path)
            if flags & (os.O_WRONLY | os.O_RDWR | os.O_CREAT):
                self.assertTrue(Path(target).is_relative_to(self.root), 'Write escaped isolated test fixtures')
            return actual_open(target, flags, *args, **kwargs)

        def fixture_chmod(path, *args, **kwargs):
            target = self.map_path(path)
            self.assertTrue(Path(target).is_relative_to(self.root), 'chmod escaped isolated test fixtures')
            return actual_chmod(target, *args, **kwargs)

        self.enterContext(patch.object(host, 'Path', side_effect=lambda path='.': Path(self.map_path(path))))
        self.enterContext(patch.object(host.os, 'open', side_effect=fixture_open))
        self.enterContext(patch.object(host.os, 'chmod', side_effect=fixture_chmod))

    def map_path(self, path):
        if isinstance(path, int):
            return path
        text = os.fspath(path)
        for prefix in ('/etc/hakopod', '/opt/hakopod', '/var/lib/hakopod'):
            if text == prefix or text.startswith(prefix + '/'):
                return self.host_root / text.lstrip('/')
        return path

    def installed(self, name):
        return Path(self.map_path('/etc/hakopod/' + name))

    def prepare(self, resume=False):
        captured = io.StringIO()
        with contextlib.redirect_stdout(captured):
            host.prepare(self.config, 'arm64', self.artifacts, resume)
        self.assertNotIn(PASSWORD, captured.getvalue())
        self.assertNotIn(ENCODED_PASSWORD, captured.getvalue())
        return json.loads(self.installed('installation.json').read_text())

    def facts(self, **changes):
        return dict(dict(version_num=170005, database='hakopod', username='hakopod', writable=True,
                         owner=True, connect=True, schema_access=True, default_schema='public',
                         schema_empty=True, tls=True), **changes)

    def test_existing_modes_render_without_postgres_resources_or_literal_database_uri(self):
        for mode in ('local', 'external'):
            with self.subTest(mode=mode):
                self.config['database_mode'] = mode
                self.url_file.write_text(LOCAL_URL if mode == 'local' else EXTERNAL_URL)
                session = self.installed('secrets/session-secret')
                session.parent.mkdir(parents=True, exist_ok=True)
                session.write_text('b' * 64)
                session.chmod(0o600)
                rendered = self.root / ('render-' + mode)
                host.render(self.config, 'arm64', INSTALLATION_ID, rendered)
                self.assertFalse((rendered / 'postgres.json').exists())
                self.assertFalse(self.installed('secrets/postgres-password').exists())
                api = (rendered / 'api.env').read_text()
                self.assertIn('HAKOPOD_DATABASE_URL_FILE="/etc/hakopod/secrets/database-url"', api)
                self.assertIn('HAKOPOD_MANAGED_POSTGRES="false"', api)
                self.assertNotIn('HAKOPOD_DATABASE_URL=', api)
                for path in rendered.iterdir():
                    text = path.read_text()
                    self.assertNotIn(PASSWORD, text)
                    self.assertNotIn(ENCODED_PASSWORD, text)
                    self.assertNotIn('postgresql://', text)
                service = (rendered / 'hakopod-api.service').read_text()
                self.assertIn('User=hakopod-api', service)
                self.assertIn('PrivateTmp=yes', service)
                self.assertIn('ProtectHome=yes', service)
                self.assertIn('ProtectSystem=strict', service)

    def test_prepare_preserves_source_creates_private_matching_copies_and_digest_marker(self):
        original = self.url_file.read_bytes()
        state = self.prepare()
        self.assertEqual(self.url_file.read_bytes(), original)
        canonical = self.installed('database-url')
        runtime = self.installed('secrets/database-url')
        self.assertEqual(canonical.read_bytes(), runtime.read_bytes())
        self.assertEqual(stat.S_IMODE(canonical.stat().st_mode), 0o600)
        self.assertEqual(stat.S_IMODE(runtime.stat().st_mode), 0o600)
        self.assertNotIn(PASSWORD, json.dumps(state))
        self.assertNotIn(ENCODED_PASSWORD, self.installed('config.json').read_text())
        self.assertEqual(set(state['database_files']), {'/etc/hakopod/database-url', '/etc/hakopod/secrets/database-url'})
        host.verify_database_state(state)
        self.assertFalse(self.installed('secrets/postgres-password').exists())
        self.assertFalse(Path(self.map_path('/var/lib/hakopod/postgres')).exists())

    def test_custom_ca_is_copied_once_rewritten_and_bound_to_resume_digest(self):
        source_ca = self.root / 'operator-ca.pem'
        source_ca.write_text('-----BEGIN CERTIFICATE-----\nfixture-only\n-----END CERTIFICATE-----\n')
        source_ca.chmod(0o644)
        self.url_file.write_text(EXTERNAL_URL)
        self.config.update(database_mode='external', database_ca_file=str(source_ca))
        state = self.prepare()
        installed_ca = self.installed('database-ca.pem')
        self.assertEqual(installed_ca.read_bytes(), source_ca.read_bytes())
        self.assertEqual(stat.S_IMODE(installed_ca.stat().st_mode), 0o644)
        uri = self.installed('database-url').read_text().strip()
        connection = host.database.parse_url(uri, 'external', inspect_files=False)
        self.assertEqual(connection.ca_file, '/etc/hakopod/database-ca.pem')
        self.assertIn('/etc/hakopod/database-ca.pem', state['database_files'])
        self.url_file.unlink()
        source_ca.unlink()
        self.assertEqual(self.prepare(resume=True)['database_files'], state['database_files'])
        installed_ca.write_text('changed certificate')
        with self.assertRaisesRegex(ValueError, 'changed'):
            host.verify_resume(self.config, 'arm64', self.artifacts)

    def test_resume_uses_saved_copies_after_source_disappears_and_keeps_api_mode_0400(self):
        state = self.prepare()
        self.installed('secrets/database-url').chmod(0o400)
        self.url_file.unlink()
        before = {name: self.installed(name).read_bytes() for name in ('database-url', 'secrets/database-url', 'secrets/setup-token')}
        self.assertEqual(host.verify_resume(self.config, 'arm64', self.artifacts)['id'], state['id'])
        resumed = self.prepare(resume=True)
        self.assertEqual(resumed['id'], state['id'])
        for name, content in before.items():
            self.assertEqual(self.installed(name).read_bytes(), content)
        self.assertEqual(stat.S_IMODE(self.installed('secrets/database-url').stat().st_mode), 0o400)
        with patch.object(host.database, '_capture', side_effect=[
                (0, json.dumps(self.facts(schema_empty=False)).encode(), b''),
                (0, b'psql (PostgreSQL) 15.10\n', b'')]):
            result = host.database.preflight(self.config, resume=True, installed_url_path='/etc/hakopod/database-url')
        self.assertEqual(result['server_major'], 17)

    def test_both_copies_are_checked_before_resume_and_never_rewritten_after_tampering(self):
        self.prepare()
        for name in ('database-url', 'secrets/database-url'):
            with self.subTest(name=name):
                target = self.installed(name)
                before = target.read_bytes()
                target.write_text('changed')
                with self.assertRaisesRegex(ValueError, 'changed'):
                    host.verify_resume(self.config, 'arm64', self.artifacts)
                with self.assertRaisesRegex(ValueError, 'changed'):
                    self.prepare(resume=True)
                self.assertEqual(target.read_text(), 'changed')
                target.write_bytes(before)

    def test_saved_credential_symlink_or_public_mode_is_refused(self):
        state = self.prepare()
        target = self.installed('secrets/database-url')
        target.chmod(0o644)
        with self.assertRaisesRegex(ValueError, 'Secret file'):
            host.verify_database_state(state)
        target.chmod(0o600)
        content = target.read_bytes()
        target.unlink()
        outside = self.root / 'other-private-file'
        outside.write_bytes(content)
        outside.chmod(0o600)
        target.symlink_to(outside)
        with self.assertRaisesRegex(ValueError, 'symlink'):
            host.verify_database_state(state)

    def test_changed_mode_or_artifact_cannot_adopt_saved_credentials(self):
        self.prepare()
        with self.assertRaisesRegex(ValueError, 'Resume inputs'):
            host.verify_resume(dict(self.config, database_mode='managed', database_url_file=''), 'arm64', self.artifacts)
        bundle = self.artifacts / 'hakopod_0.1.0-dev_linux_arm64.tar.gz'
        bundle.write_bytes(b'changed artifact')
        with self.assertRaisesRegex(ValueError, 'checksum'):
            host.verify_resume(self.config, 'arm64', self.artifacts)

    def test_preflight_verifies_owned_marker_before_connecting_to_nonempty_database(self):
        self.prepare()
        self.url_file.unlink()
        response = json.dumps([{'addr_info': [{'local': self.config['node_ip']}]}])
        with patch.object(host, 'platform_preflight'), patch.object(host.shutil, 'which', return_value='/fixture/tool'), \
                patch.object(host.subprocess, 'check_output', return_value=response), \
                patch.object(host.database, 'preflight', return_value={'endpoint': '127.0.0.1:5432/hakopod', 'server_version': '17.5'}) as probe:
            with contextlib.redirect_stdout(io.StringIO()):
                host.preflight(self.config, 'arm64', True, self.artifacts)
            probe.assert_called_once_with(self.config, resume=True, installed_url_path='/etc/hakopod/database-url')
            probe.reset_mock()
            self.installed('database-url').write_text('tampered')
            with self.assertRaisesRegex(ValueError, 'changed'):
                host.preflight(self.config, 'arm64', True, self.artifacts)
            probe.assert_not_called()

    def test_interrupted_prepare_rechecks_original_source_as_fresh_database(self):
        with patch.object(host, 'preserve_database', side_effect=RuntimeError('simulated interruption')):
            with self.assertRaisesRegex(RuntimeError, 'simulated interruption'):
                self.prepare()
        state = json.loads(self.installed('installation.json').read_text())
        self.assertFalse(state.get('secrets_created', False))
        self.assertFalse(self.installed('database-url').exists())
        self.assertTrue(self.url_file.exists())
        response = json.dumps([{'addr_info': [{'local': self.config['node_ip']}]}])
        with patch.object(host, 'platform_preflight'), patch.object(host.shutil, 'which', return_value='/fixture/tool'), \
                patch.object(host.subprocess, 'check_output', return_value=response), \
                patch.object(host.database, 'preflight', return_value={'endpoint': '127.0.0.1:5432/hakopod', 'server_version': '17.5'}) as probe:
            with contextlib.redirect_stdout(io.StringIO()):
                host.preflight(self.config, 'arm64', True, self.artifacts)
            probe.assert_called_once_with(self.config, resume=False, installed_url_path=None)
        resumed = self.prepare(resume=True)
        self.assertEqual(resumed['id'], state['id'])
        self.assertTrue(resumed['secrets_created'])
        host.verify_database_state(resumed)


if __name__ == '__main__':
    unittest.main()
