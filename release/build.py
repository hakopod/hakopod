#!/usr/bin/env python3
"""Build release archives and real SBOMs without Docker.

No upload, tagging, signing, repository creation or publication is performed.
Go compilation and Syft are bounded to two logical processors and a 256 MiB
Go soft-memory target each; targets are built sequentially.
"""
import gzip
import argparse
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
PUBLIC_BUILD_TAG='hakopod_selfhosted'

def compile_command(package, destination, *, tags='', ldflags='-s -w'):
    """Shared bounded Go build flags; callers select their own module/package."""
    command = ['go', 'build', '-p', '2', '-trimpath', '-buildvcs=false']
    if tags:
        command.append('-tags=' + tags)
    return command + ['-ldflags=' + ldflags, '-o', str(destination), package]

def build_command(command, destination, version):
    flags='-s -w'+(' -X main.version='+version if command=='hakopod' else '')
    # Public artifacts always retain their closed-signup build tag.
    return compile_command('./cmd/'+command, destination, tags=PUBLIC_BUILD_TAG, ldflags=flags)

def run(args,**kwargs):
    return subprocess.run(args,cwd=kwargs.pop('cwd',ROOT),env=kwargs.pop('env',ENV),check=True,**kwargs)

def output(args):
    return subprocess.check_output(args,cwd=ROOT,env=ENV,text=True).strip()

def fingerprint():
    files=set()
    for folder in ('cmd','internal','api','auth','templates'):
        files.update(p for p in (ROOT/folder).rglob('*') if p.is_file() and '.git' not in p.parts and '__pycache__' not in p.parts and p.suffix != '.pyc')
    files.update(ROOT/p for p in ('go.mod','go.sum','web/package.json','web/pnpm-lock.yaml','LICENSE','NOTICE'))
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

def normalize_sbom_paths(destination,scan,go_cache,prefix='hakopod'):
    """Keep catalog content intact while replacing machine-specific paths."""
    replacements=[(str(scan),'$RELEASE_STAGE'),(go_cache,'$GOPATH/pkg/mod'),(str(Path.home()),'$HOME')]
    def normalize(value):
        if isinstance(value,dict):return {key:normalize(item) for key,item in value.items()}
        if isinstance(value,list):return [normalize(item) for item in value]
        if isinstance(value,str):
            for original,replacement in replacements:value=value.replace(original,replacement)
        return value
    for suffix in ('spdx.json','cyclonedx.json','syft.json'):
        path=destination/(prefix+'.'+suffix)
        path.write_text(json.dumps(normalize(json.loads(path.read_text())),separators=(',',':'))+'\n')

def main():
    parser=argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--version',default='0.1.0-dev',help='Release tag version without v; injected into the CLI')
    args=parser.parse_args()
    version=args.version
    if not re.fullmatch(r'(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)(?:-[A-Za-z0-9]+(?:[.-][A-Za-z0-9]+)*)?',version) or len(version)>64:
        raise SystemExit('Version must be a release number without v, for example 0.1.0-alpha.2')
    source_revision=output(['git','rev-parse','--verify','HEAD'])
    source_dirty=bool(output(['git','status','--porcelain','--ignore-submodules=all']))
    run([str(ROOT/'release/install-syft.sh')])
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
            run(build_command(command, directory/command, version),env=env,cwd=source)
        for filename in ('LICENSE','NOTICE'):shutil.copyfile(source/filename,directory/filename)
        shutil.copytree(notices/'go',directory/'third-party-licenses')
        template_notices=directory/'third-party-licenses'/'hakopod-templates'
        template_notices.mkdir()
        for filename in ('LICENSE','LICENSE-Dokploy','NOTICE','THIRD_PARTY_NOTICES.md'):shutil.copyfile(source/'templates'/filename,template_notices/filename)
        if system=='linux':
            (directory/'api').mkdir()
            shutil.copyfile(source/'api/openapi.json',directory/'api/openapi.json')
        (directory/'README.txt').write_text(
            f'Hakopod {version} - {system}/{arch}\n\n'
            'See installer/README.md in the repository for host installation requirements.\n'
            'Run hakopod version/help to inspect the CLI.\n'
            + ('The OpenAPI contract is embedded in the server; api/openapi.json is also\nincluded for external tooling. The server requires an existing configured\nPostgreSQL database, Kubernetes credentials and explicit environment\nconfiguration; see the repository docs.\n' if system=='linux' else '')
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
         '--parallelism','2','--base-path',str(scan),'--source-name','hakopod-release','--source-version',version,
         '-o','spdx-json='+str(destination/'hakopod.spdx.json'),
         '-o','cyclonedx-json='+str(destination/'hakopod.cyclonedx.json'),
         '-o','syft-json='+str(destination/'hakopod.syft.json')],env=sbom_env)
    normalize_sbom_paths(destination,scan,go_cache)
    for directory in bundles:archive(directory,destination/(directory.name+'.tar.gz'),epoch)
    archive(notices,destination/f'hakopod_{version}_dependency-notices.tar.gz',epoch)
    shutil.copyfile(notices/'inventory.json',destination/'dependency-license-inventory.json')
    try:revision=subprocess.check_output(['git','rev-parse','--verify','HEAD'],cwd=ROOT,env=ENV,text=True,stderr=subprocess.DEVNULL).strip()
    except subprocess.CalledProcessError:revision=None
    # Public UI sources are restored from the tracked, checksum-verified bundle
    # in CI without submodule credentials. Their bytes have a separate fingerprint.
    status=output(['git','status','--porcelain','--ignore-submodules=all'])
    provenance={'version':version,'built_at':datetime.now(timezone.utc).isoformat(),'go_version':output(['go','version']),
                'syft_version':'1.51.1','source_revision':source_revision,'source_dirty':source_dirty or bool(status),'source_fingerprint_sha256':before,
                'source_changed_during_build':after!=before or revision!=source_revision,
                'source_file_hashes':manifest,'target_platforms':[system+'/'+arch for system,arch in TARGETS],
                'product':'self-hosted','go_build_tags':[PUBLIC_BUILD_TAG],'binary_capabilities':{'public_signup':False},
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
    run(['python3','release/verify-archives.py','--version',version])
    print(f'Built {len(bundles)} platform archives; SPDX packages={len(spdx["packages"])}, CycloneDX components={len(cdx["components"])}',flush=True)
    print(f'Local artifacts and checksums: {destination}',flush=True)

if __name__=='__main__':main()
