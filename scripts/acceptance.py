#!/usr/bin/env python3
"""Exercise a real API, PostgreSQL and Kubernetes deployment lifecycle.

Local development only. Select a separate fixture with --application to preserve
demo/development/shop. Creates a temporary scoped key, records a compact JSON
report without credentials, and revokes that key.
"""
import argparse
from datetime import datetime, timedelta, timezone
import http.client
import json
import os
import re
from pathlib import Path
import subprocess
import sys
import threading
import time
import urllib.error
import urllib.parse
import urllib.request
import uuid

ROOT = Path(__file__).resolve().parents[1]

class APIError(AssertionError):
    def __init__(self, status, message):
        self.status = status
        super().__init__(message)

class API:
    def __init__(self, origin, key):
        self.origin, self.key = origin.rstrip('/'), key

    def call(self, method, path, data=None, expected=200, idem=None, authenticated=True):
        headers = {'Content-Type':'application/json'}
        if authenticated:
            headers['Authorization'] = 'Bearer ' + self.key
        if idem:
            headers['Idempotency-Key'] = idem
        request = urllib.request.Request(self.origin + '/api/v1' + path, data=json.dumps(data).encode() if data is not None else None, method=method, headers=headers)
        try:
            with urllib.request.urlopen(request, timeout=15) as response:
                status, body = response.status, response.read(1 << 20)
        except urllib.error.HTTPError as error:
            status, body = error.code, error.read(1 << 20)
        decoded = json.loads(body) if body else None
        if status != expected:
            # Never include request headers, bodies or key-creation responses.
            detail = decoded.get('error', {}) if isinstance(decoded, dict) else {}
            raise APIError(status, f'{method} {path}: expected HTTP {expected}, got {status}: {detail}')
        return decoded

    def wait(self, deployment_id, wanted='succeeded', timeout=360):
        deadline = time.monotonic() + timeout
        while time.monotonic() < deadline:
            try:
                deployment = self.call('GET', '/deployments/' + deployment_id)
            except APIError as error:
                if error.status not in (429, 503):
                    raise
                time.sleep(2)
                continue
            except (urllib.error.URLError, TimeoutError):
                time.sleep(2)
                continue
            if deployment['status'] not in ('queued', 'running'):
                if deployment['status'] != wanted:
                    raise AssertionError(f'Deployment {deployment_id}: expected {wanted}, got {deployment["status"]}: {deployment.get("error", "")}')
                return deployment
            time.sleep(1)
        raise AssertionError(f'Deployment {deployment_id} exceeded acceptance timeout')

class Traffic:
    def __init__(self, url):
        parsed = urllib.parse.urlsplit(url)
        self.host, self.port = parsed.hostname, parsed.port or 80
        self.stop = threading.Event()
        self.count, self.errors = 0, 0
        self.thread = threading.Thread(target=self.run, daemon=True)

    def get(self):
        # Connect directly to loopback; the Host header exercises actual HAProxy routing.
        connection = http.client.HTTPConnection('127.0.0.1', self.port, timeout=3)
        try:
            connection.request('GET', '/api', headers={'Host': self.host})
            response = connection.getresponse()
            body = response.read(65536)
            if response.status != 200:
                raise AssertionError(f'Public /api returned {response.status}')
            return json.loads(body)
        finally:
            connection.close()

    def run(self):
        while not self.stop.is_set():
            self.count += 1
            try:
                self.get()
            except Exception:
                self.errors += 1
            self.stop.wait(0.25)

    def finish(self):
        self.stop.set()
        self.thread.join(timeout=5)
        return {'requests':self.count, 'errors':self.errors, 'interval_seconds':0.25}

def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--api-url', default=os.environ.get('HAKOPOD_API_URL', 'http://127.0.0.1:8080'))
    parser.add_argument('--admin-key-file', default=str(ROOT / '.local/admin-key'))
    parser.add_argument('--project', default='demo')
    parser.add_argument('--environment', default='development')
    parser.add_argument('--application', default='shop', help='Use a separate application name to preserve the interactive sample')
    parser.add_argument('--skip-network', action='store_true')
    args = parser.parse_args()
    if not re.fullmatch(r'[a-z][a-z0-9-]{0,62}', args.application):
        raise SystemExit('Choose a valid application slug.')
    def selected_application(toml):
        if 'name = "shop"' not in toml:
            raise AssertionError('Acceptance fixture application header changed.')
        return toml.replace('name = "shop"', 'name = "' + args.application + '"', 1)
    if urllib.parse.urlsplit(args.api_url).hostname not in ('127.0.0.1', 'localhost', '::1'):
        raise SystemExit('This mutation-heavy acceptance test is restricted to a loopback API.')
    key = Path(args.admin_key_file).read_text().strip()
    admin = API(args.api_url, key)
    report = {'passed':False, 'application':args.application, 'started_at':datetime.now(timezone.utc).isoformat(), 'checks':[], 'deployments':[]}
    def passed(name):
        report['checks'].append(name)
        print('PASS ' + name, flush=True)
    admin.call('GET', '/me', expected=401, authenticated=False)
    passed('unauthenticated API request denied')
    admin.call('POST', '/projects', {'name':args.project, 'environment':args.environment}, expected=201)
    expires = (datetime.now(timezone.utc) + timedelta(minutes=30)).isoformat().replace('+00:00','Z')
    created = admin.call('POST', '/keys', {'name':'Local acceptance deployer', 'project':args.project, 'environment':args.environment, 'permissions':['deployments:write','deployments:read','logs:read'], 'expires_at':expires}, expected=201)
    deployer = API(args.api_url, created['key'])
    key_id = created['metadata']['id']
    report['temporary_key_id'] = key_id
    traffic = None
    try:
        deployer.call('GET', '/keys', expected=403)
        deployer.call('GET', '/nodes', expected=403)
        passed('deployment key cannot administer keys or nodes')
        baseline = selected_application((ROOT / 'examples/shop/hakopod.toml').read_text())
        scope = {'project':args.project, 'environment':args.environment}
        deployer.call('POST', '/plan', dict(scope, toml='schema_version = 999\nname = "invalid"'), expected=400)
        deployer.call('POST', '/plan', {'project':'forbidden-project','environment':'production','toml':baseline}, expected=403)
        passed('invalid TOML and unauthorized project rejected')
        plan = deployer.call('POST', '/plan', dict(scope, toml=baseline))
        idem = 'acceptance-' + uuid.uuid4().hex
        payload = dict(scope, toml=baseline, expected_revision=plan['expected_revision'])
        accepted = deployer.call('POST', '/deployments', payload, expected=202, idem=idem)
        duplicate = deployer.call('POST', '/deployments', payload, expected=202, idem=idem)
        assert duplicate['id'] == accepted['id'], 'Duplicate request created a second operation'
        deployer.call('POST', '/deployments', payload, expected=409, idem='stale-' + uuid.uuid4().hex)
        passed('duplicate request deduplicated; stale revision rejected')
        first = deployer.wait(accepted['id'])
        report['deployments'].append({'id':first['id'],'revision':first['revision'],'status':first['status']})
        app_id = first['application_id']
        app = deployer.call('GET', '/applications/' + app_id)
        services = app['observed']['services']
        url = next(service['url'] for service in services if service['name'] == 'web')
        assert all(not s.get('url') for s in services if s['name']=='api'), 'Private API reported a public URL'
        traffic = Traffic(url)
        deadline = time.monotonic() + 30
        while True:
            try:
                baseline_response = traffic.get()
                break
            except Exception:
                if time.monotonic() >= deadline:
                    raise
                time.sleep(1)
        assert baseline_response['service'] == 'private-api'
        passed('public HAProxy URL proxies to real private API')
        if not args.skip_network:
            subprocess.run([sys.executable, str(ROOT/'scripts/network-acceptance.py'), '--application-id', app_id, '--replace-pod'], check=True)
            passed('generated network policies, pod-IP denial, DNS and pod replacement')
        subprocess.run([sys.executable, str(ROOT/'examples/shop/variants.py')], check=True)
        traffic.thread.start()
        for filename, wanted in [('shop-updated.toml','succeeded'), ('shop-failed-readiness.toml','failed')]:
            content = selected_application((ROOT/'.local/examples'/filename).read_text())
            plan = deployer.call('POST', '/plan', dict(scope, toml=content, service='api'))
            result = deployer.call('POST', '/deployments', dict(scope, toml=content, service='api', expected_revision=plan['expected_revision']), expected=202, idem=uuid.uuid4().hex)
            finished = deployer.wait(result['id'], wanted)
            report['deployments'].append({'id':finished['id'],'revision':finished['revision'],'status':finished['status']})
            if wanted == 'succeeded':
                assert traffic.get()['python'].startswith('3.14.'), 'Updated image did not reach traffic'
                passed('service-targeted image update became ready and reached traffic')
            else:
                assert finished.get('error'), 'Failed readiness omitted an actionable cause'
                assert any(event['type'] == 'recovered' for event in finished.get('events', [])), 'Failed release did not record successful recovery of the prior healthy group'
                assert traffic.get()['python'].startswith('3.14.'), 'Recovery did not preserve the prior healthy API artifact'
                passed('failed readiness reported as failed operation')
                passed('previous healthy application recovered after failed rollout')
        current = deployer.call('GET', '/applications/' + app_id)
        rollback = deployer.call('POST', '/applications/' + app_id + '/rollback', {'revision':first['revision'], 'expected_revision':current['revision']}, expected=202, idem=uuid.uuid4().hex)
        rolled = deployer.wait(rollback['id'])
        assert traffic.get()['python'].startswith('3.13.'), 'Rollback did not restore the original artifact'
        report['deployments'].append({'id':rolled['id'],'revision':rolled['revision'],'status':rolled['status']})
        passed('explicit rollback created a new revision from original image digest')
        report['traffic'] = traffic.finish()
        traffic = None
        passed(f'traffic probe recorded {report["traffic"]["requests"]} requests and {report["traffic"]["errors"]} errors')
        report['application_id'] = app_id
        report['url'] = url
        report['passed'] = True
    finally:
        failed_before_cleanup = sys.exc_info()[0] is not None
        if traffic is not None and traffic.thread.is_alive():
            report['traffic'] = traffic.finish()
        cleanup_error = None
        for attempt in range(3):
            try:
                admin.call('DELETE', '/keys/' + key_id)
                deployer.call('GET', '/me', expected=401)
                passed('revoked deployment key rejected')
                cleanup_error = None
                break
            except (APIError, urllib.error.URLError, TimeoutError) as error:
                cleanup_error = error
                time.sleep(2)
        if cleanup_error is not None:
            report['passed'] = False
            report['cleanup_error'] = f'Temporary key {key_id} could not be revoked; it expires automatically at {expires}'
            print(report['cleanup_error'], file=sys.stderr)
        report['finished_at'] = datetime.now(timezone.utc).isoformat()
        destination = ROOT / '.local/acceptance.json'
        destination.write_text(json.dumps(report, indent=2) + '\n')
        if cleanup_error is not None and not failed_before_cleanup:
            raise cleanup_error
    print('Real-cluster acceptance complete; report: .local/acceptance.json', flush=True)

if __name__ == '__main__':
    try:
        main()
    except (AssertionError, subprocess.SubprocessError, urllib.error.URLError) as exc:
        print('FAIL ' + str(exc), file=sys.stderr)
        raise SystemExit(1)
