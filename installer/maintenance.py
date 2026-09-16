#!/usr/bin/env python3
"""Installer-owned maintenance. No workload, OS, K3s or database-version upgrades."""
import argparse
import contextlib
import fcntl
import hashlib
import http.server
import http.client
import ssl
import urllib.parse
import json
import os
from pathlib import Path
import pwd
import re
import shutil
import socket
import socketserver
import struct
import subprocess
import tarfile
import tempfile
import threading
import time
import urllib.request

import host
import credentials

STATE = Path('/var/lib/hakopod/maintenance')
SOCKET = '/run/hakopod-maintenance/control.sock'
REPO = 'https://api.github.com/repos/hakopod/hakopod'
CONFIG = Path('/etc/hakopod/config.json')
MARKER = Path('/etc/hakopod/installation.json')
CURRENT = Path('/opt/hakopod/current')
RELEASE_DIR = Path('/opt/hakopod/releases')
INSTALL_LOCK = Path('/run/lock/hakopod-install.lock')
LOCK = threading.Lock()
CACHE = None
CACHE_AT = 0


def version_key(value):
    match = re.fullmatch(r'(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-([0-9A-Za-z.-]+))?', value)
    if not match or len(value) > 64:
        raise ValueError('Invalid release version')
    parts = []
    for part in (match[4] or '').split('.'):
        if match[4] is not None:
            if not part or (part.isdigit() and len(part) > 1 and part[0] == '0'):
                raise ValueError('Invalid prerelease version')
            parts.append((0, int(part)) if part.isdigit() else (1, part))
    return (int(match[1]), int(match[2]), int(match[3]), match[4] is None, tuple(parts))


def fetch(url, limit):
    request = urllib.request.Request(url, headers={'User-Agent': 'hakopod-maintenance', 'Accept': 'application/vnd.github+json'})
    with urllib.request.urlopen(request, timeout=3) as response:
        if not response.url.startswith('https://'):
            raise ValueError('Release download requires HTTPS')
        data = response.read(limit + 1)
    if len(data) > limit:
        raise ValueError('Release response exceeds its size limit')
    return data


def current():
    target = CURRENT.resolve(strict=True)
    if target.parent != RELEASE_DIR:
        raise ValueError('Current release is outside the installer directory')
    version_key(target.name)
    return target.name


def eligible(releases, installed):
    key = version_key(installed)
    choices = []
    for release in releases:
        try:
            candidate = release['tag_name'].removeprefix('v')
            newer = version_key(candidate) > key
        except (KeyError, TypeError, ValueError):
            continue
        if release.get('draft') or (key[3] and release.get('prerelease')) or not newer:
            continue
        if version_key(candidate)[:2] != key[:2]:
            continue
        names = {a.get('name') for a in release.get('assets', [])}
        if 'upgrade.json' in names and 'SHA256SUMS' in names:
            choices.append((version_key(candidate), release))
    return max(choices, key=lambda pair: pair[0])[1] if choices else None


def release_check():
    global CACHE, CACHE_AT
    installed = current()
    if CACHE is not None and CACHE.get("current_version") == installed and time.monotonic() - CACHE_AT < 900:
        return CACHE
    installed = current()
    try:
        releases = json.loads(fetch(REPO + '/releases?per_page=30', 2 << 20))
        if not isinstance(releases, list):
            raise ValueError('Invalid release list')
        release = None
        for _ in range(3):
            candidate = eligible(releases, installed)
            if candidate is None: break
            releases.remove(candidate)
            hashes = checksum_map(fetch(asset_url(candidate, 'SHA256SUMS'),65536))
            raw = fetch(asset_url(candidate,'upgrade.json'),65536)
            if hashes.get('upgrade.json') != hashlib.sha256(raw).hexdigest(): raise ValueError('Manifest checksum mismatch')
            try: validate_manifest(json.loads(raw), installed, candidate['tag_name'].removeprefix('v'))
            except ValueError: continue
            release = candidate
            break
        CACHE = {'current_version': installed, 'latest_version': release['tag_name'].removeprefix('v') if release else installed,
                 'update_available': release is not None, 'checked_at': time.strftime('%Y-%m-%dT%H:%M:%SZ', time.gmtime()), 'check_error': ''}
    except Exception:
        CACHE = {'current_version': installed, 'latest_version': '', 'update_available': False,
                 'checked_at': '', 'check_error': 'Could not check GitHub releases. Try again later.'}
    CACHE_AT = time.monotonic()
    return CACHE


def save_state(value):
    STATE.mkdir(mode=0o700, parents=True, exist_ok=True)
    temp = STATE / 'status.tmp'
    temp.write_text(json.dumps(value) + '\n')
    os.chmod(temp, 0o600)
    temp.replace(STATE / 'status.json')


def status():
    result = dict(release_check())
    result['current_version'] = current()
    result['operation'] = json.loads((STATE / 'status.json').read_text()) if (STATE / 'status.json').exists() else {'status': 'idle', 'message': '', 'version': ''}
    return result


def asset_url(release, name):
    matches = [a for a in release.get('assets', []) if a.get('name') == name]
    if len(matches) != 1:
        raise ValueError('Required release asset is missing or duplicated')
    url = matches[0].get('browser_download_url', '')
    expected = 'https://github.com/hakopod/hakopod/releases/download/' + release['tag_name'] + '/' + name
    if url != expected:
        raise ValueError('Release asset must belong to hakopod/hakopod')
    return url


def checksum_map(data):
    result = {}
    for line in data.decode('ascii').splitlines():
        match = re.fullmatch(r'([a-f0-9]{64})  ([A-Za-z0-9_.-]+)', line)
        if not match or match[2] in result:
            raise ValueError('Invalid release checksums')
        result[match[2]] = match[1]
    return result


def download(release, name, checksums, directory, limit):
    if name not in checksums:
        raise ValueError('Required release checksum is missing')
    destination = directory / name
    request = urllib.request.Request(asset_url(release, name), headers={'User-Agent': 'hakopod-maintenance'})
    digest = hashlib.sha256()
    total = 0
    with urllib.request.urlopen(request, timeout=20) as response, destination.open('xb') as output:
        if not response.url.startswith('https://'):
            raise ValueError('Release download requires HTTPS')
        deadline = time.monotonic() + 300
        while True:
            chunk = response.read(1 << 20)
            if not chunk:
                break
            total += len(chunk)
            if total > limit or time.monotonic() > deadline:
                raise ValueError('Release download exceeded its limit')
            digest.update(chunk)
            output.write(chunk)
    if digest.hexdigest() != checksums[name]:
        raise ValueError('Release checksum mismatch')
    return destination


def validate_manifest(manifest, installed, target):
    if manifest.get('schema_version') != 1 or manifest.get('version') != target or installed not in manifest.get('from_versions', []):
        raise ValueError(f'Upgrade from {installed} to {target} is not supported by this release. No services were stopped. Use a release that explicitly supports your installed version; do not use --resume to upgrade.')
    if version_key(target) <= version_key(installed) or version_key(target)[:2] != version_key(installed)[:2]:
        raise ValueError('Only newer releases in the same major/minor series are supported')
    if manifest.get('runtime_pins_sha256') != host.digest(Path(__file__).with_name('pins.json')):
        raise ValueError('Runtime dependencies changed; follow the release manual upgrade guide')


def validate_installed_runtime():
    arch = {'aarch64':'arm64','x86_64':'amd64'}.get(os.uname().machine)
    if not arch: raise ValueError('Unsupported architecture')
    pins = json.loads(Path(__file__).with_name('pins.json').read_text())
    saved = Path('/opt/hakopod/maintenance/pins.json')
    if saved.exists() and host.digest(saved) != host.digest(Path(__file__).with_name('pins.json')):
        raise ValueError('Installed runtime pins differ; use a reviewed manual upgrade')
    # alpha.4 predates the maintenance service but retained verified downloads.
    paths = {'k3s':Path('/opt/hakopod/tools/k3s'),
             'helm':Path('/var/lib/hakopod/downloads/helm-'+arch+'.tar.gz'),
             'node':Path('/var/lib/hakopod/downloads/node-'+arch+'.tar.gz')}
    for name,path in paths.items():
        if not path.is_file() or host.digest(path) != pins[name][arch]['sha256']:
            raise ValueError('Installed runtime/cache does not match supported pins; verify the host before upgrading')


def command(args, **kwargs):
    return subprocess.run(args, check=True, timeout=kwargs.pop('timeout', 120), stderr=subprocess.DEVNULL, **kwargs)


def dashboard_health(config):
    origin = urllib.parse.urlsplit(config['dashboard_origin'])
    connection = http.client.HTTPConnection(origin.hostname, config['dashboard_port'], timeout=3)
    address = config['node_ip'] if origin.scheme == 'https' else '127.0.0.1'
    sock = socket.create_connection((address, config['dashboard_port']),timeout=3)
    if origin.scheme == 'https':
        context = ssl.create_default_context()
        context.load_verify_locations('/etc/hakopod/dashboard.crt')
        context.verify_flags |= ssl.VERIFY_X509_PARTIAL_CHAIN
        try: sock = context.wrap_socket(sock, server_hostname=origin.hostname)
        except BaseException: sock.close(); raise
    connection.sock = sock
    try:
        connection.request('GET', '/')
        return connection.getresponse().status == 200
    finally: connection.close()


def backup(config, directory):
    with tarfile.open(directory / 'configuration.tar.gz', 'w:gz') as archive:
        archive.add('/etc/hakopod', arcname='etc/hakopod', recursive=True)
    with (directory / 'database.dump').open('xb') as output:
        max_bytes = min(8 << 30, shutil.disk_usage(directory).free - (1 << 30))
        if max_bytes < 1 << 30: raise ValueError('Insufficient backup space')
        bound = ['prlimit', '--fsize=' + str(max_bytes), '--']
        if config.get('database_mode', 'managed') == 'managed':
            command(bound + ['/opt/hakopod/tools/kubectl', '--kubeconfig=/etc/hakopod/admin-kubeconfig', '-n', 'hakopod-system',
                     'exec', 'deployment/postgres', '--', 'pg_dump', '-U', 'hakopod', '-d', 'hakopod', '-Fc'], stdout=output, timeout=300)
        else:
            env = dict(os.environ, PGDATABASE=Path('/etc/hakopod/database-url').read_text().strip())
            command(bound + ['pg_dump', '-Fc'], env=env, stdout=output, timeout=300)
    with (directory / 'database.dump').open('rb') as check:
        valid = check.read(5) == b'PGDMP'
    if not valid:
        raise ValueError('PostgreSQL backup is not a custom-format dump')


def upgrade(target, lock_held=False, install_lock=None):
    if not lock_held and not LOCK.acquire(blocking=False):
        raise ValueError('An upgrade is already running')
    owned_lock = install_lock
    acquired = install_lock is not None
    try:
        if owned_lock is None:
            owned_lock = INSTALL_LOCK.open('a')
            fcntl.flock(owned_lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
            acquired = True
        return perform_upgrade(target, owned_lock)
    except (Exception, SystemExit) as error:
        if acquired:
            existing = json.loads((STATE / 'status.json').read_text()) if (STATE / 'status.json').exists() else {}
            if existing.get('status') != 'failed' and existing.get('version') == target:
                save_state({'status':'failed','version':target,'message':str(error) if isinstance(error,ValueError) else 'Upgrade could not start. Inspect the maintenance journal.'})
        raise
    finally:
        if owned_lock is not None: owned_lock.close()
        LOCK.release()


def perform_upgrade(target, install_lock):
    global CACHE
    with contextlib.nullcontext(install_lock):
        installed = current()
        config = json.loads(CONFIG.read_text())
        marker = json.loads(MARKER.read_text())
        if not marker.get('completed') or config.get('deployment_mode', 'self-hosted') != 'self-hosted':
            raise ValueError('Maintenance requires a completed self-hosted installation')
        version_key(target)
        stopped = switched = False
        def progress(state, message):
            save_state({'status': state, 'message': message, 'version': target, 'previous_version': installed})
        progress('downloading', 'Downloading and verifying release artifacts.')
        try:
            release = json.loads(fetch(REPO + '/releases/tags/v' + target, 2 << 20))
            if release.get('draft') or release.get('tag_name') != 'v' + target:
                raise ValueError('Release is unavailable')
            if version_key(installed)[3] and release.get('prerelease'):
                raise ValueError('Stable installations cannot upgrade to a prerelease')
            checksums = checksum_map(fetch(asset_url(release, 'SHA256SUMS'), 65536))
            if shutil.disk_usage('/var/lib/hakopod').free < 3 << 30 or shutil.disk_usage('/opt/hakopod').free < 2 << 30:
                raise ValueError('Upgrade needs 3 GiB free for backups and 2 GiB for release staging')
            with tempfile.TemporaryDirectory(prefix='stage-', dir=STATE) as tmp:
                stage = Path(tmp)
                manifest = json.loads(download(release, 'upgrade.json', checksums, stage, 65536).read_text())
                validate_manifest(manifest, installed, target)
                validate_installed_runtime()
                arch = {'aarch64': 'arm64', 'x86_64': 'amd64'}.get(os.uname().machine)
                if not arch:
                    raise ValueError('Unsupported architecture')
                binary_root = 'hakopod_' + target + '_linux_' + arch
                dashboard_root = 'hakopod_' + target + '_dashboard'
                for name in (binary_root, dashboard_root):
                    source = download(release, name + '.tar.gz', checksums, stage, 512 << 20)
                    host.unpack(source, stage / name, name)
                binary = stage / binary_root / binary_root
                dashboard = stage / dashboard_root / dashboard_root
                if subprocess.check_output([str(binary / 'hakopod'), 'version'], timeout=10, stderr=subprocess.DEVNULL).decode().strip() != target:
                    raise ValueError('Downloaded binary version does not match')
                if not (dashboard / 'dist/server/server.js').is_file() or not (dashboard / 'serve.mjs').is_file():
                    raise ValueError('Dashboard runtime is missing')
                destination = RELEASE_DIR / target
                if destination.exists():
                    raise ValueError('Target release directory already exists; inspect the previous attempt')
                shutil.move(str(dashboard), binary / 'dashboard')
                # Copy onto the destination filesystem before the atomic switch.
                shutil.copytree(binary, destination, symlinks=True)
                progress('backing_up', 'Stopping management services and backing up PostgreSQL and configuration. Applications keep running.')
                stopped = True
                command(['systemctl', 'stop', 'hakopod-dashboard', 'hakopod-api'])
                backup_dir = STATE / ('backup-' + str(time.time_ns()))
                backup_dir.mkdir(mode=0o700)
                backup(config, backup_dir)
                credentials.repair_namespace(marker['id'])
                enable()
                progress('restarting', 'Starting the new API and dashboard. Keep this page open.')
                link = CURRENT.with_name('current.next')
                if link.exists() or link.is_symlink():
                    raise ValueError('Unexpected release switch file')
                link.symlink_to('releases/' + target)
                link.replace(CURRENT)
                switched = True
                CACHE = None
                command(['systemctl', 'start', 'hakopod-api', 'hakopod-dashboard'])
                for attempt in range(45):
                    try:
                        with urllib.request.urlopen('http://127.0.0.1:8080/readyz', timeout=2) as response:
                            ready = response.status == 200
                        dashboard_ready = dashboard_health(config)
                        if ready and dashboard_ready:
                            break
                    except Exception:
                        pass
                    if attempt == 44:
                        raise ValueError('New services did not become ready; inspect the installation journal')
                    time.sleep(2)
                progress('succeeded', 'Upgrade complete. Configuration and database backups are retained on the server.')
                CACHE = None
        except (Exception, SystemExit) as error:
            recovery_failed = False
            if stopped and not switched:
                try: command(['systemctl', 'start', 'hakopod-api', 'hakopod-dashboard'])
                except Exception: recovery_failed = True
            if switched:
                # New migrations may already have committed. Never automatically
                # run an older server against an unknown database schema.
                save_state({'status': 'failed', 'message': 'Upgrade needs administrator recovery. Inspect journalctl -u hakopod-api and retained backups; automatic database rollback was not attempted.', 'version': target, 'previous_version': installed})
            else:
                save_state({'status': 'failed', 'message': 'Management services need manual restart; inspect the maintenance journal.' if recovery_failed else (str(error) if isinstance(error, ValueError) else 'Upgrade stopped before switching releases. Inspect the maintenance journal.'), 'version': target, 'previous_version': installed})
            raise


def redact_log(text):
    if re.search(r'(?i)(password|secret|token|authorization|cookie|private.key|gh[pousr]_|glpat-)',text):
        return '[credential-related entry omitted; inspect the host journal if needed]'
    return re.sub(r'(?i)(?:postgres(?:ql)?|https?)://[^\s@/]+:[^\s@/]+@', '[redacted]@', text)


def logs():
    process = subprocess.Popen(['journalctl', '--unit=hakopod-api.service', '--no-pager', '--lines=200', '--output=json', '--output-fields=__REALTIME_TIMESTAMP,MESSAGE,PRIORITY'], stdout=subprocess.PIPE, stderr=subprocess.DEVNULL)
    timer = threading.Timer(5, process.kill)
    timer.start()
    try:
        data = process.stdout.read((512 << 10) + 1)
        if len(data) > 512 << 10:
            raise ValueError('Journal response is too large; inspect it on the host')
        if process.wait(timeout=1) != 0:
            raise ValueError('Journal read failed')
    finally:
        timer.cancel()
        if process.poll() is None: process.kill()
        process.wait(timeout=2)
        process.stdout.close()
    entries = []
    for line in data.splitlines():
        item = json.loads(line)
        text = item.get('MESSAGE', '')
        if not isinstance(text, str):
            text = '[binary log entry]'
        if 'PRIVATE KEY' in text: text = '[private key entry redacted]'
        text = redact_log(text)
        entries.append({'timestamp': item.get('__REALTIME_TIMESTAMP', ''), 'message': text[:2048]})
    return {'entries': entries, 'observed_at': time.strftime('%Y-%m-%dT%H:%M:%SZ', time.gmtime())}


class Handler(http.server.BaseHTTPRequestHandler):
    def log_message(self, *_):
        pass

    def do_GET(self):
        self.handle_request()

    def do_POST(self):
        self.handle_request()

    def handle_request(self):
        self.connection.settimeout(25)
        peer_uid = struct.unpack('3i', self.connection.getsockopt(socket.SOL_SOCKET, socket.SO_PEERCRED, 12))[1]
        if peer_uid not in (0, pwd.getpwnam('hakopod-api').pw_uid):
            self.send_error(403); return
        try:
            if self.command == 'GET' and self.path == '/status':
                result = status()
            elif self.command == 'GET' and self.path == '/logs':
                result = logs()
            elif self.command == 'POST' and self.path == '/upgrade':
                size = int(self.headers.get('Content-Length', '0'))
                if not 0 < size <= 256:
                    raise ValueError('Invalid request size')
                body = json.loads(self.rfile.read(size))
                if set(body) != {'version'}:
                    raise ValueError('Only version is accepted')
                version_key(body['version'])
                if not LOCK.acquire(blocking=False):
                    raise ValueError('An upgrade is already running')
                install_lock = None
                try:
                    install_lock = INSTALL_LOCK.open('a')
                    fcntl.flock(install_lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
                except BaseException:
                    if install_lock is not None: install_lock.close()
                    LOCK.release()
                    raise
                # Serial HTTP request processing prevents duplicate dispatch;
                # take the lock before replying and release it in the worker.
                def work():
                    try: upgrade(body['version'], lock_held=True, install_lock=install_lock)
                    except (Exception, SystemExit): pass
                try:
                    save_state({'status': 'queued', 'message': 'Upgrade request accepted.', 'version': body['version']})
                    thread = threading.Thread(target=work, daemon=False)
                    thread.start()
                except BaseException:
                    install_lock.close()
                    LOCK.release()
                    raise
                result = {'accepted': True}
            else:
                self.send_error(404); return
            data = json.dumps(result).encode()
            self.send_response(200); self.send_header('Content-Type', 'application/json'); self.send_header('Content-Length', str(len(data))); self.end_headers(); self.wfile.write(data)
        except Exception:
            self.send_error(409, 'Maintenance request failed. Inspect the host journal or status.')


class Server(socketserver.UnixStreamServer):
    allow_reuse_address = False


def serve():
    if os.geteuid() != 0:
        raise ValueError('Maintenance requires root')
    os.umask(0o077)
    STATE.mkdir(mode=0o700, parents=True, exist_ok=True)
    if (STATE / 'status.json').exists():
        previous = json.loads((STATE / 'status.json').read_text())
        active = False
        with INSTALL_LOCK.open('a') as lock:
            try: fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
            except BlockingIOError: active = True
        if not active and previous.get('status') not in ('idle', 'succeeded', 'failed'):
            previous.update(status='failed', message='Maintenance was interrupted. Inspect the host and backups before retrying.')
            save_state(previous)
    directory = Path(SOCKET).parent
    directory.mkdir(mode=0o750, parents=True, exist_ok=True)
    os.chown(directory, 0, pwd.getpwnam('hakopod-api').pw_gid)
    if Path(SOCKET).exists():
        if not Path(SOCKET).is_socket(): raise ValueError('Unexpected maintenance socket path')
        Path(SOCKET).unlink()
    with Server(SOCKET, Handler) as server:
        os.chown(SOCKET, 0, pwd.getpwnam('hakopod-api').pw_gid)
        os.chmod(SOCKET, 0o660)
        server.serve_forever()


def enable():
    if os.geteuid() != 0:
        raise ValueError('Maintenance setup requires root')
    config = json.loads(CONFIG.read_text())
    if config.get('deployment_mode', 'self-hosted') != 'self-hosted':
        raise ValueError('Maintenance is only for self-hosted installations')
    destination = Path('/opt/hakopod/maintenance')
    source = Path(__file__).resolve().parent
    if destination.exists():
        # Do not replace a running privileged helper as part of an API upgrade.
        # Helper/runtime upgrades require their own reviewed migration.
        if (destination / 'maintenance.py').is_file(): return
        raise ValueError('Unexpected maintenance directory')
    destination.mkdir(mode=0o755)
    for item in list(source.glob('*.py')) + [source / 'pins.json']:
        shutil.copyfile(item, destination / item.name)
        os.chmod(destination / item.name, 0o644)
    for module in ('storage','cert-manager'):
        shutil.copytree(source.parent / 'deploy' / module, destination / 'modules' / module)
    shutil.copyfile(source / 'hakopod-maintenance.service', '/etc/systemd/system/hakopod-maintenance.service')
    command(['systemctl', 'daemon-reload'])
    command(['systemctl', 'enable', '--now', 'hakopod-maintenance'])


if __name__ == '__main__':
    parser = argparse.ArgumentParser()
    parser.add_argument('--serve', action='store_true')
    parser.add_argument('--enable', action='store_true')
    parser.add_argument('--upgrade', metavar='VERSION')
    parser.add_argument('--check-upgrade', metavar='VERSION')
    parser.add_argument('--manifest', type=Path)
    args = parser.parse_args()
    if args.check_upgrade:
        try:
            if args.manifest is None: raise ValueError('An upgrade manifest is required')
            validate_manifest(host.read_json(args.manifest), current(), args.check_upgrade)
        except ValueError as error:
            raise SystemExit('Hakopod upgrade: ' + str(error)) from None
    elif args.enable:
        enable()
    elif args.serve:
        serve()
    elif args.upgrade:
        if os.geteuid() != 0: raise SystemExit('Run the upgrade as root')
        os.umask(0o077); STATE.mkdir(mode=0o700, parents=True, exist_ok=True)
        try:
            upgrade(args.upgrade)
        except ValueError as error:
            raise SystemExit('Hakopod upgrade: ' + str(error)) from None
    else:
        print(json.dumps(status()))
