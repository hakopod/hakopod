#!/usr/bin/env python3
"""Produce bounded, atomic Supabase development evidence from real check boundaries."""
import argparse, hashlib, json, os, platform, re, runpy, secrets, selectors, signal, subprocess, sys, tempfile, time
from datetime import datetime, timezone
from pathlib import Path

HERE=Path(__file__).resolve().parent; ROOT=HERE.parent.parent
VERIFY=runpy.run_path(str(ROOT/'release/verify-supabase-runtime.py'))
MAX_COMMAND_OUTPUT=2*1024*1024; COMMAND_TIMEOUT=30

def utc_now(): return datetime.now(timezone.utc).isoformat(timespec='milliseconds').replace('+00:00','Z')
def atomic_json(path,value,exclusive=False):
 path=Path(path); VERIFY['reject_symlink_ancestors'](path); path.parent.mkdir(parents=True,exist_ok=True); VERIFY['reject_symlink_ancestors'](path.parent)
 fd,name=tempfile.mkstemp(prefix='.'+path.name+'-',dir=path.parent); temporary=Path(name)
 try:
  os.fchmod(fd,0o600)
  with os.fdopen(fd,'w') as output: json.dump(value,output,sort_keys=True,separators=(',',':'));output.write('\n');output.flush();os.fsync(output.fileno())
  if exclusive:
   os.link(temporary,path);temporary.unlink()
  else: temporary.replace(path)
 except Exception:
  try: temporary.unlink()
  except FileNotFoundError: pass
  raise

def load(path): return VERIFY['read_json'](path)
def command(args):
 process=subprocess.Popen(args,stdout=subprocess.PIPE,stderr=subprocess.PIPE,start_new_session=True); selector=selectors.DefaultSelector(); buffers={'stdout':bytearray(),'stderr':bytearray()}
 selector.register(process.stdout,selectors.EVENT_READ,'stdout');selector.register(process.stderr,selectors.EVENT_READ,'stderr');started=time.monotonic()
 try:
  while selector.get_map():
   if time.monotonic()-started>COMMAND_TIMEOUT: raise ValueError('Evidence observation command exceeded its time limit')
   for key,_ in selector.select(.1):
    chunk=os.read(key.fd,65536)
    if not chunk:selector.unregister(key.fileobj);continue
    if len(buffers[key.data])+len(chunk)>MAX_COMMAND_OUTPUT:raise ValueError('Evidence observation command exceeded its output limit')
    buffers[key.data].extend(chunk)
  if process.wait(timeout=2)!=0:raise ValueError('Evidence observation command failed; raw output was withheld')
  return buffers['stdout'].decode()
 except Exception:
  if process.poll() is None:os.killpg(process.pid,signal.SIGKILL);process.wait()
  raise
 finally:selector.close()

def kubectl(context,*args): return command(['kubectl','--context',context,*args])
def observe_cluster(context):
 current=command(['kubectl','config','current-context']).strip()
 if context!='k3d-hakopod-dev' or current!=context:raise ValueError('Supabase evidence requires the active k3d-hakopod-dev context')
 cluster_uid=kubectl(context,'get','namespace','kube-system','-o','jsonpath={.metadata.uid}').strip()
 nodes=json.loads(kubectl(context,'get','nodes','--chunk-size=32','-o','json'),object_pairs_hook=VERIFY['reject_duplicate_pairs'])
 names=sorted(item.get('metadata',{}).get('name','') for item in nodes.get('items',[]))
 if not cluster_uid or not names or any(not name for name in names):raise ValueError('Kubernetes cluster identity is incomplete')
 return cluster_uid,names

def begin(args):
 state=Path(args.state);events=Path(args.events)
 if state.exists() or events.exists():raise ValueError('Use fresh Supabase evidence state and event paths')
 if sys.platform!='linux' or platform.machine() not in ('x86_64','amd64'):raise ValueError('Supabase native evidence requires Linux AMD64')
 images=VERIFY['validate_images'](load(args.images));cluster_uid,nodes=observe_cluster(args.context);sources=VERIFY['source_files'](args.source)
 value={'schema_version':1,'run_id':secrets.token_hex(16),'started_at':utc_now(),'started_monotonic':time.monotonic(),'context':args.context,'cluster_uid':cluster_uid,'node_names':nodes,'source_files':sources,'images':images,'embedded_assets':VERIFY['embedded_assets'](args.source),'producer':{**VERIFY['PRODUCER'],'runner_sha256':VERIFY['file_hash'](Path(args.source)/VERIFY['PRODUCER']['runner_path']),'producer_sha256':VERIFY['file_hash'](Path(args.source)/VERIFY['PRODUCER']['producer_path'])},'resources':{},'identities':{},'failures':[]}
 atomic_json(state,value);atomic_json(events,{'schema_version':1,'run_id':value['run_id'],'events':[]});print(value['run_id'])

def state_value(path):
 value=load(path)
 if set(value)!={'schema_version','run_id','started_at','started_monotonic','context','cluster_uid','node_names','source_files','images','embedded_assets','producer','resources','identities','failures'} or value.get('schema_version')!=1:raise ValueError('Supabase evidence state is malformed')
 return value

def bind_resource(args):
 state=state_value(args.state)
 if args.role in state['resources']:raise ValueError('Supabase resource role is already bound')
 operation=load(args.operation_observation)
 if not isinstance(operation,dict) or set(operation)!={'id','platform_id','kind','status'} or operation.get('kind')!='create' or operation.get('status')!='succeeded' or any(not isinstance(operation.get(k),str) or not operation[k] for k in ('id','platform_id')):raise ValueError('Supabase create operation observation is malformed')
 namespace='managed-platform-'+operation['platform_id'];namespace_value=json.loads(kubectl(state['context'],'get','namespace',namespace,'-o','json'),object_pairs_hook=VERIFY['reject_duplicate_pairs']);metadata=namespace_value.get('metadata',{});labels=metadata.get('labels',{})
 if metadata.get('name')!=namespace or not metadata.get('uid') or labels.get('app.kubernetes.io/managed-by')!='hakopod' or labels.get('hakopod.io/managed-platform-id')!=operation['platform_id'] or labels.get('hakopod.io/owner-operation-id')!=operation['id']:raise ValueError('Supabase resource namespace ownership is invalid')
 state['resources'][args.role]={'platform_id':operation['platform_id'],'namespace':namespace,'namespace_uid':metadata['uid'],'create_operation_id':operation['id']};atomic_json(args.state,state)

def observe_identity(args):
 state=state_value(args.state);component=args.component
 if component in state['identities'] or args.role not in state['resources']:raise ValueError('Supabase process identity observation is duplicate or unbound')
 resource=state['resources'][args.role];pod=json.loads(kubectl(state['context'],'-n',resource['namespace'],'get','pod',args.pod,'-o','json'),object_pairs_hook=VERIFY['reject_duplicate_pairs']);metadata=pod.get('metadata',{});labels=metadata.get('labels',{})
 if metadata.get('name')!=args.pod or not metadata.get('uid') or labels.get('app.kubernetes.io/managed-by')!='hakopod' or labels.get('hakopod.io/managed-platform-id')!=resource['platform_id'] or labels.get('app.kubernetes.io/component')!=component:raise ValueError('Supabase identity pod ownership is invalid')
 containers=pod.get('spec',{}).get('containers',[])
 if not isinstance(containers,list) or len(containers)!=1 or containers[0].get('image')!=state['images'][component]:raise ValueError('Supabase identity pod image is invalid')
 with tempfile.TemporaryDirectory(prefix='supabase-producer-docker-') as temporary:
  config=Path(temporary)/'config';config.mkdir();values=json.loads(VERIFY['docker'](['image','inspect',state['images'][component]],config),object_pairs_hook=VERIFY['reject_duplicate_pairs'])
  if not isinstance(values,list) or len(values)!=1 or values[0].get('Os')!='linux' or values[0].get('Architecture')!='amd64':raise ValueError('Supabase identity image platform is invalid')
  configured,config_uid,config_gid=VERIFY['resolve_image_identity'](state['images'][component],values[0],config,VERIFY['docker'])
 process=kubectl(state['context'],'-n',resource['namespace'],'exec','pod/'+args.pod,'--','/bin/sh','-ceu','printf "%s:%s\\n" "$(id -u)" "$(id -g)"; awk \'/^(CapInh|CapPrm|CapEff|CapBnd|CapAmb|NoNewPrivs|Seccomp):/{print}\' /proc/1/status')
 lines=process.splitlines();match=re.fullmatch(r'([0-9]+):([0-9]+)',lines[0] if lines else '')
 if not match or len(lines)!=8:raise ValueError('Supabase live process identity observation is malformed')
 uid,gid=int(match.group(1)),int(match.group(2))
 expected={'CapInh:':'0000000000000000','CapPrm:':'0000000000000000','CapEff:':'0000000000000000','CapBnd:':'0000000000000000','CapAmb:':'0000000000000000','NoNewPrivs:':'1','Seccomp:':'2'};observed={parts[0]:parts[1] for parts in (line.split() for line in lines[1:]) if len(parts)==2}
 if uid!=config_uid or gid!=config_gid or observed!=expected:raise ValueError('Supabase live process identity differs from the image or security contract')
 observation={'component':component,'image':state['images'][component],'config_user':configured,'uid':uid,'gid':gid,'pod':args.pod,'pod_uid':metadata['uid'],'process_status':observed};digest=hashlib.sha256(json.dumps(observation,sort_keys=True,separators=(',',':')).encode()).hexdigest()
 state['identities'][component]={'image':state['images'][component],'config_user':configured,'uid':uid,'gid':gid,'process_observation_sha256':digest};atomic_json(args.state,state)

def add_event(args):
 state=state_value(args.state);stream=load(args.events)
 if stream.get('schema_version')!=1 or stream.get('run_id')!=state['run_id'] or set(stream)!={'schema_version','run_id','events'} or not isinstance(stream['events'],list):raise ValueError('Supabase event stream belongs to another run')
 if args.case not in VERIFY['REQUIRED_CASES'] or any(item.get('case')==args.case for item in stream['events']):raise ValueError('Unknown or duplicate Supabase acceptance case')
 if args.case=='edge-runtime-isolation':
  observation=load(args.evidence)
  expected={'unauthenticated_status':401,'invalid_token_status':401,'invalid_token_code':'UNAUTHORIZED_INVALID_JWT_FORMAT','authenticated_fixture':True}
  if observation!=expected:raise ValueError('Supabase Edge authentication observation is invalid')
 elapsed=time.monotonic()-state['started_monotonic'];stream['events'].append({'sequence':len(stream['events'])+1,'case':args.case,'status':'passed','run_id':state['run_id'],'evidence_sha256':VERIFY['file_hash'](args.evidence),'elapsed_seconds':round(elapsed,3)});atomic_json(args.events,stream)

def record_failure(args):
 state=state_value(args.state)
 if not isinstance(args.code,str) or not args.code or len(args.code)>128:raise ValueError('Failure code is malformed')
 state['failures'].append({'code':args.code,'at':utc_now()});atomic_json(args.state,state)

def verify_absence(state,resource):
 if kubectl(state['context'],'get','namespace',resource['namespace'],'--ignore-not-found','-o','name').strip():raise ValueError('Supabase namespace cleanup is incomplete')
 pvs=json.loads(kubectl(state['context'],'get','pv','--chunk-size=128','-o','json'),object_pairs_hook=VERIFY['reject_duplicate_pairs'])
 if not isinstance(pvs.get('items'),list) or len(pvs['items'])>512:raise ValueError('Supabase persistent-volume observation exceeded its item limit')
 for item in pvs.get('items',[]):
  if item.get('spec',{}).get('claimRef',{}).get('namespace')==resource['namespace']:raise ValueError('Supabase persistent-volume cleanup is incomplete')

def finalize(args):
 state=state_value(args.state);stream=load(args.events)
 if Path(args.report).exists() or Path(args.cleanup).exists():raise ValueError('Use fresh Supabase report and cleanup output paths')
 if state['failures']:raise ValueError('Supabase evidence contains recorded failures')
 if set(state['resources'])!={'source','recovery_target'} or set(state['identities'])!=VERIFY['COMPONENTS']:raise ValueError('Supabase resource or identity observations are incomplete')
 if state['resources']['source']['platform_id']==state['resources']['recovery_target']['platform_id']:raise ValueError('Supabase recovery target is not separate')
 for resource in state['resources'].values():verify_absence(state,resource)
 if stream.get('run_id')!=state['run_id']:raise ValueError('Supabase event stream belongs to another run')
 elapsed=round(time.monotonic()-state['started_monotonic'],3);VERIFY['validate_events'](stream.get('events'),state['run_id'],elapsed)
 sources_after=VERIFY['source_files'](args.source)
 if sources_after!=state['source_files']:raise ValueError('Supabase source changed during acceptance')
 finished=utc_now();environment={'context':state['context'],'cluster_uid':state['cluster_uid'],'node_names':state['node_names'],'source':state['resources']['source'],'recovery_target':state['resources']['recovery_target']}
 report={'schema_version':2,'producer':state['producer'],'run_id':state['run_id'],'context':state['context'],'execution':'native','platform':'linux/amd64','started_at':state['started_at'],'finished_at':finished,'elapsed_seconds':elapsed,'environment':environment,'passed':True,'exit_code':0,'limit_error':'','source_files':state['source_files'],'source_files_after':sources_after,'images':state['images'],'identities':state['identities'],'embedded_assets':state['embedded_assets'],'event_file_sha256':VERIFY['file_hash'](args.events),'log_sha256':VERIFY['file_hash'](args.sanitized_log),'test_events':stream['events'],'failed_cases':[],'storage_qualification':{'storage_class':'local-path','provisioner':'rancher.io/local-path','encrypted_storage_class_qualified':False},'public_endpoint_qualified':False,'physical_zones_qualified':False}
 cleanup={'schema_version':2,'run_id':state['run_id'],'context':state['context'],'status':'verified','errors':[]}
 for role,path in (('source',args.source_delete_operation),('recovery_target',args.recovery_target_delete_operation)):
  operation=load(path);resource=state['resources'][role]
  if not isinstance(operation,dict) or set(operation)!={'id','platform_id','kind','status'} or operation.get('platform_id')!=resource['platform_id'] or operation.get('kind')!='delete' or operation.get('status')!='succeeded' or not isinstance(operation.get('id'),str) or not operation['id']:raise ValueError('Supabase delete operation observation is missing or unsuccessful')
  cleanup[role]={**resource,'delete_operation_id':operation['id'],'verified_absent':True,'persistent_volumes_absent':True}
 VERIFY['validate_acceptance'](report,cleanup,sources_after,state['images'],state['identities'],state['producer']['runner_sha256'],state['producer']['producer_sha256'],state['embedded_assets'])
 atomic_json(args.cleanup,cleanup,exclusive=True);atomic_json(args.report,report,exclusive=True)

def parser():
 root=argparse.ArgumentParser(description=__doc__);subs=root.add_subparsers(dest='command',required=True)
 p=subs.add_parser('begin');p.add_argument('--source',type=Path,required=True);p.add_argument('--images',type=Path,required=True);p.add_argument('--state',type=Path,required=True);p.add_argument('--events',type=Path,required=True);p.add_argument('--context',default='k3d-hakopod-dev');p.set_defaults(func=begin)
 p=subs.add_parser('bind-resource');p.add_argument('--state',type=Path,required=True);p.add_argument('--role',choices=('source','recovery_target'),required=True);p.add_argument('--operation-observation',type=Path,required=True);p.set_defaults(func=bind_resource)
 p=subs.add_parser('observe-identity');p.add_argument('--state',type=Path,required=True);p.add_argument('--role',choices=('source','recovery_target'),default='source');p.add_argument('--component',choices=sorted(VERIFY['COMPONENTS']),required=True);p.add_argument('--pod',required=True);p.set_defaults(func=observe_identity)
 p=subs.add_parser('event');p.add_argument('--state',type=Path,required=True);p.add_argument('--events',type=Path,required=True);p.add_argument('--case',required=True);p.add_argument('--evidence',type=Path,required=True);p.set_defaults(func=add_event)
 p=subs.add_parser('fail');p.add_argument('--state',type=Path,required=True);p.add_argument('--code',required=True);p.set_defaults(func=record_failure)
 p=subs.add_parser('finalize');p.add_argument('--source',type=Path,required=True);p.add_argument('--state',type=Path,required=True);p.add_argument('--events',type=Path,required=True);p.add_argument('--sanitized-log',type=Path,required=True);p.add_argument('--source-delete-operation',type=Path,required=True);p.add_argument('--recovery-target-delete-operation',type=Path,required=True);p.add_argument('--report',type=Path,required=True);p.add_argument('--cleanup',type=Path,required=True);p.set_defaults(func=finalize)
 return root
if __name__=='__main__':
 try:
  args=parser().parse_args();args.func(args)
 except (ValueError,OSError,subprocess.SubprocessError): raise SystemExit('Supabase evidence rejected')
