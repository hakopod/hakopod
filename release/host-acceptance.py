#!/usr/bin/env python3
"""Install exact release bytes on an explicitly disposable GitHub-hosted VM."""
import argparse
from datetime import datetime, timezone
import hashlib
import io
import json
import os
from pathlib import Path
import platform
import re
import secrets
import shutil
import subprocess
import sys
import time
import urllib.request
import urllib.error
from unittest.mock import patch
from types import SimpleNamespace

ROOT = Path(__file__).resolve().parents[1]
WORK = Path('/var/tmp/hakopod-host-acceptance')
REDACTIONS = []


def diagnostics(output):
    for value in REDACTIONS:
        output = output.replace(value, '[redacted]')
    secret_dir = Path('/etc/hakopod/secrets')
    if secret_dir.is_dir():
        for path in secret_dir.iterdir():
            if path.is_file() and path.stat().st_size <= 65536:
                value = path.read_text(errors='replace').strip()
                if value:
                    output = output.replace(value, '[redacted]')
    output = re.sub(r'postgres(?:ql)?://\S+', '[database URL redacted]', output)
    return output[-6000:]


def require_rejection(result, diagnostic, marker_exists):
    if result.returncode == 0 or marker_exists or diagnostic not in result.stdout + result.stderr:
        raise RuntimeError('Negative check did not reach the intended database guard')


def verify_reports(directory, reports, version, revision):
    modes = {('x86_64', 'managed'), ('aarch64', 'managed'), ('x86_64', 'local'), ('x86_64', 'external')}
    manifest = directory / 'upgrade.json'
    sources = json.loads(manifest.read_text())['from_versions'] if manifest.exists() else []
    expected = {(machine, mode, source) for source in ['', *sources] for machine, mode in modes}
    if len(reports) != len(expected) or {(r['machine'], r['mode'], r.get('upgrade_from', '')) for r in reports} != expected:
        raise ValueError('Native host evidence incomplete')
    archives = {p.name for p in directory.glob('*.tar.gz')}
    for report in reports:
        if report['status'] != 'passed' or report['version'] != version or report['source_revision'] != revision:
            raise ValueError('Native host evidence has failed or mismatched source/version')
        if report.get('upgrade_from') and (not report.get('source_artifact_sha256') or report.get('upgrade_method') != 'bootstrap'):
            raise ValueError('Upgrade evidence must identify the published source artifacts and tested method')
        hashes = report['artifact_sha256']
        if not archives <= hashes.keys() or 'installer.sh' not in hashes:
            raise ValueError('Host evidence does not cover installer and archives')
        for name, expected_hash in hashes.items():
            if not re.fullmatch(r'[A-Za-z0-9][A-Za-z0-9._-]*', name):
                raise ValueError('Invalid evidence artifact name')
            with (directory / name).open('rb') as stream:
                actual = hashlib.file_digest(stream, 'sha256').hexdigest()
            if actual != expected_hash:
                raise ValueError('Host evidence does not match release bytes')


def run(argv, *, input=None, timeout=300, ok=True, env=None):
    result = subprocess.run(argv, input=input, text=True, stdout=subprocess.PIPE,
                            stderr=subprocess.PIPE, timeout=timeout, env=env)
    if ok and result.returncode:
        # Database tools can include credentials in diagnostics. Never publish them.
        detail = ''
        if len(argv) > 1 and Path(argv[1]).name == 'install.sh':
            detail = '\n' + diagnostics(result.stdout + result.stderr)
        raise RuntimeError(f'{Path(argv[0]).name} failed with exit {result.returncode}' + detail)
    return result


def guard():
    if (os.geteuid() != 0 or platform.system() != 'Linux'
            or os.environ.get('GITHUB_ACTIONS') != 'true'
            or os.environ.get('RUNNER_ENVIRONMENT') != 'github-hosted'
            or os.environ.get('HAKOPOD_DISPOSABLE_HOST') != 'yes'):
        raise RuntimeError('Only run on an explicitly disposable GitHub-hosted Linux runner')
    if Path('/proc/1/comm').read_text().strip() != 'systemd':
        raise RuntimeError('Native systemd VM required')
    if WORK.exists() or Path('/etc/hakopod').exists():
        raise RuntimeError('Fixture requires a fresh runner')
    if shutil.disk_usage('/').free < 30 * 1024**3:
        raise RuntimeError('Runner has less than the supported 30 GiB free disk')


def write(path, body, mode=0o600):
    path.write_text(body)
    path.chmod(mode)


def pg_fixture(mode):
    run(['apt-get', 'update', '-qq'])
    run(['apt-get', 'install', '-y', '--no-install-recommends', 'postgresql', 'openssl'], timeout=600)
    bins = sorted(Path('/usr/lib/postgresql').glob('*/bin/initdb'))
    if not bins:
        raise RuntimeError('PostgreSQL fixture tools unavailable')
    binary = bins[-1].parent
    data = WORK / 'postgres'
    data.mkdir(mode=0o700)
    run(['chown', 'postgres:postgres', str(data)])
    # Only the fixture subdirectory is writable by postgres.
    WORK.chmod(0o711)
    run(['runuser', '-u', 'postgres', '--', str(binary / 'initdb'), '-D', str(data),
         '--auth-local=peer', '--auth-host=scram-sha-256'])
    extra = "\nlisten_addresses='127.0.0.1'\nport=55432\nunix_socket_directories='/var/run/postgresql'\n"
    extra += "shared_buffers='32MB'\nmax_connections=30\n"
    ca = ''
    if mode == 'external':
        cert = data / 'server.crt'
        key = data / 'server.key'
        run(['openssl', 'req', '-x509', '-newkey', 'rsa:2048', '-nodes', '-days', '2',
             '-subj', '/CN=db.hakopod.test', '-addext', 'subjectAltName=DNS:db.hakopod.test',
             '-keyout', str(key), '-out', str(cert)])
        run(['chown', 'postgres:postgres', str(key), str(cert)])
        key.chmod(0o600)
        extra += f"ssl=on\nssl_cert_file='{cert}'\nssl_key_file='{key}'\n"
        ca = str(WORK / 'database-ca.pem')
        shutil.copyfile(cert, ca)
        Path(ca).chmod(0o600)
        with Path('/etc/hosts').open('a') as hosts:
            hosts.write('\n127.0.0.1 db.hakopod.test\n')
    with (data / 'postgresql.conf').open('a') as config:
        config.write(extra)
    run(['runuser', '-u', 'postgres', '--', str(binary / 'pg_ctl'), '-D', str(data),
         '-l', str(data / 'fixture.log'), '-w', 'start'])
    password = secrets.token_hex(24)
    REDACTIONS.append(password)
    psql = ['runuser', '-u', 'postgres', '--', 'psql', '-X', '-v', 'ON_ERROR_STOP=1', '-p', '55432']
    run(psql + ['-d', 'postgres'], input=(
        f"CREATE ROLE hakopod_fixture LOGIN PASSWORD '{password}';\n"
        "CREATE DATABASE hakopod_fixture OWNER hakopod_fixture;\n"
        "CREATE DATABASE unrelated_sentinel;\n"))
    sentinel = secrets.token_hex(16)
    run(psql + ['-d', 'unrelated_sentinel'], input=(
        "CREATE TABLE sentinel(value text NOT NULL);\n"
        f"INSERT INTO sentinel VALUES ('{sentinel}');\n"))
    hostname = 'db.hakopod.test' if mode == 'external' else '127.0.0.1'
    sslmode = 'verify-full' if mode == 'external' else 'disable'
    url = f'postgresql://hakopod_fixture:{password}@{hostname}:55432/hakopod_fixture?sslmode={sslmode}'
    write(WORK / 'database-url', url + '\n')
    return {'database_url_file': str(WORK / 'database-url'), 'database_ca_file': ca}, psql, sentinel, password


def http_json(path):
    with urllib.request.urlopen('http://127.0.0.1:8080' + path, timeout=10) as response:
        return json.load(response)


def health(setup_required=True):
    for _ in range(60):
        try:
            status = http_json('/api/v1/auth/status')
            if status.get('setup_required') is not setup_required:
                raise RuntimeError('Unexpected first-user setup state')
            if status.get('signup_enabled') is not False:
                raise RuntimeError('Public self-hosted binary exposed public signup')
            with urllib.request.urlopen('http://127.0.0.1:3000', timeout=10) as response:
                if response.status == 200:
                    return
        except (OSError, ValueError):
            pass
        time.sleep(2)
    raise RuntimeError('Installed API/dashboard did not recover')


def api(method, path, token='', body=None, expected=200):
    request = urllib.request.Request('http://127.0.0.1:8080/api/v1' + path, method=method,
                                    data=json.dumps(body).encode() if body is not None else None)
    request.add_header('Content-Type', 'application/json')
    if token: request.add_header('Authorization', 'Bearer ' + token)
    try:
        response = urllib.request.urlopen(request, timeout=20)
    except urllib.error.HTTPError as error:
        response = error
    with response:
        value = json.load(response)
        if response.status != expected:
            raise RuntimeError(f'Credential acceptance {method} {path} returned {response.status}, expected {expected}')
        return value


def published_source(version, arch):
    # Download only fixed public release assets; verify before executing the old installer.
    body = (ROOT / 'scripts/installer.sh').read_text().split("<<'HAKOPOD_BOOTSTRAP_PY'\n", 1)[1].split('\nHAKOPOD_BOOTSTRAP_PY', 1)[0]
    namespace = {'__name__': 'acceptance_bootstrap'}
    exec(compile(body, str(ROOT / 'scripts/installer.sh'), 'exec'), namespace)
    bootstrap = SimpleNamespace(**namespace)
    bootstrap.version(version)
    destination = WORK / 'source-artifacts'
    destination.mkdir()
    base = 'https://github.com/hakopod/hakopod/releases/download/v' + version + '/'
    bootstrap.download(base + 'SHA256SUMS', destination / 'SHA256SUMS', 65536)
    hashes = bootstrap.checksums(destination / 'SHA256SUMS')
    names = [f'hakopod_{version}_{kind}.tar.gz' for kind in ('installer', 'linux_' + arch, 'dashboard')]
    for name in names:
        bootstrap.download(base + name, destination / name, 512 << 20, hashes[name])
    # The host installer validates the archives for this architecture, not unavailable siblings.
    return destination, {name: hashes[name] for name in names}


def credential_fixture(kube, marker, report):
    ident = marker['id']
    namespace = {'apiVersion': 'v1', 'kind': 'Namespace', 'metadata': {'name': 'hakopod-system',
                 'labels': {'hakopod.com/installation': ident, 'app.kubernetes.io/managed-by': 'hakopod'}}}
    run(kube + ['apply', '--server-side', '--field-manager=hakopod-installer', '-f', '-'], input=json.dumps(namespace))
    token = api('POST', '/auth/setup', body={'name': 'Installer acceptance', 'email': 'owner@example.test',
                'password': secrets.token_hex(24), 'setup_token': Path('/etc/hakopod/secrets/setup-token').read_text().strip()})['token']
    REDACTIONS.append(token)
    api('POST', '/projects', token, {'name': 'acceptance', 'environment': 'test'}, 201)
    api('GET', '/git/connections', token)
    registry = {'project': 'acceptance', 'environment': 'test', 'name': 'fixture', 'registry': 'ghcr.io',
                'username': 'acceptance', 'password': secrets.token_hex(24)}
    REDACTIONS.append(registry['password'])
    api('POST', '/registries', token, registry, 201)
    # Reproduce the old dashboard ACME apply with the same field manager. It
    # dropped managed-by from the existing PostgreSQL namespace.
    del namespace['metadata']['labels']['app.kubernetes.io/managed-by']
    run(kube + ['apply', '--server-side', '--field-manager=hakopod-installer', '-f', '-'], input=json.dumps(namespace))
    labels = json.loads(run(kube + ['get', 'namespace', 'hakopod-system', '-o', 'json']).stdout)['metadata']['labels']
    if 'app.kubernetes.io/managed-by' in labels:
        raise RuntimeError('Credential fixture did not reproduce the server-side apply bug')
    api('GET', '/git/connections', token, expected=503)
    api('POST', '/registries', token, dict(registry, name='after-repair'), 503)
    report['checks'].append('Reproduced Git list and registry-save failures from the old ACME namespace apply')
    return token, registry


def upgrade_candidate(kit, artifacts, version):
    # Target artifacts are not public yet. Redirect only their fixed official
    # URLs to the already checksummed candidate bytes; real systemd, Kubernetes,
    # database dumps, migrations, extraction, switching and health checks run.
    sys.path.insert(0, str(kit / 'installer'))
    import maintenance
    original = urllib.request.urlopen
    base = 'https://github.com/hakopod/hakopod/releases/download/v' + version + '/'
    names = maintenance.checksum_map((artifacts / 'SHA256SUMS').read_bytes())
    release = {'tag_name': 'v' + version, 'prerelease': True,
               'assets': [{'name': name, 'browser_download_url': base + name} for name in [*names, 'SHA256SUMS']]}
    class Response:
        def __init__(self, stream, url): self.stream, self.url = stream, url
        def read(self, *args): return self.stream.read(*args)
        def __enter__(self): return self
        def __exit__(self, *args): self.stream.close()
    def urlopen(request, *args, **kwargs):
        url = request.full_url if hasattr(request, 'full_url') else request
        if url == maintenance.REPO + '/releases/tags/v' + version:
            return Response(io.BytesIO(json.dumps(release).encode()), url)
        if url.startswith(base):
            name = url[len(base):]
            if name not in [*names, 'SHA256SUMS']: raise RuntimeError('Unexpected candidate asset request')
            return Response((artifacts / name).open('rb'), url)
        return original(request, *args, **kwargs)
    with patch.object(urllib.request, 'urlopen', side_effect=urlopen):
        previous_mask = os.umask(0o077)
        try:
            maintenance.STATE.mkdir(mode=0o700, parents=True, exist_ok=True)
            maintenance.upgrade(version)
        finally:
            os.umask(previous_mask)
    if maintenance.current() != version or json.loads((maintenance.STATE / 'status.json').read_text())['status'] != 'succeeded':
        raise RuntimeError('Upgrade did not switch to the target release')
    backups = list(maintenance.STATE.glob('backup-*'))
    if len(backups) != 1 or (backups[0] / 'database.dump').read_bytes()[:5] != b'PGDMP' or not (backups[0] / 'configuration.tar.gz').is_file():
        raise RuntimeError('Upgrade backups are missing')


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--version', required=True)
    parser.add_argument('--artifact-dir', required=True, type=Path)
    parser.add_argument('--mode', required=True, choices=('managed', 'local', 'external'))
    parser.add_argument('--report', required=True, type=Path)
    parser.add_argument('--upgrade-from', default='')
    args = parser.parse_args()
    guard()
    WORK.mkdir(mode=0o700)
    report = {'status': 'running', 'mode': args.mode, 'machine': platform.machine(),
              'source_revision': os.environ.get('GITHUB_SHA'), 'version': args.version,
              'started_at': datetime.now(timezone.utc).isoformat(), 'checks': [], 'upgrade_from': args.upgrade_from,
              'limitations': ['No reboot, public DNS, ACME issuance or physical AWS RDS instance was exercised.',
                              'Existing database modes use a separate PostgreSQL fixture on the same disposable VM.']}
    args.report.parent.mkdir(parents=True, exist_ok=True)
    try:
        artifacts = args.artifact_dir.resolve()
        sums = {}
        for line in (artifacts / 'SHA256SUMS').read_text().splitlines():
            digest, name = line.split('  ', 1)
            path = artifacts / name
            if path.parent != artifacts or not path.is_file():
                raise RuntimeError('Invalid checksum inventory')
            with path.open('rb') as stream:
                actual = hashlib.file_digest(stream, 'sha256').hexdigest()
            if digest != actual:
                raise RuntimeError('Artifact checksum mismatch')
            sums[name] = actual
        report['artifact_sha256'] = sums
        root = f'hakopod_{args.version}_installer'
        run(['python3', str(ROOT / 'installer/host.py'), 'unpack', '--source',
             str(artifacts / f'{root}.tar.gz'), '--destination', str(WORK / 'kit'), '--root', root])
        kit = WORK / 'kit' / root
        candidate_kit = kit
        install_artifacts = artifacts
        install_version = args.version
        if args.upgrade_from:
            arch = {'x86_64': 'amd64', 'aarch64': 'arm64'}[platform.machine()]
            install_version = args.upgrade_from
            install_artifacts, report['source_artifact_sha256'] = published_source(install_version, arch)
            source_root = f'hakopod_{install_version}_installer'
            run(['python3', str(ROOT / 'installer/host.py'), 'unpack', '--source',
                 str(install_artifacts / (source_root + '.tar.gz')), '--destination', str(WORK / 'source-kit'), '--root', source_root])
            kit = WORK / 'source-kit' / source_root
        # Swap changes apply only to this disposable hosted runner, never developer machines.
        run(['swapoff', '-a'])
        route = json.loads(run(['ip', '-j', 'route', 'get', '1.1.1.1']).stdout)[0]
        address = route.get('prefsrc') or route.get('src')
        if not address:
            raise RuntimeError('Runner has no routable IPv4 address')
        config = json.loads((kit / 'installer/example.json').read_text())
        config.update(version=install_version, node_ip=address, supervisor_host=address,
                      node_name='hakopod-acceptance', app_domain='apps.hakopod.test',
                      database_mode=args.mode, acme='off', storage=False, install_docker=False)
        psql, sentinel, password = None, None, None
        if args.mode != 'managed':
            database, psql, sentinel, password = pg_fixture(args.mode)
            config.update(database)
        config_path = WORK / 'config.json'
        write(config_path, json.dumps(config))
        install = ['bash', str(kit / 'scripts/install.sh'), '--artifact-dir', str(install_artifacts),
                   '--config', str(config_path), '--yes']
        report['checks'].append('Checksums verified for exact downloaded artifacts')
        db_check = ['python3', '-c',
                    'import sys,json; sys.path.insert(0,sys.argv[1]); import database; '
                    'database.preflight(json.load(open(sys.argv[2])))',
                    str(kit / 'installer'), str(config_path)]
        if psql:
            run(db_check)
            # A database with existing application objects must never be adopted.
            run(psql + ['-d', 'hakopod_fixture'], input='CREATE TABLE public.do_not_adopt(id integer);\n')
            refused = run(install, ok=False, timeout=600)
            require_rejection(refused, 'A fresh installation requires an empty public schema',
                              Path('/etc/hakopod/installation.json').exists())
            run(psql + ['-d', 'hakopod_fixture'], input='DROP TABLE public.do_not_adopt;\n')
            run(db_check)
            report['checks'].append('Nonempty supplied database refused before installation')
        if args.mode == 'external':
            trusted_ca = Path(config['database_ca_file']).read_bytes()
            wrong_ca = WORK / 'wrong-ca.pem'
            run(['openssl', 'req', '-x509', '-newkey', 'rsa:2048', '-nodes', '-days', '2',
                 '-subj', '/CN=untrusted-fixture', '-keyout', str(WORK / 'wrong-ca.key'),
                 '-out', str(wrong_ca)])
            Path(config['database_ca_file']).write_bytes(wrong_ca.read_bytes())
            rejected = run(install, ok=False, timeout=600)
            require_rejection(rejected, 'PostgreSQL connection or read-only preflight failed',
                              Path('/etc/hakopod/installation.json').exists())
            Path(config['database_ca_file']).write_bytes(trusted_ca)
            run(db_check)
            report['checks'].append('Untrusted PostgreSQL certificate refused before cluster mutation')
        installed = run(install, timeout=1200)
        if password and password in installed.stdout + installed.stderr:
            raise RuntimeError('Installer exposed the fixture database password')
        health()
        marker_path = Path('/etc/hakopod/installation.json')
        marker = json.loads(marker_path.read_text())
        if not marker.get('completed'):
            raise RuntimeError('Installer did not record completion')
        protected = {p.name: hashlib.sha256(p.read_bytes()).hexdigest()
                     for p in Path('/etc/hakopod/secrets').iterdir() if p.is_file()}
        if not protected.get('setup-token'):
            raise RuntimeError('Setup token missing')
        report['checks'].append('API and dashboard healthy; first-user setup remains open; public signup is closed')
        # Resume must retain secrets and installation identity.
        run(install + ['--resume'], timeout=1200)
        if json.loads(marker_path.read_text()) != marker:
            raise RuntimeError('Resume changed installation identity')
        for name, digest in protected.items():
            if hashlib.sha256((Path('/etc/hakopod/secrets') / name).read_bytes()).hexdigest() != digest:
                raise RuntimeError('Resume regenerated a secret')
        report['checks'].append('Idempotent resume preserves installation identity and secrets')
        run(['systemctl', 'restart', 'hakopod-api', 'hakopod-dashboard'], timeout=120)
        health()
        report['checks'].append('API/dashboard process restart recovers without creating an account')
        kube = ['/opt/hakopod/tools/k3s', 'kubectl', '--kubeconfig', '/etc/hakopod/admin-kubeconfig']
        # The installer owns this cluster. Do not use ambient kubectl contexts.
        nodes = run(kube + ['get', 'nodes', '-o', 'json'])
        items = json.loads(nodes.stdout)['items']
        if not items or not all(any(c['type'] == 'Ready' and c['status'] == 'True'
                                   for c in node['status']['conditions']) for node in items):
            raise RuntimeError('Installed node is not Ready')
        report['checks'].append('Dedicated installed Kubernetes node Ready')
        token, registry = credential_fixture(kube, marker, report)
        # This real workload must survive management-service replacement untouched.
        image = json.loads((candidate_kit / 'installer/pins.json').read_text())['postgres']
        pod = {'apiVersion': 'v1', 'kind': 'Pod', 'metadata': {'name': 'upgrade-sentinel', 'namespace': 'hakopod-system'},
               'spec': {'containers': [{'name': 'sentinel', 'image': image, 'command': ['sleep', '1800'],
                        'resources': {'requests': {'cpu': '1m', 'memory': '8Mi'}, 'limits': {'cpu': '50m', 'memory': '32Mi'}}}]}}
        run(kube + ['create', '-f', '-'], input=json.dumps(pod))
        run(kube + ['-n', 'hakopod-system', 'wait', '--for=condition=Ready', 'pod/upgrade-sentinel', '--timeout=180s'])
        def pod_identity():
            value = json.loads(run(kube + ['-n', 'hakopod-system', 'get', 'pod', 'upgrade-sentinel', '-o', 'json']).stdout)
            return value['metadata']['uid'], value['status']['containerStatuses'][0]['containerID'], value['status']['containerStatuses'][0]['restartCount']
        before_pod = pod_identity()
        before_secrets = json.loads(run(kube + ['-n', 'hakopod-system', 'get', 'secrets', '-o', 'json']).stdout)
        before_data = {item['metadata']['name']: item.get('data') for item in before_secrets['items']}
        if args.upgrade_from:
            upgrade_candidate(candidate_kit, artifacts, args.version)
            report['upgrade_method'] = 'bootstrap'
            report['checks'].append('Published source upgraded using candidate bootstrap helper; real database/configuration backups retained')
        else:
            run(['python3', str(candidate_kit / 'installer/credentials.py')])
        health(setup_required=False)
        if pod_identity() != before_pod:
            raise RuntimeError('Credential repair or upgrade restarted the workload')
        after_secrets = json.loads(run(kube + ['-n', 'hakopod-system', 'get', 'secrets', '-o', 'json']).stdout)
        if {item['metadata']['name']: item.get('data') for item in after_secrets['items']} != before_data:
            raise RuntimeError('Credential repair or upgrade changed Kubernetes secret data')
        for name, digest in protected.items():
            if hashlib.sha256((Path('/etc/hakopod/secrets') / name).read_bytes()).hexdigest() != digest:
                raise RuntimeError('Credential repair or upgrade changed an installation secret')
        api('GET', '/git/connections', token)
        registries = api('GET', '/registries?project=acceptance&environment=test', token)
        if not any(item['name'] == 'fixture' for item in registries['items']):
            raise RuntimeError('Upgrade lost registry database metadata')
        api('POST', '/registries', token, dict(registry, name='after-repair'), 201)
        report['checks'].append('Git listing and registry save recovered; existing account/session, secrets and running workload preserved')
        if psql:
            result = run(psql + ['-d', 'unrelated_sentinel', '-Atc', 'SELECT value FROM sentinel']).stdout.strip()
            if result != sentinel:
                raise RuntimeError('Unrelated database sentinel changed')
            report['checks'].append('Unrelated PostgreSQL database preserved across install and resume')
        report['status'] = 'passed'
    except Exception as error:
        report['status'] = 'failed'
        report['error_type'] = type(error).__name__
        # Publish only errors authored here, not arbitrary library/command details.
        if isinstance(error, RuntimeError):
            report['error'] = str(error)
        raise
    finally:
        report['finished_at'] = datetime.now(timezone.utc).isoformat()
        args.report.write_text(json.dumps(report, indent=2) + '\n')


if __name__ == '__main__':
    try:
        main()
    except Exception as error:
        print('Host acceptance failed: ' + type(error).__name__ + '; inspect the redacted report.', file=sys.stderr)
        sys.exit(1)
