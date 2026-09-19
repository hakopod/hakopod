#!/usr/bin/env python3
"""Trust-boundary tests; no root, systemd or real cluster mutation."""
import hashlib
import importlib.util
import io
import json
import os
from pathlib import Path
import pty
import subprocess
import tarfile
import tempfile
import unittest
from unittest.mock import patch

HERE = Path(__file__).resolve().parent
spec = importlib.util.spec_from_file_location('host', HERE / 'host.py')
host = importlib.util.module_from_spec(spec); spec.loader.exec_module(host)

class InstallerTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory(); self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name).resolve()
        self.config = dict(host.DEFAULTS, app_domain='apps.example.test', node_ip='192.0.2.10', acme='off')
    def config_file(self, changes=None):
        path = self.root / 'config.json'; path.write_text(json.dumps(dict(self.config, **(changes or {})))); return path
    def bundle(self, entries):
        path = self.root / 'sample.tar.gz'
        with tarfile.open(path, 'w:gz') as archive:
            for name, kind, content in entries:
                entry = tarfile.TarInfo(name)
                if kind == 'link': entry.type = tarfile.SYMTYPE; entry.linkname = content; archive.addfile(entry)
                elif kind == 'hardlink': entry.type = tarfile.LNKTYPE; entry.linkname = content; archive.addfile(entry)
                else: entry.size = len(content); archive.addfile(entry, io.BytesIO(content))
        return path
    def test_configuration_is_strict_and_never_shell(self):
        self.assertEqual(host.config(self.config_file())['supervisor_host'], '192.0.2.10')
        for changes in ({'unknown': True}, {'schema_version': 2}, {'max_pods': True},
            {'app_domain': 'apps.test;touch /tmp/pwned'}, {'node_ip': '127.0.0.1'},
            {'dashboard_port': 6443}, {'dashboard_origin': 'http://example.test:3000'},
            {'dashboard_origin': 'http://localhost:3000/'}, {'acme': 'production'}, {'storage': 'false'}):
            with self.subTest(changes=changes), self.assertRaises(ValueError): host.config(self.config_file(changes))
        path = self.root / 'duplicate.json'; path.write_text('{"schema_version":1,"schema_version":1}')
        with self.assertRaisesRegex(ValueError, 'Duplicate'): host.config(path)
    def test_https_requires_separate_domain_and_matching_port(self):
        valid = dict(dashboard_mode='https', dashboard_port=8443, dashboard_origin='https://console.example.test:8443', tls_cert_file='/root/chain.pem', tls_key_file='/root/key.pem')
        self.assertEqual(host.config(self.config_file(valid))['dashboard_mode'], 'https')
        for origin in ('https://foo.apps.example.test:8443', 'https://apps.example.test:8443', 'https://console.example.test', 'https://user@console.example.test:8443'):
            with self.assertRaises(ValueError): host.config(self.config_file(dict(valid, dashboard_origin=origin)))
    def test_letsencrypt_dashboard_is_independent_of_application_acme(self):
        values = dict(dashboard_mode='https', dashboard_certificate='letsencrypt', dashboard_port=8443,
                      dashboard_origin='https://console.example.test:8443', acme='off', acme_email='admin@example.test')
        config = host.config(self.config_file(values))
        for changes in ({'acme_email': ''}, {'tls_key_file': '/root/key.pem'}, {'dashboard_certificate': 'unknown'}, {'dashboard_mode': 'ssh'}):
            with self.assertRaises(ValueError): host.config(self.config_file(dict(values, **changes)))
        secret = self.root / 'secret'; secret.write_text('a' * 64 + '\n'); secret.chmod(0o600)
        with patch.object(host, 'regular', return_value=secret): host.render(config, 'amd64', 'a' * 32, self.root / 'tls-render')
        rendered = self.root / 'tls-render'
        issuer = json.loads((rendered / 'dashboard-issuer.json').read_text())
        self.assertEqual(issuer['spec']['acme']['server'], 'https://acme-v02.api.letsencrypt.org/directory')
        certificate = json.loads((rendered / 'dashboard-certificate.json').read_text())
        self.assertEqual(certificate['spec']['dnsNames'], ['console.example.test'])
        self.assertEqual(certificate['spec']['secretTemplate']['labels'][host.LABEL], 'a' * 32)
        self.assertFalse((rendered / 'issuer.json').exists())
        self.assertIn('/etc/hakopod/dashboard-tls/current/dashboard.crt', (rendered / 'dashboard.env').read_text())
        self.assertIn('OnUnitActiveSec=6h', (rendered / 'hakopod-dashboard-certificate.timer').read_text())
    def test_default_certificate_setting_preserves_legacy_resume_fingerprint(self):
        legacy = dict(self.config)
        legacy.pop('dashboard_certificate')
        self.assertEqual(host.fingerprint(legacy, 'amd64', {}), host.fingerprint(self.config, 'amd64', {}))
    def test_archive_rejects_traversal_links_devices_duplicates(self):
        bad = [
            [('../escape', 'file', b'x')], [('/absolute', 'file', b'x')],
            [('wrong-root/file', 'file', b'x')], [('bundle/link', 'link', '/etc/passwd')],
            [('bundle/link', 'link', '../../outside')], [('bundle/link', 'hardlink', 'bundle/file')],
            [('bundle/link', 'link', 'target'), ('bundle/link/child', 'file', b'x')],
            [('bundle/file', 'file', b'x'), ('bundle/file', 'file', b'y')],
        ]
        for entries in bad:
            with self.subTest(entries=entries), self.assertRaises(ValueError):
                host.unpack(self.bundle(entries), self.root / 'out', 'bundle')
            self.assertFalse((self.root / 'out').exists(), 'Archive is validated before extraction starts')
    def test_archive_keeps_internal_package_links_and_file_bytes(self):
        source = self.bundle([('bundle/node_modules/pkg', 'link', '.store/pkg'), ('bundle/node_modules/.store/pkg/index.js', 'file', b'export default 1')])
        host.unpack(source, self.root / 'out', 'bundle')
        self.assertEqual((self.root / 'out/bundle/node_modules/pkg/index.js').read_bytes(), b'export default 1')
    def test_artifact_checksum_and_dry_run_do_not_write_state(self):
        path = self.config_file()
        for suffix in ('linux_arm64', 'dashboard'):
            name = 'hakopod_0.1.0-dev_' + suffix + '.tar.gz'; (self.root / name).write_bytes(b'fixture only')
        manifest = self.root / 'SHA256SUMS'
        manifest.write_text(''.join(host.digest(p) + '  ' + p.name + '\n' for p in sorted(self.root.glob('*.tar.gz'))))
        before = sorted(p.name for p in self.root.iterdir())
        result = subprocess.run(['bash', str(HERE.parent / 'scripts/install.sh'), '--config', str(path), '--artifact-dir', str(self.root), '--dry-run', '--arch', 'arm64'], capture_output=True, text=True)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn('No host paths, services, credentials or cluster resources are changed', result.stdout)
        self.assertEqual(sorted(p.name for p in self.root.iterdir()), before)
        (self.root / 'hakopod_0.1.0-dev_dashboard.tar.gz').write_bytes(b'tampered')
        with self.assertRaisesRegex(ValueError, 'checksum mismatch'): host.artifacts(self.root, self.config, 'arm64')
    def test_resume_fingerprint_binds_config_pins_and_bytes(self):
        first = host.fingerprint(self.config, 'arm64', {'archive': 'a' * 64})
        for config, arch, artifacts in [(dict(self.config, max_pods=60), 'arm64', {'archive': 'a' * 64}), (dict(self.config, deployment_mode='managed-cloud'), 'arm64', {'archive': 'a' * 64}), (self.config, 'amd64', {'archive': 'a' * 64}), (self.config, 'arm64', {'archive': 'b' * 64})]:
            self.assertNotEqual(host.fingerprint(config, arch, artifacts), first)
    def test_foreign_kubernetes_objects_are_refused(self):
        path = self.root / 'object.json'
        path.write_text(json.dumps({'kind': 'Secret', 'metadata': {'name': 'postgres', 'labels': {}}}))
        with self.assertRaisesRegex(ValueError, 'unrelated'): host.owned(path, 'a' * 32)
        path.write_text(json.dumps({'kind': 'Secret', 'metadata': {'name': 'postgres', 'labels': {host.LABEL: 'a' * 32}}}))
        host.owned(path, 'a' * 32)
    def test_rendered_limits_secrets_and_ingress_exposure(self):
        secret = self.root / 'secret'; secret.write_text('a' * 64 + '\n'); secret.chmod(0o600)
        with patch.object(host, 'regular', return_value=secret): host.render(self.config, 'arm64', 'b' * 32, self.root / 'render')
        rendered = self.root / 'render'
        pg = json.loads((rendered / 'postgres.json').read_text())['items']
        deployment = next(item for item in pg if item['kind'] == 'Deployment')
        self.assertEqual(deployment['spec']['template']['spec']['containers'][0]['resources']['limits']['memory'], '256Mi')
        pv = next(item for item in pg if item['kind'] == 'PersistentVolume')
        self.assertEqual(pv['spec']['persistentVolumeReclaimPolicy'], 'Retain')
        ingress = json.loads((rendered / 'haproxy.json').read_text())['kubernetes-ingress']['controller']
        self.assertEqual(ingress['deployment']['hostPorts'], {'http': 80, 'https': 443, 'stat': 0})
        self.assertEqual(ingress['service']['type'], 'ClusterIP')
        self.assertIn('MemoryMax=256M', (rendered / 'hakopod-api.service').read_text())
        self.assertIn('127.0.0.1:8080', (rendered / 'api.env').read_text())
        self.assertIn('HAKOPOD_MANAGED_POSTGRES="true"', (rendered / 'api.env').read_text())
        self.assertIn('HAKOPOD_DEPLOYMENT_MODE="self-hosted"', (rendered / 'api.env').read_text())
        self.assertIn('HAKOPOD_SERVERLESS_ADDRESS="' + self.config['node_ip'] + ':8082"', (rendered / 'api.env').read_text())
        self.assertIn('ReadWritePaths=/var/lib/hakopod/backups', (rendered / 'hakopod-api.service').read_text())
        self.assertNotIn('ReadWritePaths=/var/lib/hakopod/backups', (rendered / 'hakopod-dashboard.service').read_text())
        self.assertFalse((rendered / 'api.env').stat().st_mode & 0o077)
    def test_public_tcp_ports_are_explicit_bounded_and_rendered(self):
        for value in ([587, 587], [80], [8080], [8082], [3000], [True], ['587'], [0], [65536], list(range(20000, 20257))):
            with self.subTest(value=value), self.assertRaises(ValueError):
                host.config(self.config_file({'public_tcp_ports': value}))
        c = host.config(self.config_file({'public_tcp_ports': [65535, 587, 12345, 1, 465]}))
        self.assertEqual(c['public_tcp_ports'], [1, 465, 587, 12345, 65535])
        provisioned = list(range(20000, 20256))
        self.assertEqual(host.config(self.config_file({'public_tcp_ports': list(reversed(provisioned))}))['public_tcp_ports'], provisioned)
        secret = self.root / 'secret'; secret.write_text('a' * 64 + '\n'); secret.chmod(0o600)
        with patch.object(host, 'regular', return_value=secret): host.render(c, 'arm64', 'b' * 32, self.root / 'render')
        rendered = self.root / 'render'
        ingress = json.loads((rendered / 'haproxy.json').read_text())['kubernetes-ingress']['controller']
        self.assertEqual(ingress['service']['tcpPorts'], [{'name': 'tcp-' + str(port), 'port': port, 'targetPort': port} for port in [1, 465, 587, 12345, 65535]])
        self.assertIn('HAKOPOD_PUBLIC_TCP_PORTS="1,465,587,12345,65535"', (rendered / 'api.env').read_text())

    def test_deployment_mode_defaults_and_managed_cloud_rejects_public_tcp(self):
        omitted = dict(self.config); omitted.pop('deployment_mode')
        path = self.root / 'legacy.json'; path.write_text(json.dumps(omitted))
        self.assertEqual(host.config(path)['deployment_mode'], 'self-hosted')
        for value in ('', 'cloud', 'managed_cloud', 'SELF-HOSTED', True):
            with self.subTest(value=value), self.assertRaisesRegex(ValueError, 'deployment_mode'):
                host.config(self.config_file({'deployment_mode': value}))
        with self.assertRaisesRegex(ValueError, 'managed-cloud does not support public_tcp_ports'):
            host.config(self.config_file({'deployment_mode': 'managed-cloud', 'public_tcp_ports': [587]}))
        c = host.config(self.config_file({'deployment_mode': 'managed-cloud'}))
        secret = self.root / 'secret'; secret.write_text('a' * 64 + '\n'); secret.chmod(0o600)
        with patch.object(host, 'regular', return_value=secret): host.render(c, 'amd64', 'b' * 32, self.root / 'render')
        rendered = self.root / 'render'
        ingress = json.loads((rendered / 'haproxy.json').read_text())['kubernetes-ingress']['controller']
        self.assertEqual(ingress['service']['tcpPorts'], [])
        self.assertEqual(ingress['deployment']['hostPorts'], {'http': 80, 'https': 443, 'stat': 0})
        environment = (rendered / 'api.env').read_text()
        self.assertIn('HAKOPOD_DEPLOYMENT_MODE="managed-cloud"', environment)
        self.assertIn('HAKOPOD_PUBLIC_TCP_PORTS=""', environment)

    def test_interactive_mode_selects_public_tcp_prompt_without_host_changes(self):
        for suffix in ('linux_arm64', 'dashboard'):
            (self.root / ('hakopod_0.1.0-dev_' + suffix + '.tar.gz')).write_bytes(b'fixture only')
        (self.root / 'SHA256SUMS').write_text(''.join(host.digest(p) + '  ' + p.name + '\n' for p in sorted(self.root.glob('*.tar.gz'))))
        for mode in ('self-hosted', 'managed-cloud'):
            with self.subTest(mode=mode):
                answers = [mode, '', 'apps.example.test', '192.0.2.10', '', '', '', '', 'off']
                if mode == 'self-hosted': answers.append('12345,587')
                answers.extend([''] * 8 + ['no'] * 3)
                master, slave = pty.openpty()
                try:
                    os.write(master, ('\n'.join(answers) + '\n').encode())
                    result = subprocess.run(['bash', str(HERE.parent / 'scripts/install.sh'), '--artifact-dir', str(self.root), '--dry-run', '--arch', 'arm64'], stdin=slave, capture_output=True, text=True, timeout=20)
                finally:
                    os.close(slave); os.close(master)
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertIn('Deployment mode: ' + mode, result.stdout)
                self.assertIn('No host paths, services, credentials or cluster resources are changed', result.stdout)
                if mode == 'managed-cloud':
                    self.assertNotIn('Public TCP ports to provision', result.stderr)
                    self.assertNotIn('Additional TCP host ports:', result.stdout)
                    self.assertIn('public TCP is unavailable', result.stdout)
                else:
                    self.assertIn('Public TCP ports to provision', result.stderr)
                    self.assertIn('Additional TCP host ports: 587, 12345', result.stdout)

    def test_secret_file_symlink_and_permissions_rejected(self):
        path = self.root / 'secret'; path.write_text('never print this'); path.chmod(0o644)
        with self.assertRaises(ValueError): host.regular(path, True)
        path.chmod(0o600); link = self.root / 'link'; link.symlink_to(path)
        with self.assertRaises(ValueError): host.regular(link, True)

if __name__ == '__main__': unittest.main(verbosity=2)
