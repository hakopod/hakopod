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
MAX_UPGRADE_CANDIDATES = 8
CLEANUP_OWNER_UID = 0


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
        for _ in range(MAX_UPGRADE_CANDIDATES):
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
    cache = STATE / 'downloads'
    cache.mkdir(mode=0o700, parents=True, exist_ok=True)
    cached = cache / (release['tag_name'].removeprefix('v') + '-' + name)
    if cached.exists():
        if not cached.is_file() or cached.is_symlink() or cached.stat().st_uid != CLEANUP_OWNER_UID or cached.stat().st_mode & 0o077 or cached.stat().st_size > limit or host.digest(cached) != checksums[name]:
            raise ValueError('Cached release asset failed its identity check')
        shutil.copyfile(cached, destination)
        return destination
    request = urllib.request.Request(asset_url(release, name), headers={'User-Agent': 'hakopod-maintenance'})
    digest = hashlib.sha256()
    total = 0
    temporary = cache / ('.download-' + str(time.time_ns()))
    with urllib.request.urlopen(request, timeout=20) as response, temporary.open('xb') as output:
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
        temporary.unlink(missing_ok=True)
        raise ValueError('Release checksum mismatch')
    os.chmod(temporary, 0o600)
    temporary.replace(cached)
    shutil.copyfile(cached, destination)
    return destination


class UnsupportedUpgradeError(ValueError):
    """A valid release does not accept this installed source version."""


def validate_manifest(manifest, installed, target):
    if not isinstance(manifest, dict) or manifest.get('schema_version') != 1 or manifest.get('version') != target:
        raise ValueError('Invalid upgrade compatibility manifest')
    sources = manifest.get('from_versions')
    if not isinstance(sources, list) or len(sources) > 2 or any(not isinstance(source, str) for source in sources):
        raise ValueError('Invalid upgrade source versions')
    for source in sources:
        version_key(source)
    if installed not in sources:
        accepted = ', '.join(sources) if sources else 'none'
        raise UnsupportedUpgradeError(f'Upgrade from {installed} to {target} is not supported by this release. No services were stopped. Supported source versions: {accepted}. Use a release that explicitly supports your installed version; do not use --resume to upgrade.')
    if version_key(target) <= version_key(installed) or version_key(target)[:2] != version_key(installed)[:2]:
        raise ValueError('Only newer releases in the same major/minor series are supported')
    if manifest.get('runtime_pins_sha256') != host.digest(Path(__file__).with_name('pins.json')):
        raise ValueError('Runtime dependencies changed; follow the release manual upgrade guide')


def check_upgrade(manifest, target):
    installed = current()
    try:
        validate_manifest(manifest, installed, target)
    except UnsupportedUpgradeError as error:
        # Reuse the bounded release check; it verifies each candidate's checksum,
        # source compatibility and runtime pins before suggesting a next step.
        result = release_check()
        candidate = result.get('latest_version')
        if (result.get('update_available') and result.get('current_version') == installed
                and candidate and version_key(installed) < version_key(candidate) < version_key(target)):
            raise UnsupportedUpgradeError(str(error) + '\nNext supported step: sudo sh installer.sh --upgrade --version ' + candidate
                                          + '\nAfter that succeeds, retry: sudo sh installer.sh --upgrade --version ' + target) from None
        raise


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


class ReadinessError(ValueError):
    """Safe, bounded health-check diagnostics with no response bodies."""


def api_health():
    # This is always the local API, regardless of shell proxy settings.
    connection = http.client.HTTPConnection('127.0.0.1', 8080, timeout=3)
    try:
        connection.request('GET', '/readyz')
        response = connection.getresponse()
        response.read(65536)
        if response.status != 200:
            raise ReadinessError(f'HTTP {response.status}' + (' (PostgreSQL is unavailable)' if response.status == 503 else ''))
        return True
    finally: connection.close()


def dashboard_health(config):
    origin = urllib.parse.urlsplit(config['dashboard_origin'])
    connection = http.client.HTTPConnection(origin.hostname, config['dashboard_port'], timeout=3)
    address = config['node_ip'] if origin.scheme == 'https' else '127.0.0.1'
    context = None
    if origin.scheme == 'https':
        context = ssl.create_default_context()
        context.load_verify_locations(str(host.dashboard_tls_paths(config)[0]))
        context.verify_flags |= ssl.VERIFY_X509_PARTIAL_CHAIN
    sock = socket.create_connection((address, config['dashboard_port']),timeout=3)
    if context is not None:
        try: sock = context.wrap_socket(sock, server_hostname=origin.hostname)
        except BaseException: sock.close(); raise
    connection.sock = sock
    try:
        connection.request('GET', '/')
        response = connection.getresponse()
        # Finish the SSR response before closing; an early disconnect can abort
        # dashboard requests and add misleading AbortError entries to the journal.
        if len(response.read((1 << 20) + 1)) > 1 << 20:
            raise ReadinessError('dashboard response exceeds 1 MiB')
        if response.status != 200: raise ReadinessError(f'HTTP {response.status}')
        return True
    finally: connection.close()


def readiness(config):
    results = {}
    for name, check in (('API', api_health), ('Dashboard', lambda: dashboard_health(config))):
        try:
            results[name] = 'ready' if check() else 'readiness check failed'
        except ReadinessError as error: results[name] = str(error)
        except ssl.SSLCertVerificationError: results[name] = 'TLS certificate verification failed; check the hostname, expiry and configured certificate'
        except FileNotFoundError: results[name] = 'configured certificate file is missing'
        except TimeoutError: results[name] = 'connection or response timed out'
        except ConnectionRefusedError: results[name] = 'connection refused; check the service journal'
        except ssl.SSLError: results[name] = 'TLS handshake failed'
        except (OSError, http.client.HTTPException): results[name] = 'connection failed; check the service listener and journal'
        except Exception: results[name] = 'readiness check failed; inspect the service journal'
    return results


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
            connection = host.database.parse_url(host.database.read_url_file('/etc/hakopod/database-url'), config['database_mode'])
            env = host.database.command_environment(connection)
            env['PGAPPNAME'] = 'hakopod-upgrade-backup'
            try:
                command(bound + ['pg_dump', '--no-password', '-Fc'], env=env, stdout=output, timeout=300)
            except subprocess.CalledProcessError:
                raise ValueError('PostgreSQL backup failed. Check the saved database connection, TLS trust and pg_dump version before retrying.') from None
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
        staged_destination = None
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
            staging = STATE / 'staging'; staging.mkdir(mode=0o700, exist_ok=True)
            with tempfile.TemporaryDirectory(prefix='stage-', dir=staging) as tmp:
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
                staged_destination = destination
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
                    checks = readiness(config)
                    if all(value == 'ready' for value in checks.values()): break
                    if attempt == 44:
                        raise ReadinessError('Upgrade readiness failed. ' + '; '.join(f'{name}: {value}' for name, value in checks.items()))
                    time.sleep(2)
                progress('succeeded', 'Upgrade complete. Configuration and database backups are retained on the server.')
                CACHE = None
        except (Exception, SystemExit) as error:
            recovery_failed = False
            cleanup_failed = False
            if staged_destination is not None and not switched:
                try: shutil.rmtree(staged_destination)
                except OSError: cleanup_failed = True
            if stopped and not switched:
                try: command(['systemctl', 'start', 'hakopod-api', 'hakopod-dashboard'])
                except Exception: recovery_failed = True
            if switched:
                # New migrations may already have committed. Never automatically
                # run an older server against an unknown database schema.
                detail = str(error) + '. ' if isinstance(error, ReadinessError) else ''
                save_state({'status': 'failed', 'message': 'Upgrade needs administrator recovery. ' + detail + 'Inspect journalctl -u hakopod-api -u hakopod-dashboard and retained backups; automatic database rollback was not attempted.', 'version': target, 'previous_version': installed})
            else:
                message = 'Management services need manual restart; inspect the maintenance journal.' if recovery_failed else (str(error) if isinstance(error, ValueError) else 'Upgrade stopped before switching releases. Inspect the maintenance journal.')
                if cleanup_failed: message += ' Staged release files need administrator cleanup before retrying.'
                save_state({'status': 'failed', 'message': message, 'version': target, 'previous_version': installed})
            raise


def redact_log(text):
    if re.search(r'(?i)(password|secret|token|authorization|cookie|private.key|gh[pousr]_|glpat-)',text):
        return '[credential-related entry omitted; inspect the host journal if needed]'
    return re.sub(r'(?i)(?:postgres(?:ql)?|https?)://[^\s@/]+:[^\s@/]+@', '[redacted]@', text)


def cleanup_roots():
    # Only installer-owned transient data is eligible. Releases are rollback
    # material; backups, database/application volumes and K3s state are outside
    # these roots and can never appear in a review.
    return ((STATE / 'downloads', 'verified-release-cache'),)


def cleanup_item(path, category, now):
    stat = path.lstat()
    if not path.is_file() or path.is_symlink() or stat.st_nlink != 1 or stat.st_uid != CLEANUP_OWNER_UID or stat.st_size < 1 or stat.st_size > 512 << 20 or now - stat.st_mtime < 86400:
        return None
    identity = hashlib.sha256(f'{path}:{stat.st_dev}:{stat.st_ino}:{stat.st_size}:{stat.st_mtime_ns}'.encode()).hexdigest()
    return {'id': identity, 'category': category, 'path': str(path), 'bytes': stat.st_blocks * 512}


def cleanup_preview():
    now = time.time(); deadline = time.monotonic() + 5; items = []; scanned = 0; planned = 0
    for root, category in cleanup_roots():
        if not root.exists(): continue
        root_stat = root.lstat()
        if not root.is_dir() or root.is_symlink() or root_stat.st_uid != CLEANUP_OWNER_UID or root_stat.st_mode & 0o022: continue
        for path in root.iterdir():
            scanned += 1
            if scanned > 512 or time.monotonic() > deadline: raise ValueError('Cleanup inventory exceeded its scan bound')
            try:
                item = cleanup_item(path, category, now)
            except FileNotFoundError:
                continue
            if item: items.append(item); planned += item['bytes']
            if len(items) > 256: raise ValueError('Cleanup inventory exceeds 256 files')
            if planned > 8 << 30: raise ValueError('Cleanup inventory exceeds 8 GiB')
    usage = shutil.disk_usage('/var/lib/hakopod')
    return {'schema_version': 1, 'observed_at': time.strftime('%Y-%m-%dT%H:%M:%SZ', time.gmtime()),
            'filesystem': {'capacity_bytes': usage.total, 'available_bytes': usage.free}, 'items': items,
            'protected': ['database volumes', 'application volumes', 'backup archives', 'K3s state and secrets', 'installed releases and rollback material'],
            'planned_bytes': planned}


def cleanup_save(path, value):
    temporary = path.with_suffix('.tmp')
    with temporary.open('w') as stream:
        os.chmod(temporary, 0o600)
        json.dump(value, stream)
        stream.write('\n')
        stream.flush()
        os.fsync(stream.fileno())
    temporary.replace(path)
    descriptor = os.open(path.parent, os.O_RDONLY | os.O_DIRECTORY)
    try:
        os.fsync(descriptor)
    finally:
        os.close(descriptor)


def cleanup_execute(body):
    if set(body) != {'schema_version', 'operation_id', 'items'} or body['schema_version'] != 1 or not re.fullmatch(r'[0-9a-f]{32}', body['operation_id']) or not isinstance(body['items'], list) or len(body['items']) > 256:
        raise ValueError('Invalid cleanup operation')
    ids = set()
    for item in body['items']:
        if not isinstance(item, dict) or set(item) != {'id', 'category', 'path', 'bytes'} or not isinstance(item['id'], str) or not re.fullmatch(r'[0-9a-f]{64}', item['id']) or item['id'] in ids or not isinstance(item['bytes'], int) or item['bytes'] < 0 or item['bytes'] > 512 << 20:
            raise ValueError('Invalid cleanup item')
        ids.add(item['id'])
    if sum(item['bytes'] for item in body['items']) > 8 << 30:
        raise ValueError('Cleanup exceeds 8 GiB')
    request_hash = hashlib.sha256(json.dumps(body, sort_keys=True).encode()).hexdigest()
    receipt_path = STATE / ('cleanup-' + body['operation_id'] + '.json')
    if not LOCK.acquire(blocking=False):
        raise ValueError('Another maintenance operation is running')
    install_lock = None
    receipt = None
    try:
        install_lock = INSTALL_LOCK.open('a')
        fcntl.flock(install_lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        if receipt_path.exists():
            receipt = json.loads(receipt_path.read_text())
            if receipt.get('request_hash') != request_hash:
                raise ValueError('Cleanup operation identity changed')
            if receipt.get('status') == 'succeeded':
                return receipt
        if receipt is None:
            receipt = {'schema_version': 1, 'operation_id': body['operation_id'], 'request_hash': request_hash,
                       'status': 'running', 'removed': [], 'skipped': [], 'uncertain': [],
                       'planned_bytes': sum(item['bytes'] for item in body['items']),
                       'available_before_bytes': shutil.disk_usage('/var/lib/hakopod').free}
        receipt['status'] = 'running'
        cleanup_save(receipt_path, receipt)
        reviewed = {item['id']: item for item in cleanup_preview()['items']}
        done = {item['id'] for category in ['removed', 'skipped', 'uncertain'] for item in receipt[category]}
        deadline = time.monotonic() + 20
        for requested in body['items']:
            if requested['id'] in done:
                continue
            if time.monotonic() > deadline:
                raise ValueError('Cleanup exceeded its time bound')
            current = reviewed.get(requested['id'])
            if current != requested:
                category = 'uncertain' if receipt.get('pending_id') == requested['id'] else 'skipped'
                receipt[category].append({'id': requested['id'], 'reason': 'file changed, missing or protected'})
                receipt.pop('pending_id', None)
                cleanup_save(receipt_path, receipt)
                continue
            path = Path(current['path'])
            try:
                immediate = cleanup_item(path, current['category'], time.time())
            except FileNotFoundError:
                immediate = None
            if immediate != current:
                receipt['skipped'].append({'id': current['id'], 'reason': 'file changed or is protected'})
                receipt.pop('pending_id', None)
                cleanup_save(receipt_path, receipt)
                continue
            receipt['pending_id'] = current['id']
            cleanup_save(receipt_path, receipt)
            path.unlink()
            receipt['removed'].append({'id': current['id'], 'bytes': current['bytes']})
            receipt.pop('pending_id', None)
            cleanup_save(receipt_path, receipt)
        after = shutil.disk_usage('/var/lib/hakopod').free
        receipt.update(status='succeeded', removed_bytes=sum(item['bytes'] for item in receipt['removed']),
                       available_after_bytes=after, reclaimed_bytes=max(0, after-receipt['available_before_bytes']),
                       completed_at=time.strftime('%Y-%m-%dT%H:%M:%SZ', time.gmtime()))
        cleanup_save(receipt_path, receipt)
        return receipt
    except Exception:
        if receipt is not None and receipt.get('request_hash') == request_hash:
            receipt['status'] = 'interrupted'
            receipt['removed_bytes'] = sum(item['bytes'] for item in receipt['removed'])
            cleanup_save(receipt_path, receipt)
        raise
    finally:
        if install_lock is not None: install_lock.close()
        LOCK.release()


def cleanup_status(operation_id):
    if not re.fullmatch(r'[0-9a-f]{32}', operation_id):
        raise ValueError('Invalid cleanup operation')
    receipt = STATE / ('cleanup-' + operation_id + '.json')
    if not receipt.is_file():
        return {'schema_version': 1, 'operation_id': operation_id, 'status': 'not_started'}
    result = json.loads(receipt.read_text())
    if result.get('status') == 'running' and not LOCK.locked():
        result['status'] = 'interrupted'
    return result


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
            elif self.command == 'GET' and self.path == '/cleanup/preview':
                result = cleanup_preview()
            elif self.command == 'POST' and self.path == '/cleanup/execute':
                size = int(self.headers.get('Content-Length', '0'))
                if not 0 < size <= 256 << 10: raise ValueError('Invalid cleanup request size')
                result = cleanup_execute(json.loads(self.rfile.read(size)))
            elif self.command == 'GET' and self.path.startswith('/cleanup/status?'):
                values=urllib.parse.parse_qs(urllib.parse.urlsplit(self.path).query,strict_parsing=True)
                if set(values)!={'operation_id'} or len(values['operation_id'])!=1: raise ValueError('Invalid cleanup status request')
                result=cleanup_status(values['operation_id'][0])
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
    for module in ('storage','cert-manager','database-controllers'):
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
            check_upgrade(host.read_json(args.manifest), args.check_upgrade)
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
