import json
import subprocess
import tempfile
import unittest
from pathlib import Path

import access_vpn

PEER = 'xTIBA5rboUvnH4htodjb6e697QjLERt1NAB4mZqp8Dg='
VPN = {'address': '10.99.0.2/32', 'peer_public_key': PEER, 'peer_endpoint': 'vpn.example.com:51820', 'allowed_ips': ['10.99.0.0/24'], 'keepalive': 25}


class FakeHost:
    """Records commands and answers the read-only ones like a host would."""

    def __init__(self, exists=True, peers=(PEER,), routes=(), addresses=()):
        self.calls, self.exists, self.peers, self.routes, self.addresses = [], exists, list(peers), list(routes), list(addresses)

    def __call__(self, args, **kwargs):
        self.calls.append(args)
        out, code = '', 0
        if args[:2] == ['wg', 'genkey']:
            out = 'cHJpdmF0ZS1rZXktZm9yLXRlc3RzLW9ubHktMDAwMDA9\n'
        elif args[:2] == ['wg', 'pubkey']:
            out = 'U0VSVkVSLVBVQkxJQy1LRVktRk9SLVRFU1RTLTAwMDA9\n'
        elif args[:3] == ['ip', 'link', 'show']:
            code = 0 if self.exists else 1
        elif args[-1] == 'peers':
            out = '\n'.join(self.peers)
        elif args[:3] == ['ip', '-j', 'address']:
            out = json.dumps([{'addr_info': [{'local': a.split('/')[0], 'prefixlen': int(a.split('/')[1])} for a in self.addresses]}])
        elif args[:3] == ['ip', '-j', 'route']:
            out = json.dumps(list(self.routes))
        elif args[-1] == 'dump':
            out = 'priv\tpub\t0\toff\n' + f'{PEER}\t(none)\t203.0.113.9:51820\t10.99.0.0/24\t1761700000\t1200\t3400\t25'
        return subprocess.CompletedProcess(args, code, out, '')


class AccessVPNTest(unittest.TestCase):
    def setUp(self):
        self.dir = tempfile.TemporaryDirectory()
        self.key = Path(self.dir.name) / 'wireguard' / 'private.key'

    def tearDown(self):
        self.dir.cleanup()

    def test_creates_interface_peer_routes_and_reports_status(self):
        host = FakeHost(exists=False, peers=(PEER, 'b2xkLXBlZXI='), routes=[{'dst': '10.50.0.0/24', 'protocol': 'boot'}, {'dst': '10.99.0.0/24', 'protocol': 'kernel'}], addresses=['10.98.0.2/32'])
        status = access_vpn.apply(VPN, run=host, key=self.key)
        self.assertEqual(self.key.stat().st_mode & 0o777, 0o600)
        self.assertEqual(status['public_key'], 'U0VSVkVSLVBVQkxJQy1LRVktRk9SLVRFU1RTLTAwMDA9')
        self.assertEqual((status['rx_bytes'], status['tx_bytes']), (1200, 3400))
        self.assertTrue(status['latest_handshake'].startswith('2025-'))
        self.assertIn(['ip', 'link', 'add', 'dev', 'hakopod-wg', 'type', 'wireguard'], host.calls)
        self.assertIn(['wg', 'set', 'hakopod-wg', 'peer', 'b2xkLXBlZXI=', 'remove'], host.calls)
        self.assertIn(['ip', 'address', 'del', '10.98.0.2/32', 'dev', 'hakopod-wg'], host.calls)
        self.assertIn(['ip', 'route', 'del', '10.50.0.0/24', 'dev', 'hakopod-wg'], host.calls)
        self.assertNotIn(['ip', 'route', 'del', '10.99.0.0/24', 'dev', 'hakopod-wg'], host.calls)
        self.assertFalse(any('genkey' in call for call in host.calls[1:] if call[:2] == ['wg', 'genkey']))

    def test_keeps_an_existing_key(self):
        self.key.parent.mkdir(parents=True)
        self.key.write_text('existing\n')
        host = FakeHost()
        access_vpn.apply(VPN, run=host, key=self.key)
        self.assertNotIn(['wg', 'genkey'], [call[:2] for call in host.calls])
        self.assertEqual(self.key.read_text(), 'existing\n')

    def test_no_connector_removes_the_interface(self):
        host = FakeHost(exists=True)
        self.assertIsNone(access_vpn.apply(None, run=host, key=self.key))
        self.assertIn(['ip', 'link', 'del', 'dev', 'hakopod-wg'], host.calls)

    def test_refuses_unsafe_state(self):
        for change in ({'allowed_ips': ['0.0.0.0/0']}, {'allowed_ips': ['10.42.1.0/24']}, {'peer_endpoint': 'x;reboot:51820'},
                       {'peer_public_key': 'AAAA'}, {'keepalive': 0}, {'extra': 1}, {'allowed_ips': ['10.99.0.1/24']}):
            host = FakeHost()
            with self.assertRaises(ValueError, msg=change):
                access_vpn.apply({**VPN, **change}, run=host, key=self.key)
            self.assertEqual(host.calls, [])


if __name__ == '__main__':
    unittest.main()
