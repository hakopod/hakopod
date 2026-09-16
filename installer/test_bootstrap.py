"""Bootstrap verification and real pipe/terminal handoff without host changes."""
import hashlib
import contextlib
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
    def test_upgrade_preflight_failure_is_returned_without_running_upgrade(self):
        with tempfile.TemporaryDirectory() as temporary, contextlib.ExitStack() as stack:
            target = '0.1.0-alpha.10'
            names = [f'hakopod_{target}_{kind}.tar.gz' for kind in ('installer', 'linux_amd64', 'dashboard')] + ['upgrade.json']
            def extract(source, destination, root):
                helper = destination / root / 'installer/maintenance.py'
                helper.parent.mkdir(parents=True)
                helper.write_text("# supports '--check-upgrade'\n")
            stack.enter_context(patch.object(bootstrap, 'architecture', return_value='amd64'))
            stack.enter_context(patch.object(bootstrap.os, 'geteuid', return_value=0))
            stack.enter_context(patch.object(bootstrap.shutil, 'which', return_value='/bin/bash'))
            stack.enter_context(patch.object(bootstrap, 'download'))
            stack.enter_context(patch.object(bootstrap, 'checksums', return_value=dict.fromkeys(names, 'a'*64)))
            stack.enter_context(patch.object(bootstrap, 'extract_kit', side_effect=extract))
            run = stack.enter_context(patch.object(bootstrap.subprocess, 'run', return_value=types.SimpleNamespace(returncode=17)))
            self.assertEqual(bootstrap.main(['--upgrade', '--version', target, '--yes']), 17)
            self.assertEqual(run.call_count, 1)
            self.assertIn('--check-upgrade', run.call_args.args[0])

    def release(self, value, prerelease=True, draft=False):
        names = ['SHA256SUMS', 'installer.sh'] + ['hakopod_' + value + '_' + part + '.tar.gz'
                 for part in ('installer', 'dashboard', 'linux_amd64', 'linux_arm64')]
        return dict(tag_name='v' + value, draft=draft, prerelease=prerelease, assets=[{'name': name} for name in names])

    def test_release_selection_uses_complete_published_versions(self):
        releases = [self.release('0.1.0-alpha.9'), self.release('0.1.0-alpha.10'), self.release('0.1.0-alpha.11', draft=True)]
        incomplete = self.release('0.1.0-alpha.12'); incomplete['assets'].pop()
        releases.append(incomplete)
        self.assertEqual(bootstrap.release_version(releases), '0.1.0-alpha.10')
        releases += [self.release('0.1.0', prerelease=False), self.release('0.2.0-beta.1')]
        self.assertEqual(bootstrap.release_version(releases), '0.1.0')
        for invalid in ([], {}, [incomplete], [self.release('../escape')]):
            with self.assertRaises(ValueError): bootstrap.release_version(invalid)

    def test_discovery_failure_does_not_use_packaged_version(self):
        with tempfile.TemporaryDirectory() as directory, patch.object(bootstrap, 'download', side_effect=OSError('unavailable')):
            with self.assertRaisesRegex(ValueError, 'no old version was selected'):
                bootstrap.latest_version(Path(directory))

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

    def fixtures(self, directory, arch='amd64', release_version=None):
        assets = directory / 'assets'; assets.mkdir()
        version = release_version or bootstrap.DEFAULT_VERSION
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
        (assets / 'releases.json').write_text(json.dumps([self.release(version)]))
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
        name = 'releases.json' if request.full_url.startswith('https://api.github.com/') else request.full_url.rsplit('/', 1)[-1]
        return Response((Path(os.environ['FIXTURE_ASSETS']) / name).read_bytes())
urllib.request.build_opener = lambda *args: Opener()
''')
        # Ensure the script chooses this interpreter even on machines with several Pythons.
        bin_dir = directory / 'bin'; bin_dir.mkdir()
        (bin_dir / 'python3').symlink_to(sys.executable)
        env = dict(os.environ, PYTHONPATH=str(directory), FIXTURE_ASSETS=str(assets), FIXTURE_ARCH='x86_64' if arch == 'amd64' else 'aarch64',
                   BOOTSTRAP=str(SCRIPT), PATH=str(bin_dir) + os.pathsep + os.environ['PATH'])
        return assets, env

    def test_extended_tar_header_cannot_bypass_decompression_bound(self):
        import gzip
        with tempfile.TemporaryDirectory() as temporary:
            source = Path(temporary) / 'kit.tar.gz'
            info = tarfile.TarInfo('pax'); info.type = tarfile.XHDTYPE; info.size = 41 * bootstrap.MIB
            with gzip.open(source, 'wb') as stream:
                stream.write(info.tobuf())
                for _ in range(41): stream.write(b'\0' * bootstrap.MIB)
            with self.assertRaisesRegex(ValueError, 'decompression bounds'):
                bootstrap.extract_kit(source, Path(temporary) / 'out', 'kit')

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
                (assets / 'releases.json').unlink()  # Explicit pins must work without release discovery.
                env['FIXTURE_ARCH'] = 'x86_64' if arch == 'amd64' else 'aarch64'
                config = directory / 'config.json'; config.write_text(json.dumps({'version': bootstrap.DEFAULT_VERSION}))
                args = ['sh', str(SCRIPT), '--config', str(config), '--dry-run', '--resume', '--yes']
                result = subprocess.run(args, env=env, capture_output=True, text=True, start_new_session=True, timeout=10)
                self.assertEqual(result.returncode, 0, result.stderr)
                for flag in ('<--resume>', '<--dry-run>', '<--yes>', '<--version> <' + bootstrap.DEFAULT_VERSION + '>', '<--arch> <' + arch + '>'):
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
            _, env = self.fixtures(Path(temporary), release_version='0.1.0-alpha.99')
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
                self.assertIn(b'Downloading Hakopod 0.1.0-alpha.99', output)
            finally:
                if process.poll() is None: process.kill(); process.wait()
                os.close(master)


if __name__ == '__main__':
    unittest.main()
