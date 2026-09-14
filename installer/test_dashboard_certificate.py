import base64
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch

sys.path.insert(0, str(Path(__file__).resolve().parent))
import dashboard_certificate as certificate


class DashboardCertificateTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name).resolve()

    def test_only_owned_bounded_tls_secret_is_accepted(self):
        secret = {'metadata': {'name': 'hakopod-dashboard-tls', 'namespace': 'hakopod-system',
                              'labels': {certificate.host.LABEL: 'a' * 32}},
                  'type': 'kubernetes.io/tls',
                  'data': {'tls.crt': base64.b64encode(b'certificate').decode(), 'tls.key': base64.b64encode(b'key').decode()}}
        self.assertEqual(certificate.secret_material(secret, 'a' * 32), (b'certificate', b'key'))
        with self.assertRaises(ValueError): certificate.secret_material(secret, 'b' * 32)
        secret['data']['tls.key'] = '!' * 32
        with self.assertRaises(ValueError): certificate.secret_material(secret, 'a' * 32)
        secret['data']['tls.key'] = 'A' * 65537
        with self.assertRaises(ValueError): certificate.secret_material(secret, 'a' * 32)

    def test_rotation_is_atomic_unchanged_skips_restart_and_failure_rolls_back(self):
        root = self.root / 'tls'
        with patch.object(certificate, 'ROOT', root), patch.object(certificate.os, 'chown'), \
                patch.object(certificate, 'validate_pair'), patch.object(certificate, 'run') as run:
            self.assertTrue(certificate.install_pair(b'cert1', b'key1', 'console.example.test', 1000, 1000, restart=False))
            first = os.readlink(root / 'current')
            self.assertEqual((root / 'current/dashboard.key').stat().st_mode & 0o777, 0o400)
            self.assertFalse(certificate.install_pair(b'cert1', b'key1', 'console.example.test', 1000, 1000))
            run.assert_not_called()
            (root / 'applied').unlink()  # Simulate an interrupted TLS reload.
            self.assertTrue(certificate.install_pair(b'cert1', b'key1', 'console.example.test', 1000, 1000))
            run.assert_called_once_with(['systemctl', 'try-restart', 'hakopod-dashboard.service'])
            run.reset_mock()
            def restart(command):
                self.assertEqual(command, ['systemctl', 'try-restart', 'hakopod-dashboard.service'])
                self.assertEqual((root / 'current/dashboard.crt').read_bytes(), b'cert2')
                self.assertEqual((root / 'current/dashboard.key').read_bytes(), b'key2')
            run.side_effect = restart
            certificate.install_pair(b'cert2', b'key2', 'console.example.test', 1000, 1000)
            second = os.readlink(root / 'current')
            self.assertNotEqual(first, second)
            run.side_effect = ValueError('restart failed')
            with self.assertRaises(ValueError): certificate.install_pair(b'cert3', b'key3', 'console.example.test', 1000, 1000)
            self.assertEqual(os.readlink(root / 'current'), second)
            run.side_effect = None
            certificate.install_pair(b'cert4', b'key4', 'console.example.test', 1000, 1000)
            self.assertEqual(len(list(root.glob('generation-*'))), 2)

    def test_invalid_renewal_keeps_current_material(self):
        root = self.root / 'tls'
        with patch.object(certificate, 'ROOT', root), patch.object(certificate.os, 'chown'), \
                patch.object(certificate, 'validate_pair') as validate, patch.object(certificate, 'run') as run:
            certificate.install_pair(b'cert1', b'key1', 'console.example.test', 1000, 1000, restart=False)
            previous = os.readlink(root / 'current')
            validate.side_effect = ValueError('invalid certificate')
            with self.assertRaises(ValueError): certificate.install_pair(b'bad-cert', b'key2', 'console.example.test', 1000, 1000)
            self.assertEqual(os.readlink(root / 'current'), previous)
            run.assert_not_called()

    @unittest.skipUnless(shutil.which('openssl'), 'OpenSSL is required for certificate verification')
    def test_real_pem_hostname_trust_and_key_checks(self):
        cert, key = self.root / 'dashboard.crt', self.root / 'dashboard.key'
        subprocess.run(['openssl', 'req', '-x509', '-newkey', 'rsa:2048', '-nodes', '-days', '2',
                        '-subj', '/CN=console.example.test', '-addext', 'subjectAltName=DNS:console.example.test',
                        '-keyout', str(key), '-out', str(cert)], check=True, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        with self.assertRaises(ValueError): certificate.validate_pair(self.root, 'console.example.test')
        # Trust only this test certificate in this test's child commands.
        with patch.dict(os.environ, {'SSL_CERT_FILE': str(cert)}):
            certificate.validate_pair(self.root, 'console.example.test')
            with self.assertRaises(ValueError): certificate.validate_pair(self.root, 'wrong.example.test')
            subprocess.run(['openssl', 'genrsa', '-out', str(key), '2048'], check=True, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
            with self.assertRaises(ValueError): certificate.validate_pair(self.root, 'console.example.test')
