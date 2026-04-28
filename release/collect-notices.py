#!/usr/bin/env python3
"""Preserve upstream license/notice files and write a path-sanitized inventory.

Go inventory covers modules used by cmd packages, including nested third-party
notices. Dashboard inventory covers packages installed by the frozen pnpm lock.
Neither inventory silently treats missing licenses as permissive.
"""
import argparse
from collections import Counter
import hashlib
import json
from pathlib import Path
import re
import shutil
import subprocess

ROOT = Path(__file__).resolve().parents[1]

def decode_stream(text):
    decoder = json.JSONDecoder()
    while text.strip():
        value, end = decoder.raw_decode(text.lstrip())
        yield value
        text = text.lstrip()[end:]

def slug(name):
    return re.sub(r'[^A-Za-z0-9._-]+','_',name)[:150]+'-'+hashlib.sha256(name.encode()).hexdigest()[:8]

def license_files(directory, recursive=False):
    candidates = directory.rglob('*') if recursive else directory.iterdir()
    return sorted(path for path in candidates if path.is_file() and path.name.upper().startswith(('LICENSE','COPYING','NOTICE')) and path.stat().st_size < 2*1024*1024)

def preserve(source, files, target):
    saved = []
    for path in files:
        relative = path.relative_to(source)
        destination = target/relative
        destination.parent.mkdir(parents=True,exist_ok=True)
        shutil.copyfile(path,destination)
        saved.append(str(relative))
    return saved

def identify(text):
    # A readable review aid, not an automatic legal conclusion. Preserve the
    # original files, especially modules with different licenses per file.
    found=[]
    if 'Apache License' in text or 'Apache license' in text: found.append('Apache-2.0')
    if 'Permission is hereby granted, free of charge' in text: found.append('MIT')
    if 'Redistribution and use in source and binary forms' in text:
        found.append('BSD-3-Clause' if ('Neither the name' in text or 'Neither the names' in text or 'neither the name' in text) else 'BSD-2-Clause')
    if 'ISC License' in text: found.append('ISC')
    return ' AND '.join(sorted(set(found))) or 'REVIEW_REQUIRED'

def main():
    parser=argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--output',required=True)
    parser.add_argument('--source',default=str(ROOT),help='Go source snapshot; installed dashboard packages are read from the working tree')
    args=parser.parse_args()
    out=Path(args.output).resolve()
    go_root=Path(args.source).resolve()
    out.mkdir(parents=True,exist_ok=True)
    linked=set(subprocess.check_output(['go','list','-deps','-f','{{if .Module}}{{.Module.Path}}{{end}}','./cmd/...'],cwd=go_root,text=True).splitlines())
    modules=list(decode_stream(subprocess.check_output(['go','list','-m','-json','all'],cwd=go_root,text=True)))
    inventory={'scope':'Go cmd import graph plus installed dashboard packages; build/development dependencies included','go':[],'dashboard':[]}
    for module in sorted(modules,key=lambda m:m['Path']):
        if module['Path'] not in linked or module.get('Main'): continue
        directory=Path(module['Dir']) if module.get('Dir') else None
        target=out/'go'/slug(module['Path']+'@'+module.get('Version',''))
        files=license_files(directory,recursive=True) if directory else []
        text='\n'.join(path.read_text(errors='replace') for path in files)
        saved=preserve(directory,files,target) if directory else []
        inventory['go'].append({'name':module['Path'],'version':module.get('Version'),'direct':not module.get('Indirect',False),'license_review_hint':identify(text),'license_directory':str(target.relative_to(out)),'files':saved})
    goroot=Path(subprocess.check_output(['go','env','GOROOT'],cwd=ROOT,text=True).strip())
    stdlib=out/'go'/'go-standard-library'
    stdlib.mkdir(parents=True,exist_ok=True)
    shutil.copyfile(goroot/'LICENSE',stdlib/'LICENSE')
    if (goroot/'PATENTS').exists():shutil.copyfile(goroot/'PATENTS',stdlib/'PATENTS')
    inventory['go_standard_library']={'version':subprocess.check_output(['go','env','GOVERSION'],cwd=ROOT,text=True).strip(),'license':'BSD-3-Clause','license_directory':'go/go-standard-library'}
    installed=ROOT/'web/node_modules/.pnpm'
    if not installed.exists():raise SystemExit('Run pnpm --dir web install --frozen-lockfile before collecting dashboard notices')
    seen=set()
    for metadata in sorted(installed.glob('*/node_modules/*/package.json'))+sorted(installed.glob('*/node_modules/@*/*/package.json')):
        # Resolve real package roots, deduplicating pnpm's dependency symlinks.
        if metadata.parent.is_symlink():continue
        data=json.loads(metadata.read_text())
        identity=(data.get('name'),data.get('version'))
        if identity in seen or not all(identity):continue
        seen.add(identity)
        name,version=identity
        directory=metadata.parent
        target=out/'dashboard'/slug(name+'@'+version)
        declared=data.get('license','REVIEW_REQUIRED')
        if not isinstance(declared,str):declared=json.dumps(declared,sort_keys=True)
        saved=preserve(directory,license_files(directory),target)
        inventory['dashboard'].append({'name':name,'version':version,'license_declared':declared,'license_directory':str(target.relative_to(out)),'files':saved,'repository':data.get('repository'),'homepage':data.get('homepage')})
    inventory['dashboard'].sort(key=lambda p:(p['name'],p['version']))
    inventory['counts']={'go_modules':len(inventory['go']),'installed_dashboard_packages':len(inventory['dashboard']),'go_license_hints':dict(Counter(p['license_review_hint'] for p in inventory['go'])),'dashboard_license_declarations':dict(Counter(p['license_declared'] for p in inventory['dashboard'])),'missing_notice_files':[p['name'] for p in inventory['go']+inventory['dashboard'] if not p['files']]}
    (out/'inventory.json').write_text(json.dumps(inventory,indent=2,sort_keys=True)+'\n')
    shutil.copyfile(ROOT/'web/THIRD_PARTY_NOTICES.md',out/'dashboard'/'HAKOPOD-DASHBOARD-NOTICES.md')
    print(json.dumps(inventory['counts'],indent=2))

if __name__=='__main__':main()
