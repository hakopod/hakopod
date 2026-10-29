import ipaddress
import shutil
import subprocess
import tempfile
import unittest
from pathlib import Path

import access_firewall


class AccessFirewallTest(unittest.TestCase):
    def setUp(self):
        self.calls = []
        self.dir = tempfile.TemporaryDirectory()
        self.saved = Path(self.dir.name) / 'access.nft'

    def tearDown(self):
        self.dir.cleanup()

    def run_nft(self, args, **kwargs):
        self.calls.append((args, kwargs.get('input')))

    def apply(self, body, config=None):
        return access_firewall.apply(body, config or {'dashboard_mode': 'https', 'dashboard_port': 8443}, run=self.run_nft,
                                     addresses=lambda: [ipaddress.ip_network('192.0.2.4/32')], saved=self.saved)

    def test_enabled_policy_limits_only_dashboard_and_kubernetes_api(self):
        self.assertEqual(self.apply({'enabled': True, 'allow_cidrs': ['203.0.113.0/24', '2001:db8::/32'], 'tunnel_only': False}), {'applied': True})
        text = self.calls[0][1]
        self.assertTrue(text.startswith('table inet hakopod_access {}\ndelete table inet hakopod_access\n'))
        self.assertIn('tcp dport { 6443, 8443 } counter drop', text)
        self.assertIn('elements = { 192.0.2.4/32, 203.0.113.0/24 }', text)
        self.assertIn('elements = { 2001:db8::/32 }', text)
        self.assertIn('iif "lo" accept', text)
        self.assertNotIn(' 80', text.replace('8443', ''))
        self.assertEqual(self.saved.read_text(), text)

    def test_tunnel_only_closes_kubernetes_api_to_everything_but_the_tunnel(self):
        self.apply({'enabled': True, 'allow_cidrs': ['10.99.0.0/24'], 'tunnel_only': True})
        text = self.calls[0][1]
        self.assertIn('tcp dport { 6443 } iifname { "hakopod-wg", "hakopod-wgs" } accept', text)
        self.assertIn('tcp dport { 6443 } counter drop', text)
        self.assertNotIn('8443', text)
        self.assertNotIn('10.99.0.0/24', text)

    def test_unchanged_rules_are_not_reapplied(self):
        body = {'enabled': True, 'allow_cidrs': ['203.0.113.0/24'], 'tunnel_only': False}
        self.apply(body)
        self.apply(body)
        self.assertEqual(len(self.calls), 1)

    def test_ssh_dashboard_mode_limits_only_kubernetes_api(self):
        self.apply({'enabled': True, 'allow_cidrs': ['203.0.113.0/24'], 'tunnel_only': False}, {'dashboard_mode': 'ssh', 'dashboard_port': 3000})
        self.assertIn('tcp dport { 6443 } counter drop', self.calls[0][1])

    def test_disabled_policy_removes_the_table(self):
        self.saved.write_text('old')
        self.assertEqual(self.apply({'enabled': False, 'allow_cidrs': [], 'tunnel_only': False}), {'applied': False})
        self.assertEqual(self.calls[0][1], 'table inet hakopod_access {}\ndelete table inet hakopod_access\n')
        self.assertFalse(self.saved.exists())

    def test_rejects_unsafe_input(self):
        for body in ({'enabled': True, 'allow_cidrs': [], 'tunnel_only': False}, {'enabled': True, 'allow_cidrs': ['0.0.0.0/0'], 'tunnel_only': False},
                     {'enabled': True, 'allow_cidrs': ['10.0.0.1/8'], 'tunnel_only': False}, {'enabled': True, 'allow_cidrs': ['1.2.3.0/24; flush ruleset'], 'tunnel_only': False},
                     {'enabled': 'yes', 'allow_cidrs': [], 'tunnel_only': False}, {'enabled': True, 'allow_cidrs': ['1.2.3.0/24'], 'tunnel_only': 'yes'},
                     {'enabled': True, 'allow_cidrs': [], 'ports': [22]}):
            with self.assertRaises(ValueError, msg=body):
                self.apply(body)
        self.assertEqual(self.calls, [])

    def test_ruleset_is_valid_nftables(self):
        # GitHub-hosted runners have nftables and passwordless sudo; -c only checks.
        if not shutil.which('nft') or subprocess.run(['sudo', '-n', 'true'], capture_output=True).returncode:
            self.skipTest('nft with sudo is not available')
        text = access_firewall.ruleset([ipaddress.ip_network('203.0.113.0/24'), ipaddress.ip_network('2001:db8::/32')], [6443, 8443], tunnel=('hakopod-wg', 'hakopod-wgs'))
        result = subprocess.run(['sudo', '-n', 'nft', '-c', '-f', '-'], input=text, text=True, capture_output=True)
        self.assertEqual(result.returncode, 0, result.stderr)


if __name__ == '__main__':
    unittest.main()
