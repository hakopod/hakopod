#!/bin/sh
# Bootstrap a prebuilt release; the verified Bash installer owns host changes.
set -eu
set +x
umask 077

hakopod_bootstrap() {
  command -v python3 >/dev/null 2>&1 || { printf '%s\n' 'Hakopod requires Python 3.10+, Bash, curl and system CA certificates.' >&2; return 1; }
  python3 - "$@" <<'HAKOPOD_BOOTSTRAP_PY'
import argparse
import gzip
import hashlib
import json
import os
from pathlib import Path, PurePosixPath
import platform
import re
import shutil
import signal
import subprocess
import sys
import tarfile
import tempfile
import time
import urllib.error
import urllib.parse
import urllib.request

DEFAULT_VERSION = '0.1.0-alpha.1'
RELEASES = 'https://github.com/hakopod/hakopod/releases/download'
VERSION = re.compile(r'(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)(?:-[a-zA-Z0-9]+(?:[.-][a-zA-Z0-9]+)*)?')
MIB = 1024 * 1024

def version(value):
    if not isinstance(value, str) or len(value) > 64 or not VERSION.fullmatch(value):
        raise ValueError('Version must be an explicit release number, such as 0.1.0-alpha.1 (without v)')
    return value

def architecture(system, machine):
    if system != 'Linux':
        raise ValueError('Prebuilt host installation supports Linux only')
    values = {'x86_64': 'amd64', 'aarch64': 'arm64', 'arm64': 'arm64'}
    if machine not in values:
        raise ValueError('Prebuilt host installation requires an amd64 or arm64 machine')
    return values[machine]

class HTTPSRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        if urllib.parse.urlsplit(newurl).scheme != 'https':
            raise ValueError('Release downloads cannot redirect outside HTTPS')
        return super().redirect_request(req, fp, code, msg, headers, newurl)

def download(url, destination, limit, expected=None):
    if urllib.parse.urlsplit(url).scheme != 'https':
        raise ValueError('Release downloads require HTTPS')
    opener = urllib.request.build_opener(HTTPSRedirect())
    request = urllib.request.Request(url, headers={'User-Agent': 'hakopod-installer', 'Accept-Encoding': 'identity'})
    digest, size, deadline = hashlib.sha256(), 0, time.monotonic() + 300
    def expired(signum, frame):
        raise TimeoutError('Release download exceeded its 300-second deadline')
    previous = signal.signal(signal.SIGALRM, expired)
    signal.alarm(300)
    try:
        with opener.open(request, timeout=20) as source, destination.open('xb') as target:
            length = source.headers.get('Content-Length')
            if length is not None and (not length.isdigit() or int(length) > limit):
                raise ValueError('Release download exceeds its size bound')
            while chunk := source.read(MIB):
                size += len(chunk)
                if size > limit or time.monotonic() > deadline:
                    raise ValueError('Release download exceeded its size or time bound')
                digest.update(chunk)
                target.write(chunk)
        if expected is not None and digest.hexdigest() != expected:
            raise ValueError('Release checksum mismatch: ' + destination.name)
    except BaseException:
        destination.unlink(missing_ok=True)
        raise
    finally:
        signal.alarm(0)
        signal.signal(signal.SIGALRM, previous)

def checksums(path):
    values = {}
    for line in path.read_text(encoding='ascii').splitlines():
        match = re.fullmatch(r'([0-9a-f]{64})  ([A-Za-z0-9][A-Za-z0-9._-]{0,200})', line)
        if not match or match[2] in values:
            raise ValueError('Invalid or duplicate release checksum entry')
        values[match[2]] = match[1]
    if not values or len(values) > 128:
        raise ValueError('Release checksum inventory exceeds its bound or is empty')
    return values

def extract_kit(source, destination, root):
    # Only the small installer kit is extracted here. The verified kit validates
    # its larger server/dashboard bundles with installer/host.py.
    class BoundedTar:
        def __init__(self, stream):
            self.stream, self.used = stream, 0
        def read(self, size):
            data = self.stream.read(min(size, 40 * MIB - self.used + 1))
            self.used += len(data)
            if self.used > 40 * MIB:
                raise ValueError('Installer kit headers or payload exceed decompression bounds')
            return data
    # Bound the decompressed stream as well as ordinary members: tar PAX headers
    # are processed internally before the member iterator exposes their sizes.
    with gzip.open(source, 'rb') as stream, tarfile.open(fileobj=BoundedTar(stream), mode='r|') as archive:
        total, count, seen = 0, 0, set()
        for member in archive:
            count += 1
            total += member.size
            path = PurePosixPath(member.name)
            if count > 4096 or total > 32 * MIB or member.size < 0:
                raise ValueError('Installer kit exceeds extraction bounds')
            if (path.is_absolute() or '..' in path.parts or not path.parts or path.parts[0] != root
                    or '\\' in member.name or str(path) in seen or len(member.name) > 1024):
                raise ValueError('Unsafe or duplicate installer kit path')
            if not (member.isfile() or member.isdir()):
                raise ValueError('Installer kit links and special files are forbidden')
            seen.add(str(path))
            target = destination.joinpath(*path.parts)
            target.parent.mkdir(parents=True, exist_ok=True)
            if member.isdir():
                target.mkdir(exist_ok=True)
            else:
                with archive.extractfile(member) as src, target.open('xb') as dst:
                    shutil.copyfileobj(src, dst, MIB)
                target.chmod(0o700 if member.mode & 0o111 else 0o600)
    if not (destination / root / 'scripts/install.sh').is_file() or not (destination / root / 'installer/host.py').is_file():
        raise ValueError('Release is missing the installer entrypoint')

def read_config(path):
    def unique(pairs):
        result = {}
        for key, value in pairs:
            if key in result:
                raise ValueError('Duplicate configuration key: ' + key)
            result[key] = value
        return result
    if path.is_symlink() or not path.is_file() or path.stat().st_size > 65536:
        raise ValueError('Configuration must be a regular file of at most 64 KiB')
    value = json.loads(path.read_text(), object_pairs_hook=unique)
    if not isinstance(value, dict):
        raise ValueError('Configuration must be a JSON object')
    return value

def main(argv=None):
    parser = argparse.ArgumentParser(description='Download and verify a prebuilt Hakopod release, then review its installation plan.')
    parser.add_argument('--version', help='Exact release without v; defaults to the config version or ' + DEFAULT_VERSION)
    parser.add_argument('--config', type=Path)
    parser.add_argument('--dry-run', action='store_true')
    parser.add_argument('--resume', action='store_true')
    parser.add_argument('--yes', action='store_true', help='Accept the host plan; requires --config')
    args = parser.parse_args(argv)
    if sys.version_info < (3, 10):
        raise ValueError('Python 3.10 or newer is required')
    arch = architecture(platform.system(), platform.machine())
    if not shutil.which('bash'):
        raise ValueError('Bash is required to run the verified host installer')
    if args.yes and not args.config:
        raise ValueError('--yes requires an explicit --config file')
    if args.resume and not args.config:
        raise ValueError('--resume requires the original --config file, usually /etc/hakopod/config.json')
    config = read_config(args.config) if args.config else {}
    selected = version(args.version or config.get('version', DEFAULT_VERSION))
    if config.get('version', selected) != selected:
        raise ValueError('--version differs from the configuration; resume must use the original version')
    if not args.dry_run and os.geteuid() != 0:
        raise ValueError('Run the installer as root on a dedicated Linux host, or use --dry-run to review it')
    terminal = None
    if not args.config or (not args.yes and not args.dry_run):
        try:
            terminal = open('/dev/tty', 'r')
            if not os.isatty(terminal.fileno()):
                raise OSError('not a terminal')
        except OSError:
            raise ValueError('Interactive installation needs /dev/tty; use --config with --dry-run or --yes without a terminal') from None
    try:
        with tempfile.TemporaryDirectory(prefix='hakopod-bootstrap-') as temporary:
            directory = Path(temporary)
            base = RELEASES + '/v' + selected + '/'
            manifest = directory / 'SHA256SUMS'
            print('Downloading Hakopod ' + selected + ' for Linux/' + arch, flush=True)
            download(base + 'SHA256SUMS', manifest, 32768)
            hashes = checksums(manifest)
            kit = 'hakopod_' + selected + '_installer'
            names = [(kit + '.tar.gz', 16 * MIB), ('hakopod_' + selected + '_linux_' + arch + '.tar.gz', 128 * MIB),
                     ('hakopod_' + selected + '_dashboard.tar.gz', 128 * MIB)]
            for name, limit in names:
                if name not in hashes:
                    raise ValueError('Release checksum inventory is missing ' + name)
                download(base + name, directory / name, limit, hashes[name])
            extract_kit(directory / (kit + '.tar.gz'), directory / 'kit', kit)
            command = ['bash', str(directory / 'kit' / kit / 'scripts/install.sh'), '--artifact-dir', str(directory),
                       '--arch', arch, '--version', selected]
            if args.config:
                command += ['--config', str(args.config.resolve())]
            for flag in ('dry_run', 'resume', 'yes'):
                if getattr(args, flag):
                    command.append('--' + flag.replace('_', '-'))
            # Never consume the pipe containing this script as prompt input.
            return subprocess.run(command, stdin=terminal or subprocess.DEVNULL, check=False).returncode
    finally:
        if terminal:
            terminal.close()

if __name__ == '__main__':
    try:
        sys.exit(main())
    except (ValueError, OSError, tarfile.TarError, urllib.error.URLError) as error:
        # Download URLs are fixed, public release assets; no credentials are used.
        print('Hakopod installer: ' + str(error), file=sys.stderr)
        sys.exit(1)
HAKOPOD_BOOTSTRAP_PY
}
hakopod_bootstrap "$@"
