#!/usr/bin/env python3
"""Check a local development image and attach actual image SBOMs to a release.

Starts only short-lived containers with no network, credentials or writable root
filesystem. Does not touch PostgreSQL, Kubernetes, external registries or tags.
Syft may read public package metadata; no image is uploaded or published.
"""
import argparse
from datetime import datetime, timezone
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import subprocess

ROOT=Path(__file__).resolve().parents[1]
ENV=dict(os.environ,GOMAXPROCS='2',GOMEMLIMIT='256MiB',SYFT_CHECK_FOR_APP_UPDATE='false')
spec=importlib.util.spec_from_file_location('release_build',ROOT/'release/build.py')
build=importlib.util.module_from_spec(spec)
spec.loader.exec_module(build)

def command(args,timeout=30,check=True):
    return subprocess.run(args,cwd=ROOT,env=ENV,text=True,capture_output=True,timeout=timeout,check=check)

def main():
    parser=argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--image',default='hakopod-api:dev')
    parser.add_argument('--version',default='0.1.0-dev')
    args=parser.parse_args()
    if not args.version or Path(args.version).name!=args.version or args.version in ('.','..'):
        raise SystemExit('version must be one safe path component')
    destination=ROOT/'.local/releases'/args.version
    if not (destination/'.hakopod-generated').is_file():raise SystemExit('Build the matching release archives first')
    provenance=json.loads((destination/'provenance.json').read_text())
    source_hash,_=build.fingerprint()
    if source_hash!=provenance['source_fingerprint_sha256']:raise SystemExit('Release archives are stale; rebuild before image verification')
    stage=ROOT/'.local/image-verification'/args.version
    stage.mkdir(parents=True,exist_ok=True)
    metadata=json.loads(command(['docker','image','inspect',args.image]).stdout)[0]
    image_id=metadata['Id']
    config=metadata['Config']
    assert config['User']=='65532:65532',config['User']
    assert config['Entrypoint']==['/hakopod-server'],config['Entrypoint']
    assert 'GOMEMLIMIT=192MiB' in config['Env'] and 'GOMAXPROCS=2' in config['Env']
    platform=metadata['Os']+'/'+metadata['Architecture']
    bounds=['--network','none','--read-only','--memory','96m','--cpus','0.5','--pids-limit','32','--cap-drop','ALL','--security-opt','no-new-privileges']
    container=None
    try:
        container=command(['docker','create','--label','app.kubernetes.io/managed-by=hakopod-release-verification',*bounds,image_id]).stdout.strip()
        for source,target in [('/hakopod-server','hakopod-server'),('/licenses/hakopod/LICENSE','LICENSE'),('/licenses/hakopod/NOTICE','NOTICE')]:
            command(['docker','cp',container+':'+source,str(stage/target)])
        result=command(['docker','start','--attach',container],check=False)
        state=json.loads(command(['docker','inspect','--format','{{json .State}}',container]).stdout)
        output=(result.stdout+result.stderr).strip()
        assert state['ExitCode']==1 and 'HAKOPOD_DATABASE_URL is required' in output,(state['ExitCode'],output)
        assert not state['OOMKilled']
        for name in ('LICENSE','NOTICE'):assert (stage/name).read_bytes()==(ROOT/name).read_bytes()
    finally:
        if container:command(['docker','rm','-f',container])
    binary=stage/'hakopod-server'
    archive_verification=json.loads((destination/'verification.json').read_text())
    archive_binary='hakopod_'+args.version+'_'+platform.replace('/','_')+'/hakopod-server'
    expected=next(item['sha256'] for item in archive_verification['binaries'] if item['path']==archive_binary)
    assert hashlib.sha256(binary.read_bytes()).hexdigest()==expected,'image binary differs from the verified release executable'
    assert (ROOT/'api/openapi.json').read_bytes() in binary.read_bytes(),'embedded OpenAPI contract differs from source'
    go_metadata=command(['go','version','-m',str(binary)]).stdout
    assert 'CGO_ENABLED=0' in go_metadata and 'go1.26.8' in go_metadata
    cli=ROOT/'.local/release-stage'/args.version/'sbom-source'/('hakopod_'+args.version+'_'+platform.replace('/','_'))/'hakopod'
    cli_version=command(['docker','run','--rm',*bounds,'--entrypoint','/release/hakopod','--mount','type=bind,src='+str(cli)+',dst=/release/hakopod,readonly',image_id,'version']).stdout.strip()
    assert cli_version==args.version
    print('Image configuration, isolated startup, embedded contract, licenses and native Linux CLI verified.',flush=True)
    go_cache=command(['go','env','GOMODCACHE']).stdout.strip()
    ENV['SYFT_GOLANG_LOCAL_MOD_CACHE_DIR']=go_cache
    print('Cataloging the actual image, including distroless operating-system packages.',flush=True)
    scan=command([str(ROOT/'.local/bin/syft'),'scan','docker:'+image_id,'--config',str(ROOT/'release/syft.yaml'),'--parallelism','2',
                  '-o','spdx-json='+str(destination/'hakopod-api.spdx.json'),
                  '-o','cyclonedx-json='+str(destination/'hakopod-api.cyclonedx.json'),
                  '-o','syft-json='+str(destination/'hakopod-api.syft.json')],timeout=300)
    build.normalize_sbom_paths(destination,stage,go_cache,prefix='hakopod-api')
    spdx=json.loads((destination/'hakopod-api.spdx.json').read_text())
    cdx=json.loads((destination/'hakopod-api.cyclonedx.json').read_text())
    syft=json.loads((destination/'hakopod-api.syft.json').read_text())
    assert spdx['spdxVersion']=='SPDX-2.3' and cdx['bomFormat']=='CycloneDX'
    types={kind:sum(p['type']==kind for p in syft['artifacts']) for kind in sorted({p['type'] for p in syft['artifacts']})}
    assert types.get('go-module',0)>10 and types.get('deb',0)>0,types
    assert source_hash==build.fingerprint()[0],'source changed during verification'
    report={'verified_at':datetime.now(timezone.utc).isoformat(),'image_reference':args.image,'image_id':image_id,'platform':platform,
            'docker_engine_version':command(['docker','version','--format','{{.Server.Version}}']).stdout.strip(),
            'image_size_bytes':metadata['Size'],'source_fingerprint_sha256':source_hash,'dockerfile_sha256':hashlib.sha256((ROOT/'Dockerfile').read_bytes()).hexdigest(),
            'binary_sha256':hashlib.sha256(binary.read_bytes()).hexdigest(),'go_version':'go1.26.8','cgo_enabled':False,
            'runtime_user':config['User'],'runtime_go_soft_memory_limit':'192MiB','runtime_go_processors':2,
            'build_go_soft_memory_limit':'256MiB','build_go_parallelism':2,
            'smoke_container_limits':{'memory_bytes':96*1024*1024,'cpus':0.5,'pids':32,'network':'none','root_filesystem':'read-only'},
            'startup_exit_code':1,'startup_message':'HAKOPOD_DATABASE_URL is required','oom_killed':False,
            'embedded_openapi_matches_source':True,'root_license_and_notice_match_source':True,'native_linux_cli_version':cli_version,
            'server_binary_matches_release_archive':True,
            'sbom':{'spdx_package_count':len(spdx['packages']),'cyclonedx_component_count':len(cdx['components']),'package_types':types},
            'limitations':['This startup smoke deliberately supplies no database, credentials or network; it does not establish full API/reconciler operation inside a container.',
                           'Image license obligations and vulnerability triage remain release gates; creating an SBOM does not establish clearance.',
                           'No image was published, uploaded or signed.']}
    (destination/'image-verification.json').write_text(json.dumps(report,indent=2)+'\n')
    (destination/'SHA256SUMS').write_text('\n'.join(hashlib.sha256(path.read_bytes()).hexdigest()+'  '+path.name for path in sorted(destination.iterdir()) if path.is_file() and not path.name.startswith('.') and path.name!='SHA256SUMS')+'\n')
    print(json.dumps({'image_id':image_id,'image_size_bytes':metadata['Size'],'platform':platform,'sbom_package_types':types,'artifacts':str(destination)},indent=2),flush=True)

if __name__=='__main__':main()
