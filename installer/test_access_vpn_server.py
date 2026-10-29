import subprocess
import tempfile
import unittest
from pathlib import Path

import access_vpn_server

PEER = 'xTIBA5rboUvnH4htodjb6e697QjLERt1NAB4mZqp8Dg='
SERVER = {'listen_port': 51820, 'address': '10.100.0.1/24', 'peers': [{'public_key': PEER, 'address': '10.100.0.2'}]}
CONFIG = {'node_ip': '192.0.2.4', 'dashboard_mode': 'https', 'dashboard_port': 8443}


class FakeHost:
    def __init__(self, exists=True, peers=()):
        self.calls, self.inputs, self.exists, self.peers = [], [], exists, list(peers)

    def __call__(self, args, **kwargs):
        self.calls.append(args)
        self.inputs.append(kwargs.get('input'))
        out, code = '', 0
        if args[:2] == ['wg', 'genkey']:
            out = 'cHJpdmF0ZQ==\n'
        elif args[:2] == ['wg', 'pubkey']:
            out = 'U0VSVkVS\n'
        elif args[:3] == ['ip', 'link', 'show']:
            code = 0 if self.exists else 1
        elif args[-1] == 'peers':
            out = '\n'.join(self.peers)
        elif args[-1] == 'latest-handshakes':
            out = f'{PEER}\t1761700000\nb3RoZXI=\t0'
        return subprocess.CompletedProcess(args, code, out, '')


class AccessVPNServerTest(unittest.TestCase):
    def setUp(self):
        self.dir = tempfile.TemporaryDirectory()
        self.key = Path(self.dir.name) / 'server.key'

    def tearDown(self):
        self.dir.cleanup()

    def test_listens_with_one_peer_per_device_and_forwards_the_dashboard(self):
        host = FakeHost(exists=False, peers=('cmV2b2tlZA==',))
        report = access_vpn_server.apply(SERVER, CONFIG, run=host, key=self.key)
        self.assertEqual(self.key.stat().st_mode & 0o777, 0o600)
        self.assertIn(['wg', 'set', 'hakopod-wgs', 'listen-port', '51820', 'private-key', str(self.key)], host.calls)
        self.assertIn(['wg', 'set', 'hakopod-wgs', 'peer', 'cmV2b2tlZA==', 'remove'], host.calls)
        self.assertIn(['wg', 'set', 'hakopod-wgs', 'peer', PEER, 'allowed-ips', '10.100.0.2/32'], host.calls)
        nat = next(text for text in host.inputs if text and 'dnat' in text)
        self.assertIn('iifname "hakopod-wgs" ip daddr 10.100.0.1 tcp dport { 6443, 8443 } dnat to 192.0.2.4', nat)
        self.assertEqual(report['server_public_key'], 'U0VSVkVS')
        self.assertEqual([p['public_key'] for p in report['peers']], [PEER])

    def test_off_removes_interface_and_forwarding(self):
        host = FakeHost(exists=True)
        self.assertIsNone(access_vpn_server.apply(None, CONFIG, run=host, key=self.key))
        self.assertIn(['ip', 'link', 'del', 'dev', 'hakopod-wgs'], host.calls)
        self.assertIn('delete table ip hakopod_vpn_server', host.inputs[-1])

    def test_refuses_unsafe_state(self):
        for change in ({'address': '10.42.0.1/24'}, {'listen_port': 0}, {'peers': [{'public_key': PEER, 'address': '10.200.0.2'}]},
                       {'peers': [{'public_key': 'AAAA', 'address': '10.100.0.2'}]}, {'extra': 1}):
            host = FakeHost(exists=True)
            with self.assertRaises(ValueError, msg=change):
                access_vpn_server.apply({**SERVER, **change}, CONFIG, run=host, key=self.key)
            self.assertEqual(host.calls, [['ip', 'link', 'show', 'dev', 'hakopod-wgs']])


if __name__ == '__main__':
    unittest.main()
