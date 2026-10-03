#!/usr/bin/env python3
"""Verify the exact Supabase release runtime and its native evidence."""
import argparse, hashlib, json, math, os, re, selectors, shutil, signal, subprocess, tempfile, time
from datetime import datetime, timezone
from pathlib import Path

ROOT=Path(__file__).resolve().parent.parent
DIGEST=re.compile(r'[0-9a-f]{64}'); RUN_ID=re.compile(r'[0-9a-f]{32}')
IMAGE=re.compile(r'[a-z0-9]+(?:[._-][a-z0-9]+)*(?:[.:][a-z0-9]+(?:[._-][a-z0-9]+)*)*(?:/[a-z0-9]+(?:[._-][a-z0-9]+)*)+@sha256:[0-9a-f]{64}')
MAX_SOURCE_FILES=4096; MAX_SOURCE_BYTES=128*1024*1024; MAX_REPORT_BYTES=4*1024*1024
MAX_DOCKER_STDOUT=2*1024*1024; MAX_DOCKER_STDERR=64*1024; DOCKER_TIMEOUT=600
COMPONENTS={'api-gateway','auth','database','edge-runtime','image-proxy','pooler','postgres-meta','realtime','rest','storage','studio'}
REQUIRED_CASES={'create-owned-resources','component-image-and-identity','gateway-tls','database-tls-hostname','database-plaintext-refusal','jwt-role-isolation','row-level-security','auth-redirect-and-signup','realtime-authorization','storage-roundtrip','image-transformation','edge-runtime-isolation','studio-admin-isolation','postgres-meta-public-management','pooler-connection-bounds','pooler-admin-jwt-isolation','network-policy-isolation','restart-persistence','unsafe-rotation-refusal','database-credential-and-jwt-rotation','database-credential-rotation-retry','backup-separate-resource-restore','cancellation-recovery','ownership-fencing','owned-cleanup'}
REPORT_FIELDS={'schema_version','producer','run_id','context','execution','platform','started_at','finished_at','elapsed_seconds','environment','passed','exit_code','limit_error','source_files','source_files_after','images','identities','embedded_assets','event_file_sha256','log_sha256','test_events','failed_cases','storage_qualification','public_endpoint_qualified','physical_zones_qualified'}
SOURCE_DIRECTORIES=('internal','auth','templates','cmd/hakopod-server','examples/supabase-native-acceptance'); SOURCE_FILES=('go.mod','go.sum','examples/owned-pod-stream.py')
PRODUCER={'name':'hakopod-supabase-native-acceptance','schema_version':1,'runner_path':'examples/supabase-native-acceptance/run.sh','producer_path':'examples/supabase-native-acceptance/evidence.py'}

def reject_symlink_ancestors(path):
    for item in (Path(path).absolute(),*Path(path).absolute().parents):
        if (item.exists() or item.is_symlink()) and item.is_symlink(): raise ValueError('Symbolic paths are not permitted for Supabase qualification evidence')

def file_hash(path,limit=64*1024*1024):
    path=Path(path); reject_symlink_ancestors(path)
    if not path.is_file() or path.stat().st_size>limit: raise ValueError('Missing or oversized Supabase qualification artifact: '+path.name)
    digest=hashlib.sha256()
    with path.open('rb') as source:
        for block in iter(lambda:source.read(1024*1024),b''): digest.update(block)
    return digest.hexdigest()

def reject_duplicate_pairs(pairs):
    result={}
    for key,value in pairs:
        if key in result: raise ValueError('Duplicate JSON key in Supabase qualification evidence: '+str(key))
        result[key]=value
    return result

def read_json(path,limit=MAX_REPORT_BYTES):
    file_hash(path,limit)
    try: value=json.loads(Path(path).read_text(),object_pairs_hook=reject_duplicate_pairs,parse_constant=lambda value: (_ for _ in ()).throw(ValueError('Non-finite JSON number: '+value)))
    except (json.JSONDecodeError,UnicodeDecodeError) as error: raise ValueError('Supabase qualification JSON is malformed') from error
    if not isinstance(value,dict): raise ValueError('Supabase qualification JSON must be an object')
    return value

def source_files(root):
    root=Path(root); reject_symlink_ancestors(root)
    if not root.is_dir(): raise ValueError('Missing Supabase source root')
    paths=[root/name for name in SOURCE_FILES]; entries=0
    for name in SOURCE_DIRECTORIES:
        directory=root/name; reject_symlink_ancestors(directory)
        if not directory.is_dir(): raise ValueError('Missing Supabase source directory: '+name)
        pending=[directory]
        while pending:
            with os.scandir(pending.pop()) as children:
                for child in children:
                    if child.name in ('.git','.DS_Store','__pycache__') or child.name.endswith(('.pyc','.pyo')): continue
                    entries+=1
                    if entries>MAX_SOURCE_FILES: raise ValueError('Supabase source traversal exceeded its file limit')
                    if child.is_symlink(): raise ValueError('Symbolic Supabase qualification source is not permitted: '+child.name)
                    if child.is_dir(follow_symlinks=False): pending.append(Path(child.path))
                    elif child.is_file(follow_symlinks=False): paths.append(Path(child.path))
    result={}; total=0
    for path in sorted(set(paths)):
        digest=file_hash(path); total+=path.stat().st_size
        if total>MAX_SOURCE_BYTES: raise ValueError('Supabase source inventory exceeded its byte limit')
        result[path.relative_to(root).as_posix()]=digest
    return result

def embedded_assets(root): return {n:d for n,d in source_files(root).items() if n.startswith('internal/managedplatform/supabase-assets/')}

def validate_images(images):
    if not isinstance(images,dict) or set(images)!=COMPONENTS: raise ValueError('Supabase qualification requires the complete component image inventory')
    if any(not isinstance(v,str) or not IMAGE.fullmatch(v) or ('.' not in v.split('/',1)[0] and ':' not in v.split('/',1)[0] and v.split('/',1)[0]!='localhost') for v in images.values()): raise ValueError('Supabase qualification images must use fully qualified immutable repository digests')
    return images

def release_runtime_qualified(root,images):
    path=Path(root)/'internal/managedplatform/supabase_qualification.go'
    file_hash(path,1024*1024)
    source=path.read_text()
    gates=re.findall(r'^func SupabaseReleaseQualified\(\) bool \{ return (true|false) \}$',source,re.MULTILINE)
    releases=re.findall(r'^const SupabaseReleaseQualificationID = "([^"]+)"$',source,re.MULTILINE)
    inventories=re.findall(r'^const SupabaseReleaseImageInventorySHA256 = "([a-f0-9]{64})"$',source,re.MULTILINE)
    tables=re.findall(r'^var supabaseReleaseImages = map\[string\]string\{\n(.*?)^\}',source,re.MULTILINE|re.DOTALL)
    if len(gates)!=1 or releases!=['supabase-0.8.2-linux-amd64'] or len(inventories)!=1 or len(tables)!=1:
        raise ValueError('Supabase compiled release contract is missing or malformed')
    compiled={}
    for line in tables[0].splitlines():
        match=re.fullmatch(r'\s*"([a-z-]+)":\s*"([^"]+)",',line)
        if not match or match[1] in compiled: raise ValueError('Supabase compiled image inventory is malformed')
        compiled[match[1]]=match[2]
    validate_images(compiled)
    digest=hashlib.sha256('\n'.join(name+'='+compiled[name] for name in sorted(COMPONENTS)).encode()).hexdigest()
    if compiled!=images or digest!=inventories[0]:
        raise ValueError('Supabase native images differ from the compiled release inventory')
    return gates[0]=='true'

def capabilities(root,images):
    # Runtime qualification and a target deployment's approval are separate.
    # The native run cannot approve another cluster's disks, routing or zones.
    return {'development_evidence_recorded':True,'release_runtime_qualified':release_runtime_qualified(root,images),'development_cluster_qualified':False,'cluster_qualified':False,'encrypted_storage_class_qualified':False,'public_endpoint_qualified':False,'physical_zones_qualified':False}

def validate_identities(identities,images):
    fields={'uid','gid','image','config_user','process_observation_sha256'}
    if not isinstance(identities,dict) or set(identities)!=COMPONENTS: raise ValueError('Supabase qualification requires the complete identity inventory')
    for component,item in identities.items():
        if not isinstance(item,dict) or set(item)!=fields or type(item['uid']) is not int or type(item['gid']) is not int or item['uid']<1 or item['gid']<1 or item['image']!=images[component] or not isinstance(item['config_user'],str) or not item['config_user'] or not isinstance(item['process_observation_sha256'],str) or not DIGEST.fullmatch(item['process_observation_sha256']): raise ValueError('Supabase component identity is malformed or belongs to another image')
    return identities

def parse_timestamp(value):
    if not isinstance(value,str) or not value.endswith('Z'): raise ValueError('Supabase evidence timestamps must be UTC RFC3339')
    try: parsed=datetime.fromisoformat(value[:-1]+'+00:00')
    except ValueError as error: raise ValueError('Supabase evidence timestamps must be UTC RFC3339') from error
    if parsed.tzinfo!=timezone.utc: raise ValueError('Supabase evidence timestamps must be UTC RFC3339')
    return parsed

def validate_producer(value,runner_hash,producer_hash):
    expected={**PRODUCER,'runner_sha256':runner_hash,'producer_sha256':producer_hash}
    if not isinstance(value,dict) or type(value.get('schema_version')) is not int or value!=expected: raise ValueError('Supabase evidence was not emitted by the committed producer')

def validate_environment(value):
    if not isinstance(value,dict) or set(value)!={'context','cluster_uid','node_names','source','recovery_target'}: raise ValueError('Supabase execution environment is malformed')
    nodes=value.get('node_names')
    if value.get('context')!='k3d-hakopod-dev' or not isinstance(value.get('cluster_uid'),str) or not value['cluster_uid'] or not isinstance(nodes,list) or not nodes or len(nodes)>32 or nodes!=sorted(set(nodes)) or any(not isinstance(n,str) or not n or len(n)>128 for n in nodes): raise ValueError('Supabase execution cluster identity is malformed')
    fields={'platform_id','namespace','namespace_uid','create_operation_id'}
    for name in ('source','recovery_target'):
        item=value.get(name)
        if not isinstance(item,dict) or set(item)!=fields or any(not isinstance(item[k],str) or not item[k] for k in fields) or item['namespace']!='managed-platform-'+item['platform_id']: raise ValueError('Supabase resource environment is not bound to its platform')
    if value['source']['platform_id']==value['recovery_target']['platform_id']: raise ValueError('Supabase recovery target must be a separate resource')
    return value

def validate_events(events,run_id,elapsed):
    if not isinstance(events,list) or len(events)!=len(REQUIRED_CASES): raise ValueError('Supabase native acceptance requires the exact bounded case set')
    seen=set()
    for index,event in enumerate(events,1):
        case=event.get('case') if isinstance(event,dict) else None; event_elapsed=event.get('elapsed_seconds') if isinstance(event,dict) else None
        if not isinstance(event,dict) or set(event)!={'sequence','case','status','run_id','evidence_sha256','elapsed_seconds'} or type(event.get('sequence')) is not int or event['sequence']!=index or case not in REQUIRED_CASES or case in seen or event.get('status')!='passed' or event.get('run_id')!=run_id or not isinstance(event.get('evidence_sha256'),str) or not DIGEST.fullmatch(event['evidence_sha256']) or isinstance(event_elapsed,bool) or not isinstance(event_elapsed,(int,float)) or not math.isfinite(event_elapsed) or event_elapsed<0 or event_elapsed>elapsed: raise ValueError('Supabase native acceptance contains invalid or unbound case evidence')
        seen.add(case)
    if seen!=REQUIRED_CASES: raise ValueError('Supabase native acceptance is missing required cases')

def validate_cleanup(receipt,report):
    if not isinstance(receipt,dict) or set(receipt)!={'schema_version','run_id','context','status','source','recovery_target','errors'} or type(receipt.get('schema_version')) is not int or receipt['schema_version']!=2 or receipt.get('run_id')!=report['run_id'] or receipt.get('context')!=report['context'] or receipt.get('status')!='verified' or receipt.get('errors')!=[]: raise ValueError('Supabase cleanup receipt is missing, unsuccessful or belongs to another run')
    fields={'platform_id','namespace','namespace_uid','create_operation_id','delete_operation_id','verified_absent','persistent_volumes_absent'}
    for name in ('source','recovery_target'):
        item=receipt.get(name); environment=report['environment'][name]
        if not isinstance(item,dict) or set(item)!=fields or {k:item[k] for k in environment}!=environment or not isinstance(item['delete_operation_id'],str) or not item['delete_operation_id'] or item['verified_absent'] is not True or item['persistent_volumes_absent'] is not True: raise ValueError('Supabase owned cleanup is incomplete or not bound to this run')

def validate_acceptance(report,cleanup,sources,images,identities,runner_hash,producer_hash,assets):
    if not isinstance(report,dict) or set(report)!=REPORT_FIELDS or type(report.get('schema_version')) is not int or report['schema_version']!=2 or report.get('context')!='k3d-hakopod-dev' or report.get('execution')!='native' or report.get('platform')!='linux/amd64': raise ValueError('Supabase native report is missing or malformed; legacy receipts are unsupported')
    validate_producer(report['producer'],runner_hash,producer_hash)
    run_id=report.get('run_id'); elapsed=report.get('elapsed_seconds')
    if not isinstance(run_id,str) or not RUN_ID.fullmatch(run_id): raise ValueError('Supabase run identity is malformed')
    if isinstance(elapsed,bool) or not isinstance(elapsed,(int,float)) or not math.isfinite(elapsed) or elapsed<=0 or elapsed>14400: raise ValueError('Supabase elapsed time is malformed or unbounded')
    measured=(parse_timestamp(report.get('finished_at'))-parse_timestamp(report.get('started_at'))).total_seconds()
    if measured<=0 or abs(measured-elapsed)>5: raise ValueError('Supabase elapsed time does not match its timestamps')
    environment=validate_environment(report.get('environment'))
    if environment['context']!=report['context']: raise ValueError('Supabase report context changed during execution')
    if report.get('passed') is not True or type(report.get('exit_code')) is not int or report['exit_code']!=0 or report.get('limit_error')!='' or report.get('failed_cases')!=[]: raise ValueError('A failed, skipped or bounded-out Supabase run cannot qualify')
    if report.get('source_files')!=sources or report.get('source_files_after')!=sources: raise ValueError('Supabase evidence does not match the final source')
    if report.get('images')!=images or report.get('identities')!=identities: raise ValueError('Supabase image or identity inventory changed during acceptance')
    if report.get('embedded_assets')!=assets: raise ValueError('Supabase embedded assets changed during acceptance')
    if any(not isinstance(report.get(k),str) or not DIGEST.fullmatch(report[k]) for k in ('event_file_sha256','log_sha256')): raise ValueError('Supabase evidence hashes are malformed')
    storage=report.get('storage_qualification')
    if not isinstance(storage,dict) or set(storage)!={'storage_class','provisioner','encrypted_storage_class_qualified'} or storage.get('storage_class')!='local-path' or storage.get('provisioner')!='rancher.io/local-path' or storage.get('encrypted_storage_class_qualified') is not False: raise ValueError('Supabase development storage qualification is missing or overstated')
    if report.get('public_endpoint_qualified') is not False or report.get('physical_zones_qualified') is not False: raise ValueError('Supabase development acceptance cannot assert public endpoint or physical-zone qualification')
    validate_events(report.get('test_events'),run_id,elapsed); validate_cleanup(cleanup,report)

def validate_metadata(directory,root=ROOT):
    directory,root=Path(directory),Path(root); reject_symlink_ancestors(directory); manifest=read_json(directory/'manifest.json')
    keys={'schema_version','platform','run_id','environment','source_files','images','identities','embedded_assets','tooling','evidence','files','capability'}
    if set(manifest)!=keys or type(manifest.get('schema_version')) is not int or manifest['schema_version']!=2 or manifest.get('platform')!='linux/amd64': raise ValueError('Supabase qualification manifest is malformed')
    sources=source_files(root)
    if manifest.get('source_files')!=sources: raise ValueError('Supabase source changed after native qualification')
    images=validate_images(manifest.get('images')); identities=validate_identities(manifest.get('identities'),images); assets=embedded_assets(root)
    if not assets or manifest.get('embedded_assets')!=assets: raise ValueError('Supabase embedded asset inventory changed')
    tooling={'recorder_sha256':file_hash(root/'release/record-supabase-qualification.py'),'verifier_sha256':file_hash(root/'release/verify-supabase-runtime.py'),'runner_sha256':file_hash(root/PRODUCER['runner_path']),'producer_sha256':file_hash(root/PRODUCER['producer_path'])}
    if manifest.get('tooling')!=tooling: raise ValueError('Supabase qualification tooling changed')
    evidence=manifest.get('evidence')
    if not isinstance(evidence,dict) or set(evidence)!={'producer','event_file_sha256','log_sha256'} or not isinstance(evidence.get('producer'),dict) or type(evidence['producer'].get('schema_version')) is not int or evidence['producer']!={**PRODUCER,'runner_sha256':tooling['runner_sha256'],'producer_sha256':tooling['producer_sha256']} or any(not isinstance(evidence[k],str) or not DIGEST.fullmatch(evidence[k]) for k in ('event_file_sha256','log_sha256')): raise ValueError('Supabase qualification evidence producer changed')
    if not isinstance(manifest.get('files'),dict) or set(manifest['files'])!={'native-acceptance.json','cleanup-receipt.json'}: raise ValueError('Supabase qualification artifact inventory is incomplete')
    for name,digest in manifest['files'].items():
        if not isinstance(digest,str) or not DIGEST.fullmatch(digest) or file_hash(directory/name,MAX_REPORT_BYTES)!=digest: raise ValueError('Supabase qualification artifact checksum changed: '+name)
    capability=manifest.get('capability'); expected_capability=capabilities(root,images)
    if not isinstance(capability,dict) or set(capability)!=set(expected_capability) or any(capability[key] is not value for key,value in expected_capability.items()): raise ValueError('Supabase qualification capability boundary is invalid')
    report=read_json(directory/'native-acceptance.json'); cleanup=read_json(directory/'cleanup-receipt.json')
    if manifest['run_id']!=report.get('run_id') or manifest['environment']!=report.get('environment'): raise ValueError('Supabase manifest belongs to another run or environment')
    validate_acceptance(report,cleanup,sources,images,identities,tooling['runner_sha256'],tooling['producer_sha256'],assets)
    return manifest

def docker(args,config,stdout_limit=MAX_DOCKER_STDOUT):
    process=subprocess.Popen(['docker',*args],stdout=subprocess.PIPE,stderr=subprocess.PIPE,env=dict(os.environ,DOCKER_CONFIG=str(config)),start_new_session=True)
    selector=selectors.DefaultSelector(); selector.register(process.stdout,selectors.EVENT_READ,('stdout',stdout_limit)); selector.register(process.stderr,selectors.EVENT_READ,('stderr',MAX_DOCKER_STDERR)); buffers={'stdout':bytearray(),'stderr':bytearray()}; started=time.monotonic()
    try:
        while selector.get_map():
            if time.monotonic()-started>DOCKER_TIMEOUT: raise ValueError('Docker metadata command exceeded its time limit')
            for key,_ in selector.select(.1):
                chunk=os.read(key.fd,65536)
                if not chunk: selector.unregister(key.fileobj); continue
                name,limit=key.data
                if len(buffers[name])+len(chunk)>limit: raise ValueError('Docker metadata command exceeded its output limit')
                buffers[name].extend(chunk)
        if process.wait(timeout=2)!=0: raise ValueError('Docker metadata command failed; raw output was withheld')
        return buffers['stdout'].decode()
    except Exception:
        if process.poll() is None: os.killpg(process.pid,signal.SIGKILL); process.wait()
        raise
    finally: selector.close()

def parse_accounts(text,minimum):
    result={}
    for line in text.splitlines():
        if not line or line.startswith('#'): continue
        parts=line.split(':')
        if len(parts)<minimum: raise ValueError('Image account metadata is malformed')
        result[parts[0]]=parts
    return result

def resolve_image_identity(reference,inspect,config,runner):
    configured=inspect.get('Config',{}).get('User')
    if not isinstance(configured,str) or not configured: raise ValueError('Supabase image has no non-root Config.User')
    container=runner(['create','--network','none',reference],config).strip()
    if not re.fullmatch(r'[0-9a-f]{12,64}',container): raise ValueError('Docker returned an invalid metadata container identity')
    with tempfile.TemporaryDirectory(prefix='supabase-image-identity-') as temporary:
        passwd_path,group_path=Path(temporary)/'passwd',Path(temporary)/'group'
        try:
            runner(['cp',container+':/etc/passwd',str(passwd_path)],config,1024); runner(['cp',container+':/etc/group',str(group_path)],config,1024)
            passwd=parse_accounts(passwd_path.read_text(),4); groups=parse_accounts(group_path.read_text(),3)
        finally: runner(['rm','-f',container],config,1024)
    user,sep,group=configured.partition(':')
    if user.isdecimal():
        uid=int(user); matches=[p for p in passwd.values() if p[2]==user]; default_gid=int(matches[0][3]) if len(matches)==1 else None
    elif user in passwd: uid,default_gid=int(passwd[user][2]),int(passwd[user][3])
    else: raise ValueError('Named image Config.User is absent from /etc/passwd')
    if sep and group.isdecimal(): gid=int(group)
    elif sep and group in groups: gid=int(groups[group][2])
    elif not sep and default_gid is not None: gid=default_gid
    else: raise ValueError('Image Config.User group cannot be resolved safely')
    if uid<1 or gid<1: raise ValueError('Supabase image Config.User must resolve to a non-root identity')
    return configured,uid,gid

def verify_images(images,identities,runner=docker):
    with tempfile.TemporaryDirectory(prefix='supabase-release-verification-') as temporary:
        config=Path(temporary)/'anonymous'; config.mkdir()
        for component,reference in sorted(images.items()):
            runner(['pull','--platform','linux/amd64',reference],config)
            values=json.loads(runner(['image','inspect',reference],config),object_pairs_hook=reject_duplicate_pairs)
            repository,digest=reference.rsplit('@',1)
            if repository.rfind(':')>repository.rfind('/'): repository=repository[:repository.rfind(':')]
            if not isinstance(values,list) or len(values)!=1 or values[0].get('Os')!='linux' or values[0].get('Architecture')!='amd64' or repository+'@'+digest not in values[0].get('RepoDigests',[]): raise ValueError('Supabase image platform or pulled digest changed: '+component)
            configured,uid,gid=resolve_image_identity(reference,values[0],config,runner); expected=identities[component]
            if (configured,uid,gid)!=(expected['config_user'],expected['uid'],expected['gid']): raise ValueError('Supabase image Config.User does not match native process evidence: '+component)

def atomic_directory(output):
    output=Path(output); reject_symlink_ancestors(output)
    if output.exists(): raise ValueError('Use a fresh Supabase verification output directory')
    output.parent.mkdir(parents=True,exist_ok=True); reject_symlink_ancestors(output.parent)
    return Path(tempfile.mkdtemp(prefix='.'+output.name+'-',dir=output.parent)),output

def verify(directory,output,root=ROOT,runner=docker):
    manifest=validate_metadata(directory,root)
    if manifest['capability']['release_runtime_qualified'] is not True:
        raise ValueError('Supabase release gate is closed; candidate evidence cannot qualify a release')
    verify_images(manifest['images'],manifest['identities'],runner); temporary,output=atomic_directory(output)
    try:
        for name in ['manifest.json',*manifest['files']]: shutil.copyfile(Path(directory)/name,temporary/name)
        report={'schema_version':2,'platform':'linux/amd64','anonymous_pull_verified':True,'image_config_identities_verified':True,'native_acceptance_reused':True,'release_runtime_qualified':True,'deployment_qualified':False,'images':manifest['images']}
        (temporary/'release-verification.json').write_text(json.dumps(report,indent=2,sort_keys=True)+'\n'); temporary.rename(output)
    except Exception: shutil.rmtree(temporary,ignore_errors=True); raise

if __name__=='__main__':
    parser=argparse.ArgumentParser(description=__doc__); parser.add_argument('--qualification',type=Path,default=ROOT/'release/managed-supabase'); parser.add_argument('--output',type=Path,required=True); arguments=parser.parse_args(); verify(arguments.qualification,arguments.output)
