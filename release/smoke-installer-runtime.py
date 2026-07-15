#!/usr/bin/env python3
"""Internal fixture for smoke-installer.py; must only run inside its container."""
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import pwd
import re
import signal
import subprocess
import time
import urllib.error
import urllib.request

if not Path('/.dockerenv').is_file() or not Path('/work/rendered').is_dir():
    raise SystemExit('Run only via release/smoke-installer.py in its disposable container')
spec = importlib.util.spec_from_file_location('host', Path(os.environ['KIT_ROOT']) / 'installer/host.py')
host = importlib.util.module_from_spec(spec); spec.loader.exec_module(host)
config = host.config('/work/config.json')
before = {p.name: hashlib.sha256(p.read_bytes()).hexdigest() for p in Path('/etc/hakopod/secrets').iterdir()}
host.prepare(config, os.environ['ARCH'], '/artifacts', True)
assert before == {p.name: hashlib.sha256(p.read_bytes()).hexdigest() for p in Path('/etc/hakopod/secrets').iterdir()}
missing = Path('/etc/hakopod/secrets/auth-encryption-key'); retained = missing.read_bytes(); missing.unlink()
try:
    try: host.prepare(config, os.environ['ARCH'], '/artifacts', True)
    except ValueError as error: assert 'Missing preserved secret' in str(error)
    else: raise AssertionError('Resume regenerated an established secret')
finally:
    missing.write_bytes(retained); missing.chmod(0o400)
ingress = Path('/work/ingress.yaml').read_text()
assert sorted(re.findall(r'^\s+hostPort: (\d+)\s*$', ingress, re.M)) == ['443', '80']
assert 'type: NodePort' not in ingress and 'nodePort:' not in ingress
environment = dict(os.environ, NODE_ENV='production', HOST='127.0.0.1', PORT='3000',
    HAKOPOD_WEB_ORIGIN='http://localhost:3000', HAKOPOD_API_URL='http://127.0.0.1:8080',
    HAKOPOD_SESSION_SECRET=Path('/etc/hakopod/secrets/session-secret').read_text().strip())
log = Path('/work/dashboard.log').open('w')
process = subprocess.Popen(['runuser', '-u', 'hakopod-dashboard', '--', '/opt/hakopod/tools/node/bin/node',
    '--max-old-space-size=192', '/opt/hakopod/current/dashboard/serve.mjs'],
    env=environment, stdout=log, stderr=log, start_new_session=True)
def request(path, data=None, headers=None):
    try:
        with urllib.request.urlopen(urllib.request.Request('http://127.0.0.1:3000' + path, data=data, headers=headers or {}), timeout=5) as response:
            return response.status, response.read(), response.headers
    except urllib.error.HTTPError as response: return response.code, response.read(), response.headers
try:
    for attempt in range(60):
        if process.poll() is not None: raise AssertionError('Dashboard exited: ' + Path('/work/dashboard.log').read_text())
        try:
            status, source, _ = request('/')
            if status == 200: break
        except OSError: pass
        time.sleep(0.5)
    else: raise AssertionError('Dashboard never became ready')
    assert status == 200
    stylesheet = re.search(rb'href="([^\"]+\.css)"', source)
    assert stylesheet, 'SSR stylesheet missing'
    status, css, headers = request(stylesheet[1].decode())
    assert status == 200 and css and 'text/css' in headers['Content-Type']
    assert request('/api/me')[0] == 401
    assert request('/api/plan', b'{}', {'Origin': 'https://untrusted.example'})[0] == 403
    assert request('/session', b'x' * 4097, {'Origin': 'http://localhost:3000', 'Content-Type': 'application/json'})[0] == 413
    node_rss = []
    dashboard_uid = pwd.getpwnam('hakopod-dashboard').pw_uid
    for path in Path('/proc').glob('[0-9]*/status'):
        try:
            status = path.read_text()
            if int(re.search(r'^Uid:\s+(\d+)', status, re.M)[1]) == dashboard_uid:
                node_rss.append(int(re.search(r'^VmRSS:\s+(\d+)', status, re.M)[1]))
        except (OSError, TypeError): pass
    assert node_rss and all(value > 0 for value in node_rss), 'Dashboard RSS was not observed'
    print(json.dumps(dict(result='PASS', architecture=os.environ['ARCH'], dashboard_rss_kib=node_rss,
        checks=['real SSR/static assets', 'unprivileged startup under umask077', '401 unauthenticated', '403 cross-origin', '413 oversized sign-in', 'resume preserves secrets', 'missing established key refused', 'only HAProxy host ports80/443'])))
finally:
    os.killpg(process.pid, signal.SIGTERM)
    try: process.wait(timeout=15)
    except subprocess.TimeoutExpired: os.killpg(process.pid, signal.SIGKILL); process.wait(timeout=5)
    log.close()
