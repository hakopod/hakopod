import json
import tempfile
import unittest
from pathlib import Path

import modules


class PublicTCPModuleTest(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        root = Path(self.tmp.name).resolve()
        self.config = root / 'config.json'
        self.config.write_text(json.dumps({'deployment_mode': 'self-hosted', 'dashboard_port': 8443, 'public_tcp_ports': [465]}))
        self.env = root / 'api.env'
        self.env.write_text('HAKOPOD_APP_DOMAIN="apps.example.com"\nHAKOPOD_PUBLIC_TCP_PORTS="465"\n')
        self.chart = root / 'chart'
        self.chart.mkdir()
        (self.chart / 'Chart.yaml').write_text('name: hakopod-platform\n')
        self.commands = []
        self.claims = []
        self.values = {'kubernetes-ingress': {'controller': {'service': {'type': 'ClusterIP', 'tcpPorts': []}}}}

    def tearDown(self):
        self.tmp.cleanup()

    def apply(self, ports, free=lambda port: True):
        def run(*args):
            self.commands.append(args)
            if 'upgrade' in args:
                self.uploaded = json.loads(Path(args[args.index('--values') + 1]).read_text())
        return modules.set_public_tcp_ports(
            ports, config_path=self.config, api_env=self.env, chart=self.chart, run_command=run,
            read_json=lambda *args: {'items': [{'metadata': {'name': 'hakopod-tcp-port-' + str(p)}} for p in self.claims]},
            port_free=free, output=lambda *args, **kwargs: json.dumps(self.values))

    def test_adds_ports_to_haproxy_api_and_config_together(self):
        self.apply([587, 465])
        self.assertEqual([p['port'] for p in self.uploaded['kubernetes-ingress']['controller']['service']['tcpPorts']], [465, 587])
        self.assertEqual(self.uploaded['kubernetes-ingress']['controller']['service']['type'], 'ClusterIP')
        self.assertIn('HAKOPOD_PUBLIC_TCP_PORTS="465,587"', self.env.read_text())
        self.assertIn('HAKOPOD_APP_DOMAIN="apps.example.com"', self.env.read_text())
        self.assertEqual(json.loads(self.config.read_text())['public_tcp_ports'], [465, 587])
        self.assertEqual(self.commands[-1], ('systemctl', 'restart', 'hakopod-api'))

    def test_refuses_to_remove_a_claimed_port(self):
        self.claims = [465]
        with self.assertRaisesRegex(ValueError, 'still claimed.*465'):
            self.apply([587])
        self.assertEqual(self.commands, [])
        self.assertEqual(json.loads(self.config.read_text())['public_tcp_ports'], [465])

    def test_refuses_busy_new_ports_and_platform_ports(self):
        with self.assertRaisesRegex(ValueError, 'already in use.*587'):
            self.apply([465, 587], free=lambda port: port != 587)
        with self.assertRaisesRegex(ValueError, 'non-platform'):
            self.apply([8443])
        self.assertEqual(self.commands, [])

    def test_unchanged_list_does_nothing(self):
        self.apply([465])
        self.assertEqual(self.commands, [])

    def test_ports_flag_only_for_public_tcp(self):
        with self.assertRaisesRegex(ValueError, 'requires --ports'):
            modules.apply_module('public-tcp')
        with self.assertRaisesRegex(ValueError, 'applies only to public-tcp'):
            modules.apply_module('storage', ports=[587])


if __name__ == '__main__':
    unittest.main()
