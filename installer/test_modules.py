import hashlib
import io
import json
import tarfile
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch

import maintenance
import modules

VERSION = '0.1.0-alpha.34'
BASE = 'https://github.com/hakopod/hakopod/releases/download/v' + VERSION + '/'


def agent_archive(binary=b'\x7fELF agent'):
    stream = io.BytesIO()
    with tarfile.open(fileobj=stream, mode='w:gz') as bundle:
        info = tarfile.TarInfo(f'hakopod-vpn_{VERSION}_linux_arm64/hakopod-vpn')
        info.size, info.mode = len(binary), 0o755
        bundle.addfile(info, io.BytesIO(binary))
    return stream.getvalue()


class Response(io.BytesIO):
    url = BASE + 'asset'

    def __enter__(self):
        return self

    def __exit__(self, *args):
        self.close()


class VPNAgentInstallTest(unittest.TestCase):
    """The private-access module fetches hakopod-vpn from a fake release."""

    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.dir = Path(temporary.name)
        (self.dir / 'units').mkdir()
        self.commands = []

    def install(self, served, listed=None):
        names = ('SHA256SUMS', f'hakopod-vpn_{VERSION}_linux_arm64.tar.gz')
        release = {'tag_name': 'v' + VERSION, 'assets': [{'name': n, 'browser_download_url': BASE + n} for n in names]}
        sums = f'{hashlib.sha256(listed or served).hexdigest()}  {names[1]}\n'.encode()
        fetched = {maintenance.REPO + '/releases/tags/v' + VERSION: json.dumps(release).encode(), BASE + 'SHA256SUMS': sums}
        with patch.object(maintenance, 'current', return_value=VERSION), patch.object(maintenance, 'STATE', self.dir / 'state'), \
             patch.object(maintenance, 'fetch', lambda url, limit: fetched[url]), \
             patch.object(maintenance.urllib.request, 'urlopen', lambda request, timeout: Response(served)), \
             patch.object(modules.shutil, 'which', return_value='/usr/sbin/nft'), patch.object(modules.os, 'chown'), \
             patch.object(modules, 'run', lambda *args: self.commands.append(args)), \
             patch.object(modules.subprocess, 'run', return_value=modules.subprocess.CompletedProcess([], 1)):
            modules.install_vpn_agent('aarch64', agent=self.dir / 'vpn' / 'hakopod-vpn', units=self.dir / 'units')

    def test_verified_agent_is_installed_with_its_timer(self):
        self.install(agent_archive())
        agent = self.dir / 'vpn' / 'hakopod-vpn'
        self.assertEqual(agent.read_bytes(), b'\x7fELF agent')
        self.assertEqual(agent.stat().st_mode & 0o777, 0o755)
        self.assertIn('ExecStart=/opt/hakopod/vpn/hakopod-vpn sync', (self.dir / 'units' / 'hakopod-vpn.service').read_text())
        self.assertIn('OnUnitActiveSec=10s', (self.dir / 'units' / 'hakopod-vpn.timer').read_text())
        self.assertEqual(self.commands[-1], ('systemctl', 'enable', '--now', 'hakopod-vpn.timer'))

    def test_checksum_mismatch_installs_nothing(self):
        with self.assertRaisesRegex(ValueError, 'checksum mismatch'):
            self.install(agent_archive(b'tampered'), listed=agent_archive())
        self.assertFalse((self.dir / 'vpn').exists())
        self.assertEqual(self.commands, [])

    def test_refuses_without_nftables_or_on_an_unknown_architecture(self):
        with patch.object(modules.shutil, 'which', return_value=None), self.assertRaisesRegex(ValueError, 'nftables'):
            modules.install_vpn_agent('aarch64')
        with patch.object(modules.shutil, 'which', return_value='/usr/sbin/nft'), self.assertRaisesRegex(ValueError, 'x86_64 and aarch64'):
            modules.install_vpn_agent('riscv64')


class ModulesCLITest(unittest.TestCase):
    def test_vitess_plan_and_apply_reach_managed_database_module(self):
        for option in ('--plan', '--apply-reviewed-plan'):
            with self.subTest(option=option), patch.object(modules, 'main') as main:
                modules.cli(['managed-databases', '--engines', 'vitess', option, '/tmp/review.json'])
                kwargs = {
                    'engines': ['vitess'],
                    'plan_path': '/tmp/review.json' if option == '--plan' else None,
                    'review_path': '/tmp/review.json' if option == '--apply-reviewed-plan' else None,
                }
                main.assert_called_once_with('managed-databases', None, **kwargs)

    def test_runtime_class_only_reaches_managed_actions_module(self):
        with patch.object(modules, 'main') as main:
            modules.cli(['managed-actions', '--runtime-class-only'])
        main.assert_called_once_with('managed-actions', None, engines=None, plan_path=None, review_path=None,
                                     runtime_class_only=True)

    def test_adopt_node_reaches_managed_actions_module(self):
        with patch.object(modules, 'main') as main:
            modules.cli(['managed-actions', '--adopt-node', 'spot-1'])
        main.assert_called_once_with('managed-actions', None, engines=None, plan_path=None, review_path=None,
                                     adopt_node='spot-1')

    def test_runtime_class_only_rejects_other_modules_and_profiles(self):
        for module, profile, options in (('storage', None, {'runtime_class_only': True}),
                                         ('managed-actions', 'vfs', {'runtime_class_only': True}),
                                         ('storage', None, {'adopt_node': 'spot-1'}),
                                         ('managed-actions', None, {'runtime_class_only': True, 'adopt_node': 'spot-1'})):
            with self.subTest(module=module, options=options), patch.object(modules.os, 'geteuid', return_value=0), \
                 patch.object(modules.os, 'umask'), self.assertRaises(ValueError):
                modules.apply_module(module, profile, **options)

    def test_unsupported_engine_is_rejected_before_module_handoff(self):
        with patch.object(modules, 'main') as main, self.assertRaises(SystemExit):
            modules.cli(['managed-databases', '--engines', 'unsupported', '--plan', '/tmp/review.json'])
        main.assert_not_called()


if __name__ == '__main__':
    unittest.main()
