"""Bootstrap verification and real pipe/terminal handoff without host changes."""
import hashlib
import io
import json
import os
from pathlib import Path
import platform
import select
import subprocess
import sys
import tarfile
import tempfile
import time
import types
import unittest
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[1]
SCRIPT = ROOT / 'scripts/installer.sh'
SOURCE = SCRIPT.read_text().split("<<'HAKOPOD_BOOTSTRAP_PY'\n", 1)[1].split('\nHAKOPOD_BOOTSTRAP_PY', 1)[0]
bootstrap = types.ModuleType('hakopod_bootstrap')
exec(compile(SOURCE, str(SCRIPT), 'exec'), bootstrap.__dict__)


class BootstrapTest(unittest.TestCase):
    def test_platform_and_version_rejection(self):
        self.assertEqual(bootstrap.architecture('Linux', 'x86_64'), 'amd64')
        for arch in ('aarch64', 'arm64'):
            self.assertEqual(bootstrap.architecture('Linux', arch), 'arm64')
        for system, arch in [('Darwin', 'arm64'), ('Linux', 'i686'), ('Windows', 'AMD64')]:
            with self.assertRaises(ValueError): bootstrap.architecture(system, arch)
        for version in ('../latest', 'latest', 'v0.1.0', '0.1.0;id', '0.1.0\n', '01.2.3', '0.1', '0.1.0-alpha..1'):
            with self.assertRaises(ValueError): bootstrap.version(version)
        self.assertEqual(bootstrap.version('0.1.0-alpha.1'), '0.1.0-alpha.1')

    def test_checksum_inventory_is_strict(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / 'SHA256SUMS'
            line = 'a' * 64 + '  artifact.tar.gz\n'
            path.write_text(line)
            self.assertEqual(bootstrap.checksums(path), {'artifact.tar.gz': 'a' * 64})
            for data in ('', line * 2, 'a' * 64 + '  ../escape\n', 'b' * 63 + '  artifact.tar.gz\n'):
                path.write_text(data)
                with self.assertRaises(ValueError): bootstrap.checksums(path)

    def test_bounded_verified_https_download(self):
        class Response(io.BytesIO):
            headers = {}
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / 'archive'
            with self.assertRaises(ValueError): bootstrap.download('http://example.com/a', path, 10)
            with patch.object(bootstrap.urllib.request, 'build_opener') as opener:
                for limit, expected in [(2, None), (32, '0' * 64)]:
                    opener.return_value.open.return_value = Response(b'bytes')
                    with self.assertRaises(ValueError): bootstrap.download('https://example.com/a', path, limit, expected)
                    self.assertFalse(path.exists())
                opener.return_value.open.return_value = Response(b'bytes')
                bootstrap.download('https://example.com/a', path, 32, hashlib.sha256(b'bytes').hexdigest())
                self.assertEqual(path.read_bytes(), b'bytes')
        redirect = bootstrap.HTTPSRedirect()
        with self.assertRaises(ValueError): redirect.redirect_request(None, None, 302, '', {}, 'http://example.com/a')

    def test_kit_rejects_traversal_links_and_bombs(self):
        for name, kind, size in [('../escape', tarfile.REGTYPE, 0), ('kit/escape', tarfile.SYMTYPE, 0),
                                  ('kit/escape', tarfile.LNKTYPE, 0), ('kit/escape', tarfile.FIFOTYPE, 0),
                                  ('kit/large', tarfile.REGTYPE, 33 * bootstrap.MIB)]:
            with self.subTest(name=name, kind=kind, size=size), tempfile.TemporaryDirectory() as directory:
                source = Path(directory) / 'kit.tar.gz'
                # A header alone is sufficient: the bound must reject before reading payload.
                import gzip
                info = tarfile.TarInfo(name); info.type = kind; info.size = size; info.linkname = '/tmp/escape'
                with gzip.open(source, 'wb') as stream: stream.write(info.tobuf() + b'\0' * 1024)
                with self.assertRaises(ValueError): bootstrap.extract_kit(source, Path(directory) / 'out', 'kit')
                self.assertFalse((Path(directory) / 'escape').exists())

    def fixtures(self, directory, arch='amd64'):
        assets = directory / 'assets'; assets.mkdir()
        version = '0.1.0-alpha.1'
        kit = 'hakopod_' + version + '_installer'
        installer = b'''#!/usr/bin/env bash
set -eu
printf 'FIXTURE INSTALLER'
printf ' <%s>' "$@"
printf '\\n'
if [ -t 0 ]; then printf 'TTY INPUT: '; IFS= read -r reply; printf 'TTY RECEIVED %s\\n' "$reply"; fi
'''
        with tarfile.open(assets / (kit + '.tar.gz'), 'w:gz') as archive:
            for name, value in [('scripts/install.sh', installer), ('installer/host.py', b'# fixture')]:
                info = tarfile.TarInfo(kit + '/' + name); info.size = len(value); info.mode = 0o644
                archive.addfile(info, io.BytesIO(value))
        for name in ('linux_' + arch, 'dashboard'):
            (assets / ('hakopod_' + version + '_' + name + '.tar.gz')).write_bytes(b'verified fixture')
        (assets / 'SHA256SUMS').write_text(''.join(hashlib.sha256(path.read_bytes()).hexdigest() + '  ' + path.name + '\n'
            for path in sorted(assets.iterdir())))
        # Redirect the real bootstrap's network only inside its test subprocess.
        hook = directory / 'sitecustomize.py'
        hook.write_text('''import io, os, platform, urllib.request
from pathlib import Path
platform.system = lambda: 'Linux'
platform.machine = lambda: os.environ['FIXTURE_ARCH']
class Response(io.BytesIO):
    headers = {}
class Opener:
    def open(self, request, timeout):
        return Response((Path(os.environ['FIXTURE_ASSETS']) / request.full_url.rsplit('/', 1)[-1]).read_bytes())
urllib.request.build_opener = lambda *args: Opener()
''')
        # Ensure the script chooses this interpreter even on machines with several Pythons.
        bin_dir = directory / 'bin'; bin_dir.mkdir()
        (bin_dir / 'python3').symlink_to(sys.executable)
        env = dict(os.environ, PYTHONPATH=str(directory), FIXTURE_ASSETS=str(assets), FIXTURE_ARCH='x86_64' if arch == 'amd64' else 'aarch64',
                   BOOTSTRAP=str(SCRIPT), PATH=str(bin_dir) + os.pathsep + os.environ['PATH'])
        return assets, env

    def test_pipe_without_terminal_requires_explicit_inputs_before_download(self):
        with tempfile.TemporaryDirectory() as temporary:
            directory = Path(temporary)
            _, env = self.fixtures(directory)
            result = subprocess.run(['sh', '-c', 'cat "$BOOTSTRAP" | sh -s -- --dry-run'], env=env,
                capture_output=True, text=True, start_new_session=True, timeout=10)
            self.assertNotEqual(result.returncode, 0)
            self.assertIn('/dev/tty', result.stderr)
            self.assertNotIn('Downloading', result.stdout)

    def test_corruption_prevents_execution_and_flags_reach_host_installer(self):
        for arch in ('amd64', 'arm64'):
            with self.subTest(arch=arch), tempfile.TemporaryDirectory() as temporary:
                directory = Path(temporary)
                assets, env = self.fixtures(directory, arch)
                env['FIXTURE_ARCH'] = 'x86_64' if arch == 'amd64' else 'aarch64'
                config = directory / 'config.json'; config.write_text(json.dumps({'version': '0.1.0-alpha.1'}))
                args = ['sh', str(SCRIPT), '--config', str(config), '--dry-run', '--resume', '--yes']
                result = subprocess.run(args, env=env, capture_output=True, text=True, start_new_session=True, timeout=10)
                self.assertEqual(result.returncode, 0, result.stderr)
                for flag in ('<--resume>', '<--dry-run>', '<--yes>', '<--version> <0.1.0-alpha.1>', '<--arch> <' + arch + '>'):
                    self.assertIn(flag, result.stdout)
                next(assets.glob('*_linux_*.tar.gz')).write_bytes(b'corrupt')
                result = subprocess.run(args, env=env, capture_output=True, text=True, start_new_session=True, timeout=10)
                self.assertNotEqual(result.returncode, 0)
                self.assertIn('checksum mismatch', result.stderr)
                self.assertNotIn('FIXTURE INSTALLER', result.stdout)

    @unittest.skipUnless(os.name == 'posix', 'PTY test requires POSIX')
    def test_real_pipe_prompts_read_controlling_terminal(self):
        import fcntl
        import pty
        import termios
        with tempfile.TemporaryDirectory() as temporary:
            _, env = self.fixtures(Path(temporary))
            env['FIXTURE_ARCH'] = 'x86_64'
            master, slave = pty.openpty()
            def session():
                os.setsid()
                fcntl.ioctl(0, termios.TIOCSCTTY, 0)
            process = subprocess.Popen(['sh', '-c', 'cat "$BOOTSTRAP" | sh -s -- --dry-run'], env=env,
                stdin=slave, stdout=slave, stderr=slave, preexec_fn=session)
            os.close(slave)
            output, replied, deadline = b'', False, time.monotonic() + 10
            try:
                while time.monotonic() < deadline:
                    if select.select([master], [], [], 0.1)[0]:
                        try: data = os.read(master, 8192)
                        except OSError: break
                        if not data: break
                        output += data
                        if b'TTY INPUT:' in output and not replied:
                            os.write(master, b'fixture-reply\n'); replied = True
                    if process.poll() is not None: break
                self.assertEqual(process.wait(timeout=2), 0, output.decode())
                self.assertIn(b'TTY RECEIVED fixture-reply', output)
            finally:
                if process.poll() is None: process.kill(); process.wait()
                os.close(master)


if __name__ == '__main__':
    unittest.main()
