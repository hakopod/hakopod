#!/usr/bin/env python3
"""Prove the installed K3s policy engine across two containerized nodes.

Creates a tainted, 1 GiB worker plus disposable policy fixtures, then removes both.
This tests CNI enforcement; network-acceptance.py separately tests Go-generated policy.
"""
import importlib.util
import json
from pathlib import Path
import subprocess
import time
import uuid

ROOT = Path(__file__).resolve().parents[1]
module_spec = importlib.util.spec_from_file_location('network', ROOT/'scripts/network-acceptance.py')
network = importlib.util.module_from_spec(module_spec)
module_spec.loader.exec_module(network)
k, obj, probe = network.kubectl, network.obj, network.probe
WORKER = 'k3d-hakopod-check-worker-0'
SERVER = 'k3d-hakopod-dev-server-0'
K3D = str(ROOT/'.local/bin/k3d')

def pod(namespace, name, node):
    return {'apiVersion':'v1','kind':'Pod','metadata':{'namespace':namespace,'name':name,'labels':{'network':'backend','service':name}},'spec':{
        'nodeSelector':{'kubernetes.io/hostname':node}, 'restartPolicy':'Never', 'automountServiceAccountToken':False,
        'tolerations':[{'key':'hakopod.io/acceptance','operator':'Equal','value':'cross-node','effect':'NoSchedule'}],
        'securityContext':{'runAsNonRoot':True,'runAsUser':10001,'runAsGroup':10001,'seccompProfile':{'type':'RuntimeDefault'}},
        'containers':[{'name':'probe','image':network.IMAGE,'command':['python','-u','-m','http.server','8080'],
            'securityContext':{'allowPrivilegeEscalation':False,'capabilities':{'drop':['ALL']}},
            'resources':{'requests':{'cpu':'10m','memory':'16Mi'},'limits':{'cpu':'100m','memory':'64Mi'}},
            'readinessProbe':{'tcpSocket':{'port':8080},'periodSeconds':1}}]}}

def main():
    if k('config','current-context').strip() != 'k3d-hakopod-dev':
        raise SystemExit('Only the isolated k3d-hakopod-dev cluster is permitted.')
    existing = subprocess.run(['docker','container','inspect',WORKER], capture_output=True)
    if existing.returncode == 0:
        labels = json.loads(existing.stdout)[0]['Config']['Labels']
        if labels.get('com.hakopod.acceptance') != 'cross-node':
            raise SystemExit('Refusing to use a worker not owned by this acceptance test.')
    else:
        subprocess.run([K3D,'node','create','hakopod-check-worker','--cluster','hakopod-dev','--role','agent','--memory','1024m','--runtime-label','com.hakopod.acceptance=cross-node','--k3s-arg','--node-taint=hakopod.io/acceptance=cross-node:NoSchedule','--wait','--timeout','180s'],check=True)
    k('taint','node',WORKER,'hakopod.io/acceptance=cross-node:NoSchedule','--overwrite')
    namespaces = ['hp-crossnode-'+uuid.uuid4().hex[:8] for _ in range(2)]
    allowed, outsider = namespaces
    report = {'passed':False,'checks':[],'nodes':[SERVER,WORKER]}
    try:
        items = [{'apiVersion':'v1','kind':'Namespace','metadata':{'name':ns,'labels':{'hakopod.io/acceptance':'cross-node','pod-security.kubernetes.io/enforce':'restricted'}}} for ns in namespaces]
        items += [
            {'apiVersion':'networking.k8s.io/v1','kind':'NetworkPolicy','metadata':{'name':'deny-default','namespace':allowed},'spec':{'podSelector':{},'policyTypes':['Ingress','Egress']}},
            {'apiVersion':'networking.k8s.io/v1','kind':'NetworkPolicy','metadata':{'name':'backend','namespace':allowed},'spec':{
                'podSelector':{'matchLabels':{'network':'backend'}},'policyTypes':['Ingress','Egress'],
                'ingress':[{'from':[{'podSelector':{'matchLabels':{'network':'backend'}}}],'ports':[{'protocol':'TCP','port':8080}]}],
                'egress':[
                    {'to':[{'podSelector':{'matchLabels':{'network':'backend'}}}],'ports':[{'protocol':'TCP','port':8080}]},
                    {'to':[{'namespaceSelector':{'matchLabels':{'kubernetes.io/metadata.name':'kube-system'}},'podSelector':{'matchLabels':{'k8s-app':'kube-dns'}}}],'ports':[{'protocol':'UDP','port':53},{'protocol':'TCP','port':53}]}
                ]}},
            {'apiVersion':'v1','kind':'Service','metadata':{'name':'api','namespace':allowed},'spec':{'selector':{'service':'server'},'ports':[{'port':8080,'targetPort':8080}]}}
        ]
        k('apply','-f','-',data=json.dumps({'apiVersion':'v1','kind':'List','items':items}))
        time.sleep(2)
        pods = [pod(allowed,'server',SERVER),pod(allowed,'client',WORKER),pod(outsider,'outsider',WORKER)]
        k('apply','-f','-',data=json.dumps({'apiVersion':'v1','kind':'List','items':pods}))
        for ns, name in [(allowed,'server'),(allowed,'client'),(outsider,'outsider')]:
            k('-n',ns,'wait','--for=condition=Ready','pod/'+name,'--timeout=120s')
        server = obj('-n',allowed,'get','pod','server')
        client = obj('-n',allowed,'get','pod','client')
        assert server['spec']['nodeName'] != client['spec']['nodeName']
        service_ip = obj('-n',allowed,'get','service','api')['spec']['clusterIP']
        pod_ip = server['status']['podIP']
        probe(allowed,'client','api',8080,True)
        probe(allowed,'client',pod_ip,8080,True)
        report['checks'].append('allowed service DNS and direct pod IP across different nodes')
        probe(outsider,'outsider',service_ip,8080,False)
        probe(outsider,'outsider',pod_ip,8080,False)
        outsider_ip = obj('-n',outsider,'get','pod','outsider')['status']['podIP']
        probe(allowed,'client',outsider_ip,8080,False)
        probe(outsider,'outsider',outsider_ip,8080,True)
        report['checks'].append('cross-node destination ingress and worker source egress denied')
        k('-n',allowed,'delete','pod','server','--wait=true')
        k('apply','-f','-',data=json.dumps(pod(allowed,'server',SERVER)))
        k('-n',allowed,'wait','--for=condition=Ready','pod/server','--timeout=120s')
        probe(allowed,'client','api',8080,True)
        assert obj('-n',allowed,'get','service','api')['spec']['clusterIP'] == service_ip
        report['checks'].append('cross-node DNS survives server pod replacement')
        report['passed'] = True
        print('PASS cross-node CNI acceptance; temporary worker will be removed.',flush=True)
    finally:
        for ns in namespaces:
            k('delete','namespace',ns,'--ignore-not-found=true','--wait=true','--timeout=120s')
        # The dedicated taint prevents ordinary application scheduling here.
        pods = obj('get','pods','-A','--field-selector','spec.nodeName='+WORKER)['items']
        if pods:
            raise RuntimeError('Temporary worker has unexpected pods; retained for diagnosis instead of deleting workloads')
        subprocess.run([K3D,'node','delete',WORKER],check=True)
        k('delete','node',WORKER,'--ignore-not-found=true')
        (ROOT/'.local/cross-node-acceptance.json').write_text(json.dumps(report,indent=2)+'\n')

if __name__ == '__main__':
    main()
