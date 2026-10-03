#!/usr/bin/env python3
"""Install the disposable, mutually authenticated Neon control-plane bridge."""
import argparse, base64, json, os, pathlib, re, subprocess, sys

ID = re.compile(r"^[0-9a-f]{32}$")
UID = re.compile(r"^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$")
ROOT = pathlib.Path("/srv/hakopod-backup-scratch/neon-native-prereq-v1/control-plane-bridge")
IMAGE = "ghcr.io/hakopod/neon-compute-tls@sha256:edd0d8aa4edcb1a79ee3341eb5047c7e9ef75bdc441ccea07c00cd3325cf4fda"
PROXY_IMAGE = "ghcr.io/hakopod/neon-storage@sha256:a787c50ec7a89677e6ee2b17878bb251ecb2f2cc8c7a1f267a3c78e900b69e24"
NAMESPACE = "hakopod-system"
UNIT = "hakopod-neon-control-relay.service"

def run(argv, data=None, timeout=60):
    result = subprocess.run(argv, input=data, stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=timeout)
    if result.returncode or len(result.stdout) > (2 << 20) or len(result.stderr) > (2 << 20):
        raise RuntimeError("bounded bridge command failed; output withheld")
    return result.stdout.decode()

def protected(name, maximum=1 << 20):
    path = ROOT / name
    info = path.lstat()
    if path.is_symlink() or not path.is_file() or info.st_mode & 0o077 or not 0 < info.st_size <= maximum:
        raise RuntimeError("protected bridge input is invalid")
    return path.read_bytes()

def kube(kubeconfig, *args, data=None):
    timeout = 200 if "rollout" in args else 90
    return run(["kubectl", "--request-timeout=30s", "--kubeconfig", kubeconfig, "--context", "k3d-hakopod-dev", *args], data, timeout)

def start_host():
    runtime = ROOT / "host-runtime"
    runtime.mkdir(mode=0o750, exist_ok=True)
    os.chown(runtime, 0, 99)
    for name in ("ca.crt", "relay.pem"):
        target = runtime / name
        target.write_bytes(protected(name))
        target.chmod(0o440)
        os.chown(target, 0, 99)
    config = runtime / "haproxy.cfg"
    config.write_text("""global
  maxconn 64
  log stdout format raw local0
defaults
  mode http
  timeout connect 2s
  timeout client 10s
  timeout server 10s
  timeout http-request 5s
frontend control
  bind 172.18.0.1:19443 ssl crt /bridge/relay.pem ca-file /bridge/ca.crt verify required ssl-min-ver TLSv1.2
  default_backend api
backend api
  server api 127.0.0.1:18880 check
""")
    config.chmod(0o440)
    os.chown(config, 0, 99)
    subprocess.run(["systemctl", "stop", UNIT], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    run(["systemd-run", "--unit=" + UNIT.removesuffix(".service"), "--property=RuntimeMaxSec=3h", "--property=MemoryMax=134217728", "--property=CPUQuota=50%", "--property=NoNewPrivileges=yes", "docker", "run", "--rm", "--network", "host", "--read-only", "--tmpfs", "/tmp:rw,noexec,nosuid,size=16m", "--mount", "type=bind,src=" + str(runtime) + ",dst=/bridge,readonly", IMAGE, "haproxy", "-W", "-db", "-f", "/bridge/haproxy.cfg"])

def manifest(platform_ids):
    config = """global
  maxconn 64
  log stdout format raw local0
defaults
  mode http
  timeout connect 2s
  timeout client 10s
  timeout server 10s
  timeout http-request 5s
frontend service
  bind :443 ssl crt /bridge/service.pem ssl-min-ver TLSv1.2
  default_backend relay
backend relay
  server relay 172.18.0.1:19443 ssl ca-file /bridge-config/ca.crt crt /bridge/client.pem verify required verifyhost 172.18.0.1 check
"""
    labels = {"app.kubernetes.io/name": "hakopod-neon-control", "hakopod.io/native-acceptance": "neon"}
    objects = [
      {"apiVersion":"v1","kind":"ConfigMap","metadata":{"name":"hakopod-neon-control","namespace":NAMESPACE,"labels":labels},"immutable":True,"data":{"haproxy.cfg":config,"ca.crt":protected("ca.crt").decode()}},
      {"apiVersion":"v1","kind":"Secret","metadata":{"name":"hakopod-neon-control-tls","namespace":NAMESPACE,"labels":labels},"type":"Opaque","immutable":True,"data":{name:base64.b64encode(protected(name)).decode() for name in ("service.pem","client.pem")}},
      {"apiVersion":"apps/v1","kind":"Deployment","metadata":{"name":"hakopod-neon-control","namespace":NAMESPACE,"labels":labels},"spec":{"replicas":1,"selector":{"matchLabels":labels},"template":{"metadata":{"labels":labels},"spec":{"automountServiceAccountToken":False,"enableServiceLinks":False,"securityContext":{"runAsNonRoot":True,"runAsUser":99,"runAsGroup":99,"fsGroup":99,"sysctls":[{"name":"net.ipv4.ip_unprivileged_port_start","value":"0"}],"seccompProfile":{"type":"RuntimeDefault"}},"containers":[{"name":"bridge","image":IMAGE,"imagePullPolicy":"IfNotPresent","args":["haproxy","-W","-db","-f","/bridge-config/haproxy.cfg"],"ports":[{"name":"https","containerPort":443}],"readinessProbe":{"tcpSocket":{"port":"https"},"periodSeconds":2,"timeoutSeconds":1},"resources":{"requests":{"cpu":"25m","memory":"32Mi"},"limits":{"cpu":"100m","memory":"64Mi"}},"securityContext":{"allowPrivilegeEscalation":False,"readOnlyRootFilesystem":True,"capabilities":{"drop":["ALL"]}},"volumeMounts":[{"name":"config","mountPath":"/bridge-config","readOnly":True},{"name":"tls","mountPath":"/bridge","readOnly":True}]}],"volumes":[{"name":"config","configMap":{"name":"hakopod-neon-control","items":[{"key":"haproxy.cfg","path":"haproxy.cfg"},{"key":"ca.crt","path":"ca.crt"}],"defaultMode":288}},{"name":"tls","secret":{"secretName":"hakopod-neon-control-tls","items":[{"key":"service.pem","path":"service.pem"},{"key":"client.pem","path":"client.pem"}],"defaultMode":288}}]}}}},
      {"apiVersion":"v1","kind":"Service","metadata":{"name":"hakopod-control","namespace":NAMESPACE,"labels":labels},"spec":{"selector":labels,"ports":[{"name":"https","port":443,"targetPort":"https"}]}},
      {"apiVersion":"networking.k8s.io/v1","kind":"NetworkPolicy","metadata":{"name":"hakopod-neon-control-ingress","namespace":NAMESPACE,"labels":labels},"spec":{"podSelector":{"matchLabels":labels},"policyTypes":["Ingress"],"ingress":[{"from":[{"namespaceSelector":{"matchExpressions":[{"key":"hakopod.io/managed-platform-id","operator":"In","values":platform_ids}]},"podSelector":{"matchLabels":{"hakopod.io/neon-role":"proxy"}}}],"ports":[{"protocol":"TCP","port":443}]}]}},
      {"apiVersion":"networking.k8s.io/v1","kind":"NetworkPolicy","metadata":{"name":"hakopod-neon-control-egress","namespace":NAMESPACE,"labels":labels},"spec":{"podSelector":{"matchLabels":labels},"policyTypes":["Egress"],"egress":[{"to":[{"ipBlock":{"cidr":"172.18.0.1/32"}}],"ports":[{"protocol":"TCP","port":19443}]}]}}
    ]
    return json.dumps({"apiVersion":"v1","kind":"List","items":objects}, separators=(",", ":")).encode()

def allow(kubeconfig, platform_id):
    if not ID.fullmatch(platform_id):
        raise RuntimeError("platform identity is malformed")
    state = ROOT / "allowed-platforms.json"
    values = [] if not state.exists() else json.loads(protected(state.name).decode())
    if isinstance(values, dict):
        allowed, uids = values.get("platform_ids"), values.get("uids")
        if set(values) != {"platform_ids","uids"} or not isinstance(allowed, list) or not isinstance(uids, dict):
            raise RuntimeError("bridge ownership state is invalid")
        for resource, uid in uids.items():
            current = json.loads(kube(kubeconfig, "-n", NAMESPACE, "get", resource, "-o", "json"))
            if not UID.fullmatch(uid) or current["metadata"]["uid"] != uid:
                raise RuntimeError("bridge resource ownership changed")
        values = allowed
    else:
        uids = None
    values = sorted(set(values) | {platform_id})
    if len(values) > 3 or any(not ID.fullmatch(value) for value in values):
        raise RuntimeError("bridge platform allowlist is invalid")
    resources = (("configmap","hakopod-neon-control"),("secret","hakopod-neon-control-tls"),("deployment","hakopod-neon-control"),("service","hakopod-control"),("networkpolicy","hakopod-neon-control-ingress"),("networkpolicy","hakopod-neon-control-egress"))
    if uids is None:
        for kind, name in resources:
            if kube(kubeconfig, "-n", NAMESPACE, "get", kind, name, "--ignore-not-found", "-o", "name").strip():
                raise RuntimeError("bridge fixed-name resource already exists")
        kube(kubeconfig, "create", "-f", "-", data=manifest(values))
        uids = {kind+"/"+name:json.loads(kube(kubeconfig, "-n", NAMESPACE, "get", kind, name, "-o", "json"))["metadata"]["uid"] for kind,name in resources}
    else:
        policy = next(item for item in json.loads(manifest(values))["items"] if item["kind"] == "NetworkPolicy" and item["metadata"]["name"] == "hakopod-neon-control-ingress")
        kube(kubeconfig, "-n", NAMESPACE, "patch", "networkpolicy", "hakopod-neon-control-ingress", "--type=merge", "-p", json.dumps({"spec":policy["spec"]}, separators=(",", ":")))
        if json.loads(kube(kubeconfig, "-n", NAMESPACE, "get", "networkpolicy", "hakopod-neon-control-ingress", "-o", "json"))["metadata"]["uid"] != uids["networkpolicy/hakopod-neon-control-ingress"]:
            raise RuntimeError("bridge ingress policy ownership changed")
    temporary = state.with_suffix(".tmp")
    temporary.write_text(json.dumps({"platform_ids":values,"uids":uids}, separators=(",", ":")) + "\n")
    temporary.chmod(0o600); temporary.replace(state)
    kube(kubeconfig, "-n", NAMESPACE, "rollout", "status", "deployment/hakopod-neon-control", "--timeout=180s")

def cleanup(kubeconfig):
    state = ROOT / "allowed-platforms.json"
    resources = (("configmap","hakopod-neon-control"),("secret","hakopod-neon-control-tls"),("deployment","hakopod-neon-control"),("service","hakopod-control"),("networkpolicy","hakopod-neon-control-ingress"),("networkpolicy","hakopod-neon-control-egress"))
    if state.exists():
        value = json.loads(protected(state.name).decode())
        if set(value) != {"platform_ids","uids"}:
            raise RuntimeError("bridge cleanup state is invalid")
        for resource, uid in value["uids"].items():
            current = json.loads(kube(kubeconfig, "-n", NAMESPACE, "get", resource, "-o", "json"))
            if not UID.fullmatch(uid) or current["metadata"]["uid"] != uid:
                raise RuntimeError("bridge cleanup refused foreign resource")
        kube(kubeconfig, "-n", NAMESPACE, "delete", "deployment/hakopod-neon-control", "service/hakopod-control", "networkpolicy/hakopod-neon-control-ingress", "networkpolicy/hakopod-neon-control-egress", "configmap/hakopod-neon-control", "secret/hakopod-neon-control-tls", "--wait=true")
        for kind, name in resources:
            if kube(kubeconfig, "-n", NAMESPACE, "get", kind, name, "--ignore-not-found", "-o", "name").strip():
                raise RuntimeError("bridge cleanup is incomplete")
    subprocess.run(["systemctl", "stop", UNIT], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    state.unlink(missing_ok=True)

def probe_start(kubeconfig, platform_id, proxy_secret, ca_kind):
    if not ID.fullmatch(platform_id) or not re.fullmatch(r"platform-tls-proxy-[0-9a-f]{16}-r1", proxy_secret):
        raise RuntimeError("issuer probe identity is malformed")
    if ca_kind not in ("correct", "wrong"):
        raise RuntimeError("issuer probe CA selection is invalid")
    namespace = "managed-platform-" + platform_id
    labels = {"app.kubernetes.io/name":"hakopod-neon-wrong-ca", "hakopod.io/neon-role":"proxy", "hakopod.io/managed-platform-id":platform_id}
    names = (("configmap", "hakopod-neon-wrong-ca"), ("deployment", "hakopod-neon-wrong-ca"), ("networkpolicy", "hakopod-neon-wrong-ca-egress"))
    for kind, name in names:
        if kube(kubeconfig, "-n", namespace, "get", kind, name, "--ignore-not-found", "-o", "name").strip():
            raise RuntimeError("issuer probe fixed-name resource already exists")
    config = {"apiVersion":"v1","kind":"ConfigMap","metadata":{"name":"hakopod-neon-wrong-ca","namespace":namespace,"labels":labels},"immutable":True,"data":{"ca.crt":protected("ca.crt" if ca_kind == "correct" else "wrong-ca.crt").decode()}}
    deployment = {"apiVersion":"apps/v1","kind":"Deployment","metadata":{"name":"hakopod-neon-wrong-ca","namespace":namespace,"labels":labels},"spec":{"replicas":1,"selector":{"matchLabels":{"app.kubernetes.io/name":"hakopod-neon-wrong-ca"}},"template":{"metadata":{"labels":labels},"spec":{"automountServiceAccountToken":False,"enableServiceLinks":False,"securityContext":{"runAsNonRoot":True,"runAsUser":10001,"runAsGroup":10001,"seccompProfile":{"type":"RuntimeDefault"}},"containers":[{"name":"proxy","image":PROXY_IMAGE,"command":["proxy"],"args":["--proxy=0.0.0.0:5432","--http=0.0.0.0:7001","--mgmt=127.0.0.1:7000","--tls-key=/proxy-auth/tls.key","--tls-cert=/proxy-auth/tls.crt","--auth-backend=control-plane","--auth-endpoint=https://hakopod-control.hakopod-system.svc/api/v1/internal/neon/proxy"],"env":[{"name":"NEON_PROXY_TO_CONTROLPLANE_TOKEN","valueFrom":{"secretKeyRef":{"name":proxy_secret,"key":"token"}}},{"name":"SSL_CERT_FILE","value":"/control-plane/ca.crt"}],"ports":[{"name":"postgres","containerPort":5432},{"name":"http","containerPort":7001}],"readinessProbe":{"httpGet":{"path":"/v1/status","port":"http"}},"resources":{"requests":{"cpu":"25m","memory":"64Mi"},"limits":{"cpu":"100m","memory":"128Mi"}},"securityContext":{"allowPrivilegeEscalation":False,"readOnlyRootFilesystem":True,"capabilities":{"drop":["ALL"]}},"volumeMounts":[{"name":"tmp","mountPath":"/tmp"},{"name":"proxy-auth","mountPath":"/proxy-auth","readOnly":True},{"name":"control-plane","mountPath":"/control-plane","readOnly":True}]}],"volumes":[{"name":"tmp","emptyDir":{"sizeLimit":"16Mi"}},{"name":"proxy-auth","secret":{"secretName":proxy_secret}},{"name":"control-plane","configMap":{"name":"hakopod-neon-wrong-ca","items":[{"key":"ca.crt","path":"ca.crt"}]}}]}}}}
    policy = {"apiVersion":"networking.k8s.io/v1","kind":"NetworkPolicy","metadata":{"name":"hakopod-neon-wrong-ca-egress","namespace":namespace,"labels":labels},"spec":{"podSelector":{"matchLabels":{"app.kubernetes.io/name":"hakopod-neon-wrong-ca"}},"policyTypes":["Egress"],"egress":[{"to":[{"namespaceSelector":{"matchLabels":{"kubernetes.io/metadata.name":NAMESPACE}},"podSelector":{"matchLabels":{"app.kubernetes.io/name":"hakopod-neon-control"}}}],"ports":[{"protocol":"TCP","port":443}]}]}}
    kube(kubeconfig, "create", "-f", "-", data=json.dumps({"apiVersion":"v1","kind":"List","items":[config,deployment,policy]}, separators=(",", ":")).encode())
    kube(kubeconfig, "-n", namespace, "rollout", "status", "deployment/hakopod-neon-wrong-ca", "--timeout=180s")
    uids = {kind+"/"+name:json.loads(kube(kubeconfig, "-n", namespace, "get", kind, name, "-o", "json"))["metadata"]["uid"] for kind,name in names}
    state = ROOT / "issuer-probe-state.json"
    temporary = state.with_suffix(".tmp")
    temporary.write_text(json.dumps({"platform_id":platform_id,"ca_kind":ca_kind,"uids":uids}, separators=(",", ":")) + "\n")
    temporary.chmod(0o600); temporary.replace(state)

def probe_verify_wrong(kubeconfig, platform_id):
    state = json.loads(protected("issuer-probe-state.json").decode())
    if set(state) != {"platform_id","ca_kind","uids"} or state.get("platform_id") != platform_id or state.get("ca_kind") != "wrong" or not all(UID.fullmatch(value) for value in state.get("uids", {}).values()):
        raise RuntimeError("wrong-issuer probe state differs")
    logs = kube(kubeconfig, "-n", "managed-platform-" + platform_id, "logs", "deployment/hakopod-neon-wrong-ca", "--tail=100")
    if not re.search(r"(?i)(unknownissuer|unknown issuer|certificate verify|invalid peer certificate|unable to get local issuer)", logs):
        raise RuntimeError("wrong-issuer probe lacked a certificate trust rejection")

def probe_stop(kubeconfig, platform_id):
    if not ID.fullmatch(platform_id):
        raise RuntimeError("issuer probe identity is malformed")
    state = json.loads(protected("issuer-probe-state.json").decode())
    if state.get("platform_id") != platform_id or set(state.get("uids", {})) != {"configmap/hakopod-neon-wrong-ca","deployment/hakopod-neon-wrong-ca","networkpolicy/hakopod-neon-wrong-ca-egress"}:
        raise RuntimeError("issuer probe cleanup state differs")
    namespace = "managed-platform-" + platform_id
    for resource, uid in state["uids"].items():
        current = json.loads(kube(kubeconfig, "-n", namespace, "get", resource, "-o", "json"))
        if current["metadata"]["uid"] != uid:
            raise RuntimeError("issuer probe resource ownership changed")
    kube(kubeconfig, "-n", namespace, "delete", "deployment/hakopod-neon-wrong-ca", "configmap/hakopod-neon-wrong-ca", "networkpolicy/hakopod-neon-wrong-ca-egress", "--wait=true")
    for resource in state["uids"]:
        if kube(kubeconfig, "-n", namespace, "get", resource, "--ignore-not-found", "-o", "name").strip():
            raise RuntimeError("issuer probe cleanup is incomplete")
    (ROOT / "issuer-probe-state.json").unlink()

def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("action", choices=("host-start", "allow", "cleanup", "probe-start", "probe-verify-wrong", "probe-stop"))
    parser.add_argument("--kubeconfig")
    parser.add_argument("--platform-id")
    parser.add_argument("--proxy-secret")
    parser.add_argument("--ca-kind")
    args = parser.parse_args()
    if args.action == "host-start": start_host()
    elif args.action == "allow": allow(args.kubeconfig, args.platform_id)
    elif args.action == "cleanup": cleanup(args.kubeconfig)
    elif args.action == "probe-start": probe_start(args.kubeconfig, args.platform_id, args.proxy_secret, args.ca_kind)
    elif args.action == "probe-verify-wrong": probe_verify_wrong(args.kubeconfig, args.platform_id)
    else: probe_stop(args.kubeconfig, args.platform_id)

if __name__ == "__main__":
    try: main()
    except Exception as error:
        print("control-plane bridge failed: " + str(error), file=sys.stderr)
        raise SystemExit(1)
