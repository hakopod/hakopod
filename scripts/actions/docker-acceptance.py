#!/usr/bin/env python3
"""Exercise Docker semantics in one sandbox; no GitHub account is registered."""
import json
import os
from pathlib import Path
import subprocess
import time

kube = ['kubectl', '--kubeconfig', str(Path('.local/kubeconfig').resolve()), '--context', 'k3d-hakopod-dev']
if os.environ.get('GITHUB_ACTIONS') != 'true':
    raise SystemExit('This fixture only runs in an isolated GitHub Actions job')
namespace = 'hakopod-actions-runtime-test'
def apply(value):
    subprocess.run(kube + ['apply', '-f', '-'], input=json.dumps(value), text=True, check=True)

def command(args, **kwargs):
    return subprocess.check_output(kube + args, text=True, **kwargs)

image = 'docker.io/library/docker:29.8.1-dind@sha256:3f3c01aaaebf7cce837356b688b7c059a4749f10bd7660dec7c58fc454a283f0'
busybox = 'docker.io/library/busybox:1.37.0@sha256:9db7b59979c38555a39def84a31fb98b5296952f9e3afd4f6f11f05b07adfab0'
script = r'''
set -eu
# Use the official dind image's legacy netfilter tools for gVisor's supported API.
export PATH="/usr/local/sbin/.iptables-legacy:$PATH"
iptables --version
# All capabilities and mounts are inside gVisor, never on the host kernel.
ip route show
iface=$(ip route show | awk '$1=="default" {for (i=1;i<NF;i++) if ($i=="dev") {print $(i+1); exit}}')
[ -n "$iface" ]
addr=$(ip -4 -o addr show dev "$iface" | awk '{for (i=1;i<NF;i++) if ($i=="inet") {split($(i+1),a,"/"); print a[1]; exit}}')
mtu=$(ip -o link show dev "$iface" | awk '{for (i=1;i<NF;i++) if ($i=="mtu") {print $(i+1); exit}}')
[ -n "$addr" ] && [ -n "$mtu" ]
iptables_cmd=iptables
if command -v iptables-legacy >/dev/null; then iptables_cmd=iptables-legacy; fi
echo 1 > /proc/sys/net/ipv4/ip_forward
"$iptables_cmd" -t nat -A POSTROUTING -o "$iface" -j SNAT --to-source "$addr" -p tcp
"$iptables_cmd" -t nat -A POSTROUTING -o "$iface" -j SNAT --to-source "$addr" -p udp
dockerd --host=unix:///var/run/docker.sock --iptables=false --ip6tables=false --mtu="$mtu" --storage-driver=vfs --feature=containerd-snapshotter=false > /tmp/daemon.log 2>&1 &
daemon_pid=$!
trap 'kill "$daemon_pid" 2>/dev/null || true' EXIT
ready=false
for i in $(seq 1 60); do
  if docker info >/dev/null 2>&1; then ready=true; break; fi
  if ! kill -0 "$daemon_pid"; then cat /tmp/daemon.log; exit 1; fi
  sleep 1
done
if [ "$ready" != true ]; then cat /tmp/daemon.log; exit 1; fi
[ ! -e /var/run/secrets/kubernetes.io/serviceaccount/token ]
docker pull "$TEST_IMAGE"
# Container action and shared workspace mount.
mkdir -p /workspace
printf 'workspace-isolated\n' > /workspace/input
test "$(docker run --rm -v /workspace:/work "$TEST_IMAGE" cat /work/input)" = workspace-isolated
# Service networking through aliases, as used by GitHub container jobs.
docker network create actions-job
docker run -d --name service --network actions-job --network-alias database -p 127.0.0.1:18081:8080 "$TEST_IMAGE" sh -c 'mkdir -p /www; echo service-ready >/www/index.html; exec httpd -f -p 8080 -h /www'
for i in $(seq 1 30); do
  if docker run --rm --network actions-job "$TEST_IMAGE" wget -qO- http://database:8080/ > /tmp/service-result; then break; fi
  sleep 1
done
failed=0
if [ "$(cat /tmp/service-result)" != service-ready ]; then
  failed=1
  docker network inspect actions-job
  docker run --rm --network actions-job "$TEST_IMAGE" sh -c 'cat /etc/resolv.conf; nslookup database || true'
  tail -60 /tmp/daemon.log
fi
# A shell job reaches a published service port from the runner network namespace.
if [ "$(wget -qO- http://127.0.0.1:18081/)" != service-ready ]; then failed=1; fi
# BuildKit must execute a Dockerfile RUN instruction inside the sandbox.
printf 'FROM %s\nRUN echo build-ready > /result\nCMD ["cat", "/result"]\n' "$TEST_IMAGE" > /workspace/Dockerfile
if docker build -t actions-build-test /workspace; then
  if [ "$(docker run --rm actions-build-test)" != build-ready ]; then failed=1; fi
else
  failed=1
fi
docker rm -f service
docker network rm actions-job
[ "$failed" = 0 ]
echo 'PASS: container execution, shared workspace, service DNS, published service ports, Docker build'
'''
apply({'apiVersion': 'node.k8s.io/v1', 'kind': 'RuntimeClass', 'metadata': {'name': 'hakopod-actions'}, 'handler': 'hakopod-actions'})
subprocess.run(kube + ['create', 'namespace', namespace], check=True)
try:
    apply({'apiVersion': 'v1', 'kind': 'Pod', 'metadata': {'name': 'docker-test', 'namespace': namespace}, 'spec': {
        'runtimeClassName': 'hakopod-actions', 'automountServiceAccountToken': False,
        'restartPolicy': 'Never', 'activeDeadlineSeconds': 900, 'terminationGracePeriodSeconds': 15,
        'containers': [{'name': 'test', 'image': image, 'command': ['sh', '-c', script],
            'env': [{'name': 'TEST_IMAGE', 'value': busybox}],
            'resources': {'requests': {'cpu': '500m', 'memory': '1Gi'}, 'limits': {'cpu': '2', 'memory': '4Gi', 'ephemeral-storage': '6Gi'}},
            'securityContext': {'privileged': False, 'runAsUser': 0, 'capabilities': {'drop': ['ALL'], 'add': ['AUDIT_WRITE','CHOWN','DAC_OVERRIDE','FOWNER','FSETID','KILL','MKNOD','NET_BIND_SERVICE','NET_ADMIN','NET_RAW','SETFCAP','SETGID','SETPCAP','SETUID','SYS_ADMIN','SYS_CHROOT','SYS_PTRACE']}},
            'volumeMounts': [{'name': 'docker', 'mountPath': '/var/lib/docker'}]}],
        'volumes': [{'name': 'docker', 'emptyDir': {'medium': 'Memory', 'sizeLimit': '2Gi'}}]}})
    deadline = time.monotonic() + 960
    while time.monotonic() < deadline:
        pod = json.loads(command(['-n', namespace, 'get', 'pod', 'docker-test', '-o', 'json']))
        phase = pod.get('status', {}).get('phase')
        if phase in ('Succeeded', 'Failed'):
            print(command(['-n', namespace, 'logs', 'docker-test', '--tail=150']))
            if phase != 'Succeeded':
                raise RuntimeError('Docker sandbox acceptance failed')
            break
        time.sleep(5)
    else:
        raise RuntimeError('Docker sandbox acceptance timed out')
finally:
    subprocess.run(kube + ['-n', namespace, 'describe', 'pod', 'docker-test'], check=False)
    subprocess.run(kube + ['delete', 'namespace', namespace, '--wait=true', '--timeout=120s'], check=True)
