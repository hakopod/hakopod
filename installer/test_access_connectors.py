import json
import subprocess
import tempfile
import unittest
from pathlib import Path
from unittest import mock

import access_connectors as c

PROFILE = 'client\ndev tun0\nproto udp\nremote vpn.example.com 1194\n<ca>\nup /bin/sh\n</ca>\n'


class FakeHost:
    def __init__(self):
        self.calls = []

    def __call__(self, args, **kwargs):
        self.calls.append(args)
        out = ''
        if args[:3] == ['tailscale', 'status', '--json']:
            out = json.dumps({'BackendState': 'Running', 'Self': {'TailscaleIPs': ['100.101.1.2']}})
        elif args[:2] == ['systemctl', 'is-active']:
            out = 'active\n'
        elif args[:2] == ['ip', '-j']:
            out = json.dumps([{'addr_info': [{'local': '10.8.0.6'}]}])
        elif args[:2] == ['swanctl', '--list-sas']:
            out = 'hakopod: #1, ESTABLISHED, IKEv2\n  hakopod: #1, INSTALLED, TUNNEL\n'
        return subprocess.CompletedProcess(args, 0, out, '')


class ConnectorsTest(unittest.TestCase):
    def setUp(self):
        self.dir = tempfile.TemporaryDirectory()
        root = Path(self.dir.name)
        self.state = root / 'state'
        patches = [mock.patch.object(c, 'OPENVPN_DIR', root / 'openvpn'), mock.patch.object(c, 'OPENVPN_UNIT', root / 'hakopod-openvpn.service'),
                   mock.patch.object(c, 'SWANCTL', root / 'swanctl.conf'), mock.patch.object(c, 'RUN', root / 'run'),
                   mock.patch.object(c.shutil, 'which', lambda name: '/usr/bin/' + name)]
        for patch in patches:
            patch.start()
            self.addCleanup(patch.stop)

    def tearDown(self):
        self.dir.cleanup()

    def test_openvpn_forces_its_device_ignores_pushed_routes_and_runs_no_scripts(self):
        text = c.openvpn_config(PROFILE, c.networks(['10.20.0.0/16', '2001:db8::/32']))
        self.assertNotIn('dev tun0', text)
        self.assertIn('dev hakopod-ovpn\ndev-type tun\nroute-nopull\nscript-security 1\n', text)
        self.assertIn('route 10.20.0.0 255.255.0.0', text)
        self.assertIn('route-ipv6 2001:db8::/32', text)
        self.assertIn('up /bin/sh', text)  # inside <ca>, certificate data only
        for bad in ('up /tmp/x', 'plugin /x.so', 'redirect-gateway def1', 'key /etc/shadow', 'config /x'):
            with self.assertRaises(ValueError, msg=bad):
                c.openvpn_config(PROFILE + bad + '\n', [])

    def test_openvpn_starts_once_and_reports(self):
        host = FakeHost()
        config = {'routes': ['10.20.0.0/16'], 'profile': PROFILE}
        report = c.apply({'openvpn': config}, run=host, state=self.state)
        self.assertEqual(report, {'type': 'openvpn', 'connected': True, 'address': '10.8.0.6', 'error': ''})
        self.assertIn(['systemctl', 'restart', 'hakopod-openvpn'], host.calls)
        conf = c.OPENVPN_DIR / 'hakopod.conf'
        self.assertEqual(conf.stat().st_mode & 0o777, 0o600)
        host.calls.clear()
        c.apply({'openvpn': config}, run=host, state=self.state)
        self.assertNotIn(['systemctl', 'restart', 'hakopod-openvpn'], host.calls)

    def test_tailscale_keeps_k3s_networking_and_never_leaves_the_key_behind(self):
        host = FakeHost()
        report = c.apply({'tailscale': {'hostname': 'hakopod', 'tags': ['tag:hakopod'], 'login_server': 'https://hs.example.com', 'auth_key': 'tskey-auth-0123456789abcdef'}}, run=host, state=self.state)
        up = next(call for call in host.calls if call[:2] == ['tailscale', 'up'])
        for flag in ('--netfilter-mode=off', '--accept-dns=false', '--accept-routes=false', '--advertise-tags=tag:hakopod', '--login-server=https://hs.example.com'):
            self.assertIn(flag, up)
        self.assertFalse(any('tskey' in part for part in up))
        self.assertFalse((c.RUN / 'tailscale.key').exists())
        self.assertNotIn('tskey', (self.state / 'tailscale.sha256').read_text())
        self.assertEqual(report['address'], '100.101.1.2')

    def test_ipsec_writes_a_private_config_and_reports_established(self):
        host = FakeHost()
        report = c.apply({'ipsec': {'remote_address': 'vpn.example.com', 'local_id': 'hakopod.example.com', 'remote_id': 'vpn.example.com', 'remote_networks': ['10.30.0.0/16'], 'psk': 'a-long-pre-shared-key-value'}}, run=host, state=self.state)
        self.assertTrue(report['connected'])
        self.assertEqual(c.SWANCTL.stat().st_mode & 0o777, 0o600)
        self.assertIn('remote_ts = 10.30.0.0/16', c.SWANCTL.read_text())
        for bad in ({'remote_networks': ['0.0.0.0/0']}, {'psk': 'short'}, {'local_id': 'a b'}, {'remote_address': 'x;y'}):
            config = {'remote_address': 'vpn.example.com', 'local_id': 'h', 'remote_id': 'v', 'remote_networks': ['10.30.0.0/16'], 'psk': 'a-long-pre-shared-key-value', **bad}
            self.assertNotEqual(c.apply({'ipsec': config}, run=FakeHost(), state=self.state)['error'], '', bad)

    def test_switching_connectors_tears_down_the_previous_one(self):
        host = FakeHost()
        c.apply({'openvpn': {'routes': ['10.20.0.0/16'], 'profile': PROFILE}}, run=host, state=self.state)
        c.apply({}, run=host, state=self.state)
        self.assertIn(['systemctl', 'disable', '--now', 'hakopod-openvpn'], host.calls)
        self.assertFalse((c.OPENVPN_DIR / 'hakopod.conf').exists())


if __name__ == '__main__':
    unittest.main()
