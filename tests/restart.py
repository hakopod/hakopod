#!/usr/bin/env python3
"""Opt-in local management-process crash/recovery and scoped CLI verification.

Run after lifecycle acceptance (never in parallel): source .local/env;
python3 tests/restart.py --server-pid PID. Only the explicitly supplied Hakopod
server process is stopped. A replacement process is left running and recorded.
"""
import argparse
from datetime import datetime, timedelta, timezone
import json
import os
from pathlib import Path
import signal
import subprocess
import sys
import time
import uuid

ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT / 'scripts'))
from acceptance import API, Traffic

def main():
    parser=argparse.ArgumentParser();parser.add_argument('--server-pid',type=int,required=True);args=parser.parse_args()
    command=subprocess.check_output(['ps','-p',str(args.server_pid),'-o','command='],text=True).strip()
    if command not in ('bin/hakopod-server',str(ROOT/'bin/hakopod-server')):
        raise RuntimeError('Refusing to stop a process that is not the explicitly identified Hakopod server')
    admin=API('http://127.0.0.1:8080',(ROOT/'.local/admin-key').read_text().strip())
    key=admin.call('POST','/keys',{'name':'restart-proof','project':'demo','environment':'development','application':'cli-check','permissions':['deployments:write','deployments:read','logs:read'],'expires_at':(datetime.now(timezone.utc)+timedelta(hours=1)).isoformat()},expected=201)
    api=API(admin.origin,key['key']);traffic=None;report={'passed':False}
    try:
        apps=api.call('GET','/applications?project=demo&environment=development')['items']
        app=next(a for a in apps if a['name']=='cli-check')
        current=api.call('GET','/applications/'+app['id'])
        spec=current['spec'];spec['services']['api'].setdefault('env',{})['RESTART_PROOF']=uuid.uuid4().hex
        # Cold-start delay keeps the release in flight when the process is killed.
        spec['services']['api']['args'][0]='import time; time.sleep(15)\n'+spec['services']['api']['args'][0]
        plan=api.call('POST','/plan',{'project':'demo','environment':'development','spec':spec})
        deployment=api.call('POST','/deployments',{'project':'demo','environment':'development','spec':plan['spec'],'expected_revision':plan['expected_revision']},expected=202,idem=str(uuid.uuid4()))
        url=next(s['url'] for s in current['observed']['services'] if s['name']=='web')
        traffic=Traffic(url);traffic.thread.start()
        deadline=time.monotonic()+45
        while time.monotonic()<deadline:
            state=api.call('GET','/deployments/'+deployment['id'])
            if state['status']=='running' and state.get('resolved_spec'):
                break
            if state['status'] not in ('queued','running'):
                raise AssertionError('Expected an in-flight deployment')
            time.sleep(.25)
        else: raise AssertionError('Deployment did not start')
        os.kill(args.server_pid,signal.SIGKILL)
        time.sleep(3)
        if traffic.errors: raise AssertionError('Application traffic failed while management was down')
        with (ROOT/'.local/server.log').open('ab') as log:
            server=subprocess.Popen([str(ROOT/'bin/hakopod-server')],cwd=ROOT,stdout=log,stderr=log,start_new_session=True,env=os.environ.copy())
        (ROOT/'.local/server.pid').write_text(str(server.pid)+'\n')
        deadline=time.monotonic()+30
        while time.monotonic()<deadline:
            try: api.call('GET','/me');break
            except Exception: time.sleep(.25)
        else: raise AssertionError('Replacement management process did not become ready')
        result=api.wait(deployment['id'],timeout=240)
        assert result['id']==deployment['id'] and result['revision']==deployment['revision']
        env=os.environ.copy();env.update(HAKOPOD_API_URL=api.origin,HAKOPOD_API_KEY=key['key'],HAKOPOD_CONFIG=str(ROOT/'.local/nonexistent-ci-config'))
        status=subprocess.run([str(ROOT/'bin/hakopod'),'status','cli-check','--project','demo','--environment','development','--json'],cwd=ROOT,env=env,check=True,capture_output=True,text=True)
        parsed=json.loads(status.stdout);assert parsed['revision']==deployment['revision']
        logs=subprocess.run([str(ROOT/'bin/hakopod'),'logs','cli-check','--project','demo','--environment','development','--service','api','--tail','5'],cwd=ROOT,env=env,check=True,capture_output=True,text=True)
        traffic.finish()
        report={'passed':True,'operation':result['id'],'revision':result['revision'],'status':result['status'],'server_pid':server.pid,'traffic_requests':traffic.count,'traffic_errors':traffic.errors,'scoped_cli_status':True,'scoped_cli_log_bytes':len(logs.stdout),'same_operation_resumed':True,'browser_session_required':False}
        print(json.dumps(report,indent=2))
    finally:
        if traffic: traffic.finish()
        try: admin.call('DELETE','/keys/'+key['metadata']['id'])
        except Exception: pass
        (ROOT/'.local/restart-acceptance.json').write_text(json.dumps(report,indent=2)+'\n')

if __name__=='__main__': main()
