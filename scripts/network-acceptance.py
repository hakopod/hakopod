#!/usr/bin/env python3
"""Probe real Hakopod-generated network policy. Restricted to the local dev cluster.

Creates one disposable outsider pod to distinguish source egress from destination
ingress, and removes that namespace afterwards. Optionally replaces an API pod.
"""
import argparse
import hashlib
import json
import os
from pathlib import Path
import subprocess
import sys
import time
import uuid

ROOT = Path(__file__).resolve().parents[1]
KUBE = ['kubectl', '--kubeconfig', str(ROOT / '.local/kubeconfig')]
IMAGE = 'python:3.13.15-alpine@sha256:7415fbc3c9e4979cc717d92377ab2bc7b2b4a2af1ac03cc52b5f3f88efedaf3a'

def kubectl(*args, data=None):
    return subprocess.run(KUBE + list(args), input=data, text=True, capture_output=True, check=True, timeout=150).stdout

def obj(*args):
    return json.loads(kubectl(*args, '-o', 'json'))

def ready_pod(namespace, service):
    pods = obj('-n', namespace, 'get', 'pods', '-l', f'hakopod.io/service={service}')['items']
    for pod in pods:
        if not pod['metadata'].get('deletionTimestamp') and any(c['type'] == 'Ready' and c['status'] == 'True' for c in pod.get('status', {}).get('conditions', [])):
            return pod
    raise AssertionError(f'No ready {service} pod in {namespace}')

def probe(namespace, pod, host, port, allowed):
    code = """import socket,sys
try:
    with socket.create_connection((sys.argv[1],int(sys.argv[2])), 2): pass
    print('connected')
except OSError:
    print('blocked')
"""
    result = kubectl('-n', namespace, 'exec', pod, '--', 'python', '-c', code, host, str(port)).strip()
    expected = 'connected' if allowed else 'blocked'
    if result != expected:
        raise AssertionError(f'{namespace}/{pod} -> {host}:{port}: expected {expected}, got {result}')
    print(f'PASS {host}:{port}: {expected}', flush=True)

def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--application-id', required=True)
    parser.add_argument('--replace-pod', action='store_true', help='Replace one API pod and verify service DNS after readiness')
    parser.add_argument('--other-application-id', help='Also verify denial against a second actual Hakopod application')
    args = parser.parse_args()
    if kubectl('config', 'current-context').strip() != 'k3d-hakopod-dev':
        raise SystemExit('This test only runs in the named local development cluster.')
    namespace = 'hp-' + hashlib.sha256(args.application_id.encode()).hexdigest()[:32]
    web, api = ready_pod(namespace, 'web'), ready_pod(namespace, 'api')
    web_name = web['metadata']['name']
    api_ip = api['status']['podIP']
    service = obj('-n', namespace, 'get', 'service', 'api')
    api_service_ip = service['spec']['clusterIP']
    port = service['spec']['ports'][0]['port']
    ingresses = obj('-n', namespace, 'get', 'ingresses')['items']
    assert len(ingresses) == 1, f'Expected one public web Ingress, got {len(ingresses)}'
    assert all(path['backend']['service']['name'] != 'api' for ingress in ingresses for rule in ingress['spec']['rules'] for path in rule['http']['paths']), 'Private API was publicly routed'
    print('PASS only web has public ingress', flush=True)
    probe(namespace, web_name, 'api', port, True)
    probe(namespace, web_name, api_ip, port, True)
    kubectl('-n', namespace, 'exec', web_name, '--', 'python', '-c', "import socket; socket.gethostbyname('example.com')")
    print('PASS external DNS resolves through the permitted CoreDNS exception', flush=True)
    # An unrestricted outsider confirms destination ingress denies independently.
    outsider = 'hp-netprobe-' + uuid.uuid4().hex[:10]
    fixture = {'apiVersion':'v1','kind':'List','items':[
        {'apiVersion':'v1','kind':'Namespace','metadata':{'name':outsider,'labels':{'hakopod.io/acceptance':'network','pod-security.kubernetes.io/enforce':'restricted'}}},
        {'apiVersion':'v1','kind':'Pod','metadata':{'name':'outsider','namespace':outsider},'spec':{
            'automountServiceAccountToken':False,'restartPolicy':'Never',
            'securityContext':{'runAsNonRoot':True,'runAsUser':10001,'runAsGroup':10001,'seccompProfile':{'type':'RuntimeDefault'}},
            'containers':[{'name':'probe','image':IMAGE,'command':['python','-u','-m','http.server','8080'],
                'securityContext':{'allowPrivilegeEscalation':False,'capabilities':{'drop':['ALL']}},
                'resources':{'requests':{'cpu':'10m','memory':'16Mi'},'limits':{'cpu':'100m','memory':'64Mi'}},
                'readinessProbe':{'tcpSocket':{'port':8080},'periodSeconds':1}}]}}
    ]}
    try:
        kubectl('apply', '-f', '-', data=json.dumps(fixture))
        kubectl('-n', outsider, 'wait', '--for=condition=Ready', 'pod/outsider', '--timeout=120s')
        outsider_ip = obj('-n', outsider, 'get', 'pod', 'outsider')['status']['podIP']
        probe(outsider, 'outsider', api_service_ip, port, False)
        probe(outsider, 'outsider', api_ip, port, False)
        probe(namespace, web_name, outsider_ip, 8080, False)
        # Loopback to the outsider's own listener proves the test destination is live.
        probe(outsider, 'outsider', outsider_ip, 8080, True)
        if args.other_application_id:
            other_namespace = 'hp-' + hashlib.sha256(args.other_application_id.encode()).hexdigest()[:32]
            other_service = obj('-n', other_namespace, 'get', 'service', 'api')
            other_pod = ready_pod(other_namespace, 'api')
            probe(namespace, web_name, other_service['spec']['clusterIP'], port, False)
            probe(namespace, web_name, other_pod['status']['podIP'], port, False)
        probe(namespace, web_name, '169.254.169.254', 80, False)
        # Kubernetes NetworkPolicy permits local-node traffic on some CNIs.
        # Check this explicitly; a failure here is an actionable limitation.
        control_plane = obj('-n', 'default', 'get', 'service', 'kubernetes')['spec']['clusterIP']
        probe(namespace, web_name, control_plane, 443, False)
        if args.replace_pod:
            kubectl('-n', namespace, 'delete', 'pod', api['metadata']['name'], '--wait=false')
            deadline = time.monotonic() + 120
            while time.monotonic() < deadline:
                try:
                    replacement = ready_pod(namespace, 'api')
                    if replacement['metadata']['uid'] != api['metadata']['uid']:
                        break
                except AssertionError:
                    pass
                time.sleep(1)
            else:
                raise AssertionError('Replacement API pod did not become ready')
            probe(namespace, web_name, 'api', port, True)
            assert obj('-n', namespace, 'get', 'service', 'api')['spec']['clusterIP'] == api_service_ip
            print('PASS service name and ClusterIP survive pod replacement', flush=True)
    finally:
        kubectl('delete', 'namespace', outsider, '--wait=false', '--ignore-not-found=true')
    print('Network acceptance passed against real generated resources.', flush=True)

if __name__ == '__main__':
    try:
        main()
    except (AssertionError, subprocess.SubprocessError) as exc:
        print(f'FAIL {exc}', file=sys.stderr)
        if isinstance(exc, subprocess.CalledProcessError):
            print(exc.stderr, file=sys.stderr)
        raise SystemExit(1)
