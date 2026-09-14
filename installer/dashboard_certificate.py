#!/usr/bin/env python3
"""Copy only the installation-owned dashboard TLS Secret; never log its data."""
import argparse
import base64
import fcntl
import json
import os
from pathlib import Path
import pwd
import re
import shutil
import subprocess
import tempfile
import threading
from urllib.parse import urlsplit

import host

ROOT = Path('/etc/hakopod/dashboard-tls')
CONFIG = Path('/etc/hakopod/config.json')
MARKER = Path('/etc/hakopod/installation.json')
LOCK = Path('/run/lock/hakopod-install.lock')


def run(command):
    with subprocess.Popen(command, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL) as process:
        timer = threading.Timer(20, process.kill)
        timer.start()
        try:
            output = process.stdout.read(256 * 1024 + 1)
            if len(output) > 256 * 1024:
                raise ValueError('Certificate command output exceeds its bound')
            if process.wait(timeout=2) != 0:
                raise ValueError('Certificate command failed')
            return output
        finally:
            timer.cancel()
            if process.poll() is None: process.kill()


def secret_material(secret, installation):
    if (secret.get('metadata', {}).get('labels', {}).get(host.LABEL) != installation
            or secret.get('metadata', {}).get('name') != 'hakopod-dashboard-tls'
            or secret.get('metadata', {}).get('namespace') != 'hakopod-system'
            or secret.get('type') != 'kubernetes.io/tls'):
        raise ValueError('Dashboard TLS Secret is not owned by this installation')
    result = []
    for name in ('tls.crt', 'tls.key'):
        value = secret.get('data', {}).get(name)
        if not isinstance(value, str) or not 0 < len(value) <= 65536:
            raise ValueError('Missing or oversized dashboard certificate material')
        result.append(base64.b64decode(value, validate=True))
    return tuple(result)


def validate_pair(folder, hostname):
    cert, key = str(folder / 'dashboard.crt'), str(folder / 'dashboard.key')
    run(['openssl', 'x509', '-in', cert, '-noout', '-checkend', '86400'])
    # checkhost can return success even for a mismatch on some OpenSSL versions.
    result = run(['openssl', 'x509', '-in', cert, '-noout', '-checkhost', hostname])
    if b'does match certificate' not in result:
        raise ValueError('Renewed dashboard certificate does not match its hostname')
    run(['openssl', 'verify', '-purpose', 'sslserver', '-untrusted', cert, cert])
    if run(['openssl', 'x509', '-in', cert, '-pubkey', '-noout']) != run(['openssl', 'pkey', '-in', key, '-pubout', '-passin', 'pass:']):
        raise ValueError('Dashboard certificate and key do not match')


def current_generation():
    current = ROOT / 'current'
    if not current.is_symlink():
        if current.exists(): raise ValueError('Unexpected dashboard certificate current path')
        return None
    name = os.readlink(current)
    if not re.fullmatch(r'generation-[a-z0-9_]+', name):
        raise ValueError('Unexpected dashboard certificate generation link')
    folder = ROOT / name
    if folder.is_symlink() or not folder.is_dir():
        raise ValueError('Missing dashboard certificate generation')
    return name


def point_to(name):
    temporary = ROOT / '.current-new'
    if temporary.exists() or temporary.is_symlink(): temporary.unlink()
    temporary.symlink_to(name)
    temporary.replace(ROOT / 'current')


def mark_applied(name):
    temporary = ROOT / '.applied-new'
    temporary.write_text(name)
    temporary.chmod(0o600)
    temporary.replace(ROOT / 'applied')


def install_pair(cert, key, hostname, uid, gid, restart=True):
    host.no_symlink_ancestors(ROOT)
    ROOT.mkdir(mode=0o711, exist_ok=True)
    ROOT.chmod(0o711)
    previous = current_generation()
    if previous and (ROOT / previous / 'dashboard.crt').read_bytes() == cert and (ROOT / previous / 'dashboard.key').read_bytes() == key:
        validate_pair(ROOT / previous, hostname)
        applied = ROOT / 'applied'
        if not applied.is_file() or applied.read_text() != previous:
            # Recover a crash after switching files but before reloading TLS.
            if restart: run(['systemctl', 'try-restart', 'hakopod-dashboard.service'])
            mark_applied(previous)
            return True
        return False
    folder = Path(tempfile.mkdtemp(prefix='generation-', dir=ROOT))
    switched = False
    try:
        for name, data in [('dashboard.crt', cert), ('dashboard.key', key)]:
            path = folder / name
            with path.open('xb') as target:
                target.write(data)
                target.flush()
                os.fsync(target.fileno())
            path.chmod(0o400)
            os.chown(path, uid, gid)
        validate_pair(folder, hostname)
        os.chown(folder, 0, gid)
        folder.chmod(0o750)
        point_to(folder.name)
        switched = True
        if restart:
            run(['systemctl', 'try-restart', 'hakopod-dashboard.service'])
        mark_applied(folder.name)
    except BaseException:
        if switched:
            if previous: point_to(previous)
            else: (ROOT / 'current').unlink()
            if restart and previous:
                try: run(['systemctl', 'try-restart', 'hakopod-dashboard.service'])
                except (ValueError, OSError, subprocess.SubprocessError): pass
        shutil.rmtree(folder)
        raise
    # Keep the active and preceding generation; do not accumulate private keys.
    for candidate in ROOT.glob('generation-*'):
        if candidate.name not in (folder.name, previous) and candidate.is_dir() and not candidate.is_symlink():
            shutil.rmtree(candidate)
    return True


def refresh(initial=False):
    config = host.config(host.regular(CONFIG, True))
    if config['dashboard_certificate'] != 'letsencrypt':
        raise ValueError('Automatic dashboard certificates are not configured')
    installation = host.read_json(host.regular(MARKER, True)).get('id', '')
    if not re.fullmatch(r'[0-9a-f]{32}', installation):
        raise ValueError('Invalid installation ownership marker')
    raw = run(['/opt/hakopod/tools/kubectl', '--kubeconfig=/etc/hakopod/admin-kubeconfig',
               '--request-timeout=10s', '-n', 'hakopod-system', 'get', 'secret', 'hakopod-dashboard-tls', '-o', 'json'])
    if len(raw) > 256 * 1024: raise ValueError('Dashboard TLS Secret response is oversized')
    cert, key = secret_material(json.loads(raw), installation)
    account = pwd.getpwnam('hakopod-dashboard')
    changed = install_pair(cert, key, urlsplit(config['dashboard_origin']).hostname, account.pw_uid, account.pw_gid, restart=not initial)
    print('Dashboard certificate refreshed.' if changed else 'Dashboard certificate is unchanged.')


if __name__ == '__main__':
    parser = argparse.ArgumentParser()
    parser.add_argument('--initial', action='store_true', help='Initial install already holds the installation lock; do not restart')
    args = parser.parse_args()
    try:
        if os.geteuid() != 0: raise ValueError('Dashboard certificate refresh requires root')
        os.umask(0o077)
        if args.initial:
            refresh(initial=True)
        else:
            with LOCK.open('a') as lock:
                try: fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
                except BlockingIOError: raise SystemExit('Installation maintenance is active; certificate refresh deferred.')
                refresh()
    except (ValueError, OSError, subprocess.SubprocessError):
        raise SystemExit('Dashboard certificate refresh failed; the previous certificate was retained when available. Check cert-manager, dashboard DNS, port 80 and the dashboard service, then retry.')
