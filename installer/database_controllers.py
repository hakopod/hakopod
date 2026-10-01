"""Plan or apply a reviewed, packaged database controller installation.

Never runs database workloads, removes resources, or enables Cloud admission.
The reviewed plan binds the bundle, cluster identity and every existing object.
"""
import argparse
import base64
import copy
import hashlib
import json
import os
from pathlib import Path
import re
import secrets
import subprocess
import tempfile
import time

OWNER='hakopod.com/installation'
MANAGER='hakopod-database-controllers'
ENGINES=('postgresql','redis','mysql','mongodb','clickhouse','vitess')
MAX_BYTES=32*1024*1024
MAX_OBJECTS=300
CONTROLLER_RESOURCES = {
    'v1': {'Namespace', 'ServiceAccount', 'Secret', 'ConfigMap', 'Service'},
    'apps/v1': {'Deployment'},
    'rbac.authorization.k8s.io/v1': {'Role', 'RoleBinding', 'ClusterRole', 'ClusterRoleBinding'},
    'apiextensions.k8s.io/v1': {'CustomResourceDefinition'},
    'admissionregistration.k8s.io/v1': {'MutatingWebhookConfiguration', 'ValidatingWebhookConfiguration'},
    'policy/v1': {'PodDisruptionBudget'},
}


def validate_controller_objects(objects):
    if not isinstance(objects,list) or len(objects)>MAX_OBJECTS:
        raise ValueError('Controller resource count is invalid')
    for obj in objects:
        if not isinstance(obj,dict) or obj.get('kind') not in CONTROLLER_RESOURCES.get(obj.get('apiVersion'),set()):
            raise ValueError('Controller installation accepts only explicit infrastructure kinds; customer data and database workloads are forbidden')
        metadata=obj.get('metadata')
        if not isinstance(metadata,dict) or not isinstance(metadata.get('name'),str) or not metadata['name']:
            raise ValueError('Controller resource identity is invalid')
        if obj['kind']=='Secret' and ('data' in obj or 'stringData' in obj):
            raise ValueError('Controller release contains credentials')
    if len({key(o) for o in objects})!=len(objects):
        raise ValueError('Controller resource identity is duplicated')


def digest(value):
    return hashlib.sha256(value).hexdigest()


def read(kube,*args):
    result=subprocess.run([*kube,*args,'-o','json'],capture_output=True,timeout=30)
    if result.returncode:raise ValueError('Kubernetes read failed; no resource content is logged')
    if len(result.stdout)>MAX_BYTES:raise ValueError('Kubernetes response exceeded its bound')
    return json.loads(result.stdout) if result.stdout.strip() else {}


def key(obj):
    meta=obj['metadata']
    return obj['apiVersion'],obj['kind'],meta.get('namespace',''),meta['name']


def current(kube,obj):
    args=['get',obj['kind'],obj['metadata']['name'],'--ignore-not-found']
    if obj['metadata'].get('namespace'):args+=['-n',obj['metadata']['namespace']]
    # A previously absent CRD makes lookup of its custom resource impossible.
    # Bundles contain controller infrastructure only, never database CRs.
    return read(kube,*args)


def fingerprint(obj,installation):
    if not obj:return None
    meta=obj['metadata']
    if meta.get('labels',{}).get(OWNER)!=installation:
        raise ValueError('Refusing to adopt an unowned controller resource: '+obj['kind']+'/'+meta['name'])
    return {'uid':meta['uid'],'resource_version':meta['resourceVersion']}


def bundle_file(path,limit):
    if path.is_symlink() or not path.is_file() or path.stat().st_size>limit:
        raise ValueError('Controller bundle file is missing, symbolic or oversized')
    with path.open('rb') as source:raw=source.read(limit+1)
    if len(raw)>limit:raise ValueError('Controller bundle file exceeded its bound')
    return raw


def load_bundle(bundle,engines):
    raw=bundle_file(bundle/'manifest.json',256*1024)
    manifest=json.loads(raw)
    if not isinstance(manifest,dict) or type(manifest.get('schema_version')) is not int or manifest['schema_version']!=1 or not isinstance(manifest.get('files'),dict):
        raise ValueError('Controller manifest requires schema 1 and a file checksum inventory')
    if not engines or len(set(engines))!=len(engines) or any(e not in ENGINES for e in engines):
        raise ValueError('Select supported, distinct database controllers')
    objects=[]
    for engine in sorted(engines):
        path=bundle/(engine+'.json')
        if path.name not in manifest.get('files',{}):raise ValueError('This installer bundle does not include '+engine+'; use the matching qualified release kit')
        data=bundle_file(path,MAX_BYTES)
        if digest(data)!=manifest['files'].get(path.name):raise ValueError('Controller bundle checksum mismatch')
        payload=json.loads(data)
        if not isinstance(payload,dict) or payload.get('apiVersion')!='v1' or payload.get('kind')!='List' or not isinstance(payload.get('items'),list):raise ValueError('Controller payload must contain a Kubernetes resource List')
        items=payload['items']
        if engine=='vitess':
            from vitess_controller import validate_objects
            validate_objects(items)
        objects.extend(items)
    validate_controller_objects(objects)
    return digest(raw),objects


def make_plan(bundle,engines,installation,kube):
    if not re.fullmatch(r'[a-zA-Z0-9-]{1,64}',installation):raise ValueError('Invalid installation identity')
    sha,objects=load_bundle(bundle,engines)
    cluster=read(kube,'get','namespace','kube-system')['metadata']['uid']
    nodes=read(kube,'get','nodes')['items']
    if len(nodes)>256:raise ValueError('Node list exceeds installation bound')
    if any(e in engines for e in ('mysql','mongodb','vitess')) and not any(n['metadata'].get('labels',{}).get('kubernetes.io/arch')=='amd64' and not n.get('spec',{}).get('unschedulable') and any(c.get('type')=='Ready' and c.get('status')=='True' for c in n.get('status',{}).get('conditions',[])) for n in nodes):
        raise ValueError('MySQL, MongoDB and Vitess require a ready schedulable AMD64 node')
    resources=[]
    for obj in objects:
        item={'identity':list(key(obj)),'existing':fingerprint(current(kube,obj),installation)}
        if obj['kind']=='Deployment':
            pod=obj['spec']['template']['spec']
            item['replicas']=obj['spec'].get('replicas',1)
            item['containers']=[{'name':c['name'],'image':c['image'],'resources':c.get('resources',{})} for c in pod.get('containers',[])+pod.get('initContainers',[])]
            item['node_selector']=pod.get('nodeSelector',{})
        resources.append(item)
    return {'schema_version':1,'installation':installation,'cluster_uid':cluster,'bundle_sha256':sha,'engines':sorted(engines),'created_at':int(time.time()),'resources':resources,'scope':'Controller infrastructure only. No workload, volume, runtime restart or Cloud admission change. Review aggregate controller requests and limits before applying.'}


def apply_plan(bundle,plan,kube):
    if plan.get('schema_version')!=1 or not isinstance(plan.get('created_at'),int) or not 0<=time.time()-plan['created_at']<=1800:
        raise ValueError('Controller plan is invalid or expired; review a fresh plan')
    fresh=make_plan(bundle,plan['engines'],plan['installation'],kube)
    for field in ('installation','cluster_uid','bundle_sha256','engines','resources'):
        if fresh[field]!=plan.get(field):raise ValueError('Controller plan changed; review again before applying')
    _,objects=load_bundle(bundle,plan['engines'])
    reviewed={tuple(r['identity']):r['existing'] for r in plan['resources']}
    priority={'Namespace':0,'CustomResourceDefinition':1,'ServiceAccount':2,'ClusterRole':3,'Role':3,'ClusterRoleBinding':4,'RoleBinding':4,'Secret':5,'ConfigMap':5,'Service':6,'Deployment':7}
    for original in sorted(objects,key=lambda o:priority.get(o['kind'],6)):
        obj=copy.deepcopy(original);meta=obj['metadata']
        old=current(kube,obj)
        if fingerprint(old,plan['installation'])!=reviewed[key(obj)]:raise ValueError('Resource changed after review; no further objects will be applied')
        meta.setdefault('labels',{}).update({OWNER:plan['installation'],'app.kubernetes.io/managed-by':'hakopod'})
        if obj['kind']=='Secret':
            if old:continue
            if meta.get('annotations',{}).get('hakopod.io/generate-controller-credential')!='clickhouse-v1':raise ValueError('Unknown controller credential template')
            obj['data']={'username':base64.b64encode(b'clickhouse_operator').decode(),'password':base64.b64encode(secrets.token_hex(32).encode()).decode()}
        command=['create','--field-manager='+MANAGER]
        if old:
            meta['uid']=old['metadata']['uid'];meta['resourceVersion']=old['metadata']['resourceVersion']
            command=['apply','--server-side','--field-manager='+MANAGER]
        # Never force field ownership or print an API response that could echo a Secret.
        result=subprocess.run([*kube,*command,'-f','-'],input=json.dumps(obj).encode(),capture_output=True,timeout=90)
        if result.returncode:raise ValueError('Controller resource apply failed; preserve installed resources and prepare a fresh review')
        if obj['kind']=='CustomResourceDefinition':
            result=subprocess.run([*kube,'wait','--for=condition=Established','crd/'+meta['name'],'--timeout=90s'],capture_output=True,timeout=100)
            if result.returncode:raise ValueError('Controller CRD did not become established')
    for obj in objects:
        if obj['kind']=='Deployment':
            result=subprocess.run([*kube,'-n',obj['metadata']['namespace'],'rollout','status','deployment/'+obj['metadata']['name'],'--timeout=240s'],capture_output=True,timeout=250)
            if result.returncode:raise ValueError('Controller rollout is incomplete; Cloud admission stays unchanged')
    return {'bundle_sha256':plan['bundle_sha256'],'engines':plan['engines'],'cluster_uid':plan['cluster_uid']}


def write_plan(path,plan):
    with path.open('x') as output:
        os.fchmod(output.fileno(),0o600);output.write(json.dumps(plan,indent=2)+'\n')


def main():
    parser=argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--bundle',type=Path,required=True)
    parser.add_argument('--kubeconfig',required=True)
    parser.add_argument('--context',required=True)
    parser.add_argument('--installation-id',required=True)
    parser.add_argument('--engines',nargs='+',choices=ENGINES,required=True)
    mode=parser.add_mutually_exclusive_group(required=True)
    mode.add_argument('--plan',type=Path);mode.add_argument('--apply-reviewed-plan',type=Path)
    args=parser.parse_args()
    kube=['kubectl','--kubeconfig',args.kubeconfig,'--context',args.context,'--request-timeout=20s']
    if args.plan:write_plan(args.plan,make_plan(args.bundle,args.engines,args.installation_id,kube))
    else:
        plan=json.loads(args.apply_reviewed_plan.read_text())
        if plan['installation']!=args.installation_id or plan['engines']!=sorted(args.engines):raise ValueError('Reviewed selection differs')
        apply_plan(args.bundle,plan,kube)


if __name__=='__main__':main()
