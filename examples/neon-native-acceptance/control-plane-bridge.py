#!/usr/bin/env python3
"""Install the disposable, mutually authenticated Neon control-plane bridge."""
import argparse, base64, datetime, hashlib, json, os, pathlib, re, stat, subprocess, sys, time, uuid

ID = re.compile(r"^[0-9a-f]{32}$")
UID = re.compile(r"^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$")
ROOT = pathlib.Path("/srv/hakopod-backup-scratch/neon-native-prereq-v1/control-plane-bridge")
IMAGE = "ghcr.io/hakopod/neon-compute-tls@sha256:edd0d8aa4edcb1a79ee3341eb5047c7e9ef75bdc441ccea07c00cd3325cf4fda"
NAMESPACE = "hakopod-system"
UNIT = "hakopod-neon-control-relay.service"
POOL_KEY = "hakopod.com/pool"
DNS_LABEL = re.compile(r"^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$")
INSTALL_KEY = "hakopod.io/native-acceptance-install"
BRIDGE_RESOURCES = ("configmap/hakopod-neon-control", "secret/hakopod-neon-control-tls", "deployment/hakopod-neon-control", "service/hakopod-control", "networkpolicy/hakopod-neon-control-ingress", "networkpolicy/hakopod-neon-control-egress")
PROBE_RESOURCES = ("configmap/hakopod-neon-wrong-ca", "deployment/hakopod-neon-wrong-ca", "networkpolicy/hakopod-neon-wrong-ca-egress")

def protected_policy_input(path):
    if not pathlib.Path(path).is_absolute():
        raise RuntimeError("scheduling input path must be absolute")
    fd = os.open(path, os.O_RDONLY | os.O_NOFOLLOW)
    with os.fdopen(fd, "rb") as source:
        info = os.fstat(source.fileno())
        if not stat.S_ISREG(info.st_mode) or info.st_uid != os.geteuid() or info.st_mode & 0o077 or not 1 < info.st_size <= 2 << 20:
            raise RuntimeError("scheduling input is not protected and bounded")
        value = source.read((2 << 20) + 1)
    if len(value) > 2 << 20:
        raise RuntimeError("scheduling input exceeded its bound")
    return value

def scheduling_policy(path, gate_path, kubeconfig):
    if not path:
        return {}
    raw = protected_policy_input(path)
    policy = json.loads(raw)
    gate_raw = protected_policy_input(gate_path)
    gate = json.loads(gate_raw)
    if set(policy) != {"schema_version", "gate_sha256", "scheduling_pool", "runtime_class"} or type(policy["schema_version"]) is not int or policy["schema_version"] != 1 or policy["gate_sha256"] != hashlib.sha256(gate_raw).hexdigest() or not DNS_LABEL.fullmatch(str(policy["scheduling_pool"])):
        raise RuntimeError("scheduling policy binding is invalid")
    expires = datetime.datetime.fromisoformat(gate["expires_at"].replace("Z", "+00:00"))
    nodes = gate.get("node_uids")
    if gate.get("schema_version") != 1 or gate.get("kind") != "neon" or gate.get("context") != "k3d-hakopod-dev" or gate.get("environment") != "development" or pathlib.Path(gate["kubeconfig"]).resolve() != pathlib.Path(kubeconfig).resolve() or expires.tzinfo is None or expires <= datetime.datetime.now(datetime.timezone.utc) or not isinstance(nodes, dict) or not 3 <= len(nodes) <= 8 or len(set(nodes.values())) != len(nodes) or any(not UID.fullmatch(str(uid)) for uid in [gate.get("cluster_uid"), *nodes.values()]):
        raise RuntimeError("scheduling gate identity is invalid or expired")
    cluster = json.loads(kube(kubeconfig, "get", "namespace", "kube-system", "-o", "json"))
    current_nodes = json.loads(kube(kubeconfig, "get", "nodes", "-o", "json"))["items"]
    if cluster["metadata"]["uid"] != gate["cluster_uid"] or {item["metadata"]["name"]:item["metadata"]["uid"] for item in current_nodes} != nodes:
        raise RuntimeError("scheduling cluster identity changed")
    pool = policy["scheduling_pool"]
    for node in current_nodes:
        taints = [item for item in node.get("spec", {}).get("taints", []) if item.get("key") == POOL_KEY]
        blocked = [item for item in node.get("spec", {}).get("taints", []) if item.get("key") != POOL_KEY and item.get("effect") in ("NoSchedule", "NoExecute")]
        expected = {"key":POOL_KEY, "value":pool, "effect":"NoSchedule"}
        if node["metadata"].get("deletionTimestamp") or node.get("spec", {}).get("unschedulable") or node["metadata"].get("labels", {}).get(POOL_KEY) != pool or taints != [expected] or blocked or not any(item.get("type") == "Ready" and item.get("status") == "True" for item in node.get("status", {}).get("conditions", [])):
            raise RuntimeError("attested scheduling pool is not ready and exact")
    terms = [{"matchFields":[{"key":"metadata.name", "operator":"In", "values":[name]}]} for name in sorted(nodes)]
    result = {"nodeSelector":{POOL_KEY:pool}, "tolerations":[{"key":POOL_KEY, "operator":"Equal", "value":pool, "effect":"NoSchedule"}], "affinity":{"nodeAffinity":{"requiredDuringSchedulingIgnoredDuringExecution":{"nodeSelectorTerms":terms}}}}
    runtime = policy["runtime_class"]
    if runtime is not None:
        if not isinstance(runtime, dict) or set(runtime) != {"name", "uid", "handler", "pod_fixed", "node_selector"} or not DNS_LABEL.fullmatch(str(runtime["name"])) or not DNS_LABEL.fullmatch(str(runtime["handler"])) or not UID.fullmatch(str(runtime["uid"])) or not isinstance(runtime["pod_fixed"], dict) or set(runtime["pod_fixed"]) not in (set(), {"cpu", "memory"}) or runtime["pod_fixed"] and (not re.fullmatch(r"[1-9][0-9]{0,3}m", str(runtime["pod_fixed"]["cpu"])) or not re.fullmatch(r"[1-9][0-9]{0,3}Mi", str(runtime["pod_fixed"]["memory"]))):
            raise RuntimeError("scheduling runtime identity is invalid")
        selector = runtime["node_selector"]
        if not isinstance(selector, dict) or len(selector) > 8 or any(not isinstance(key, str) or not 1 <= len(key) <= 253 or not isinstance(value, str) or not DNS_LABEL.fullmatch(value) for key, value in selector.items()):
            raise RuntimeError("scheduling runtime node selector is invalid")
        observed = json.loads(kube(kubeconfig, "get", "runtimeclass", runtime["name"], "-o", "json"))
        expected_scheduling = {"nodeSelector":selector} if selector else {}
        if observed["metadata"]["uid"] != runtime["uid"] or observed["metadata"].get("deletionTimestamp") or observed.get("handler") != runtime["handler"] or observed.get("overhead", {}).get("podFixed", {}) != runtime["pod_fixed"] or observed.get("scheduling", {}) != expected_scheduling:
            raise RuntimeError("scheduling runtime identity or overhead changed")
        if any(any(node["metadata"].get("labels", {}).get(key) != value for key, value in selector.items()) for node in current_nodes) or POOL_KEY in selector and selector[POOL_KEY] != pool:
            raise RuntimeError("attested nodes do not match the runtime selector")
        result["nodeSelector"].update(selector)
        result["runtimeClassName"] = runtime["name"]
    return result

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

def write_state(path, value):
    temporary = path.with_name(path.name + "." + uuid.uuid4().hex + ".tmp")
    fd = os.open(temporary, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    with os.fdopen(fd, "w") as output:
        json.dump(value, output, separators=(",", ":")); output.write("\n")
        output.flush(); os.fsync(output.fileno())
    temporary.replace(path)
    fd = os.open(path.parent, os.O_RDONLY | os.O_DIRECTORY)
    try: os.fsync(fd)
    finally: os.close(fd)

def observe(kubeconfig, namespace, resource):
    raw = kube(kubeconfig, "-n", namespace, "get", resource, "--ignore-not-found", "-o", "json")
    return json.loads(raw) if raw.strip() else None

def read_state(path, fields, resources):
    value = json.loads(protected(path.name))
    if not isinstance(value, dict) or set(value) != set(fields) | {"install_id", "namespace_uid", "uids"} or not ID.fullmatch(str(value.get("install_id"))) or not UID.fullmatch(str(value.get("namespace_uid"))) or not isinstance(value.get("uids"), dict) or not set(value["uids"]) <= set(resources) or any(not UID.fullmatch(str(uid)) for uid in value["uids"].values()):
        raise RuntimeError("helper ownership journal is invalid")
    return value

def recover_owned(kubeconfig, namespace, path, value, resources):
    current = json.loads(kube(kubeconfig, "get", "namespace", namespace, "-o", "json"))
    if current["metadata"]["uid"] != value["namespace_uid"]:
        raise RuntimeError("helper namespace ownership changed")
    present = {}
    for resource in resources:
        current = observe(kubeconfig, namespace, resource)
        if current is None: continue
        meta = current["metadata"]; uid = meta.get("uid")
        if not UID.fullmatch(str(uid)) or meta.get("annotations", {}).get(INSTALL_KEY) != value["install_id"] or resource in value["uids"] and value["uids"][resource] != uid:
            raise RuntimeError("helper cleanup refused foreign resource")
        value["uids"][resource] = uid; present[resource] = uid
    write_state(path, value)
    return present

def validate_manifests(kubeconfig, objects):
    # Admission of a Deployment does not run every Pod admission check.
    candidates = list(objects)
    for item in objects:
        if item["kind"] != "Deployment": continue
        template = item["spec"]["template"]
        candidates.append({"apiVersion":"v1", "kind":"Pod", "metadata":{**template["metadata"], "name":item["metadata"]["name"]+"-preflight", "namespace":item["metadata"]["namespace"]}, "spec":template["spec"]})
    kube(kubeconfig, "create", "--dry-run=server", "-f", "-", "-o", "name", data=json.dumps({"apiVersion":"v1", "kind":"List", "items":candidates}).encode())

def install_owned(kubeconfig, namespace, path, value, objects, resources):
    if path.exists(): raise RuntimeError("helper ownership journal already exists")
    if any(observe(kubeconfig, namespace, resource) is not None for resource in resources):
        raise RuntimeError("helper fixed-name resource already exists")
    validate_manifests(kubeconfig, objects)
    namespace_uid = json.loads(kube(kubeconfig, "get", "namespace", namespace, "-o", "json"))["metadata"]["uid"]
    if not UID.fullmatch(str(namespace_uid)): raise RuntimeError("helper namespace identity is invalid")
    value = {**value, "install_id":uuid.uuid4().hex, "namespace_uid":namespace_uid, "uids":{}}
    # Persist the unpredictable ownership marker before the first create. Cleanup can
    # recover a successful create whose response was lost without adopting names.
    write_state(path, value)
    for item in objects:
        item["metadata"].setdefault("annotations", {})[INSTALL_KEY] = value["install_id"]
        resource = item["kind"].lower()+"/"+item["metadata"]["name"]
        if resource not in resources: raise RuntimeError("helper resource is outside its fixed inventory")
        try:
            current = json.loads(kube(kubeconfig, "create", "-f", "-", "-o", "json", data=json.dumps(item).encode()))
        except Exception:
            raise RuntimeError("helper create failed for " + item["kind"] + "; output withheld") from None
        meta = current.get("metadata", {})
        if not UID.fullmatch(str(meta.get("uid"))) or meta.get("annotations", {}).get(INSTALL_KEY) != value["install_id"] or meta.get("name") != item["metadata"]["name"] or meta.get("namespace") != namespace:
            raise RuntimeError("helper create response ownership differs")
        value["uids"][resource] = meta["uid"]; write_state(path, value)
    return value

def cleanup_owned(kubeconfig, namespace, path, value, resources):
    present = recover_owned(kubeconfig, namespace, path, value, resources)
    paths = {"configmap":("/api/v1", "configmaps"), "secret":("/api/v1", "secrets"), "service":("/api/v1", "services"), "deployment":("/apis/apps/v1", "deployments"), "networkpolicy":("/apis/networking.k8s.io/v1", "networkpolicies")}
    for resource, uid in present.items():
        kind, name = resource.split("/"); prefix, plural = paths[kind]
        body = {"apiVersion":"v1", "kind":"DeleteOptions", "preconditions":{"uid":uid}, "propagationPolicy":"Foreground"}
        kube(kubeconfig, "delete", "--raw", prefix+"/namespaces/"+namespace+"/"+plural+"/"+name, "-f", "-", data=json.dumps(body).encode())
    deadline = time.monotonic() + 90
    while True:
        if not any(observe(kubeconfig, namespace, resource) is not None for resource in resources): break
        if time.monotonic() >= deadline: raise RuntimeError("helper cleanup is incomplete")
        time.sleep(1)
    path.unlink()

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

def manifest(platform_ids, scheduling=None):
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
      {"apiVersion":"networking.k8s.io/v1","kind":"NetworkPolicy","metadata":{"name":"hakopod-neon-control-ingress","namespace":NAMESPACE,"labels":labels},"spec":{"podSelector":{"matchLabels":labels},"policyTypes":["Ingress"],"ingress":[{"from":[{"namespaceSelector":{"matchExpressions":[{"key":"hakopod.io/managed-platform-id","operator":"In","values":platform_ids}]},"podSelector":{"matchExpressions":[{"key":"hakopod.io/neon-role","operator":"In","values":["proxy","storage-controller"]}]}}],"ports":[{"protocol":"TCP","port":443}]}]}},
      {"apiVersion":"networking.k8s.io/v1","kind":"NetworkPolicy","metadata":{"name":"hakopod-neon-control-egress","namespace":NAMESPACE,"labels":labels},"spec":{"podSelector":{"matchLabels":labels},"policyTypes":["Egress"],"egress":[{"to":[{"ipBlock":{"cidr":"172.18.0.1/32"}}],"ports":[{"protocol":"TCP","port":19443}]}]}}
    ]
    next(item for item in objects if item["kind"] == "Deployment")["spec"]["template"]["spec"].update(scheduling or {})
    return json.dumps({"apiVersion":"v1","kind":"List","items":objects}, separators=(",", ":")).encode()

def allow(kubeconfig, platform_id, scheduling=None):
    if not ID.fullmatch(platform_id):
        raise RuntimeError("platform identity is malformed")
    state = ROOT / "allowed-platforms.json"
    prior = read_state(state, {"platform_ids"}, BRIDGE_RESOURCES) if state.exists() else None
    values = prior["platform_ids"] if prior else []
    if not isinstance(values, list) or len(values) > 3 or any(not ID.fullmatch(str(value)) for value in values):
        raise RuntimeError("bridge platform allowlist is invalid")
    values = sorted(set(values) | {platform_id})
    if len(values) > 3 or any(not ID.fullmatch(value) for value in values):
        raise RuntimeError("bridge platform allowlist is invalid")
    if prior is None:
        prior = install_owned(kubeconfig, NAMESPACE, state, {"platform_ids":values}, json.loads(manifest(values, scheduling))["items"], BRIDGE_RESOURCES)
    else:
        if set(recover_owned(kubeconfig, NAMESPACE, state, prior, BRIDGE_RESOURCES)) != set(BRIDGE_RESOURCES):
            raise RuntimeError("bridge install is incomplete; cleanup is required")
        if scheduling:
            current = json.loads(kube(kubeconfig, "-n", NAMESPACE, "get", "deployment", "hakopod-neon-control", "-o", "json"))
            pod = current["spec"]["template"]["spec"]
            if {key:pod[key] for key in ("nodeSelector", "tolerations", "runtimeClassName", "affinity") if key in pod} != scheduling or pod.get("nodeName"):
                raise RuntimeError("existing bridge scheduling differs")
        policy = next(item for item in json.loads(manifest(values))["items"] if item["kind"] == "NetworkPolicy" and item["metadata"]["name"] == "hakopod-neon-control-ingress")
        owned = observe(kubeconfig, NAMESPACE, "networkpolicy/hakopod-neon-control-ingress")
        if owned is None or owned["metadata"]["uid"] != prior["uids"]["networkpolicy/hakopod-neon-control-ingress"] or owned["metadata"].get("annotations", {}).get(INSTALL_KEY) != prior["install_id"]:
            raise RuntimeError("bridge ingress policy ownership changed")
        patch = [{"op":"test", "path":"/metadata/uid", "value":owned["metadata"]["uid"]}, {"op":"test", "path":"/metadata/resourceVersion", "value":owned["metadata"]["resourceVersion"]}, {"op":"replace", "path":"/spec", "value":policy["spec"]}]
        kube(kubeconfig, "-n", NAMESPACE, "patch", "networkpolicy", "hakopod-neon-control-ingress", "--type=json", "-p", json.dumps(patch, separators=(",", ":")))
        updated = json.loads(kube(kubeconfig, "-n", NAMESPACE, "get", "networkpolicy", "hakopod-neon-control-ingress", "-o", "json"))
        if updated["metadata"]["uid"] != prior["uids"]["networkpolicy/hakopod-neon-control-ingress"] or updated["metadata"].get("annotations", {}).get(INSTALL_KEY) != prior["install_id"] or updated.get("spec") != policy["spec"]:
            raise RuntimeError("bridge ingress policy ownership or specification changed")
    prior["platform_ids"] = values; write_state(state, prior)
    kube(kubeconfig, "-n", NAMESPACE, "rollout", "status", "deployment/hakopod-neon-control", "--timeout=180s")

def cleanup(kubeconfig):
    state = ROOT / "allowed-platforms.json"
    if state.exists():
        cleanup_owned(kubeconfig, NAMESPACE, state, read_state(state, {"platform_ids"}, BRIDGE_RESOURCES), BRIDGE_RESOURCES)
    elif any(observe(kubeconfig, NAMESPACE, resource) is not None for resource in BRIDGE_RESOURCES):
        raise RuntimeError("bridge resources exist without an ownership journal")
    subprocess.run(["systemctl", "stop", UNIT], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    state.unlink(missing_ok=True)

def owned_proxy_image(deployment, platform_id, proxy_secret):
    meta = deployment.get("metadata", {}); spec = deployment.get("spec", {})
    template = spec.get("template", {}); labels = meta.get("labels", {}); pod_labels = template.get("metadata", {}).get("labels", {})
    if meta.get("name") != "neon-proxy" or meta.get("namespace") != "managed-platform-"+platform_id or not UID.fullmatch(str(meta.get("uid", ""))) or meta.get("deletionTimestamp") or labels.get("app.kubernetes.io/managed-by") != "hakopod" or labels.get("hakopod.io/managed-platform-id") != platform_id or pod_labels.get("hakopod.io/managed-platform-id") != platform_id or pod_labels.get("hakopod.io/neon-role") != "proxy":
        raise RuntimeError("issuer probe source proxy ownership is invalid")
    containers = [item for item in template.get("spec", {}).get("containers", []) if item.get("name") == "proxy"]
    secrets = [item.get("secret", {}).get("secretName") for item in template.get("spec", {}).get("volumes", []) if item.get("name") == "proxy-auth"]
    if len(containers) != 1 or secrets != [proxy_secret] or not re.fullmatch(r"[^\s]+@sha256:[0-9a-f]{64}", containers[0].get("image", "")):
        raise RuntimeError("issuer probe source image or TLS snapshot is invalid")
    return containers[0]["image"]

def probe_start(kubeconfig, platform_id, proxy_secret, ca_kind, scheduling=None):
    if not ID.fullmatch(platform_id) or not re.fullmatch(r"platform-tls-proxy-[0-9a-f]{16}-r1", proxy_secret):
        raise RuntimeError("issuer probe identity is malformed")
    if ca_kind not in ("correct", "wrong"):
        raise RuntimeError("issuer probe CA selection is invalid")
    namespace = "managed-platform-" + platform_id
    source = json.loads(kube(kubeconfig, "-n", namespace, "get", "deployment", "neon-proxy", "-o", "json"))
    proxy_image = owned_proxy_image(source, platform_id, proxy_secret)
    labels = {"app.kubernetes.io/name":"hakopod-neon-wrong-ca", "hakopod.io/neon-role":"proxy", "hakopod.io/managed-platform-id":platform_id}
    config = {"apiVersion":"v1","kind":"ConfigMap","metadata":{"name":"hakopod-neon-wrong-ca","namespace":namespace,"labels":labels},"immutable":True,"data":{"ca.crt":protected("ca.crt" if ca_kind == "correct" else "wrong-ca.crt").decode()}}
    deployment = {"apiVersion":"apps/v1","kind":"Deployment","metadata":{"name":"hakopod-neon-wrong-ca","namespace":namespace,"labels":labels},"spec":{"replicas":1,"selector":{"matchLabels":{"app.kubernetes.io/name":"hakopod-neon-wrong-ca"}},"template":{"metadata":{"labels":labels},"spec":{"automountServiceAccountToken":False,"enableServiceLinks":False,"securityContext":{"runAsNonRoot":True,"runAsUser":10001,"runAsGroup":10001,"seccompProfile":{"type":"RuntimeDefault"}},"containers":[{"name":"proxy","image":proxy_image,"command":["proxy"],"args":["--proxy=0.0.0.0:5432","--http=0.0.0.0:7001","--mgmt=127.0.0.1:7000","--tls-key=/proxy-auth/tls.key","--tls-cert=/proxy-auth/tls.crt","--auth-backend=control-plane","--auth-endpoint=https://hakopod-control.hakopod-system.svc/api/v1/internal/neon/proxy"],"env":[{"name":"NEON_PROXY_TO_CONTROLPLANE_TOKEN","valueFrom":{"secretKeyRef":{"name":proxy_secret,"key":"token"}}},{"name":"SSL_CERT_FILE","value":"/control-plane/ca.crt"}],"ports":[{"name":"postgres","containerPort":5432},{"name":"http","containerPort":7001}],"readinessProbe":{"httpGet":{"path":"/v1/status","port":"http"}},"resources":{"requests":{"cpu":"25m","memory":"64Mi"},"limits":{"cpu":"100m","memory":"128Mi"}},"securityContext":{"allowPrivilegeEscalation":False,"readOnlyRootFilesystem":True,"capabilities":{"drop":["ALL"]}},"volumeMounts":[{"name":"tmp","mountPath":"/tmp"},{"name":"proxy-auth","mountPath":"/proxy-auth","readOnly":True},{"name":"control-plane","mountPath":"/control-plane","readOnly":True}]}],"volumes":[{"name":"tmp","emptyDir":{"sizeLimit":"16Mi"}},{"name":"proxy-auth","secret":{"secretName":proxy_secret}},{"name":"control-plane","configMap":{"name":"hakopod-neon-wrong-ca","items":[{"key":"ca.crt","path":"ca.crt"}]}}]}}}}
    deployment["spec"]["template"]["spec"].update(scheduling or {})
    policy = {"apiVersion":"networking.k8s.io/v1","kind":"NetworkPolicy","metadata":{"name":"hakopod-neon-wrong-ca-egress","namespace":namespace,"labels":labels},"spec":{"podSelector":{"matchLabels":{"app.kubernetes.io/name":"hakopod-neon-wrong-ca"}},"policyTypes":["Egress"],"egress":[{"to":[{"namespaceSelector":{"matchLabels":{"kubernetes.io/metadata.name":NAMESPACE}},"podSelector":{"matchLabels":{"app.kubernetes.io/name":"hakopod-neon-control"}}}],"ports":[{"protocol":"TCP","port":443}]}]}}
    current = json.loads(kube(kubeconfig, "-n", namespace, "get", "deployment", "neon-proxy", "-o", "json"))
    if current.get("metadata", {}).get("uid") != source["metadata"]["uid"] or owned_proxy_image(current, platform_id, proxy_secret) != proxy_image:
        raise RuntimeError("issuer probe source proxy changed")
    state = ROOT / "issuer-probe-state.json"
    install_owned(kubeconfig, namespace, state, {"platform_id":platform_id,"ca_kind":ca_kind}, [config,deployment,policy], PROBE_RESOURCES)
    kube(kubeconfig, "-n", namespace, "rollout", "status", "deployment/hakopod-neon-wrong-ca", "--timeout=180s")

def probe_verify_wrong(kubeconfig, platform_id):
    path = ROOT / "issuer-probe-state.json"
    state = read_state(path, {"platform_id", "ca_kind"}, PROBE_RESOURCES)
    if state.get("platform_id") != platform_id or state.get("ca_kind") != "wrong" or set(recover_owned(kubeconfig, "managed-platform-" + platform_id, path, state, PROBE_RESOURCES)) != set(PROBE_RESOURCES):
        raise RuntimeError("wrong-issuer probe state differs")
    logs = kube(kubeconfig, "-n", "managed-platform-" + platform_id, "logs", "deployment/hakopod-neon-wrong-ca", "--tail=100")
    if not re.search(r"(?i)(unknownissuer|unknown issuer|certificate verify|invalid peer certificate|unable to get local issuer)", logs):
        raise RuntimeError("wrong-issuer probe lacked a certificate trust rejection")

def probe_stop(kubeconfig, platform_id):
    if not ID.fullmatch(platform_id):
        raise RuntimeError("issuer probe identity is malformed")
    path = ROOT / "issuer-probe-state.json"
    state = read_state(path, {"platform_id", "ca_kind"}, PROBE_RESOURCES)
    if state.get("platform_id") != platform_id:
        raise RuntimeError("issuer probe cleanup state differs")
    namespace = "managed-platform-" + platform_id
    cleanup_owned(kubeconfig, namespace, path, state, PROBE_RESOURCES)

def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("action", choices=("host-start", "allow", "cleanup", "probe-start", "probe-verify-wrong", "probe-stop", "verify-scheduling"))
    parser.add_argument("--kubeconfig")
    parser.add_argument("--platform-id")
    parser.add_argument("--proxy-secret")
    parser.add_argument("--ca-kind")
    parser.add_argument("--scheduling-policy", default="")
    parser.add_argument("--gate-attestation", default="")
    args = parser.parse_args()
    # Cleanup remains available when the gate expires or a node becomes unavailable.
    scheduling = scheduling_policy(args.scheduling_policy, args.gate_attestation, args.kubeconfig) if args.action in ("allow", "probe-start", "verify-scheduling") else {}
    if args.action == "host-start": start_host()
    elif args.action == "verify-scheduling": return
    elif args.action == "allow": allow(args.kubeconfig, args.platform_id, scheduling)
    elif args.action == "cleanup": cleanup(args.kubeconfig)
    elif args.action == "probe-start": probe_start(args.kubeconfig, args.platform_id, args.proxy_secret, args.ca_kind, scheduling)
    elif args.action == "probe-verify-wrong": probe_verify_wrong(args.kubeconfig, args.platform_id)
    else: probe_stop(args.kubeconfig, args.platform_id)

if __name__ == "__main__":
    try: main()
    except Exception as error:
        print("control-plane bridge failed: " + str(error), file=sys.stderr)
        raise SystemExit(1)
