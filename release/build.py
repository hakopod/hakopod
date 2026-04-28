#!/usr/bin/env python3
"""Build local development release archives and real SBOMs without Docker.

No upload, tagging, signing, repository creation or publication is performed.
Go compilation and Syft are bounded to two logical processors and a 256 MiB
Go soft-memory target each; targets are built sequentially.
"""
import gzip
import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import tarfile
from datetime import datetime, timezone

ROOT=Path(__file__).resolve().parents[1]
ENV=dict(os.environ,GOMAXPROCS='2',GOMEMLIMIT='256MiB',CGO_ENABLED='0',GOWORK='off')
TARGETS=[('linux','amd64'),('linux','arm64'),('darwin','amd64'),('darwin','arm64')]

def run(args,**kwargs):
    return subprocess.run(args,cwd=kwargs.pop('cwd',ROOT),env=kwargs.pop('env',ENV),check=True,**kwargs)

def output(args):
    return subprocess.check_output(args,cwd=ROOT,env=ENV,text=True).strip()

def fingerprint():
    files=set()
    for folder in ('cmd','internal','api'):
        files.update(p for p in (ROOT/folder).rglob('*') if p.is_file())
    files.update(ROOT/p for p in ('go.mod','go.sum','web/package.json','web/pnpm-lock.yaml'))
    digest=hashlib.sha256()
    manifest={}
    for path in sorted(files):
        relative=str(path.relative_to(ROOT))
        value=hashlib.sha256(path.read_bytes()).hexdigest()
        manifest[relative]=value
        digest.update((relative+'\0'+value+'\n').encode())
    return digest.hexdigest(),manifest

def archive(source,destination,epoch):
    with destination.open('wb') as stream:
        with gzip.GzipFile(fileobj=stream,mode='wb',filename='',mtime=epoch) as compressed:
            with tarfile.open(fileobj=compressed,mode='w') as bundle:
                for path in sorted(source.rglob('*')):
                    if not path.is_file():continue
                    info=bundle.gettarinfo(str(path),arcname=str(Path(source.name)/path.relative_to(source)))
                    info.uid=info.gid=0
                    info.uname=info.gname=''
                    info.mtime=epoch
                    info.mode=0o755 if path.name in ('hakopod','hakopod-server') else 0o644
                    with path.open('rb') as file:bundle.addfile(info,file)

def normalize_sbom_paths(destination,scan,go_cache):
    """Keep catalog content intact while replacing machine-specific paths."""
    replacements=[(str(scan),'$RELEASE_STAGE'),(go_cache,'$GOPATH/pkg/mod'),(str(Path.home()),'$HOME')]
    def normalize(value):
        if isinstance(value,dict):return {key:normalize(item) for key,item in value.items()}
        if isinstance(value,list):return [normalize(item) for item in value]
        if isinstance(value,str):
            for original,replacement in replacements:value=value.replace(original,replacement)
        return value
    for filename in ('hakopod.spdx.json','hakopod.cyclonedx.json','hakopod.syft.json'):
        path=destination/filename
        path.write_text(json.dumps(normalize(json.loads(path.read_text())),separators=(',',':'))+'\n')

def main():
    run([str(ROOT/'release/install-syft.sh')])
    version=output(['go','run','./cmd/hakopod','version'])
    if not re.fullmatch(r'[A-Za-z0-9._-]+',version):raise SystemExit('CLI returned an unsafe release version')
    stage=ROOT/'.local/release-stage'/version
    destination=ROOT/'.local/releases'/version
    # Both are purpose-owned generated directories. Rebuilds replace only their
    # own previous generated files, never arbitrary caller-supplied paths.
    for directory in (stage,destination):
        if directory.exists():
            if not (directory/'.hakopod-generated').is_file():raise SystemExit(f'Refusing to replace an unowned directory: {directory}')
            shutil.rmtree(directory)
        directory.mkdir(parents=True)
        (directory/'.hakopod-generated').write_text('Hakopod release tooling\n')
    source=stage/'source'
    for attempt in range(3):
        before,manifest=fingerprint()
        if source.exists():shutil.rmtree(source)
        for relative in manifest:
            target=source/relative
            target.parent.mkdir(parents=True,exist_ok=True)
            shutil.copyfile(ROOT/relative,target)
        for filename in ('LICENSE','NOTICE'):shutil.copyfile(ROOT/filename,source/filename)
        if fingerprint()[0]==before:break
    else:raise SystemExit('Source is changing during snapshot creation; retry after the current edit completes')
    epoch=int(os.environ.get('SOURCE_DATE_EPOCH','0'))
    if epoch<0:raise SystemExit('SOURCE_DATE_EPOCH must be nonnegative')
    notices=stage/'dependency-notices'
    run(['python3','release/collect-notices.py','--source',str(source),'--output',str(notices)])
    inventory=json.loads((notices/'inventory.json').read_text())
    if any(not package['files'] for package in inventory['go']):raise SystemExit('A linked Go module is missing license/notice files; review before packaging')
    scan=stage/'sbom-source'
    scan.mkdir()
    bundles=[]
    for system,arch in TARGETS:
        name=f'hakopod_{version}_{system}_{arch}'
        directory=scan/name
        directory.mkdir()
        env=dict(ENV,GOOS=system,GOARCH=arch)
        commands=['hakopod','hakopod-server'] if system=='linux' else ['hakopod']
        for command in commands:
            print(f'Building {command} for {system}/{arch}',flush=True)
            run(['go','build','-p','2','-trimpath','-buildvcs=false','-ldflags=-s -w','-o',str(directory/command),'./cmd/'+command],env=env,cwd=source)
        for filename in ('LICENSE','NOTICE'):shutil.copyfile(source/filename,directory/filename)
        shutil.copytree(notices/'go',directory/'third-party-licenses')
        if system=='linux':
            (directory/'api').mkdir()
            shutil.copyfile(source/'api/openapi.json',directory/'api/openapi.json')
        (directory/'README.txt').write_text(
            f'Hakopod {version} — {system}/{arch}\n\n'
            'Development artifact; no production installer or support guarantee.\n'
            'Run hakopod version/help to inspect the CLI.\n'
            + ('Run the server from this directory so api/openapi.json is available.\nThe server requires an existing configured PostgreSQL database, Kubernetes\ncredentials and explicit environment configuration; see the repository docs.\n' if system=='linux' else '')
            + '\nLicense and third-party notices accompany this archive.\n')
        bundles.append(directory)
    dashboard=scan/'dashboard-dependency-lock'
    dashboard.mkdir()
    for filename in ('package.json','pnpm-lock.yaml'):shutil.copyfile(source/'web'/filename,dashboard/filename)
    after,_=fingerprint()
    syft=ROOT/'.local/bin/syft'
    go_cache=output(['go','env','GOMODCACHE'])
    sbom_env=dict(ENV,SYFT_CHECK_FOR_APP_UPDATE='false',SYFT_GOLANG_LOCAL_MOD_CACHE_DIR=go_cache)
    print('Cataloging actual Go binaries and the dashboard lockfile',flush=True)
    run([str(syft),'scan','dir:'+str(scan),'--config',str(ROOT/'release/syft.yaml'),
         '--override-default-catalogers','go-module-binary-cataloger,javascript-lock-cataloger',
         '--parallelism','2','--base-path',str(scan),'--source-name','hakopod-development-release','--source-version',version,
         '-o','spdx-json='+str(destination/'hakopod.spdx.json'),
         '-o','cyclonedx-json='+str(destination/'hakopod.cyclonedx.json'),
         '-o','syft-json='+str(destination/'hakopod.syft.json')],env=sbom_env)
    normalize_sbom_paths(destination,scan,go_cache)
    for directory in bundles:archive(directory,destination/(directory.name+'.tar.gz'),epoch)
    archive(notices,destination/f'hakopod_{version}_dependency-notices.tar.gz',epoch)
    shutil.copyfile(notices/'inventory.json',destination/'dependency-license-inventory.json')
    try:revision=output(['git','rev-parse','--verify','HEAD'])
    except subprocess.CalledProcessError:revision=None
    status=output(['git','status','--porcelain'])
    provenance={'version':version,'built_at':datetime.now(timezone.utc).isoformat(),'go_version':output(['go','version']),
                'syft_version':'1.51.1','source_revision':revision,'source_dirty':bool(status),'source_fingerprint_sha256':before,
                'source_changed_during_build':after!=before,
                'source_file_hashes':manifest,'target_platforms':[system+'/'+arch for system,arch in TARGETS],
                'cgo_enabled':False,'go_build_parallelism':2,'go_soft_memory_limit':'256MiB',
                'sbom_path_normalization':'Machine-specific staging, Go module cache and home paths replaced with $RELEASE_STAGE, $GOPATH/pkg/mod and $HOME; package identities and license data retained',
                'scope':'Go CLI/server binaries plus declared dashboard dependency lock (including development/optional packages); not a container OS or built dashboard inventory',
                'published':False}
    (destination/'provenance.json').write_text(json.dumps(provenance,indent=2)+'\n')
    hashes=[]
    for path in sorted(destination.iterdir()):
        if not path.is_file() or path.name.startswith('.') or path.name=='SHA256SUMS':continue
        hashes.append(hashlib.sha256(path.read_bytes()).hexdigest()+'  '+path.name)
    (destination/'SHA256SUMS').write_text('\n'.join(hashes)+'\n')
    spdx=json.loads((destination/'hakopod.spdx.json').read_text())
    cdx=json.loads((destination/'hakopod.cyclonedx.json').read_text())
    assert spdx['spdxVersion']=='SPDX-2.3' and len(spdx.get('packages',[]))>10
    assert cdx['bomFormat']=='CycloneDX' and len(cdx.get('components',[]))>10
    print(f'Built {len(bundles)} platform archives; SPDX packages={len(spdx["packages"])}, CycloneDX components={len(cdx["components"])}',flush=True)
    print(f'Local artifacts and checksums: {destination}',flush=True)

if __name__=='__main__':main()
