"""Render checksum-pinned controller sources into a network-free installer bundle.

Build-host prerequisites: Linux Python with PyYAML. Downloads checksum-pinned Helm. Never applies resources.
The Redis image must be the separately qualified credential/TLS-safe build.
"""
import argparse
import hashlib
import json
import os
import platform
import tarfile
from pathlib import Path
import re
import subprocess
import tempfile
from urllib.request import urlopen

from database_controllers import validate_controller_objects
from vitess_controller import qualification as qualify_vitess, render as render_vitess

IMAGE = re.compile(r'^[^\s]+:[^/@:]+@sha256:[0-9a-f]{64}$')
REDIS_CONTROLLER_IMAGE = 'ghcr.io/hakopod/managed-redis-operator:candidate-36996745177-1@sha256:87a426b087355e41247210d176d82812a5c8c462cc2856789513dd00a37ab32a'
HERE = Path(__file__).resolve().parent
NAMESPACES = {'postgresql': 'cnpg-system', 'redis': 'redis-operator', 'mysql': 'mysql-operator', 'mongodb': 'mongodb-system', 'clickhouse': 'clickhouse-operator'}
RELEASE_ENGINES = ('postgresql', 'redis', 'mongodb')


def fetch(url, expected, path, limit=8 * 1024 * 1024):
    with urlopen(url, timeout=30) as response:
        data = response.read(limit + 1)
    if len(data) > limit or hashlib.sha256(data).hexdigest() != expected:
        raise ValueError('Controller source checksum or size mismatch: ' + url)
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_bytes(data)
    return data


def render(engine, pin, root, redis_image, helm):
    import yaml
    ns = NAMESPACES[engine]
    if 'url' in pin:
        raw = fetch(pin['url'], pin['sha256'], root / ('source.tgz' if engine == 'redis' else 'source.yaml'))
    else:
        for name, digest in pin['files'].items():
            fetch(pin['base'] + name, digest, root / name)
    if engine == 'redis':
        if not IMAGE.fullmatch(redis_image):
            raise ValueError('Supply the qualified Redis controller tag@sha256 image')
        tagged, digest = redis_image.split('@')
        repo, tag = tagged.rsplit(':', 1)
        values = {'redisOperator': {'imageName': repo, 'imageTag': tag+'@'+digest, 'initContainerImageTag': tag+'@'+digest, 'imagePullPolicy':'IfNotPresent', 'podAnnotations': {'hakopod.io/redis-controller-source':pin['source'], 'hakopod.io/redis-tls-policy':'ca-verified-v1'}}, 'featureGates': {'AvoidCommandLinePassword':None,'GenerateConfigInInitContainer':True}, 'manager': {'config': {'maxConcurrentReconciles':1,'execCommandTimeout':'20m'}}, 'resources': {'requests':{'cpu':'100m','memory':'128Mi'},'limits':{'cpu':'500m','memory':'384Mi'}}}
        (root/'values.json').write_text(json.dumps(values))
        raw = subprocess.check_output([str(helm),'template','redis-operator',str(root/'source.tgz'),'--namespace',ns,'--include-crds','-f',str(root/'values.json')],timeout=60)
    elif engine == 'mongodb':
        raw = subprocess.check_output([str(helm),'template','mongodb-kubernetes-operator',str(root/'helm_chart'),'--namespace',ns,'-f',str(HERE/'mongodb-controller-values.json')],timeout=60)
        raw = (root/'config/crd/bases/mongodbcommunity.mongodb.com_mongodbcommunity.yaml').read_bytes()+b'\n---\n'+(root/'config/crd/bases/mongodb.com_mongodbsearch.yaml').read_bytes()+b'\n---\n'+raw
    elif engine == 'mysql':
        raw = (root/'deploy-crds.yaml').read_bytes()+b'\n---\n'+(root/'deploy-operator.yaml').read_bytes()
    objects = [o for o in yaml.safe_load_all(raw) if o]
    # Flatten Kubernetes Lists, preserving manifest order within each kind.
    objects = [item for o in objects for item in (o['items'] if o.get('kind') == 'List' else [o])]
    if not any(o['kind']=='Namespace' and o['metadata']['name']==ns for o in objects):
        objects.insert(0,{'apiVersion':'v1','kind':'Namespace','metadata':{'name':ns}})
    for obj in objects:
        meta = obj['metadata']
        if engine=='clickhouse':
            if meta.get('namespace')=='kube-system': meta['namespace']=ns
            for subject in obj.get('subjects',[]):
                if subject.get('namespace')=='kube-system': subject['namespace']=ns
            if obj['kind']=='Secret':
                obj.pop('data',None);obj.pop('stringData',None)
                meta.setdefault('annotations',{})['hakopod.io/generate-controller-credential']='clickhouse-v1'
            if obj['kind']=='ConfigMap' and 'config.yaml' in (obj.get('data') or {}):
                config=yaml.safe_load(obj['data']['config.yaml'])
                config['watch']['namespaces']['include']=['^hdb-[a-f0-9]{32}$']
                config['clickhouse']['access'].update({'scheme':'https','port':8443})
                config['reconcile']['runtime'].update({'reconcileCHIsThreadsNumber':2,'reconcileCHKsThreadsNumber':1,'reconcileShardsThreadsNumber':1})
                config['logger'].update({'v':'0','stderrthreshold':'ERROR'})
                config['security']['clickhouse']['tls'].update({'verify':'Strict','minVersion':'1.2'})
                obj['data']['config.yaml']=yaml.safe_dump(config)
        if engine=='mongodb' and obj['kind'] in ('Role','ClusterRole'):
            obj['rules']=[rule for rule in obj['rules'] if not any(group in ('mongodb.com','ai.mongodb.com') for group in rule.get('apiGroups',[]))]
            if meta['name']=='mongodb-kubernetes-operator':obj['rules'].append({'apiGroups':['mongodb.com'],'resources':['mongodbsearch'],'verbs':['get','list','watch']})
        if obj['kind']=='Deployment':
            obj['spec']['replicas']=1
            pod=obj['spec']['template']['spec']
            if engine in ('mysql','mongodb'):pod['nodeSelector']={'kubernetes.io/arch':'amd64'}
            if engine=='postgresql':
                for container in pod['containers']:
                    container['image']=pin['image']
                    container['resources']={'requests':{'cpu':'100m','memory':'256Mi'},'limits':{'cpu':'500m','memory':'512Mi'}}
            if engine=='mysql':
                container=pod['containers'][0]
                container['image']=container['image'].split('@')[0]+'@sha256:'+pin['image_sha256']
                container['resources']={'requests':{'cpu':'100m','memory':'256Mi'},'limits':{'cpu':'500m','memory':'512Mi'}}
                container['readinessProbe']['timeoutSeconds']=5
            if engine=='clickhouse':
                annotations=obj['spec']['template']['metadata'].setdefault('annotations',{})
                annotations.update({'hakopod.io/clickhouse-security':'strict-tls-v1','hakopod.io/clickhouse-watch':'database-namespaces-v1','hakopod.io/clickhouse-reconcile':'two-databases-v1'})
                container=next(c for c in pod['containers'] if c['name']=='clickhouse-operator')
                container['image']=pin['image'];container['imagePullPolicy']='IfNotPresent'
                container['resources']={'requests':{'cpu':'100m','memory':'128Mi'},'limits':{'cpu':'500m','memory':'384Mi'}}
                pod['containers']=[container]
    return objects


def validate(objects):
    validate_controller_objects(objects)
    seen=set()
    for obj in objects:
        identity=(obj['apiVersion'],obj['kind'],obj['metadata'].get('namespace',''),obj['metadata']['name'])
        if identity in seen:raise ValueError('Duplicate controller resource')
        seen.add(identity)
        if obj['kind']=='Secret' and ('data' in obj or 'stringData' in obj):raise ValueError('Credential bytes cannot enter a release bundle')
        if obj['kind']=='Deployment':
            for c in obj['spec']['template']['spec'].get('containers',[])+obj['spec']['template']['spec'].get('initContainers',[]):
                if not IMAGE.fullmatch(c['image']):raise ValueError('Unpinned controller image: '+c['image'])
                if not c.get('resources',{}).get('requests') or not c.get('resources',{}).get('limits'):raise ValueError('Unbounded controller resources')


def helm_binary(root):
    architecture = {'x86_64': 'amd64', 'aarch64': 'arm64'}.get(platform.machine())
    if platform.system() != 'Linux' or not architecture:
        raise ValueError('Build controller packages on Linux AMD64 or ARM64')
    pin = json.loads((HERE / 'pins.json').read_text())['helm'][architecture]
    source = root / 'helm.tar.gz'
    fetch(pin['url'], pin['sha256'], source, 40 * 1024 * 1024)
    with tarfile.open(source) as archive:
        member = archive.getmember('linux-' + architecture + '/helm')
        if not member.isfile() or member.size > 100 * 1024 * 1024:
            raise ValueError('Invalid pinned Helm binary')
        data = archive.extractfile(member).read(100 * 1024 * 1024 + 1)
    path = root / 'helm'; path.write_bytes(data); path.chmod(0o700)
    return path


def build(destination, redis_image, include_vitess=False):
    pins=json.loads((HERE/'database-controller-sources.json').read_text())
    if destination.exists():raise ValueError('Use a fresh controller bundle directory')
    if include_vitess:qualify_vitess(HERE.parent)
    with tempfile.TemporaryDirectory(prefix='hakopod-controller-build-') as tmp:
        files={}
        helm=helm_binary(Path(tmp))
        for engine in RELEASE_ENGINES:
            root=Path(tmp)/engine;root.mkdir()
            objects=render(engine,pins[engine],root,redis_image,helm);validate(objects)
            files[engine+'.json']=(json.dumps({'apiVersion':'v1','kind':'List','items':objects},sort_keys=True)+'\n').encode()
        if include_vitess:
            root=Path(tmp)/'vitess';root.mkdir()
            objects,pins['vitess']=render_vitess(HERE.parent,root,fetch);validate(objects)
            files['vitess.json']=(json.dumps({'apiVersion':'v1','kind':'List','items':objects},sort_keys=True)+'\n').encode()
        manifest={'schema_version':1,'sources':pins,'helm':json.loads((HERE/'pins.json').read_text())['helm'],'redis_controller_image':redis_image,'files':{name:hashlib.sha256(data).hexdigest() for name,data in files.items()}}
        destination.mkdir(parents=True)
        for name,data in files.items():(destination/name).write_bytes(data)
        (destination/'manifest.json').write_text(json.dumps(manifest,indent=2)+'\n')


if __name__=='__main__':
    parser=argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--output',type=Path,required=True)
    parser.add_argument('--redis-controller-image',default=os.environ.get('HAKOPOD_REDIS_CONTROLLER_IMAGE') or REDIS_CONTROLLER_IMAGE)
    parser.add_argument('--include-vitess',action='store_true',help='Require native qualification and include the eight Vitess resource definitions')
    args=parser.parse_args();build(args.output,args.redis_controller_image,args.include_vitess)
