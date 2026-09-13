#!/usr/bin/env python3
"""Existing database checks without a server or host changes."""

import importlib.util
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import traceback
from types import SimpleNamespace
import unittest
from unittest.mock import patch

HERE = Path(__file__).resolve().parent
spec = importlib.util.spec_from_file_location('database', HERE / 'database.py')
database = importlib.util.module_from_spec(spec)
sys.modules[spec.name] = database
spec.loader.exec_module(database)

PASSWORD = 'fixture-only:password@/not-a-real-secret'
ENCODED_PASSWORD = 'fixture-only%3Apassword%40%2Fnot-a-real-secret'
LOCAL = 'postgresql://hakopod:' + ENCODED_PASSWORD + '@127.0.0.1:5432/hakopod'
EXTERNAL = 'postgres://hakopod:' + ENCODED_PASSWORD + '@mydb.abc123.us-east-1.rds.amazonaws.com:5432/hakopod?sslmode=verify-full'


class DatabaseTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name).resolve()
        self.url_file = self.root / 'database-url'
        self.url_file.write_text(LOCAL + '\n')
        self.url_file.chmod(0o600)
        self.config = {'database_mode': 'local', 'database_url_file': str(self.url_file), 'database_ca_file': ''}
        self.facts = dict(version_num=170005, database='hakopod', username='hakopod', writable=True,
                          owner=True, connect=True, schema_access=True, default_schema='public',
                          schema_empty=True, tls=True)

    def invoke(self, changes=None, **kwargs):
        facts = dict(self.facts, **(changes or {}))
        with patch.object(database, '_capture', side_effect=[(0, json.dumps(facts).encode(), b''),
                          (0, b'psql (PostgreSQL) 14.18 (Debian)\n', b'')]) as capture:
            result = database.preflight(self.config, **kwargs)
        return result, capture

    def test_managed_mode_neither_reads_secret_files_nor_connects(self):
        with patch.object(database, 'read_url_file') as read, patch.object(database, '_capture') as capture:
            self.assertEqual(database.preflight({})['mode'], 'managed')
            self.assertEqual(database.validate_config({})['mode'], 'managed')
            read.assert_not_called()
            capture.assert_not_called()
        for config in ({'database_mode': 'managed', 'database_url_file': '/tmp/url'},
                       {'database_mode': 'managed', 'database_ca_file': '/tmp/ca'},
                       {'database_mode': 'unknown'}):
            with self.assertRaises(ValueError):
                database.validate_config(config)

    def test_dry_run_only_validates_paths_without_accessing_secrets(self):
        self.url_file.unlink()
        with patch.object(database, 'read_url_file') as read, patch.object(database, '_capture') as capture:
            summary = database.validate_config(self.config)
            self.assertIn('not yet checked', summary['endpoint'])
            read.assert_not_called()
            capture.assert_not_called()
        with self.assertRaises(ValueError):
            database.validate_config(self.config, inspect_files=True)

    def test_configuration_requires_absolute_paths(self):
        for field in ('database_url_file', 'database_ca_file'):
            for value in ('relative', '/path\nsecret', '/path\x00secret'):
                with self.subTest(field=field, value=value), self.assertRaises(ValueError):
                    database.validate_config(dict(self.config, **{field: value}))

    def test_url_file_is_regular_private_owned_bounded_and_utf8(self):
        for mode in (0o644, 0o640, 0o700, 0o666):
            self.url_file.chmod(mode)
            with self.subTest(mode=mode), self.assertRaisesRegex(ValueError, '0400 or 0600'):
                database.read_url_file(self.url_file)
        self.url_file.chmod(0o600)
        symlink = self.root / 'link'
        symlink.symlink_to(self.url_file)
        with self.assertRaises(ValueError):
            database.read_url_file(symlink)
        with self.assertRaises(ValueError):
            database.read_url_file(self.root)
        self.url_file.write_bytes(b'x' * (database.MAX_URL_BYTES + 1))
        with self.assertRaisesRegex(ValueError, 'size limit'):
            database.read_url_file(self.url_file)
        self.url_file.write_bytes(b'\xff')
        with self.assertRaisesRegex(ValueError, 'UTF-8'):
            database.read_url_file(self.url_file)
        self.url_file.write_text(LOCAL)

    def test_secret_ownership_under_root_and_service_user(self):
        actual = self.url_file.stat()
        cases = ((0, 0, True), (1000, 0, True), (1000, 1000, True),
                 (0, 1001, False), (1000, 1001, False))
        for current_uid, owner_uid, allowed in cases:
            info = SimpleNamespace(st_mode=actual.st_mode, st_size=actual.st_size, st_uid=owner_uid)
            with self.subTest(current_uid=current_uid, owner_uid=owner_uid), \
                    patch.object(database.os, 'geteuid', return_value=current_uid), \
                    patch.object(database.os, 'fstat', return_value=info):
                if allowed:
                    self.assertEqual(database.read_url_file(self.url_file), LOCAL)
                else:
                    with self.assertRaisesRegex(ValueError, 'owned'):
                        database.read_url_file(self.url_file)

    def test_url_file_allows_only_one_trailing_line_ending(self):
        for suffix in ('', '\n', '\r\n'):
            self.url_file.write_bytes((LOCAL + suffix).encode())
            self.assertEqual(database.read_url_file(self.url_file), LOCAL)
        for suffix in ('\n\n', '\n\r\n', '\r', '\x00', ' '):
            self.url_file.write_bytes((LOCAL + suffix).encode())
            with self.subTest(suffix=suffix), self.assertRaises(ValueError):
                database.read_url_file(self.url_file)

    def test_normal_rds_uri_is_accepted_with_no_credentials_in_summary_or_repr(self):
        connection = database.parse_url(EXTERNAL, 'external')
        self.assertEqual(connection.password, PASSWORD)
        self.assertEqual(connection.username, 'hakopod')
        self.assertEqual(connection.port, 5432)
        self.assertEqual(connection.sslmode, 'verify-full')
        for value in (repr(connection), str(connection.summary())):
            for secret in (PASSWORD, ENCODED_PASSWORD, 'postgres://', 'sslmode', 'username', 'password'):
                self.assertNotIn(secret, value)
        self.assertIn('rds.amazonaws.com:5432/hakopod', connection.summary()['endpoint'])

    def test_local_accepts_loopback_tcp_and_rejects_unix_sockets(self):
        values = (
            LOCAL, 'postgresql://hakopod:fixture@localhost/hakopod',
            'postgresql://hakopod:fixture@[::1]:5432/hakopod',
            'postgresql://hakopod:fixture@127.0.0.2/hakopod',
        )
        for value in values:
            with self.subTest(value=value):
                connection = database.parse_url(value, 'local')
                normalized = database.parse_url(connection.uri(), 'local')
                self.assertEqual(connection, normalized)
        for value in ('postgresql://hakopod:fixture@remote.example.test/hakopod',
                      'postgresql://hakopod:fixture@192.168.1.10/hakopod',
                      'postgresql://hakopod:fixture@/hakopod',
                      'postgresql://hakopod:fixture@/hakopod?host=/var/run/postgresql',
                      'postgresql://hakopod:fixture@%2Fvar%2Frun%2Fpostgresql/hakopod',
                      'postgresql://hakopod:fixture@/hakopod?host=/tmp/postgres',
                      'postgresql://hakopod:fixture@/hakopod?host=/home/operator/postgres'):
            with self.subTest(value=value), self.assertRaises(ValueError):
                database.parse_url(value, 'local')

    def test_external_rejects_tls_downgrade_and_local_paths(self):
        for mode in ('', 'disable', 'allow', 'prefer', 'require', 'verify-ca', 'VERIFY-FULL'):
            uri = EXTERNAL.split('?')[0] + ('?sslmode=' + mode if mode else '')
            with self.subTest(mode=mode), self.assertRaises(ValueError):
                database.parse_url(uri, 'external')
        for host in ('127.0.0.1', 'localhost', '[::1]', '%2Fvar%2Frun%2Fpostgresql'):
            with self.subTest(host=host), self.assertRaises(ValueError):
                database.parse_url('postgresql://hakopod:fixture@' + host + '/hakopod?sslmode=verify-full', 'external')

    def test_existing_database_requires_an_explicit_nonempty_password(self):
        for mode, original in (('local', LOCAL), ('external', EXTERNAL)):
            for password in ('', None):
                uri = original.replace(':' + ENCODED_PASSWORD, ':' if password == '' else '')
                with self.subTest(mode=mode, password=password), self.assertRaisesRegex(ValueError, 'explicit password'):
                    database.parse_url(uri, mode)

    def test_url_rejects_overrides_duplicate_args_controls_and_fragments(self):
        suffixes = ('#fragment', '#', '?sslmode=disable&sslmode=verify-full',
                    '?sslmode=disable&ssl%6dode=verify-full', '?hostaddr=127.0.0.1',
                    '?service=other', '?passfile=/other', '?options=-crole%3Dother',
                    '?dbname=other', '?password=hidden', '?target_session_attrs=any',
                    '?host=/other', '?user=other', '?port=5433', '?connect_timeout=99',
                    '?connect_timeout=0', '?sslmode=prefer&', '?sslmode=prefer%00', '?unknown=value')
        for suffix in suffixes:
            with self.subTest(suffix=suffix), self.assertRaises(ValueError):
                database.parse_url(LOCAL + suffix, 'local')
        for uri in (LOCAL.replace('postgresql:', 'https:'), LOCAL.replace('postgresql:', 'postgres:' + '\n'),
                    LOCAL.replace(ENCODED_PASSWORD, 'bad%00password'), LOCAL.replace(ENCODED_PASSWORD, 'bad%7fpassword'),
                    LOCAL.replace(ENCODED_PASSWORD, 'bad%xxpassword'), LOCAL.replace(ENCODED_PASSWORD, 'bad@password'),
                    LOCAL.replace('5432', '0'), LOCAL.replace('5432', '65536'),
                    LOCAL.replace('127.0.0.1', '0.0.0.0'), LOCAL.replace('127.0.0.1', '[ff02::1]')):
            with self.subTest(uri=uri), self.assertRaises(ValueError):
                database.parse_url(uri, 'local')

    def test_database_must_be_dedicated_and_named(self):
        for name in ('postgres', 'POSTGRES', 'template0', 'template1', '', 'db/other', 'bad%2Fname', 'bad%00name', 'x' * 64):
            with self.subTest(name=name), self.assertRaisesRegex(ValueError, 'dedicated|control'):
                database.parse_url('postgresql://hakopod@localhost/' + name, 'local')
        with self.assertRaisesRegex(ValueError, 'explicit PostgreSQL user'):
            database.parse_url('postgresql://localhost/hakopod', 'local')

    def test_ca_modes_and_runtime_path_rewrite(self):
        ca = self.root / 'ca.pem'
        ca.write_text('fixture public certificate; psql validates contents')
        for mode in (0o644, 0o600, 0o400, 0o444):
            ca.chmod(mode)
            database.parse_url(EXTERNAL, 'external', str(ca))
        for mode in (0o666, 0o664, 0o700):
            ca.chmod(mode)
            with self.subTest(mode=mode), self.assertRaises(ValueError):
                database.parse_url(EXTERNAL, 'external', str(ca))
        ca.chmod(0o644)
        self.url_file.write_text(EXTERNAL)
        config = dict(self.config, database_mode='external', database_ca_file=str(ca))
        uri = database.runtime_url(config, ca_path='/etc/hakopod/database-ca.pem')
        saved = database.parse_url(uri, 'external', inspect_files=False)
        self.assertEqual(saved.ca_file, '/etc/hakopod/database-ca.pem')
        self.assertEqual(saved.password, PASSWORD)
        self.assertIn('sslmode=verify-full', uri)
        with self.assertRaisesRegex(ValueError, 'conflicts'):
            database.parse_url(uri, 'external', str(ca))
        with self.assertRaisesRegex(ValueError, 'certificate verification'):
            database.parse_url(LOCAL, 'local', str(ca))

    def test_missing_custom_ca_is_rejected_before_psql(self):
        self.url_file.write_text(EXTERNAL)
        self.config.update(database_mode='external', database_ca_file=str(self.root / 'missing.pem'))
        with patch.object(database, '_capture') as capture, self.assertRaises(ValueError):
            database.preflight(self.config)
        capture.assert_not_called()

    def test_preflight_uses_child_env_never_argv_and_does_not_inherit_secrets(self):
        with patch.dict(os.environ, {'PGPASSWORD': 'foreign-password', 'PGHOST': 'foreign-host',
                                    'PGOPTIONS': '-crole=foreign', 'PGSERVICE': 'foreign',
                                    'DATABASE_URL': 'foreign-uri', 'SECRET_TOKEN': 'foreign-token'}):
            summary, capture = self.invoke()
        self.assertEqual(summary['server_major'], 17)
        self.assertEqual(summary['server_version'], '17.5')
        self.assertEqual(summary['client_major'], 14)
        argv, env, timeout = capture.call_args_list[0].args
        self.assertEqual(timeout, 20)
        self.assertEqual(env['PGPASSWORD'], PASSWORD)
        self.assertEqual(env['PGHOST'], '127.0.0.1')
        self.assertEqual(env['PGDATABASE'], 'hakopod')
        self.assertEqual(env['PGPASSFILE'], '/dev/null')
        self.assertIn('--no-psqlrc', argv)
        self.assertIn('--no-password', argv)
        for value in (str(argv), str(summary), str(capture.call_args_list[1].args)):
            self.assertNotIn(PASSWORD, value)
            self.assertNotIn(ENCODED_PASSWORD, value)
            self.assertNotIn(LOCAL, value)
        for key in ('DATABASE_URL', 'SECRET_TOKEN', 'PGSERVICE'):
            self.assertNotIn(key, env)
        self.assertNotIn('foreign', str(env))
        self.assertIn('BEGIN READ ONLY;', database.PREFLIGHT_SQL)
        self.assertIn('ROLLBACK;', database.PREFLIGHT_SQL)
        for mutation in ('CREATE ', 'DROP ', 'ALTER ', 'INSERT ', 'UPDATE ', 'DELETE '):
            self.assertNotIn(mutation, database.PREFLIGHT_SQL)

    def test_supported_server_major_boundaries_and_older_client_are_accepted(self):
        for major in range(14, 19):
            with self.subTest(major=major):
                result, _ = self.invoke({'version_num': major * 10000 + 1})
                self.assertEqual(result['server_major'], major)
                self.assertEqual(result['client_major'], 14)
        for version in (90600, 130023, 190000, 0, -140000):
            with self.subTest(version=version), self.assertRaisesRegex(ValueError, '14 through 18'):
                self.invoke({'version_num': version})

    def test_client_version_diagnostic_failure_does_not_block_connectivity(self):
        for diagnostic in ((1, b'', b'not available'), ValueError('diagnostic timed out')):
            with self.subTest(diagnostic=diagnostic), patch.object(database, '_capture', side_effect=[
                    (0, json.dumps(self.facts).encode(), b''), diagnostic]):
                result = database.preflight(self.config)
                self.assertIsNone(result['client_major'])
                self.assertEqual(result['server_major'], 17)

    def test_rejects_foreign_database_role_replica_permissions_or_wrong_schema(self):
        for change, message in (({'database': 'another'}, 'does not match'), ({'username': 'another'}, 'does not match'),
                                ({'owner': False}, 'own the dedicated'), ({'writable': False}, 'writable primary'),
                                ({'connect': False}, 'CONNECT'), ({'schema_access': False}, 'USAGE and CREATE'),
                                ({'default_schema': 'another'}, 'default schema')):
            with self.subTest(change=change), self.assertRaisesRegex(ValueError, message):
                self.invoke(change)

    def test_fresh_install_requires_empty_schema_and_resume_requires_saved_credential(self):
        with self.assertRaisesRegex(ValueError, 'empty public schema'):
            self.invoke({'schema_empty': False})
        with self.assertRaisesRegex(ValueError, 'owned installation'):
            self.invoke({'schema_empty': False}, resume=True)
        installed = self.root / 'installed-url'
        installed.write_text(database.runtime_url(self.config))
        installed.chmod(0o600)
        self.url_file.unlink()
        result, _ = self.invoke({'schema_empty': False}, resume=True, installed_url_path=installed)
        self.assertEqual(result['server_major'], 17)
        with self.assertRaisesRegex(ValueError, 'own the dedicated'):
            self.invoke({'owner': False, 'schema_empty': False}, resume=True, installed_url_path=installed)

    def test_external_preflight_requires_actual_tls_and_uses_system_roots(self):
        self.url_file.write_text(EXTERNAL)
        self.config['database_mode'] = 'external'
        with patch.object(database, '_system_ca_file', return_value='/system/ca.pem'):
            result, capture = self.invoke()
            self.assertEqual(result['mode'], 'external')
            self.assertEqual(capture.call_args_list[0].args[1]['PGSSLROOTCERT'], '/system/ca.pem')
            self.assertEqual(capture.call_args_list[0].args[1]['PGGSSENCMODE'], 'disable')
            with self.assertRaisesRegex(ValueError, 'negotiate verified TLS'):
                self.invoke({'tls': False})

    def test_psql_failure_with_secret_diagnostics_never_exposes_values(self):
        private = (LOCAL + '\n' + PASSWORD).encode()
        for code, output, diagnostics in ((1, private, private), (0, private, b'')):
            with patch.object(database, '_capture', return_value=(code, output, diagnostics)):
                with self.assertRaises(ValueError) as caught:
                    database.preflight(self.config)
            self.assertNotIn(PASSWORD, str(caught.exception))
            self.assertNotIn(ENCODED_PASSWORD, str(caught.exception))
            self.assertNotIn(LOCAL, str(caught.exception))

    def test_underlying_exception_context_cannot_echo_a_private_uri(self):
        with patch.object(database, 'urlsplit', side_effect=ValueError(EXTERNAL)):
            try:
                database.parse_url(EXTERNAL, 'external')
            except ValueError:
                formatted = traceback.format_exc()
            else:
                self.fail('Malformed URI was accepted')
        self.assertNotIn(EXTERNAL, formatted)
        self.assertNotIn(ENCODED_PASSWORD, formatted)

    def test_missing_malformed_and_non_boolean_facts_are_refused(self):
        for output in (b'[]', b'null', b'{}', b'not-json', b'\xff'):
            with self.subTest(output=output), patch.object(database, '_capture', return_value=(0, output, b'')):
                with self.assertRaises(ValueError):
                    database.preflight(self.config)
        for changes in ({'version_num': True}, {'schema_empty': None}, {'owner': 'true'}, {'writable': 1}):
            with self.subTest(changes=changes), self.assertRaises(ValueError):
                self.invoke(changes)

    def test_capture_bounds_stdout_stderr_and_wall_time(self):
        for stream in ('stdout', 'stderr'):
            command = [sys.executable, '-c', 'import sys; sys.' + stream + '.write("x" * 100000)']
            with self.subTest(stream=stream), self.assertRaisesRegex(ValueError, 'output limit'):
                database._capture(command, database._base_env(), 2)
        with self.assertRaisesRegex(ValueError, 'timed out'):
            database._capture([sys.executable, '-c', 'import time; time.sleep(10)'], database._base_env(), 0.1)
        with self.assertRaisesRegex(ValueError, 'Cannot run psql'):
            database._capture([str(self.root / 'missing')], database._base_env(), 1)
        code, stdout, stderr = database._capture([sys.executable, '-c', 'import sys; print("ok"); sys.stderr.write("err")'], database._base_env(), 2)
        self.assertEqual((code, stdout, stderr), (0, b'ok\n', b'err'))

    def test_timeout_is_bounded_before_spawning(self):
        for timeout in (0, 61, -1, True, float('nan')):
            with self.subTest(timeout=timeout), patch.object(database, '_capture') as capture:
                with self.assertRaisesRegex(ValueError, 'timeout'):
                    database.preflight(self.config, timeout=timeout)
                capture.assert_not_called()


if __name__ == '__main__':
    unittest.main()
