#!/usr/bin/env python3
"""Focused tests for fail-closed Supabase qualification evidence."""
import copy,hashlib,json,os,runpy,shutil,subprocess,tempfile,time,unittest
from pathlib import Path
from unittest.mock import patch
HERE=Path(__file__).resolve().parent.parent
VERIFY=runpy.run_path(str(HERE/'release/verify-supabase-runtime.py')); RECORD=runpy.run_path(str(HERE/'release/record-supabase-qualification.py'))
EVIDENCE=runpy.run_path(str(HERE/'examples/supabase-native-acceptance/evidence.py'))

class SupabaseQualificationTest(unittest.TestCase):
 def setUp(self):
  self.temp=tempfile.TemporaryDirectory(dir=HERE); self.root=Path(self.temp.name)/'source'
  for name in VERIFY['SOURCE_DIRECTORIES']:(self.root/name).mkdir(parents=True)
  (self.root/'go.mod').write_text('module example.test/qualification\n');(self.root/'go.sum').write_text('');(self.root/'internal/core.go').write_text('package internal\n')
  asset=self.root/'internal/managedplatform/supabase-assets/api/config.yml';asset.parent.mkdir(parents=True);asset.write_text('static: true\n')
  (self.root/'templates/catalog.json').write_text('{}\n');(self.root/'cmd/hakopod-server/main.go').write_text('package main\n')
  runner=self.root/'examples/supabase-native-acceptance/run.sh';runner.write_text('#!/bin/sh\nexit 0\n');producer=runner.with_name('evidence.py');producer.write_text('# producer\n')
  (self.root/'examples/owned-pod-stream.py').write_text('# bounded owned Pod relay\n')
  release=self.root/'release';release.mkdir();shutil.copyfile(HERE/'release/verify-supabase-runtime.py',release/'verify-supabase-runtime.py');shutil.copyfile(HERE/'release/record-supabase-qualification.py',release/'record-supabase-qualification.py')
  self.images={n:'registry.example/'+n+'@sha256:'+'a'*64 for n in VERIFY['COMPONENTS']}
  self.write_release_contract(False)
  self.sources=VERIFY['source_files'](self.root)
  self.identities={n:{'uid':10001,'gid':10001,'image':image,'config_user':'app:app','process_observation_sha256':'b'*64} for n,image in self.images.items()}
  self.run_id='c'*32
  source={'platform_id':'source','namespace':'managed-platform-source','namespace_uid':'source-uid','create_operation_id':'create-source'};target={'platform_id':'target','namespace':'managed-platform-target','namespace_uid':'target-uid','create_operation_id':'create-target'}
  self.environment={'context':'k3d-hakopod-dev','cluster_uid':'cluster-uid','node_names':['k3d-hakopod-dev-server-0'],'source':source,'recovery_target':target}
  events=[{'sequence':i,'case':name,'status':'passed','run_id':self.run_id,'evidence_sha256':('%064x'%i),'elapsed_seconds':float(i)} for i,name in enumerate(sorted(VERIFY['REQUIRED_CASES']),1)]
  self.report={'schema_version':2,'producer':{**VERIFY['PRODUCER'],'runner_sha256':VERIFY['file_hash'](runner),'producer_sha256':VERIFY['file_hash'](producer)},'run_id':self.run_id,'context':'k3d-hakopod-dev','execution':'native','platform':'linux/amd64','started_at':'2026-10-02T00:00:00Z','finished_at':'2026-10-02T00:10:00Z','elapsed_seconds':600.0,'environment':self.environment,'passed':True,'exit_code':0,'limit_error':'','source_files':self.sources,'source_files_after':self.sources,'images':self.images,'identities':self.identities,'embedded_assets':VERIFY['embedded_assets'](self.root),'event_file_sha256':'d'*64,'log_sha256':'e'*64,'test_events':events,'failed_cases':[],'storage_qualification':{'storage_class':'local-path','provisioner':'rancher.io/local-path','encrypted_storage_class_qualified':False},'public_endpoint_qualified':False,'physical_zones_qualified':False}
  self.cleanup={'schema_version':2,'run_id':self.run_id,'context':'k3d-hakopod-dev','status':'verified','errors':[],'source':{**source,'delete_operation_id':'delete-source','verified_absent':True,'persistent_volumes_absent':True},'recovery_target':{**target,'delete_operation_id':'delete-target','verified_absent':True,'persistent_volumes_absent':True}}
 def tearDown(self):self.temp.cleanup()
 def write_release_contract(self,qualified):
  digest=hashlib.sha256('\n'.join(name+'='+self.images[name] for name in sorted(self.images)).encode()).hexdigest()
  lines=['package managedplatform','const SupabaseReleaseQualificationID = "supabase-0.8.2-linux-amd64"','const SupabaseReleaseImageInventorySHA256 = "'+digest+'"','var supabaseReleaseImages = map[string]string{']
  lines += ['\t"'+name+'": "'+image+'",' for name,image in sorted(self.images.items())]
  lines += ['}','func SupabaseReleaseQualified() bool { return '+str(qualified).lower()+' }']
  (self.root/'internal/managedplatform/supabase_qualification.go').write_text('\n'.join(lines)+'\n')
 def write(self,name,value):
  path=Path(self.temp.name)/name;path.write_text(json.dumps(value));return path
 def assemble(self,report=None,cleanup=None):return RECORD['assemble'](self.root,self.write('report.json',report or self.report),self.write('cleanup.json',cleanup or self.cleanup),Path(self.temp.name)/'qualification')
 def validate(self,report=None,cleanup=None):
  report=report or self.report;cleanup=cleanup or self.cleanup
  return VERIFY['validate_acceptance'](report,cleanup,self.sources,self.images,self.identities,report['producer']['runner_sha256'],report['producer']['producer_sha256'],report['embedded_assets'])
 def test_records_development_evidence_without_qualification(self):
  manifest=self.assemble();self.assertTrue(manifest['capability']['development_evidence_recorded']);self.assertFalse(manifest['capability']['development_cluster_qualified']);self.assertFalse(manifest['capability']['release_runtime_qualified']);VERIFY['validate_metadata'](Path(self.temp.name)/'qualification',self.root)
 def test_closed_gate_cannot_publish_or_pull_images(self):
  self.assemble();calls=[]
  with self.assertRaisesRegex(ValueError,'release gate is closed'):
   VERIFY['verify'](Path(self.temp.name)/'qualification',Path(self.temp.name)/'verified',self.root,lambda *args:calls.append(args))
  self.assertEqual(calls,[]);self.assertFalse(Path(self.temp.name,'verified').exists())
 def test_final_native_source_can_qualify_runtime_without_approving_a_deployment(self):
  self.write_release_contract(True);self.sources=VERIFY['source_files'](self.root)
  self.report['source_files']=self.sources;self.report['source_files_after']=self.sources
  manifest=self.assemble()
  self.assertTrue(manifest['capability']['release_runtime_qualified'])
  for name in ('cluster_qualified','encrypted_storage_class_qualified','public_endpoint_qualified','physical_zones_qualified'):
   self.assertFalse(manifest['capability'][name])
 def test_native_images_must_match_compiled_inventory(self):
  changed=dict(self.images);changed['studio']='registry.example/changed@sha256:'+'f'*64
  with self.assertRaisesRegex(ValueError,'compiled release inventory'):VERIFY['release_runtime_qualified'](self.root,changed)
 def test_gate_change_after_acceptance_requires_new_native_evidence(self):
  self.assemble();self.write_release_contract(True)
  with self.assertRaisesRegex(ValueError,'source changed'):VERIFY['validate_metadata'](Path(self.temp.name)/'qualification',self.root)
 def test_manifest_cannot_promote_a_closed_gate(self):
  self.assemble();path=Path(self.temp.name)/'qualification/manifest.json';value=json.loads(path.read_text());value['capability']['release_runtime_qualified']=True;path.write_text(json.dumps(value))
  with self.assertRaisesRegex(ValueError,'capability boundary'):VERIFY['validate_metadata'](path.parent,self.root)
 def test_rejects_legacy_or_uncommitted_producer(self):
  with self.assertRaisesRegex(ValueError,'legacy'):self.assemble({'schema_version':1})
  report=copy.deepcopy(self.report);report['producer']['producer_sha256']='f'*64
  with self.assertRaisesRegex(ValueError,'committed producer'):self.assemble(report)
 def test_rejects_run_environment_and_cleanup_cross_binding(self):
  for mutate in ('run','namespace','operation'):
   cleanup=copy.deepcopy(self.cleanup)
   if mutate=='run':cleanup['run_id']='f'*32
   elif mutate=='namespace':cleanup['source']['namespace_uid']='other'
   else:cleanup['source']['create_operation_id']='other'
   with self.subTest(mutate=mutate),self.assertRaises(ValueError):self.validate(cleanup=cleanup)
 def test_rejects_stale_source_and_bad_elapsed(self):
  report=copy.deepcopy(self.report);report['source_files_after']['go.mod']='f'*64
  with self.assertRaisesRegex(ValueError,'final source'):self.validate(report)
  report=copy.deepcopy(self.report);report['elapsed_seconds']=True
  with self.assertRaisesRegex(ValueError,'elapsed'):self.validate(report)
 def test_rejects_unbound_duplicate_missing_or_arbitrary_events(self):
  reports=[]
  value=copy.deepcopy(self.report);value['test_events'][0]['run_id']='f'*32;reports.append(value)
  value=copy.deepcopy(self.report);value['test_events'][0]['case']='invented';reports.append(value)
  value=copy.deepcopy(self.report);value['test_events'].pop();reports.append(value)
  value=copy.deepcopy(self.report);value['test_events'][1]['sequence']=1;reports.append(value)
  for report in reports:
   with self.subTest(count=len(report['test_events'])),self.assertRaises(ValueError):self.validate(report)
 def test_rejects_bool_identity_and_malformed_image(self):
  report=copy.deepcopy(self.report);report['identities']['studio']['uid']=True
  with self.assertRaisesRegex(ValueError,'identity'):self.assemble(report)
  report=copy.deepcopy(self.report);report['images']['studio']='Bad Repo@sha256:'+'a'*64
  with self.assertRaisesRegex(ValueError,'immutable'):self.assemble(report)
 def test_rejects_duplicate_json_keys(self):
  path=Path(self.temp.name)/'duplicate.json';path.write_text('{"schema_version":2,"schema_version":2}')
  with self.assertRaisesRegex(ValueError,'Duplicate JSON key'):VERIFY['read_json'](path)
 def test_rejects_symlinked_ancestor_and_leaves_no_output(self):
  real=Path(self.temp.name)/'real';real.mkdir();link=Path(self.temp.name)/'linked';link.symlink_to(real,target_is_directory=True);artifact=real/'value.json';artifact.write_text('{}')
  with self.assertRaisesRegex(ValueError,'Symbolic'):VERIFY['read_json'](link/'value.json')
  report=copy.deepcopy(self.report);report['run_id']='bad';output=Path(self.temp.name)/'qualification'
  with self.assertRaises(ValueError):self.assemble(report)
  self.assertFalse(output.exists())
 def test_resolves_named_and_numeric_config_users_without_start(self):
  files={};calls=[]
  def runner(args,config,limit=VERIFY['MAX_DOCKER_STDOUT']):
   calls.append(args)
   if args[0]=='create':return 'a'*64+'\n'
   if args[0]=='cp':
    path=Path(args[2]);path.write_text('app:x:1001:1002::/:/bin/sh\n' if path.name=='passwd' else 'appgroup:x:1002:\n');return ''
   return ''
  self.assertEqual(VERIFY['resolve_image_identity']('image',{'Config':{'User':'app:appgroup'}},Path(self.temp.name),runner),('app:appgroup',1001,1002));self.assertFalse(any(c[0]=='start' for c in calls))
  self.assertIn(['create','--network','none','image'],calls)
 def test_docker_failure_is_sanitized(self):
  with patch('subprocess.Popen') as popen:
   process=popen.return_value;process.stdout=object();process.stderr=object()
   # The integration path is VM-only; assert source never interpolates captured stderr.
   source=(HERE/'release/verify-supabase-runtime.py').read_text();self.assertIn('raw output was withheld',source);self.assertNotIn('CalledProcessError',source)

 def producer_state(self):
  return {'schema_version':1,'run_id':self.run_id,'started_at':'2026-10-02T00:00:00Z','started_monotonic':time.monotonic()-600,'context':'k3d-hakopod-dev','cluster_uid':'cluster-uid','node_names':['node-1'],'source_files':self.sources,'images':self.images,'embedded_assets':self.report['embedded_assets'],'producer':self.report['producer'],'resources':{},'identities':{},'failures':[]}
 def test_producer_binds_live_namespace_ownership(self):
  state_path=self.write('state.json',self.producer_state());operation=self.write('create.json',{'id':'create-source','platform_id':'source','kind':'create','status':'succeeded'})
  namespace={'metadata':{'name':'managed-platform-source','uid':'source-uid','labels':{'app.kubernetes.io/managed-by':'hakopod','hakopod.io/managed-platform-id':'source','hakopod.io/owner-operation-id':'create-source'}}}
  args=type('Args',(),{'state':state_path,'role':'source','operation_observation':operation})()
  with patch.dict(EVIDENCE['bind_resource'].__globals__,{'kubectl':lambda *unused:json.dumps(namespace)}):EVIDENCE['bind_resource'](args)
  self.assertEqual(EVIDENCE['load'](state_path)['resources']['source']['namespace_uid'],'source-uid')
  bad=copy.deepcopy(namespace);bad['metadata']['labels']['hakopod.io/owner-operation-id']='other';operation=self.write('create-target.json',{'id':'create-target','platform_id':'target','kind':'create','status':'succeeded'});args=type('Args',(),{'state':state_path,'role':'recovery_target','operation_observation':operation})()
  with patch.dict(EVIDENCE['bind_resource'].__globals__,{'kubectl':lambda *unused:json.dumps(bad)}),self.assertRaisesRegex(ValueError,'ownership'):EVIDENCE['bind_resource'](args)
 def test_producer_observes_image_and_process_identity_directly(self):
  state=self.producer_state();state['resources']['source']=self.environment['source'];state_path=self.write('identity-state.json',state)
  pod={'metadata':{'name':'studio-0','uid':'pod-uid','labels':{'app.kubernetes.io/managed-by':'hakopod','hakopod.io/managed-platform-id':'source','app.kubernetes.io/component':'studio'}},'spec':{'containers':[{'image':self.images['studio']}]}}
  process='10001:10001\nCapInh:\t0000000000000000\nCapPrm:\t0000000000000000\nCapEff:\t0000000000000000\nCapBnd:\t0000000000000000\nCapAmb:\t0000000000000000\nNoNewPrivs:\t1\nSeccomp:\t2\n'
  def kube(*args):return json.dumps(pod) if 'get' in args else process
  args=type('Args',(),{'state':state_path,'role':'source','component':'studio','pod':'studio-0'})()
  globals_dict=EVIDENCE['observe_identity'].__globals__
  with patch.dict(globals_dict,{'kubectl':kube}),patch.dict(EVIDENCE['VERIFY'],{'docker':lambda *unused:'[{"Os":"linux","Architecture":"amd64","Config":{"User":"app:app"}}]','resolve_image_identity':lambda *unused:('app:app',10001,10001)}):EVIDENCE['observe_identity'](args)
  identity=EVIDENCE['load'](state_path)['identities']['studio'];self.assertEqual((identity['uid'],identity['gid']),(10001,10001));self.assertRegex(identity['process_observation_sha256'],r'^[0-9a-f]{64}$')
 def test_producer_rejects_unknown_events_and_existing_final_outputs(self):
  state_path=self.write('event-state.json',self.producer_state());events_path=self.write('events.json',{'schema_version':1,'run_id':self.run_id,'events':[]});evidence=self.write('safe.json',{'observed':True})
  args=type('Args',(),{'state':state_path,'events':events_path,'case':'invented','evidence':evidence})()
  with self.assertRaisesRegex(ValueError,'Unknown'):EVIDENCE['add_event'](args)
  args.case='edge-runtime-isolation'
  with self.assertRaisesRegex(ValueError,'authentication observation'):EVIDENCE['add_event'](args)
  evidence.write_text(json.dumps({'unauthenticated_status':401,'invalid_token_status':401,'invalid_token_code':'UNAUTHORIZED_INVALID_JWT_FORMAT','authenticated_fixture':True}))
  EVIDENCE['add_event'](args)
  self.assertEqual(EVIDENCE['load'](events_path)['events'][0]['case'],'edge-runtime-isolation')
  Path(self.temp.name,'final.json').write_text('{}');final_args=type('Args',(),{'state':state_path,'events':events_path,'report':Path(self.temp.name,'final.json'),'cleanup':Path(self.temp.name,'cleanup-final.json')})()
  with self.assertRaisesRegex(ValueError,'fresh'):EVIDENCE['finalize'](final_args)
 def test_producer_rejects_process_mismatch_and_incomplete_absence(self):
  state=self.producer_state();state['resources']['source']=self.environment['source'];state_path=self.write('mismatch-state.json',state)
  pod={'metadata':{'name':'studio-0','uid':'pod-uid','labels':{'app.kubernetes.io/managed-by':'hakopod','hakopod.io/managed-platform-id':'source','app.kubernetes.io/component':'studio'}},'spec':{'containers':[{'image':self.images['studio']}]}}
  process='10002:10001\nCapInh:\t0000000000000000\nCapPrm:\t0000000000000000\nCapEff:\t0000000000000000\nCapBnd:\t0000000000000000\nCapAmb:\t0000000000000000\nNoNewPrivs:\t1\nSeccomp:\t2\n'
  args=type('Args',(),{'state':state_path,'role':'source','component':'studio','pod':'studio-0'})()
  with patch.dict(EVIDENCE['observe_identity'].__globals__,{'kubectl':lambda *a:json.dumps(pod) if 'get' in a else process}),patch.dict(EVIDENCE['VERIFY'],{'docker':lambda *unused:'[{"Os":"linux","Architecture":"amd64","Config":{"User":"app:app"}}]','resolve_image_identity':lambda *unused:('app:app',10001,10001)}),self.assertRaisesRegex(ValueError,'differs'):EVIDENCE['observe_identity'](args)
  with patch.dict(EVIDENCE['verify_absence'].__globals__,{'kubectl':lambda *unused:'namespace/managed-platform-source\n'}),self.assertRaisesRegex(ValueError,'namespace cleanup'):EVIDENCE['verify_absence'](state,state['resources']['source'])
  pv={'items':[{'spec':{'claimRef':{'namespace':'managed-platform-source'}}}]}
  def kube(*args):return '' if 'namespace' in args else json.dumps(pv)
  with patch.dict(EVIDENCE['verify_absence'].__globals__,{'kubectl':kube}),self.assertRaisesRegex(ValueError,'persistent-volume'):EVIDENCE['verify_absence'](state,state['resources']['source'])
 def test_behavior_helper_records_only_successful_live_boundaries(self):
  binary=Path(self.temp.name)/'bin';binary.mkdir();fake=binary/'curl'
  fake.write_text('''#!/bin/sh
 mode=${FAKE_CURL_MODE:?}
 all="$*"
 case "$mode:$all" in
  edge:*'Bearer invalid'*)
   headers='';body=''
   while [ "$#" -gt 0 ]; do
    [ "$1" = --dump-header ] && { shift; headers=$1; }
    [ "$1" = --output ] && { shift; body=$1; }
    shift
   done
   printf 'HTTP/1.1 401 Unauthorized\r\nsb-error-code: UNAUTHORIZED_INVALID_JWT_FORMAT\r\n\r\n' >"$headers"
   printf '{"code":"UNAUTHORIZED_INVALID_JWT_FORMAT"}' >"$body"
   printf 401;;
  edge:*--output*/dev/null*) printf 401;;
  edge:*) printf '{"message":"Hello from Edge Functions!"}';;
  studio:*'/mcp'*) printf 403;;
  studio:*'@'*studio.headers*) printf 200;;
  studio:*) printf 401;;
  image:*render/image*)
   while [ "$#" -gt 0 ]; do [ "$1" = --output ] && { shift; printf %s iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAFgQIAHnOcQAAAAABJRU5ErkJggg== | base64 -d >"$1"; exit 0; }; shift; done; exit 8;;
  image:*object/info*) printf 400;;
  image:*'-X DELETE'*) printf '[]';;
  image:*) exit 0;;
  *) exit 9;;
 esac
 ''');fake.chmod(0o755)
  fake_jq=binary/'jq';fake_jq.write_text('''#!/bin/sh
all="$*"
case "$all" in
 *authenticated_fixture*) printf '{"authenticated_fixture":true,"unauthenticated_status":401,"invalid_token_status":401,"invalid_token_code":"UNAUTHORIZED_INVALID_JWT_FORMAT"}';;
 *basic_auth_status*) printf '{"anonymous_status":401,"service_role_status":401,"mcp_status":403,"basic_auth_status":200}';;
 *source_sha256*) printf '{"source_sha256":"a","rendered_sha256":"b","width":1,"height":1}';;
 *) cat >/dev/null;;
esac
''');fake_jq.chmod(0o755)
  evidence_dir=Path(self.temp.name)/'hakopod-supabase-native-test'/'evidence';evidence_dir.mkdir(parents=True)
  inputs={}
  for name in ('ca','anon.headers','owner.headers','service.headers','studio.headers'):
   inputs[name]=Path(self.temp.name)/name;inputs[name].write_text('safe\n')
  environment=dict(os.environ,PATH=str(binary)+os.pathsep+os.environ['PATH'],FAKE_CURL_MODE='edge',HAKOPOD_BEHAVIOR_BASE_URL='https://example.test:18443',HAKOPOD_BEHAVIOR_HOST='example.test',HAKOPOD_BEHAVIOR_PORT='18443',HAKOPOD_BEHAVIOR_CA=str(inputs['ca']),HAKOPOD_BEHAVIOR_ANON_HEADERS=str(inputs['anon.headers']),HAKOPOD_BEHAVIOR_OWNER_HEADERS=str(inputs['owner.headers']),HAKOPOD_BEHAVIOR_SERVICE_HEADERS=str(inputs['service.headers']),HAKOPOD_BEHAVIOR_EVIDENCE_DIR=str(evidence_dir))
  subprocess.run(['sh',str(HERE/'examples/supabase-native-acceptance/behavior-checks.sh'),'edge'],check=True,env=environment,capture_output=True,text=True)
  self.assertEqual(json.loads((evidence_dir/'edge-runtime-isolation.json').read_text()),{'authenticated_fixture':True,'unauthenticated_status':401,'invalid_token_status':401,'invalid_token_code':'UNAUTHORIZED_INVALID_JWT_FORMAT'})
  environment['FAKE_CURL_MODE']='studio';environment['HAKOPOD_BEHAVIOR_STUDIO_HEADERS']=str(inputs['studio.headers'])
  subprocess.run(['sh',str(HERE/'examples/supabase-native-acceptance/behavior-checks.sh'),'studio'],check=True,env=environment,capture_output=True,text=True)
  studio=json.loads((evidence_dir/'studio-admin-isolation.json').read_text());self.assertEqual((studio['anonymous_status'],studio['service_role_status'],studio['mcp_status'],studio['basic_auth_status']),(401,401,403,200))
  environment['FAKE_CURL_MODE']='image';environment['HAKOPOD_BEHAVIOR_BUCKET']='fixture'
  subprocess.run(['sh',str(HERE/'examples/supabase-native-acceptance/behavior-checks.sh'),'image'],check=True,env=environment,capture_output=True,text=True)
  image=json.loads((evidence_dir/'image-transformation.json').read_text());self.assertEqual((image['width'],image['height']),(1,1));self.assertNotEqual(image['source_sha256'],image['rendered_sha256'])

if __name__=='__main__':unittest.main()
