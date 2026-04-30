#!/usr/bin/env python3
"""Opt-in local management-process crash/recovery and scoped CLI verification.

Run after lifecycle acceptance (never in parallel): source .local/env;
python3 tests/restart.py --server-pid PID [--application shop]. Only the explicitly
supplied Hakopod server is stopped. A replacement is left running and recorded.
"""
import argparse
from datetime import datetime, timedelta, timezone
import hashlib
import json
import os
from pathlib import Path
import signal
import subprocess
import sys
import time
from urllib.parse import urlencode
import uuid

ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT / 'scripts'))
from acceptance import API, Traffic

DELAY = '# HAKOPOD_RESTART_PROBE_BEGIN\nimport time; time.sleep(15)\n# HAKOPOD_RESTART_PROBE_END\n'
LEGACY_DELAY = 'import time; time.sleep(15)\n'
KUBE = ['kubectl', '--kubeconfig', str(ROOT / '.local/kubeconfig')]


def find_application(api, name):
    cursor = ''
    seen = set()
    for _ in range(201):
        query = urlencode({'project':'demo', 'environment':'development', 'cursor':cursor})
        page = api.call('GET', '/applications?' + query)
        for app in page['items']:
            if app['name'] == name:
                return app
        cursor = page.get('next_cursor', '')
        if not cursor or cursor in seen:
            break
        seen.add(cursor)
    raise AssertionError(f'Application {name!r} is missing; run lifecycle acceptance first or select --application')


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--server-pid', type=int, required=True)
    parser.add_argument('--application', default='shop')
    args = parser.parse_args()
    command = subprocess.check_output(['ps','-p',str(args.server_pid),'-o','command='], text=True, timeout=5).strip()
    if command not in ('bin/hakopod-server', str(ROOT/'bin/hakopod-server')):
        raise RuntimeError('Refusing to stop a process that is not the explicitly identified Hakopod server')
    context = subprocess.check_output(KUBE + ['config','current-context'], text=True, timeout=5).strip()
    if context != 'k3d-hakopod-dev':
        raise RuntimeError('Restart proof requires the named isolated k3d-hakopod-dev cluster')
    admin = API('http://127.0.0.1:8080', (ROOT/'.local/admin-key').read_text().strip())
    expires = (datetime.now(timezone.utc) + timedelta(hours=1)).isoformat()
    key = admin.call('POST','/keys',{'name':'restart-proof','project':'demo','environment':'development','application':args.application,'permissions':['deployments:write','deployments:read','logs:read'],'expires_at':expires},expected=201)
    api = API(admin.origin, key['key'])
    traffic = None
    replacement = None
    restart_required = False
    report = {'passed':False, 'application':args.application, 'temporary_key_id':key['metadata']['id'], 'started_at':datetime.now(timezone.utc).isoformat()}

    def ensure_replacement():
        nonlocal replacement
        if not restart_required:
            return
        if replacement is None:
            with (ROOT/'.local/server.log').open('ab') as log:
                replacement = subprocess.Popen([str(ROOT/'bin/hakopod-server')], cwd=ROOT, stdout=log, stderr=log, start_new_session=True, env=os.environ.copy())
            (ROOT/'.local/server.pid').write_text(str(replacement.pid)+'\n')
            report['server_pid'] = replacement.pid
        deadline = time.monotonic() + 30
        while time.monotonic() < deadline:
            if replacement.poll() is not None:
                raise AssertionError('Replacement management process exited; inspect .local/server.log')
            try:
                admin.call('GET','/me')
                return
            except Exception:
                time.sleep(.25)
        raise AssertionError('Replacement management process did not become ready')

    try:
        app = find_application(api, args.application)
        current = api.call('GET','/applications/'+app['id'])
        spec = current['spec']
        service = spec['services'].get('api', {})
        if not service.get('args') or not service.get('command') or not service['command'][0].startswith('python'):
            raise AssertionError('Restart proof requires the runnable Python demo API service')
        service.setdefault('env', {})['RESTART_PROOF'] = uuid.uuid4().hex
        source = service['args'][0]
        while source.startswith(DELAY) or source.startswith(LEGACY_DELAY):
            source = source[len(DELAY):] if source.startswith(DELAY) else source[len(LEGACY_DELAY):]
        service['args'][0] = DELAY + source
        plan = api.call('POST','/plan',{'project':'demo','environment':'development','spec':spec})
        deployment = api.call('POST','/deployments',{'project':'demo','environment':'development','spec':plan['spec'],'expected_revision':plan['expected_revision']},expected=202,idem=str(uuid.uuid4()))
        report['operation'], report['revision'] = deployment['id'], deployment['revision']
        url = next(s['url'] for s in current['observed']['services'] if s['name']=='web')
        traffic = Traffic(url)
        traffic.thread.start()
        namespace = 'hp-' + hashlib.sha256(app['id'].encode()).hexdigest()[:32]
        deadline = time.monotonic() + 60
        while time.monotonic() < deadline:
            state = api.call('GET','/deployments/'+deployment['id'])
            if state['status'] not in ('queued','running'):
                raise AssertionError('Expected an in-flight deployment')
            if state['status'] == 'running' and state.get('resolved_spec'):
                result = subprocess.run(KUBE+['-n',namespace,'get','deployment','api','-o','json'],capture_output=True,text=True,timeout=10)
                if result.returncode == 0:
                    workload = json.loads(result.stdout)
                    if workload['metadata'].get('annotations',{}).get('hakopod.io/operation') == deployment['id']:
                        report['workload_applied_before_crash'] = True
                        break
            time.sleep(.25)
        else:
            raise AssertionError('Deployment did not apply its first workload change')
        os.kill(args.server_pid, signal.SIGKILL)
        restart_required = True
        time.sleep(3)
        outage_errors = traffic.errors
        ensure_replacement()
        if outage_errors:
            raise AssertionError(f'Application traffic recorded {outage_errors} errors before management recovered')
        result = api.wait(deployment['id'], timeout=240)
        assert result['id'] == deployment['id'] and result['revision'] == deployment['revision']
        env = os.environ.copy()
        env.update(HAKOPOD_API_URL=api.origin,HAKOPOD_API_KEY=key['key'],HAKOPOD_CONFIG=str(ROOT/'.local/nonexistent-ci-config'))
        status = subprocess.run([str(ROOT/'bin/hakopod'),'status',args.application,'--project','demo','--environment','development','--json'],cwd=ROOT,env=env,check=True,capture_output=True,text=True,timeout=30)
        assert json.loads(status.stdout)['revision'] == deployment['revision']
        logs = subprocess.run([str(ROOT/'bin/hakopod'),'logs',args.application,'--project','demo','--environment','development','--service','api','--tail','5'],cwd=ROOT,env=env,check=True,capture_output=True,text=True,timeout=30)
        report.update(passed=True,status=result['status'],scoped_cli_status=True,scoped_cli_log_bytes=len(logs.stdout),same_operation_resumed=True,browser_session_required=False)
    finally:
        original_failure = sys.exc_info()[0] is not None
        recovery_error = None
        if restart_required:
            try:
                ensure_replacement()
            except Exception as error:
                recovery_error = error
                report['passed'] = False
                report['management_recovery_error'] = 'Replacement API is unavailable; inspect .local/server.log and .local/server.pid'
        if traffic:
            measurements = traffic.finish()
            report['traffic_requests'], report['traffic_errors'] = measurements['requests'], measurements['errors']
        cleanup_error = None
        for attempt in range(3):
            try:
                admin.call('DELETE','/keys/'+key['metadata']['id'])
                api.call('GET','/me',expected=401)
                report['temporary_key_revoked'] = True
                cleanup_error = None
                break
            except Exception as error:
                cleanup_error = error
                time.sleep(2)
        if cleanup_error is not None:
            report['passed'] = False
            report['cleanup_error'] = f'Temporary key {key["metadata"]["id"]} could not be revoked; it expires at {expires}'
        report['finished_at'] = datetime.now(timezone.utc).isoformat()
        (ROOT/'.local/restart-acceptance.json').write_text(json.dumps(report,indent=2)+'\n')
        if not original_failure and (recovery_error is not None or cleanup_error is not None):
            raise AssertionError(report.get('management_recovery_error') or report['cleanup_error'])
    print(json.dumps(report,indent=2))


if __name__ == '__main__':
    main()
