"""Exercise the shell bootstrap before Python exists, using fake OS tools."""
import os
from pathlib import Path
import subprocess
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[1]
PREFIX = (ROOT / 'scripts/installer.sh').read_text().split('\nhakopod_bootstrap() {', 1)[0]


class BootstrapPrerequisitesTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory(); self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name)
        self.bin = self.root / 'bin'; self.bin.mkdir()
        os_release = self.root / 'os-release'; os_release.write_text('ID=ubuntu\nVERSION_ID="24.04"\n')
        self.script = self.root / 'bootstrap.sh'
        self.script.write_text(PREFIX.replace('/etc/os-release', str(os_release)).replace('/etc/ssl/certs/ca-certificates.crt', str(self.root / 'ca.pem')) + '\nhakopod_prerequisites "$@"\n')
        self.env = dict(os.environ, PATH=str(self.bin), FIXTURE_APT_LOG=str(self.root / 'apt.log'))
        self.command('uname', 'case "$1" in -s) echo Linux;; -m) echo x86_64;; esac')
        self.command('id', 'echo 0')
        self.command('dpkg-query', 'case "$3" in python3) exit 1;; *) echo installed;; esac')
        self.command('timeout', 'shift; exec "$@"')
        self.command('apt-get', 'printf "%s\\n" "$*" >> "$FIXTURE_APT_LOG"')

    def command(self, name, body):
        path = self.bin / name; path.write_text('#!/bin/sh\n' + body + '\n'); path.chmod(0o755)

    def run_bootstrap(self, *args):
        return subprocess.run(['/bin/sh', str(self.script), *args], env=self.env, capture_output=True, text=True, timeout=5)

    def test_dry_run_does_not_install_without_python(self):
        result = self.run_bootstrap('--dry-run')
        self.assertEqual(result.returncode, 2, result.stderr)
        self.assertIn('no packages installed', result.stdout)
        self.assertFalse((self.root / 'apt.log').exists())

    def test_missing_python_is_installed_without_extra_confirmation(self):
        result = self.run_bootstrap('--config', '/root/config.json', '--yes')
        self.assertEqual(result.returncode, 0, result.stderr)
        commands = (self.root / 'apt.log').read_text().splitlines()
        self.assertEqual(len(commands), 2)
        self.assertTrue(commands[1].endswith('install -y python3'), commands[1])
        self.assertNotIn('docker', commands[1])

    def test_invalid_cli_never_installs_packages(self):
        for args in [('--unknown',), ('--config',), ('--config', '--yes'), ('--version', '../release')]:
            with self.subTest(args=args):
                self.assertNotEqual(self.run_bootstrap(*args).returncode, 0)
                self.assertFalse((self.root / 'apt.log').exists())

    def test_help_and_prepared_host_avoid_package_management(self):
        self.assertEqual(self.run_bootstrap('--help').returncode, 2)
        for command in ('python3', 'bash', 'curl'): self.command(command, 'exit 0')
        (self.root / 'ca.pem').write_text('fixture CA')
        self.assertEqual(self.run_bootstrap('--config', '/root/config.json').returncode, 0)
        self.assertFalse((self.root / 'apt.log').exists())


if __name__ == '__main__': unittest.main()
