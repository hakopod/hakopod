"""Reviewed ClickHouse sandbox setup on the installer's single local K3s node.

This explicit maintenance action restarts K3s; it does not change its default
runtime, application specifications, controller configuration or admission.
"""
import argparse
from datetime import datetime, timezone
import fcntl
import hashlib
import json
import os
from pathlib import Path
import platform
import shutil
import subprocess
import tempfile
import time
import urllib.request

from actions_runtime import VERSION, DIGESTS, unpack_runtime, atomic_write
from database_controllers import read, fingerprint, write_plan

BASE=Path('/opt/hakopod/clickhouse-runtime')
TEMPLATE=Path('/var/lib/hakopod/k3s/agent/etc/containerd/config-v3.toml.tmpl')
MARKER=Path('/etc/hakopod/installation.json')
CONFIG=Path('/etc/hakopod/config.json')
KUBE=['/opt/hakopod/tools/kubectl','--kubeconfig=/etc/hakopod/admin-kubeconfig','--request-timeout=20s']
LABEL='hakopod.com.node-restriction.kubernetes.io/clickhouse-runtime'
PROFILE='systrap-no-patching-v1'
BEGIN='# BEGIN HAKOPOD CLICKHOUSE\n'
END='# END HAKOPOD CLICKHOUSE\n'


def profile(root):
    return 'binary_name = "'+str(root/'runsc')+'"\n[runsc_config]\n  platform = "systrap"\n  systrap-disable-syscall-patching = "true"\n'


def extend_template(original,root):
    fragment=BEGIN+('[plugins."io.containerd.cri.v1.runtime".containerd.runtimes.hakopod-clickhouse]\n  runtime_type = "io.containerd.runsc.v1"\n  runtime_path = "ROOT/containerd-shim-runsc-v1"\n[plugins."io.containerd.cri.v1.runtime".containerd.runtimes.hakopod-clickhouse.options]\n  TypeUrl = "io.containerd.runsc.v1.options"\n  ConfigPath = "ROOT/runsc-clickhouse.toml"\n').replace('ROOT',str(root))+END
    if BEGIN in original:
        if original.count(BEGIN)!=1 or original.count(END)!=1 or fragment not in original:raise ValueError('Existing ClickHouse runtime differs; review an explicit runtime migration')
        return original
    if 'hakopod-clickhouse' in original or END in original:raise ValueError('An unowned ClickHouse runtime exists')
    return original.rstrip()+'\n\n'+fragment


def clear_attestation(node):
    subprocess.run([*KUBE,'label','node',node,LABEL+'-'],check=True,capture_output=True,timeout=30)
    current=read(KUBE,'get','node',node)
    if LABEL in current['metadata'].get('labels',{}):
        raise ValueError('ClickHouse runtime attestation remains; stop before node maintenance')


def plan():
    if os.geteuid()!=0 or platform.system()!='Linux' or platform.machine() not in DIGESTS:raise ValueError('Run on the installer-owned Linux AMD64/ARM64 host as root')
    marker=json.loads(MARKER.read_text());config=json.loads(CONFIG.read_text())
    if not marker.get('completed') or config.get('deployment_mode','self-hosted')!='self-hosted':raise ValueError('A completed self-hosted installation is required')
    nodes=read(KUBE,'get','nodes')['items']
    if len(nodes)!=1 or nodes[0]['metadata']['name']!=config['node_name']:raise ValueError('Multi-node runtime configuration requires a separate reviewed operator rollout')
    unit=subprocess.check_output(['systemctl','show','hakopod-k3s','--property=ExecStart','--value'],text=True,timeout=15)
    if '/opt/hakopod/tools/k3s' not in unit or '/etc/hakopod/k3s.yaml' not in unit:raise ValueError('K3s service ownership changed')
    if TEMPLATE.is_symlink() or BASE.is_symlink():raise ValueError('Runtime paths cannot be symlinks')
    if BASE.exists() and (not (BASE/'owner').is_file() or (BASE/'owner').read_text()!=marker['id']):raise ValueError('Runtime directory is unowned')
    original=TEMPLATE.read_text() if TEMPLATE.exists() else '{{ template "base" . }}\n'
    extend_template(original,BASE/VERSION)
    old=read(KUBE,'get','runtimeclass','hakopod-clickhouse','--ignore-not-found')
    return {'schema_version':1,'created_at':int(time.time()),'installation':marker['id'],'node':config['node_name'],'node_uid':nodes[0]['metadata']['uid'],'cluster_uid':read(KUBE,'get','namespace','kube-system')['metadata']['uid'],'template_sha256':hashlib.sha256(original.encode()).hexdigest(),'runtime_sha256':DIGESTS[platform.machine()],'runtime_version':VERSION,'existing_runtime':fingerprint(old,marker['id']),'action':'Install dedicated ClickHouse Systrap profile, restart owned K3s, verify fresh readiness, then attest this node. Existing workloads may be interrupted; default and Actions runtimes remain unchanged.','overhead':{'cpu':'20m','memory':'50Mi'}}


def apply(review):
    current=plan()
    if not isinstance(review.get('created_at'),int) or not 0<=time.time()-review['created_at']<=1800:raise ValueError('Runtime plan expired')
    if {k:v for k,v in current.items() if k!='created_at'}!={k:v for k,v in review.items() if k!='created_at'}:raise ValueError('Runtime plan changed')
    root=BASE/VERSION
    original=TEMPLATE.read_text() if TEMPLATE.exists() else '{{ template "base" . }}\n'
    updated=extend_template(original,root)
    clear_attestation(review['node'])
    with tempfile.TemporaryDirectory(prefix='hakopod-clickhouse-runtime-') as tmp:
        tmp=Path(tmp);archive=tmp/'runtime.tar.bz2'
        with urllib.request.urlopen(f'https://github.com/google/gvisor/releases/download/{VERSION}/gvisor-{platform.machine()}.tar.bz2',timeout=60) as response,archive.open('wb') as out:
            total=0;sha=hashlib.sha256()
            while chunk:=response.read(1024*1024):
                total+=len(chunk)
                if total>180*1024*1024:raise ValueError('Runtime download exceeded bound')
                sha.update(chunk);out.write(chunk)
        if sha.hexdigest()!=review['runtime_sha256']:raise ValueError('Runtime checksum mismatch')
        stage=tmp/'stage';unpack_runtime(archive,stage)
        (stage/'runsc-clickhouse.toml').write_text(profile(root))
        BASE.mkdir(exist_ok=True,mode=0o755);BASE.chmod(0o755)
        atomic_write(BASE/'owner',review['installation'])
        if root.exists():
            # Never replace files already used by live shim processes.
            for candidate in stage.rglob('*'):
                relative=candidate.relative_to(stage);existing=root/relative
                if candidate.is_file() and (existing.is_symlink() or not existing.is_file() or existing.read_bytes()!=candidate.read_bytes()):raise ValueError('Installed sandbox bytes changed; do not overwrite a running runtime')
        else:shutil.copytree(stage,root)
        root.chmod(0o755)
        if not (BASE/'containerd-template.before-clickhouse').exists():atomic_write(BASE/'containerd-template.before-clickhouse',original,0o600)
        atomic_write(TEMPLATE,updated)
        restarted_after=datetime.now(timezone.utc)
        subprocess.run(['systemctl','restart','hakopod-k3s'],check=True,timeout=300)
        for _ in range(90):
            lease=read(KUBE,'-n','kube-node-lease','get','lease',review['node'])
            renewed=datetime.fromisoformat(lease['spec']['renewTime'].replace('Z','+00:00'))
            if renewed>restarted_after:break
            time.sleep(1)
        else:raise ValueError('Kubelet heartbeat has not renewed since restart')
        subprocess.run([*KUBE,'wait','--for=condition=Ready','node/'+review['node'],'--timeout=180s'],check=True,timeout=200)
        generated=(TEMPLATE.parent/'config.toml').read_text()
        if 'runtimes.hakopod-clickhouse' not in generated or str(root/'runsc-clickhouse.toml') not in generated:raise ValueError('Generated runtime configuration is missing the reviewed handler')
        runtime={'apiVersion':'node.k8s.io/v1','kind':'RuntimeClass','metadata':{'name':'hakopod-clickhouse','labels':{'app.kubernetes.io/managed-by':'hakopod','hakopod.com/installation':review['installation']},'annotations':{LABEL:PROFILE}},'handler':'hakopod-clickhouse','scheduling':{'nodeSelector':{LABEL:PROFILE}},'overhead':{'podFixed':{'cpu':'20m','memory':'50Mi'}}}
        old=read(KUBE,'get','runtimeclass','hakopod-clickhouse','--ignore-not-found')
        if fingerprint(old,review['installation'])!=review['existing_runtime']:raise ValueError('RuntimeClass changed during node maintenance')
        command=['create','--field-manager=hakopod-clickhouse-runtime']
        if old:
            runtime['metadata'].update({'uid':old['metadata']['uid'],'resourceVersion':old['metadata']['resourceVersion']})
            command=['apply','--server-side','--field-manager=hakopod-clickhouse-runtime']
        subprocess.run([*KUBE,*command,'-f','-'],input=json.dumps(runtime).encode(),check=True,timeout=60)
        subprocess.run([*KUBE,'label','node',review['node'],LABEL+'='+PROFILE,'--overwrite'],check=True,timeout=30)
    print('ClickHouse sandbox prepared. Enable server.clickhouse_sandbox only after native acceptance; this action did not edit engine settings.')


if __name__=='__main__':
    parser=argparse.ArgumentParser(description=__doc__)
    mode=parser.add_mutually_exclusive_group(required=True)
    mode.add_argument('--plan',type=Path);mode.add_argument('--apply-reviewed-plan',type=Path)
    args=parser.parse_args()
    with open('/run/lock/hakopod-install.lock','a') as lock:
        fcntl.flock(lock,fcntl.LOCK_EX|fcntl.LOCK_NB)
        if args.plan:write_plan(args.plan,plan())
        else:apply(json.loads(args.apply_reviewed_plan.read_text()))
