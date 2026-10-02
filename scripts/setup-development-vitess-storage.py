"""Create only disposable S3 acceptance storage in k3d-hakopod-dev.

This is test infrastructure, not a supported storage deployment. Each generated
key can administer only its own fixture bucket. Never print keys or raw errors.
"""
import argparse
import base64
import ipaddress
import json
import os
from pathlib import Path
import secrets
import selectors
import signal
import socket
import subprocess
import sys
import time
from urllib.parse import urlsplit

import boto3
from botocore.config import Config
from botocore.exceptions import ClientError

ROOT = Path(os.environ.get('HAKOPOD_DATABASE_FIXTURE_ROOT','/srv/hakopod-backup-scratch/database-cockpit-20260929')).resolve()
STATE = ROOT / 'vitess-s3-fixture-state.json'
CONFIG = ROOT / 'vitess-native-fixture.json'
NAMESPACE = os.environ.get('HAKOPOD_DATABASE_FIXTURE_NAMESPACE','hakopod-vitess-s3-acceptance')
OWNER = os.environ.get('HAKOPOD_DATABASE_FIXTURE_OWNER','vitess-native-storage-20260930')
WEED = 'docker.io/chrislusf/seaweedfs@sha256:4e61d15fd35994cb1e43e1e553dff106794841fd9a99ade2fc8c8bfce4d7872d'
KUBE = [os.environ.get('HAKOPOD_TEST_KUBECTL',str(ROOT/'bin/kubectl')), '--kubeconfig', os.environ.get('HAKOPOD_TEST_KUBECONFIG',str(ROOT/'development-kubeconfig')), '--context', 'k3d-hakopod-dev']
NAMES = ['standalone', 'cluster', 'recovery-source', 'recovery-target', 'reseed', 'revocation']


def kube(args, value=None, timeout=30, output_limit=2*1024*1024):
    process = subprocess.Popen(KUBE+args, stdin=subprocess.PIPE if value is not None else subprocess.DEVNULL,
                               stdout=subprocess.PIPE, stderr=subprocess.PIPE, start_new_session=True)
    if value is not None:
        process.stdin.write(json.dumps(value).encode())
        process.stdin.close()
    output = bytearray()
    error = bytearray()
    deadline = time.monotonic()+timeout
    try:
        with selectors.DefaultSelector() as selected:
            selected.register(process.stdout, selectors.EVENT_READ, output)
            selected.register(process.stderr, selectors.EVENT_READ, error)
            while selected.get_map():
                if time.monotonic() >= deadline:
                    raise RuntimeError('fixture Kubernetes operation timed out')
                for key, _ in selected.select(.5):
                    block = os.read(key.fileobj.fileno(), 65536)
                    if not block:
                        selected.unregister(key.fileobj)
                        continue
                    stream = key.data
                    if len(stream)+len(block) > output_limit:
                        raise RuntimeError('fixture Kubernetes output exceeded its bound')
                    stream.extend(block)
        if process.wait(timeout=max(.1,deadline-time.monotonic())):
            raise RuntimeError('fixture Kubernetes operation failed')
        return bytes(output)
    finally:
        if process.poll() is None:
            try:
                os.killpg(process.pid, signal.SIGKILL)
            except ProcessLookupError:
                pass
            process.wait(timeout=5)
        process.stdout.close()
        process.stderr.close()


def save(path, value):
    descriptor = os.open(path, os.O_CREAT|os.O_TRUNC|os.O_WRONLY, 0o600)
    with os.fdopen(descriptor, 'w') as file:
        json.dump(value, file)
    path.chmod(0o600)


def resources(cpu, memory):
    return {'requests': {'cpu': cpu, 'memory': memory}, 'limits': {'cpu': cpu, 'memory': memory, 'ephemeral-storage': '256Mi'}}


def fresh_fixture_state(run_id):
    state = {'fixtures': {}}
    identities = []
    if run_id and (len(run_id)!=32 or any(c not in '0123456789abcdef' for c in run_id)):
        raise RuntimeError('fixture run identity is malformed')
    for name in NAMES:
        database_id = secrets.token_hex(16)
        key, secret = secrets.token_hex(16), secrets.token_hex(32)
        bucket = 'hakopod-vitess-'+(run_id[:12]+'-' if run_id else '')+name
        state['fixtures'][name] = {'database_id':database_id,'dedicated':True,
            'destination':{'id':secrets.token_hex(16),'revision':1,'project':'demo','environment':'development','name':'Vitess '+name+' development fixture','endpoint':'','region':'us-east-1','bucket':bucket,'prefix':'acceptance','path_style':True},
            'credentials':{'access_key_id':key,'secret_access_key':secret},'approved_endpoint_cidrs':[]}
        identities.append({'name':'fixture-'+name,'credentials':[{'accessKey':key,'secretKey':secret}],'actions':['Admin:'+bucket]})
    return state, identities


def storage_service(metadata):
    return {'apiVersion':'v1','kind':'Service','metadata':dict(metadata,name='s3-fixture'),'spec':{'selector':{'hakopod.io/development-fixture':OWNER,'app.kubernetes.io/component':'s3-storage'},'ports':[{'name':'s3','port':8333,'targetPort':8333}]}}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    mode = parser.add_mutually_exclusive_group(required=True)
    mode.add_argument('--provision-only', action='store_true', help='create the owned private S3 fixture before connecting its HTTPS transport')
    mode.add_argument('--https-endpoint', help='publicly trusted HTTPS transport for the existing owned S3 fixture')
    args = parser.parse_args()
    endpoint = ''
    if args.https_endpoint:
        target = urlsplit(args.https_endpoint)
        if target.scheme != 'https' or not target.hostname or target.username or target.password or target.query or target.fragment or target.path not in ('', '/') or target.port not in (None, 443):
            raise RuntimeError('fixture endpoint must be an HTTPS origin on port 443 without credentials')
        endpoint = args.https_endpoint.rstrip('/')
    current = kube(['config','current-context']).decode().strip()
    if current != 'k3d-hakopod-dev':
        raise RuntimeError('named development context is required')
    existing = kube(['get','namespace',NAMESPACE,'--ignore-not-found','-o','json'])
    if existing:
        namespace = json.loads(existing)
        if namespace['metadata'].get('deletionTimestamp'):
            raise RuntimeError('fixture namespace deletion must finish before setup')
        if not STATE.exists() or namespace['metadata'].get('labels',{}).get('hakopod.io/development-fixture') != OWNER:
            raise RuntimeError('fixture namespace already exists without matching ownership')
        state = json.loads(STATE.read_text())
        if state.get('namespace_uid') != namespace['metadata']['uid']:
            raise RuntimeError('fixture namespace was replaced')
    else:
        run_id=os.environ.get('HAKOPOD_DATABASE_FIXTURE_RUN_ID','')
        state, identities = fresh_fixture_state(run_id)
        namespace = json.loads(kube(['create','-f','-','-o','json'],{'apiVersion':'v1','kind':'Namespace','metadata':{'name':NAMESPACE,'labels':{'hakopod.io/development-fixture':OWNER,'app.kubernetes.io/managed-by':'hakopod-acceptance'}}}))
        state['namespace_uid'] = namespace['metadata']['uid']
        save(STATE,state)
        metadata = {'namespace':NAMESPACE,'labels':{'hakopod.io/development-fixture':OWNER}}
        def obj(kind,name,spec):
            return {'apiVersion':'v1','kind':kind,'metadata':dict(metadata,name=name),'spec':spec}
        quota = obj('ResourceQuota','fixture',{'hard':{'requests.cpu':'1','limits.cpu':'1','requests.memory':'1Gi','limits.memory':'1Gi','requests.storage':'3Gi','persistentvolumeclaims':'1','pods':'2' if run_id else '1'}})
        claim = obj('PersistentVolumeClaim','data',{'accessModes':['ReadWriteOnce'],'resources':{'requests':{'storage':'3Gi'}}})
        secret = {'apiVersion':'v1','kind':'Secret','metadata':dict(metadata,name='s3-credentials'),'type':'Opaque','data':{'s3.json':base64.b64encode(json.dumps({'identities':identities}).encode()).decode()}}
        security = {'allowPrivilegeEscalation':False,'capabilities':{'drop':['ALL']},'readOnlyRootFilesystem':True,'runAsNonRoot':True,'runAsUser':1000,'runAsGroup':1000}
        pod = obj('Pod','s3-fixture',{'automountServiceAccountToken':False,'restartPolicy':'Never','securityContext':{'fsGroup':1000,'seccompProfile':{'type':'RuntimeDefault'}},'nodeSelector':{'kubernetes.io/hostname':'k3d-hakopod-database-worker-0'},
            'volumes':[{'name':'data','persistentVolumeClaim':{'claimName':'data'}},{'name':'config','secret':{'secretName':'s3-credentials','defaultMode':288}},{'name':'tmp','emptyDir':{'sizeLimit':'128Mi'}}],
            'containers':[{'name':'s3','image':WEED,'command':['weed'],'args':['server','-dir=/data','-ip=127.0.0.1','-ip.bind=0.0.0.0','-volume.max=16','-master.volumeSizeLimitMB=128','-s3','-s3.port=8333','-s3.config=/etc/fixture/s3.json','-s3.autoCreateBucket=false'],'env':[{'name':'WEED_MASTER_VOLUME_GROWTH_COPY_1','value':'1'}],
                'resources':resources('750m','768Mi'),'securityContext':security,'volumeMounts':[{'name':'data','mountPath':'/data'},{'name':'config','mountPath':'/etc/fixture','readOnly':True},{'name':'tmp','mountPath':'/tmp'}],
                'readinessProbe':{'tcpSocket':{'port':8333},'periodSeconds':3,'timeoutSeconds':1,'failureThreshold':40}}]})
        pod['metadata']['labels']['app.kubernetes.io/component']='s3-storage'
        service=storage_service(metadata)
        for item in [quota,claim,secret]+([service] if run_id else [])+[pod]:
            kube(['create','-f','-'],item)
        print('Created owned development S3 storage; waiting for readiness',flush=True)
    for _ in range(90):
        pod = json.loads(kube(['get','pod','s3-fixture','-n',NAMESPACE,'-o','json']))
        if pod['metadata'].get('labels',{}).get('hakopod.io/development-fixture') != OWNER:
            raise RuntimeError('fixture pod ownership changed')
        ready = any(c['type']=='Ready' and c['status']=='True' for c in pod.get('status',{}).get('conditions',[]))
        if pod.get('status',{}).get('phase') in ('Failed','Succeeded'):
            raise RuntimeError('fixture process stopped')
        if ready:
            break
        time.sleep(2)
    else:
        raise RuntimeError('fixture S3 storage did not become ready')
    if args.provision_only:
        print('Owned private S3 fixture is ready:',NAMESPACE+'/s3-fixture:8333',flush=True)
        print('Connect an HTTPS transport, then verify it with --https-endpoint.',flush=True)
        return
    hostname = urlsplit(endpoint).hostname
    addresses = []
    for _ in range(60):
        try:
            addresses = sorted({ipaddress.ip_address(item[4][0]) for item in socket.getaddrinfo(hostname,443,type=socket.SOCK_STREAM)},key=str)
            if addresses: break
        except socket.gaierror:
            pass
        time.sleep(2)
    if not addresses or len(addresses)>16 or any(not ip.is_global for ip in addresses):
        raise RuntimeError('fixture tunnel DNS returned an unsafe address set')
    cidrs=[str(ip)+'/'+str(ip.max_prefixlen) for ip in addresses]
    clients=[]
    for entry in state['fixtures'].values():
        entry['destination']['endpoint']=endpoint
        entry['approved_endpoint_cidrs']=cidrs
        creds=entry['credentials']
        client=boto3.client('s3',endpoint_url=endpoint,region_name='us-east-1',aws_access_key_id=creds['access_key_id'],aws_secret_access_key=creds['secret_access_key'],config=Config(signature_version='s3v4',connect_timeout=5,read_timeout=15,retries={'total_max_attempts':2},s3={'addressing_style':'path'}))
        # Match the native AWS SDK's signed identity header. A proxy that
        # rewrites it cannot carry S3 requests even when unsigned probes work.
        def identity_header(request, **kwargs):
            request.headers['Accept-Encoding'] = 'identity'
        client.meta.events.register('before-sign.s3', identity_header)
        bucket=entry['destination']['bucket']
        try: client.create_bucket(Bucket=bucket)
        except ClientError as error:
            if error.response['Error']['Code'] not in ('BucketAlreadyOwnedByYou','BucketAlreadyExists'): raise
        client.head_bucket(Bucket=bucket)
        probe_prefix='acceptance/transport-probe-'+secrets.token_hex(8)+'/'
        key=probe_prefix+'probe.bin';data=b'\0Hakopod Vitess development fixture\xff'
        client.put_object(Bucket=bucket,Key=key,Body=data)
        response=client.get_object(Bucket=bucket,Key=key)
        body=response['Body'];actual=body.read(256);body.close()
        if actual!=data: raise RuntimeError('fixture HTTPS SigV4 data round trip failed')
        listing=client.list_objects_v2(Bucket=bucket,Prefix=probe_prefix,MaxKeys=2)
        if listing.get('IsTruncated') or [item['Key'] for item in listing.get('Contents',[])] != [key]: raise RuntimeError('fixture listing is outside its expected bound')
        client.delete_object(Bucket=bucket,Key=key)
        multipart=probe_prefix+'multipart-probe.bin'
        upload=client.create_multipart_upload(Bucket=bucket,Key=multipart)['UploadId']
        first=b'x'*(5<<20)
        last=b'fixture-tail'
        try:
            one=client.upload_part(Bucket=bucket,Key=multipart,UploadId=upload,PartNumber=1,Body=first)['ETag']
            two=client.upload_part(Bucket=bucket,Key=multipart,UploadId=upload,PartNumber=2,Body=last)['ETag']
            client.complete_multipart_upload(Bucket=bucket,Key=multipart,UploadId=upload,MultipartUpload={'Parts':[{'PartNumber':1,'ETag':one},{'PartNumber':2,'ETag':two}]})
            response=client.get_object(Bucket=bucket,Key=multipart,Range='bytes='+str(len(first))+'-')
            body=response['Body'];tail=body.read(64);body.close()
            if tail!=last: raise RuntimeError('fixture HTTPS multipart range verification failed')
        except Exception:
            client.abort_multipart_upload(Bucket=bucket,Key=multipart,UploadId=upload)
            raise
        finally:
            client.delete_object(Bucket=bucket,Key=multipart)
        clients.append((client,bucket))
    for index,(client,bucket) in enumerate(clients):
        other=clients[(index+1)%len(clients)][1]
        for operation in [lambda:client.head_bucket(Bucket=other),lambda:client.list_objects_v2(Bucket=other,MaxKeys=1),lambda:client.put_object(Bucket=other,Key='forbidden',Body=b'forbidden')]:
            try: operation()
            except ClientError as error:
                if error.response['ResponseMetadata']['HTTPStatusCode'] != 403 or error.response['Error']['Code'] not in ('AccessDenied','Forbidden','403'): raise RuntimeError('fixture cross-bucket refusal was not authorization')
            else: raise RuntimeError('fixture credential escaped its assigned bucket')
    save(STATE,state)
    save(CONFIG,{'fixtures':state['fixtures']})
    print('Verified',len(clients),'isolated bucket identities, HTTPS SigV4 with signed identity encoding, head/put/get/list/delete, multipart/range and cross-bucket denial',flush=True)
    print('Protected native fixture configuration:',CONFIG,flush=True)
    print('Public fixture endpoint:',endpoint,flush=True)


if __name__=='__main__':
    try: main()
    except Exception as error:
        print('Owned S3 fixture setup failed:',type(error).__name__,flush=True)
        if isinstance(error,ClientError):
            code=error.response.get('Error',{}).get('Code','')
            allowed={'AccessDenied','Forbidden','SignatureDoesNotMatch','InvalidAccessKeyId','NoSuchBucket','InvalidBucketName','NotImplemented','InternalError','AuthorizationHeaderMalformed','RequestTimeTooSkewed'}
            print('S3 operation:',error.operation_name,'status:',error.response.get('ResponseMetadata',{}).get('HTTPStatusCode'),'code:',code if code in allowed else 'unclassified',flush=True)
        sys.exit(1)
