#!/usr/bin/env python3
"""Enable optional modules on an installer-owned self-hosted cluster."""
import argparse
import fcntl
import json
import os
from pathlib import Path
import subprocess
import tempfile

from database_controllers import ENGINES

KUBE = ['/opt/hakopod/tools/kubectl', '--kubeconfig=/etc/hakopod/admin-kubeconfig', '--request-timeout=15s']

def documents(text):
    decoder=json.JSONDecoder();items=[]
    while text.strip():
        value,end=decoder.raw_decode(text.lstrip());text=text.lstrip()[end:]
        items.extend(value['items'] if value.get('kind')=='List' else [value])
    return {'apiVersion':'v1','kind':'List','items':items}

def read(*args):
    raw=subprocess.check_output([*KUBE,*args,'-o','json'],timeout=20,text=True)
    return json.loads(raw) if raw.strip() else {}

def run(*args):
    subprocess.run(args,check=True,timeout=240)

def main(module, workspace_profile=None, *, engines=None, plan_path=None, review_path=None):
    if os.geteuid()!=0:raise ValueError('Run module setup as root')
    os.umask(0o077)
    # This broader lock also covers the ordinary qualification probe, after
    # runtime installation hands off its separate local maintenance lock.
    with open("/run/lock/hakopod-install.lock","a") as lock:
        fcntl.flock(lock,fcntl.LOCK_EX | fcntl.LOCK_NB)
        options = {key: value for key, value in {'engines': engines, 'plan_path': plan_path, 'review_path': review_path}.items() if value is not None}
        return apply_module(module, workspace_profile, **options)

def apply_module(module, workspace_profile=None, *, engines=None, plan_path=None, review_path=None):
    if workspace_profile is not None and module != 'managed-actions':
        raise ValueError('Workspace profiles apply only to Managed Actions')
    if any(value is not None for value in (engines, plan_path, review_path)) and module != 'managed-databases':
        raise ValueError('Engine selection and reviewed plans apply only to Managed Databases')
    if os.geteuid()!=0:raise ValueError('Run module setup as root')
    os.umask(0o077)
    config=json.loads(Path('/etc/hakopod/config.json').read_text())
    marker=json.loads(Path('/etc/hakopod/installation.json').read_text())
    if config.get('deployment_mode','self-hosted')!='self-hosted' or not marker.get('completed'):
        raise ValueError('A completed self-hosted installer installation is required')
    root=Path(__file__).resolve().parent/'modules'
    if not root.is_dir():root=Path(__file__).resolve().parents[1]/'deploy'
    ident=marker['id']
    if module=='managed-databases':
        from database_controllers import make_plan, apply_plan, write_plan
        if bool(plan_path)==bool(review_path) or not engines:
            raise ValueError('Select engines and either --plan or --apply-reviewed-plan')
        bundle=root/'database-controllers'
        if plan_path:
            write_plan(Path(plan_path),make_plan(bundle,engines,ident,KUBE))
            print('Review the saved controller resources and allocations before applying.')
            return
        review=json.loads(Path(review_path).read_text())
        if review.get('installation')!=ident or review.get('engines')!=sorted(engines):
            raise ValueError('The reviewed controller selection differs')
        result=apply_plan(bundle,review,KUBE)
        record=Path('/etc/hakopod/database-controllers.json')
        record.write_text(json.dumps(result)+'\n');record.chmod(0o600)
        print('Selected controllers are ready. Database workloads and runtime settings were not changed.')
        return

    def owned(kind,name,namespace=''):
        args=['get',kind,name,'--ignore-not-found']
        if namespace:args+=['-n',namespace]
        item=read(*args)
        if item and item.get('metadata',{}).get('labels',{}).get('hakopod.com/installation')!=ident:
            raise ValueError('Refusing an unowned resource: '+kind+'/'+name)
    with tempfile.TemporaryDirectory(prefix='hakopod-module-') as tmp:
        if module=='managed-actions':
            from actions_runtime import install
            install(config, marker, KUBE, read, workspace_profile)
        elif module=='storage':
            for item in read('get','storageclasses')['items']:
                if item['metadata']['name']!='hakopod-local-path' and item['metadata'].get('annotations',{}).get('storageclass.kubernetes.io/is-default-class')=='true':
                    raise ValueError('Another default storage class is configured; no changes made')
            value=documents(subprocess.check_output([*KUBE,'create','--dry-run=client','-f',str(root/'storage/local-path.yaml'),'-o','json'],timeout=20,text=True))
            for item in value['items']:
                meta=item['metadata'];owned(item['kind'],meta['name'],meta.get('namespace',''))
                meta.setdefault('labels',{})['hakopod.com/installation']=ident
                if item['kind']=='ConfigMap':item['data']['config.json']=item['data']['config.json'].replace('/opt/local-path-provisioner','/var/lib/hakopod/application-volumes')
            path=Path(tmp)/'storage.json';path.write_text(json.dumps(value))
            run(*KUBE,'apply','--server-side','--field-manager=hakopod-installer','-f',str(path))
            run(*KUBE,'-n','hakopod-storage','rollout','status','deployment/local-path-provisioner','--timeout=180s')
        else:
            owned('namespace','cert-manager')
            ns=Path(tmp)/'namespace.json';ns.write_text(json.dumps({'apiVersion':'v1','kind':'Namespace','metadata':{'name':'cert-manager','labels':{'hakopod.com/installation':ident}}}))
            run(*KUBE,'apply','--server-side','--field-manager=hakopod-installer','-f',str(ns))
            env=dict(os.environ,PATH='/opt/hakopod/tools:'+os.environ['PATH'])
            context=subprocess.check_output([*KUBE,'config','current-context'],text=True,timeout=15).strip()
            subprocess.run(['bash',str(root/'cert-manager/install.sh'),'/etc/hakopod/admin-kubeconfig',context],env=env,check=True,timeout=240)
        record=Path('/etc/hakopod/modules.json')
        value=json.loads(record.read_text()) if record.exists() else {}
        value[module]=True
        record.write_text(json.dumps(value)+'\n');record.chmod(0o600)
    print(module+' module is ready. Original installer resume inputs are unchanged.')

def cli(argv=None):
    parser=argparse.ArgumentParser();parser.add_argument('module',choices=['storage','cert-manager','managed-actions','managed-databases'])
    parser.add_argument('--workspace-profile', choices=['vfs', 'shared-overlay2-v1'])
    parser.add_argument('--engines', nargs='+', choices=ENGINES)
    parser.add_argument('--plan'); parser.add_argument('--apply-reviewed-plan')
    args=parser.parse_args(argv)
    main(args.module, args.workspace_profile, engines=args.engines, plan_path=args.plan, review_path=args.apply_reviewed_plan)

if __name__=='__main__':cli()
