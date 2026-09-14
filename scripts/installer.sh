#!/bin/sh
# Bootstrap a prebuilt release; the verified Bash installer owns host changes.
set -eu
set +x
umask 077

hakopod_prerequisites() {
  bootstrap_dry_run=false
  bootstrap_help=false
  while [ "$#" -gt 0 ]; do
    case "$1" in
      --dry-run) bootstrap_dry_run=true; shift;;
      --help|-h) bootstrap_help=true; shift;;
      --resume|--yes|--upgrade) shift;;
      --config|--version)
        if [ "$#" -lt 2 ] || [ -z "$2" ]; then
          printf '%s requires a value\n' "$1" >&2; return 1
        fi
        case "$2" in --*) printf '%s requires a value\n' "$1" >&2; return 1;; esac
        if [ "$1" = --version ]; then
          case "$2" in *[!A-Za-z0-9.-]*|v*|[!0-9]*) printf '%s\n' 'Use an explicit release version without v.' >&2; return 1;; esac
        fi
        shift 2;;
      *) printf 'Unknown installer argument: %s\n' "$1" >&2; return 1;;
    esac
  done
  if "$bootstrap_help"; then
    printf '%s\n' 'Usage: sh installer.sh [--version VERSION] [--config FILE] [--dry-run] [--resume] [--yes] [--upgrade]'
    printf '%s\n' 'Installs missing Linux prerequisites, verifies prebuilt Hakopod, then reviews the host plan.'
    return 2
  fi
  # Prepared hosts skip package management. This phase can run before Python,
  # Bash or system CA certificates exist on a minimal cloud image.
  if command -v python3 >/dev/null 2>&1 && command -v bash >/dev/null 2>&1 && command -v curl >/dev/null 2>&1; then
    if [ "$(uname -s)" != Linux ] || [ -s /etc/ssl/certs/ca-certificates.crt ]; then return 0; fi
  fi
  [ "$(uname -s)" = Linux ] || { printf '%s\n' 'Hakopod host installation requires Linux.' >&2; return 1; }
  case "$(uname -m)" in x86_64|aarch64|arm64) ;; *) printf '%s\n' 'Hakopod requires amd64 or arm64.' >&2; return 1;; esac
  bootstrap_os='' bootstrap_os_version=''
  while IFS= read -r bootstrap_line; do
    case "$bootstrap_line" in
      ID=*) bootstrap_os=${bootstrap_line#ID=};;
      VERSION_ID=*) bootstrap_os_version=${bootstrap_line#VERSION_ID=};;
    esac
  done < /etc/os-release
  bootstrap_os=${bootstrap_os#\"}; bootstrap_os=${bootstrap_os%\"}
  bootstrap_os_version=${bootstrap_os_version#\"}; bootstrap_os_version=${bootstrap_os_version%\"}
  case "$bootstrap_os:$bootstrap_os_version" in ubuntu:24.04|ubuntu:26.04|debian:12|debian:13) ;;
    *) printf '%s\n' 'Supported hosts: Ubuntu 24.04/26.04 or Debian 12/13.' >&2; return 1;;
  esac
  printf '%s\n' 'Prerequisite plan: install missing Python 3, Bash, curl and system CA certificates from the configured distro repositories.'
  if "$bootstrap_dry_run"; then
    printf '%s\n' 'Dry run: no packages installed. Detailed configuration review needs these prerequisites.'
    return 2
  fi
  [ "$(id -u)" = 0 ] || { printf '%s\n' 'Run as root to install missing prerequisites, or use --dry-run.' >&2; return 1; }
  command -v apt-get >/dev/null 2>&1 || { printf '%s\n' 'The supported distro package manager apt-get is missing.' >&2; return 1; }
  # Package names are a fixed allowlist; install only missing packages.
  set --
  for bootstrap_package in python3 bash curl ca-certificates; do
    bootstrap_installed=$(dpkg-query -W '-f=${db:Status-Status}' "$bootstrap_package" 2>/dev/null) || bootstrap_installed=''
    if [ "$bootstrap_installed" != installed ]; then set -- "$@" "$bootstrap_package"; fi
  done
  [ "$#" -gt 0 ] || { printf '%s\n' 'Installed prerequisite packages are damaged; repair them with the OS package manager.' >&2; return 1; }
  DEBIAN_FRONTEND=noninteractive timeout 600 apt-get -o DPkg::Lock::Timeout=120 -o Acquire::Retries=2 -o Acquire::http::Timeout=30 -o Acquire::https::Timeout=30 update -qq
  DEBIAN_FRONTEND=noninteractive timeout 1200 apt-get -o DPkg::Lock::Timeout=120 -o Acquire::Retries=2 -o Acquire::http::Timeout=30 -o Acquire::https::Timeout=30 --no-install-recommends install -y "$@"

}

hakopod_bootstrap() {
  bootstrap_status=0
  hakopod_prerequisites "$@" || bootstrap_status=$?
  case "$bootstrap_status" in 0) ;; 2) return 0;; *) return "$bootstrap_status";; esac
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

# Release packaging stamps this provenance marker; it is not the install default.
DEFAULT_VERSION = '0.1.0-alpha.2'
RELEASES = 'https://github.com/hakopod/hakopod/releases/download'
RELEASE_INDEX = 'https://api.github.com/repos/hakopod/hakopod/releases?per_page=100'
VERSION = re.compile(r'(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)(?:-[a-zA-Z0-9]+(?:[.-][a-zA-Z0-9]+)*)?')
MIB = 1024 * 1024

def version(value):
    if not isinstance(value, str) or len(value) > 64 or not VERSION.fullmatch(value):
        raise ValueError('Version must be an explicit release number, such as 0.1.0-alpha.2 (without v)')
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

def release_version(releases):
    if not isinstance(releases, list) or len(releases) > 100:
        raise ValueError('Invalid GitHub release inventory')
    candidates = []
    for release in releases:
        if not isinstance(release, dict) or release.get('draft') is not False:
            continue
        try:
            tag = release['tag_name']
            if not isinstance(tag, str) or not tag.startswith('v'):
                continue
            candidate = version(tag[1:])
            core, separator, suffix = candidate.partition('-')
            prerelease = tuple((0, int(p)) if p.isdigit() else (1, p) for p in suffix.split('.')) if separator else ()
            if separator and any(p.isdigit() and len(p) > 1 and p.startswith('0') for p in suffix.split('.')):
                continue
            assets = {asset.get('name') for asset in release.get('assets', []) if isinstance(asset, dict)}
            required = {'SHA256SUMS', 'installer.sh', 'hakopod_' + candidate + '_installer.tar.gz',
                        'hakopod_' + candidate + '_dashboard.tar.gz',
                        'hakopod_' + candidate + '_linux_amd64.tar.gz',
                        'hakopod_' + candidate + '_linux_arm64.tar.gz'}
            if not required.issubset(assets):
                continue
            stable = not separator and release.get('prerelease') is False
            candidates.append((stable, tuple(map(int, core.split('.'))), not separator, prerelease, candidate))
        except (KeyError, TypeError, ValueError):
            continue
    if not candidates:
        raise ValueError('No complete installable GitHub release found; retry later or pass --version explicitly')
    return max(candidates)[-1]

def latest_version(directory):
    index = directory / 'releases.json'
    try:
        download(RELEASE_INDEX, index, 4 * MIB)
        return release_version(json.loads(index.read_text()))
    except (OSError, ValueError) as error:
        raise ValueError('Cannot discover an installable GitHub release. Retry later or pass an explicit --version; no old version was selected. ' + str(error)) from error

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
    parser.add_argument('--version', help='Exact release without v; otherwise use the config version or newest complete stable GitHub release (prerelease if no stable release is listed)')
    parser.add_argument('--config', type=Path)
    parser.add_argument('--dry-run', action='store_true')
    parser.add_argument('--resume', action='store_true')
    parser.add_argument('--upgrade', action='store_true', help='Upgrade an installer-owned host; never rerun first-time setup')
    parser.add_argument('--yes', action='store_true', help='Accept the host plan; requires --config')
    args = parser.parse_args(argv)
    if sys.version_info < (3, 10):
        raise ValueError('Python 3.10 or newer is required')
    arch = architecture(platform.system(), platform.machine())
    if not shutil.which('bash'):
        raise ValueError('Bash is required to run the verified host installer')
    if args.yes and not args.config and not args.upgrade:
        raise ValueError('--yes requires an explicit --config file')
    if args.resume and not args.config:
        raise ValueError('--resume requires the original --config file, usually /etc/hakopod/config.json')
    if args.upgrade and (args.resume or args.config):
        raise ValueError('--upgrade cannot be combined with --resume or --config')
    config = read_config(args.config) if args.config else {}
    selected = version(args.version or config['version']) if args.version or 'version' in config else None
    if config.get('version', selected) != selected:
        raise ValueError('--version differs from the configuration; resume must use the original version')
    if not args.dry_run and os.geteuid() != 0:
        raise ValueError('Run the installer as root on a dedicated Linux host, or use --dry-run to review it')
    terminal = None
    if (not args.upgrade and not args.config) or (not args.yes and not args.dry_run):
        try:
            terminal = open('/dev/tty', 'r')
            if not os.isatty(terminal.fileno()):
                raise OSError('not a terminal')
        except OSError:
            raise ValueError('Interactive installation needs /dev/tty; use --config with --dry-run or --yes without a terminal') from None
    try:
        with tempfile.TemporaryDirectory(prefix='hakopod-bootstrap-') as temporary:
            directory = Path(temporary)
            selected = selected or latest_version(directory)
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
            if args.upgrade:
                helper = directory / 'kit' / kit / 'installer/maintenance.py'
                if not helper.is_file():
                    raise ValueError('This release does not include upgrade support; do not use resume as an upgrade')
                if args.dry_run:
                    print('Verified release artifacts. Upgrade stops only API/dashboard, backs up PostgreSQL and configuration, then switches binaries. Compatibility is checked before stopping services.')
                    return
                if not args.yes:
                    print('Upgrade to ' + selected + ': stop API/dashboard, back up PostgreSQL/configuration, then restart. Applications keep running. Type upgrade ' + selected + ' to continue:', flush=True)
                    if terminal.readline().strip() != 'upgrade ' + selected:
                        raise ValueError('Upgrade cancelled')
                subprocess.run(['python3', str(helper), '--upgrade', selected], check=True)
                return
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
