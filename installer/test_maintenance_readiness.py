"""Exercise installer readiness against real local HTTP and verified TLS."""
import contextlib
import http.client
import http.server
from pathlib import Path
import shutil
import ssl
import subprocess
import tempfile
import threading
import unittest
from unittest.mock import patch

import maintenance as m


@contextlib.contextmanager
def listener(cert=None, key=None, status=200):
    class Handler(http.server.BaseHTTPRequestHandler):
        def do_GET(self):
            body = b'fixture response: never expose private-body-value'
            self.send_response(status)
            self.send_header('Content-Length', str(len(body)))
            self.end_headers()
            self.wfile.write(body)
        def log_message(self, *args): pass
    server = http.server.ThreadingHTTPServer(('127.0.0.1', 0), Handler)
    if cert:
        context = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
        context.load_cert_chain(cert, key)
        server.socket = context.wrap_socket(server.socket, server_side=True)
    thread = threading.Thread(target=server.serve_forever, daemon=True)
    thread.start()
    try: yield server.server_port
    finally:
        server.shutdown()
        server.server_close()
        thread.join(timeout=3)


class DashboardReadinessTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.fixture = tempfile.TemporaryDirectory()
        cls.root = Path(cls.fixture.name)
        cls.cert, cls.key = cls.root/'cert.pem', cls.root/'key.pem'
        subprocess.run(['openssl', 'req', '-x509', '-newkey', 'rsa:2048', '-nodes', '-days', '2',
                        '-subj', '/CN=dashboard.example.test',
                        '-addext', 'subjectAltName=DNS:dashboard.example.test',
                        '-addext', 'basicConstraints=critical,CA:TRUE',
                        '-addext', 'keyUsage=critical,keyCertSign,digitalSignature,keyEncipherment',
                        '-addext', 'subjectKeyIdentifier=hash',
                        '-addext', 'authorityKeyIdentifier=keyid:always',
                        '-keyout', str(cls.key), '-out', str(cls.cert)],
                       stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, check=True)
    @classmethod
    def tearDownClass(cls): cls.fixture.cleanup()

    def config(self, port, certificate='letsencrypt', hostname='dashboard.example.test'):
        return {'dashboard_origin': f'https://{hostname}:{port}', 'dashboard_port': port,
                'node_ip': '127.0.0.1', 'dashboard_certificate': certificate}

    def test_automatic_and_provided_certificates_use_the_served_generation(self):
        paths = m.host.dashboard_tls_paths
        with listener(self.cert, self.key) as port:
            for mode in ('letsencrypt', 'provided'):
                with self.subTest(mode=mode), tempfile.TemporaryDirectory() as root:
                    config = self.config(port, mode)
                    trusted, _ = paths(config, root)
                    trusted.parent.mkdir(parents=True, exist_ok=True)
                    shutil.copyfile(self.cert, trusted)
                    if mode == 'letsencrypt': self.assertFalse((Path(root)/'dashboard.crt').exists())
                    with patch.object(m.host, 'dashboard_tls_paths', side_effect=lambda c: paths(c, root)):
                        self.assertTrue(m.dashboard_health(config))

    def test_hostname_verification_remains_required(self):
        with listener(self.cert, self.key) as port, patch.object(m.host, 'dashboard_tls_paths', return_value=(self.cert, self.key)):
            with self.assertRaises(ssl.SSLCertVerificationError):
                m.dashboard_health(self.config(port, hostname='different.example.test'))

    def test_missing_configured_certificate_is_not_replaced_by_insecure_tls(self):
        with listener(self.cert, self.key) as port, patch.object(m.host, 'dashboard_tls_paths', return_value=(self.root/'missing.pem', self.key)), patch.object(m, 'api_health', return_value=True):
            self.assertEqual(m.readiness(self.config(port)), {'API': 'ready', 'Dashboard': 'configured certificate file is missing'})

    def test_http_dashboard_and_error_status(self):
        for status in (200, 503):
            with self.subTest(status=status), listener(status=status) as port:
                config = {'dashboard_origin': f'http://localhost:{port}', 'dashboard_port': port}
                if status == 200: self.assertTrue(m.dashboard_health(config))
                else:
                    with self.assertRaisesRegex(m.ReadinessError, '^HTTP 503$'):
                        m.dashboard_health(config)


class ReadinessDiagnosticsTests(unittest.TestCase):
    def test_both_services_checked_even_if_api_connection_fails(self):
        with patch.object(m, 'api_health', side_effect=ConnectionRefusedError('private-value')), patch.object(m, 'dashboard_health', return_value=True) as dashboard:
            checks = m.readiness({})
        dashboard.assert_called_once()
        self.assertEqual(checks, {'API': 'connection refused; check the service journal', 'Dashboard': 'ready'})

    def test_database_readiness_error_contains_status_not_response_body(self):
        connection = http.client.HTTPConnection
        with listener(status=503) as port, patch.object(m.http.client, 'HTTPConnection', side_effect=lambda host, ignored, timeout: connection(host, port, timeout=timeout)), patch.object(m, 'dashboard_health', return_value=True):
            self.assertEqual(m.readiness({}), {'API': 'HTTP 503 (PostgreSQL is unavailable)', 'Dashboard': 'ready'})

    def test_unexpected_errors_do_not_disclose_exception_details(self):
        with patch.object(m, 'api_health', side_effect=RuntimeError('secret-value')), patch.object(m, 'dashboard_health', side_effect=TimeoutError('secret-value')):
            self.assertNotIn('secret-value', str(m.readiness({})))
