#!/usr/bin/env python3
"""Install the disposable, mutually authenticated Neon control-plane bridge."""
import argparse, base64, datetime, hashlib, ipaddress, json, os, pathlib, pwd, re, stat, subprocess, sys, time, uuid

ID = re.compile(r"^[0-9a-f]{32}$")
UID = re.compile(r"^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$")
ROOT = pathlib.Path("/srv/hakopod-backup-scratch/neon-native-prereq-v1/control-plane-bridge")
IMAGE = "ghcr.io/hakopod/neon-compute-tls@sha256:edd0d8aa4edcb1a79ee3341eb5047c7e9ef75bdc441ccea07c00cd3325cf4fda"
NAMESPACE = "hakopod-system"
UNIT = "hakopod-neon-control-relay.service"
POOL_KEY = "hakopod.com/pool"
DNS_LABEL = re.compile(r"^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$")
INSTALL_KEY = "hakopod.io/native-acceptance-install"
BRIDGE_FIXED_RESOURCES = ("secret/hakopod-neon-control-tls", "deployment/hakopod-neon-control", "service/hakopod-control", "networkpolicy/hakopod-neon-control-ingress", "networkpolicy/hakopod-neon-control-egress")
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

def bridge_resources(config_names):
    if not isinstance(config_names,list) or not 1 <= len(config_names) <= 3 or len(set(config_names)) != len(config_names) or any(not re.fullmatch(r"hakopod-neon-control-[0-9a-f]{16}",str(name)) for name in config_names):
        raise RuntimeError("bridge configuration inventory is invalid")
    return BRIDGE_FIXED_RESOURCES + tuple("configmap/"+name for name in config_names)

def read_bridge_state(path):
    value=json.loads(protected(path.name))
    resources=bridge_resources(value.get("config_names"))
    return read_state(path,{"platform_ids","config_names"},resources),resources

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

def validate_bridge_update(kubeconfig, objects, include_config):
    deployment=next(item for item in objects if item["kind"]=="Deployment")
    template=deployment["spec"]["template"]
    pod={"apiVersion":"v1","kind":"Pod","metadata":{**template["metadata"],"name":"hakopod-neon-control-update-preflight","namespace":NAMESPACE},"spec":template["spec"]}
    candidates=[pod]
    if include_config:
        candidates.append(next(item for item in objects if item["kind"]=="ConfigMap"))
    kube(kubeconfig,"create","--dry-run=server","-f","-","-o","name",data=json.dumps({"apiVersion":"v1","kind":"List","items":candidates}).encode())

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

def patch_owned_spec(kubeconfig, namespace, resource, current, desired, value):
    meta = current.get("metadata", {})
    if meta.get("uid") != value["uids"].get(resource) or meta.get("annotations", {}).get(INSTALL_KEY) != value["install_id"] or not meta.get("resourceVersion"):
        raise RuntimeError("helper update refused foreign resource")
    patch = [{"op":"test","path":"/metadata/uid","value":meta["uid"]},
             {"op":"test","path":"/metadata/resourceVersion","value":meta["resourceVersion"]},
             {"op":"replace","path":"/spec","value":desired["spec"]}]
    kind,name = resource.split("/")
    kube(kubeconfig, "-n", namespace, "patch", kind, name, "--type=json", "-p", json.dumps(patch,separators=(",",":")))
    updated = json.loads(kube(kubeconfig, "-n", namespace, "get", resource, "-o", "json"))
    if updated.get("metadata", {}).get("uid") != meta["uid"] or updated.get("metadata", {}).get("annotations", {}).get(INSTALL_KEY) != value["install_id"] or updated.get("spec") != desired["spec"]:
        raise RuntimeError("helper update ownership or specification changed")

def refresh_bridge(kubeconfig, path, value, objects):
    desired = {item["kind"].lower()+"/"+item["metadata"]["name"]:item for item in objects}
    wanted = next(item for item in objects if item["kind"] == "ConfigMap")
    config_name = wanted["metadata"]["name"]
    config_resource = "configmap/"+config_name
    if config_name not in value["config_names"]:
        # Persist the new owned name before creation so a lost response can be
        # recovered by its unpredictable install marker without adopting a name.
        value["config_names"].append(config_name)
        value["config_names"].sort()
        write_state(path,value)
        validate_bridge_update(kubeconfig,objects,True)
        wanted["metadata"].setdefault("annotations",{})[INSTALL_KEY]=value["install_id"]
        created = json.loads(kube(kubeconfig,"create","-f","-","-o","json",data=json.dumps(wanted).encode()))
        meta = created.get("metadata", {})
        if not UID.fullmatch(str(meta.get("uid"))) or meta.get("annotations", {}).get(INSTALL_KEY) != value["install_id"]:
            raise RuntimeError("bridge configuration create response differs")
        value["uids"][config_resource]=meta["uid"]; write_state(path,value)
    else:
        validate_bridge_update(kubeconfig,objects,False)
        current=observe(kubeconfig,NAMESPACE,config_resource)
        if current is None or current.get("metadata",{}).get("uid") != value["uids"].get(config_resource) or current.get("metadata",{}).get("annotations",{}).get(INSTALL_KEY) != value["install_id"] or current.get("data") != wanted.get("data") or current.get("immutable") is not True:
            raise RuntimeError("bridge configuration ownership or content changed")
    deployment_resource = "deployment/hakopod-neon-control"
    deployment = observe(kubeconfig,NAMESPACE,deployment_resource)
    meta = deployment.get("metadata",{})
    if meta.get("uid") != value["uids"].get(deployment_resource) or meta.get("annotations",{}).get(INSTALL_KEY) != value["install_id"] or not meta.get("resourceVersion"):
        raise RuntimeError("bridge deployment ownership changed")
    annotation = desired[deployment_resource]["spec"]["template"]["metadata"]["annotations"]["hakopod.io/bridge-config-sha256"]
    volumes=deployment.get("spec",{}).get("template",{}).get("spec",{}).get("volumes",[])
    indexes=[i for i,item in enumerate(volumes) if item.get("name")=="config"]
    if len(indexes)!=1: raise RuntimeError("bridge deployment config mount changed")
    patch = [{"op":"test","path":"/metadata/uid","value":meta["uid"]},
             {"op":"test","path":"/metadata/resourceVersion","value":meta["resourceVersion"]},
             {"op":"replace","path":"/spec/template/spec/volumes/"+str(indexes[0])+"/configMap/name","value":config_name},
             {"op":"replace","path":"/spec/template/metadata/annotations/hakopod.io~1bridge-config-sha256","value":annotation}]
    kube(kubeconfig,"-n",NAMESPACE,"patch","deployment","hakopod-neon-control","--type=json","-p",json.dumps(patch,separators=(",",":")))
    updated = json.loads(kube(kubeconfig,"-n",NAMESPACE,"get",deployment_resource,"-o","json"))
    updated_volumes=updated.get("spec",{}).get("template",{}).get("spec",{}).get("volumes",[])
    if updated.get("metadata",{}).get("uid") != meta["uid"] or updated.get("metadata",{}).get("annotations",{}).get(INSTALL_KEY) != value["install_id"] or updated.get("spec",{}).get("template",{}).get("metadata",{}).get("annotations",{}).get("hakopod.io/bridge-config-sha256") != annotation or len(updated_volumes) <= indexes[0] or updated_volumes[indexes[0]].get("configMap",{}).get("name") != config_name:
        raise RuntimeError("bridge deployment rollout identity changed")
    for resource in ("networkpolicy/hakopod-neon-control-ingress", "networkpolicy/hakopod-neon-control-egress"):
        current = observe(kubeconfig,NAMESPACE,resource)
        patch_owned_spec(kubeconfig,NAMESPACE,resource,current,desired[resource],value)
    kube(kubeconfig, "-n", NAMESPACE, "rollout", "status", "deployment/hakopod-neon-control", "--timeout=180s")

def host_binding():
    path = ROOT / "host-binding.json"
    if not path.exists() and not path.is_symlink():
        return None
    value = json.loads(protected_policy_input(str(path)))
    if not isinstance(value, dict) or set(value) != {"schema_version", "address", "haproxy_path", "haproxy_sha256", "uid", "gid"} or type(value["schema_version"]) is not int or value["schema_version"] != 1:
        raise RuntimeError("native relay binding is invalid")
    if not isinstance(value["address"], str):
        raise RuntimeError("native relay requires a private IPv4 address")
    try:
        address = ipaddress.IPv4Address(value["address"])
    except (ValueError, TypeError):
        raise RuntimeError("native relay requires a private IPv4 address") from None
    networks = ("10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16")
    if not any(address in ipaddress.ip_network(network) for network in networks):
        raise RuntimeError("native relay requires a private IPv4 address")
    if value["haproxy_path"] != "/usr/sbin/haproxy" or not re.fullmatch(r"[0-9a-f]{64}", str(value["haproxy_sha256"])):
        raise RuntimeError("native relay executable binding is invalid")
    binary = pathlib.Path(value["haproxy_path"])
    info = binary.lstat()
    if not stat.S_ISREG(info.st_mode) or info.st_uid != 0 or info.st_mode & 0o022 or not info.st_mode & 0o111 or not 0 < info.st_size <= 64 << 20 or hashlib.sha256(binary.read_bytes()).hexdigest() != value["haproxy_sha256"]:
        raise RuntimeError("native relay executable changed")
    user = pwd.getpwnam("haproxy")
    if any(type(value[key]) is not int or not 0 < value[key] < 65535 for key in ("uid", "gid")) or (user.pw_uid, user.pw_gid) != (value["uid"], value["gid"]):
        raise RuntimeError("native relay service identity changed")
    interfaces = json.loads(run(["ip", "-j", "address", "show"]))
    if not isinstance(interfaces, list) or len(interfaces) > 128 or not any(item.get("family") == "inet" and item.get("local") == str(address) for interface in interfaces for item in interface.get("addr_info", [])):
        raise RuntimeError("native relay address is not assigned to this host")
    return value

def start_host():
    binding = host_binding()
    address, group = (binding["address"], binding["gid"]) if binding else ("172.18.0.1", 99)
    run(["openssl", "verify", "-CAfile", str(ROOT / "ca.crt"), "-purpose", "sslserver", "-verify_ip", address, str(ROOT / "relay.pem")])
    runtime = ROOT / "host-runtime"
    runtime.mkdir(mode=0o750, exist_ok=True)
    runtime.chmod(0o750)
    os.chown(runtime, 0, group)
    for name in ("ca.crt", "relay.pem"):
        target = runtime / name
        target.write_bytes(protected(name))
        target.chmod(0o440)
        os.chown(target, 0, group)
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
  bind RELAY_ADDRESS:19443 ssl crt /bridge/relay.pem ca-file /bridge/ca.crt verify required ssl-min-ver TLSv1.2
  default_backend api
backend api
  server api 127.0.0.1:18880 check
""".replace("RELAY_ADDRESS", address))
    config.chmod(0o440)
    os.chown(config, 0, group)
    subprocess.run(["systemctl", "stop", UNIT], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    command = ["systemd-run", "--unit=" + UNIT.removesuffix(".service"), "--property=RuntimeMaxSec=3h", "--property=MemoryMax=134217728", "--property=CPUQuota=50%", "--property=NoNewPrivileges=yes"]
    if binding:
        command += ["--property=User="+str(binding["uid"]), "--property=Group="+str(group), "--property=TasksMax=64", "--property=ProtectSystem=strict", "--property=ProtectHome=yes", "--property=PrivateTmp=yes", "--property=CapabilityBoundingSet=", "--property=RestrictAddressFamilies=AF_INET AF_INET6 AF_UNIX", "--property=BindReadOnlyPaths="+str(runtime)+":/bridge", binding["haproxy_path"], "-W", "-db", "-f", "/bridge/haproxy.cfg"]
    else:
        command += ["docker", "run", "--rm", "--network", "host", "--read-only", "--tmpfs", "/tmp:rw,noexec,nosuid,size=16m", "--mount", "type=bind,src=" + str(runtime) + ",dst=/bridge,readonly", IMAGE, "haproxy", "-W", "-db", "-f", "/bridge/haproxy.cfg"]
    run(command)

def management_routes(platform_ids):
    if not isinstance(platform_ids, list) or not 1 <= len(platform_ids) <= 3 or platform_ids != sorted(set(platform_ids)) or any(not ID.fullmatch(str(value)) for value in platform_ids):
        raise RuntimeError("bridge platform allowlist is invalid")
    groups = {6699:["neon-storage-controller"], 9898:["neon-pageserver-"+str(i) for i in range(8)],
              7676:["neon-safekeeper-"+str(i) for i in range(3)], 3081:["neon-compute-"+str(i)+"-control" for i in range(6)]}
    lines = ["resolvers kube", "  parse-resolv-conf", "  timeout resolve 1s", "  timeout retry 1s", "  hold nx 1s", "  hold valid 1s", ""]
    for port, services in groups.items():
        routes = [(service+".managed-platform-"+platform_id+".svc",
                   service+".managed-platform-"+platform_id+".svc.cluster.local",
                   "route_"+str(port)+"_"+platform_id+"_"+service.replace("-","_"))
                  for platform_id in platform_ids for service in services]
        lines += ["frontend management_"+str(port), "  bind :"+str(port), "  mode tcp", "  timeout client 90s", "  tcp-request inspect-delay 5s",
                  "  tcp-request content accept if { req.ssl_hello_type 1 }"]
        lines += ["  use_backend "+backend+" if { req.ssl_sni -i "+sni+" }" for sni,unused,backend in routes]
        lines += [""]
        for unused,target,backend in routes:
            lines += ["backend "+backend, "  mode tcp", "  timeout connect 2s", "  timeout server 90s", "  server target "+target+":"+str(port)+" resolvers kube resolve-prefer ipv4 init-addr last,libc,none check inter 2s", ""]
    return "\n".join(lines)

def manifest(platform_ids, scheduling=None, relay_address="172.18.0.1"):
    # This value comes from the protected host binding, never a workload field.
    if str(ipaddress.IPv4Address(relay_address)) != relay_address:
        raise RuntimeError("bridge relay address is invalid")
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
  server relay RELAY_ADDRESS:19443 ssl ca-file /bridge-config/ca.crt crt /bridge/client.pem verify required verifyhost RELAY_ADDRESS check
\n""".replace("RELAY_ADDRESS", relay_address) + management_routes(platform_ids)
    config_name = "hakopod-neon-control-" + hashlib.sha256(config.encode()).hexdigest()[:16]
    labels = {"app.kubernetes.io/name": "hakopod-neon-control", "hakopod.io/native-acceptance": "neon"}
    ports = [{"name":"https","containerPort":443}] + [{"name":"manage-"+str(port),"containerPort":port} for port in (6699,9898,7676,3081)]
    # The bridge also carries bounded exec streams under runsc. Its former
    # 64 MiB limit killed the process during native TLS and lifecycle checks.
    objects = [
      {"apiVersion":"v1","kind":"ConfigMap","metadata":{"name":config_name,"namespace":NAMESPACE,"labels":labels},"immutable":True,"data":{"haproxy.cfg":config,"ca.crt":protected("ca.crt").decode()}},
      {"apiVersion":"v1","kind":"Secret","metadata":{"name":"hakopod-neon-control-tls","namespace":NAMESPACE,"labels":labels},"type":"Opaque","immutable":True,"data":{name:base64.b64encode(protected(name)).decode() for name in ("service.pem","client.pem")}},
      {"apiVersion":"apps/v1","kind":"Deployment","metadata":{"name":"hakopod-neon-control","namespace":NAMESPACE,"labels":labels},"spec":{"replicas":1,"selector":{"matchLabels":labels},"template":{"metadata":{"labels":labels,"annotations":{"hakopod.io/bridge-config-sha256":hashlib.sha256(config.encode()).hexdigest()}},"spec":{"automountServiceAccountToken":False,"enableServiceLinks":False,"securityContext":{"runAsNonRoot":True,"runAsUser":99,"runAsGroup":99,"fsGroup":99,"sysctls":[{"name":"net.ipv4.ip_unprivileged_port_start","value":"0"}],"seccompProfile":{"type":"RuntimeDefault"}},"containers":[{"name":"bridge","image":IMAGE,"imagePullPolicy":"IfNotPresent","args":["haproxy","-W","-db","-f","/bridge-config/haproxy.cfg"],"ports":ports,"readinessProbe":{"tcpSocket":{"port":"https"},"periodSeconds":2,"timeoutSeconds":1},"resources":{"requests":{"cpu":"25m","memory":"128Mi"},"limits":{"cpu":"100m","memory":"256Mi"}},"securityContext":{"allowPrivilegeEscalation":False,"readOnlyRootFilesystem":True,"capabilities":{"drop":["ALL"]}},"volumeMounts":[{"name":"config","mountPath":"/bridge-config","readOnly":True},{"name":"tls","mountPath":"/bridge","readOnly":True}]}],"volumes":[{"name":"config","configMap":{"name":config_name,"items":[{"key":"haproxy.cfg","path":"haproxy.cfg"},{"key":"ca.crt","path":"ca.crt"}],"defaultMode":288}},{"name":"tls","secret":{"secretName":"hakopod-neon-control-tls","items":[{"key":"service.pem","path":"service.pem"},{"key":"client.pem","path":"client.pem"}],"defaultMode":288}}]}}}},
      {"apiVersion":"v1","kind":"Service","metadata":{"name":"hakopod-control","namespace":NAMESPACE,"labels":labels},"spec":{"selector":labels,"ports":[{"name":"https","port":443,"targetPort":"https"}]}},
      {"apiVersion":"networking.k8s.io/v1","kind":"NetworkPolicy","metadata":{"name":"hakopod-neon-control-ingress","namespace":NAMESPACE,"labels":labels},"spec":{"podSelector":{"matchLabels":labels},"policyTypes":["Ingress"],"ingress":[{"from":[{"namespaceSelector":{"matchExpressions":[{"key":"hakopod.io/managed-platform-id","operator":"In","values":platform_ids}]},"podSelector":{"matchExpressions":[{"key":"hakopod.io/neon-role","operator":"In","values":["proxy","storage-controller"]}]}}],"ports":[{"protocol":"TCP","port":443}]}]}},
      {"apiVersion":"networking.k8s.io/v1","kind":"NetworkPolicy","metadata":{"name":"hakopod-neon-control-egress","namespace":NAMESPACE,"labels":labels},"spec":{"podSelector":{"matchLabels":labels},"policyTypes":["Egress"],"egress":[{"to":[{"ipBlock":{"cidr":relay_address+"/32"}}],"ports":[{"protocol":"TCP","port":19443}]},{"to":[{"namespaceSelector":{"matchLabels":{"kubernetes.io/metadata.name":"kube-system"}}}],"ports":[{"protocol":"UDP","port":53},{"protocol":"TCP","port":53}]},{"to":[{"namespaceSelector":{"matchExpressions":[{"key":"hakopod.io/managed-platform-id","operator":"In","values":platform_ids}]},"podSelector":{"matchExpressions":[{"key":"hakopod.io/neon-role","operator":"In","values":["storage-controller","pageserver","safekeeper","compute"]}]}}],"ports":[{"protocol":"TCP","port":port} for port in (6699,9898,7676,3081)]}]}}
    ]
    next(item for item in objects if item["kind"] == "Deployment")["spec"]["template"]["spec"].update(scheduling or {})
    return json.dumps({"apiVersion":"v1","kind":"List","items":objects}, separators=(",", ":")).encode()

def allow(kubeconfig, platform_id, scheduling=None):
    if not ID.fullmatch(platform_id):
        raise RuntimeError("platform identity is malformed")
    binding = host_binding()
    relay_address = binding["address"] if binding else "172.18.0.1"
    state = ROOT / "allowed-platforms.json"
    prior,resources = read_bridge_state(state) if state.exists() else (None,None)
    values = prior["platform_ids"] if prior else []
    if not isinstance(values, list) or len(values) > 3 or any(not ID.fullmatch(str(value)) for value in values):
        raise RuntimeError("bridge platform allowlist is invalid")
    values = sorted(set(values) | {platform_id})
    if len(values) > 3 or any(not ID.fullmatch(value) for value in values):
        raise RuntimeError("bridge platform allowlist is invalid")
    if prior is None:
        objects=json.loads(manifest(values,scheduling,relay_address))["items"]
        config_name=next(item for item in objects if item["kind"]=="ConfigMap")["metadata"]["name"]
        resources=bridge_resources([config_name])
        prior = install_owned(kubeconfig, NAMESPACE, state, {"platform_ids":values,"config_names":[config_name]}, objects, resources)
    else:
        if set(recover_owned(kubeconfig, NAMESPACE, state, prior, resources)) != set(resources):
            raise RuntimeError("bridge install is incomplete; cleanup is required")
        if scheduling:
            current = json.loads(kube(kubeconfig, "-n", NAMESPACE, "get", "deployment", "hakopod-neon-control", "-o", "json"))
            pod = current["spec"]["template"]["spec"]
            if {key:pod[key] for key in ("nodeSelector", "tolerations", "runtimeClassName", "affinity") if key in pod} != scheduling or pod.get("nodeName"):
                raise RuntimeError("existing bridge scheduling differs")
        prior["platform_ids"] = values; write_state(state, prior)
        refresh_bridge(kubeconfig,state,prior,json.loads(manifest(values,scheduling,relay_address))["items"])
    prior["platform_ids"] = values; write_state(state, prior)
    if len(values)==1:
        kube(kubeconfig, "-n", NAMESPACE, "rollout", "status", "deployment/hakopod-neon-control", "--timeout=180s")

def stop_host():
    # Network/API failures must not leave the host relay listening. Retain the
    # original protected TLS inputs, but remove the service-readable copies.
    before = run(["systemctl", "show", UNIT, "--property=ActiveState", "--value"]).strip()
    if before not in ("inactive", "failed"):
        run(["systemctl", "stop", UNIT])
    if run(["systemctl", "show", UNIT, "--property=ActiveState", "--value"]).strip() not in ("inactive", "failed"):
        raise RuntimeError("host relay is still active")
    runtime = ROOT / "host-runtime"
    if runtime.is_symlink():
        raise RuntimeError("host relay runtime directory changed")
    for name in ("relay.pem", "ca.crt"):
        target = runtime / name
        if not target.exists() and not target.is_symlink():
            continue
        info = target.lstat()
        if not stat.S_ISREG(info.st_mode) or info.st_uid != 0 or info.st_nlink != 1 or info.st_mode & 0o777 != 0o440 or target.read_bytes() != protected(name):
            raise RuntimeError("host relay TLS copy changed")
        target.unlink()

def cleanup(kubeconfig):
    state = ROOT / "allowed-platforms.json"
    try:
        if state.exists():
            value,resources=read_bridge_state(state)
            cleanup_owned(kubeconfig,NAMESPACE,state,value,resources)
        else:
            configs=json.loads(kube(kubeconfig,"-n",NAMESPACE,"get","configmaps","-l","hakopod.io/native-acceptance=neon","-o","json")).get("items",[])
            if len(configs)>3 or configs or any(observe(kubeconfig, NAMESPACE, resource) is not None for resource in BRIDGE_FIXED_RESOURCES):
                raise RuntimeError("bridge resources exist without an ownership journal")
        state.unlink(missing_ok=True)
    finally:
        stop_host()

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
