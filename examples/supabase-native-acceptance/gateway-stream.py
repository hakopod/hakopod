#!/usr/bin/env python3
"""Expose one owned Supabase gateway Pod through the shared raw exec relay."""
import argparse, importlib.util, json, pathlib, re, signal, subprocess, sys, time

UID = re.compile(r"^[0-9a-f]{8}(?:-[0-9a-f]{4}){3}-[0-9a-f]{12}$")

def load_stream():
    path = pathlib.Path(__file__).resolve().parent.parent / "owned-pod-stream.py"
    spec = importlib.util.spec_from_file_location("owned_pod_stream", path)
    module = importlib.util.module_from_spec(spec); spec.loader.exec_module(module)
    return module

STREAM=load_stream()

def resolve_target(args, kube, state):
    ns = kube("get", "namespace", args.namespace, "-o", "json"); ns_labels=ns["metadata"].get("labels", {})
    if ns["metadata"].get("uid") != args.namespace_uid or ns["metadata"].get("deletionTimestamp") or ns_labels.get("hakopod.io/managed-platform-id") != args.platform_id or ns_labels.get("app.kubernetes.io/managed-by")!="hakopod": raise RuntimeError("gateway namespace identity changed")
    listing = kube("-n", args.namespace, "get", "deployments,replicasets,pods", "--chunk-size=32", "-l", "app.kubernetes.io/managed-by=hakopod,hakopod.io/managed-platform-id=" + args.platform_id + ",app.kubernetes.io/component=api-gateway", "-o", "json")
    if listing.get("metadata",{}).get("continue"): raise RuntimeError("gateway inventory is paginated")
    items=listing["items"]
    if len(items)>32: raise RuntimeError("gateway inventory exceeds bounds")
    deployments=[v for v in items if v["kind"]=="Deployment" and v["metadata"]["name"]=="supabase-api-gateway"]
    if len(deployments)!=1: raise RuntimeError("gateway deployment is ambiguous")
    deployment=deployments[0]; dm=deployment["metadata"]; status=deployment.get("status",{}); labels=dm.get("labels",{})
    state.setdefault("workload_uid",dm.get("uid"))
    owners=dm.get("ownerReferences",[])
    if not UID.fullmatch(str(state["workload_uid"])) or dm.get("uid")!=state["workload_uid"] or owners!=[{"apiVersion":"v1","kind":"Namespace","name":args.namespace,"uid":args.namespace_uid}] or labels.get("app.kubernetes.io/component")!="api-gateway" or labels.get("hakopod.io/managed-platform-id")!=args.platform_id or dm.get("deletionTimestamp"): raise RuntimeError("gateway deployment ownership changed")
    if status.get("observedGeneration")!=dm.get("generation") or deployment["spec"].get("replicas")!=1 or any(status.get(k)!=1 for k in ("replicas","updatedReplicas","readyReplicas","availableReplicas")): raise STREAM.TargetUnavailable("gateway deployment is rolling out")
    def owned(item,kind,uid):
        owners=item["metadata"].get("ownerReferences",[]); return len(owners)==1 and owners[0].get("kind")==kind and owners[0].get("uid")==uid and owners[0].get("controller") is True
    replicasets=[v for v in items if v["kind"]=="ReplicaSet" and owned(v,"Deployment",dm["uid"]) and not v["metadata"].get("deletionTimestamp")]
    pods=[v for v in items if v["kind"]=="Pod" and any(owned(v,"ReplicaSet",r["metadata"]["uid"]) for r in replicasets) and not v["metadata"].get("deletionTimestamp")]
    if len(pods)!=1: raise STREAM.TargetUnavailable("gateway Pod is rolling out")
    pod=pods[0]; owner=pod["metadata"]["ownerReferences"][0]; rs=next(r for r in replicasets if r["metadata"]["uid"]==owner["uid"])
    containers=pod["spec"].get("containers",[]); template_containers=deployment["spec"]["template"]["spec"].get("containers",[])
    ports={p.get("containerPort") for p in containers[0].get("ports",[])} if len(containers)==1 else set()
    if rs["spec"]["template"].get("spec")!=deployment["spec"]["template"].get("spec") or pod.get("status",{}).get("phase")!="Running" or not any(c.get("type")=="Ready" and c.get("status")=="True" for c in pod.get("status",{}).get("conditions",[])) or pod["spec"].get("runtimeClassName")!="runsc" or pod["spec"].get("automountServiceAccountToken") is not False or len(containers)!=1 or len(template_containers)!=1 or containers[0]["name"]!="api-gateway" or containers[0]["image"]!=args.image or template_containers[0].get("image")!=args.image or 8443 not in ports: raise RuntimeError("gateway Pod runtime differs")
    return {"kubeconfig":args.kubeconfig,"context":args.context,"namespace":args.namespace,"namespace_uid":args.namespace_uid,"pod":pod["metadata"]["name"],"pod_uid":pod["metadata"]["uid"],"owner_kind":"ReplicaSet","owner_name":rs["metadata"]["name"],"owner_uid":rs["metadata"]["uid"],"workload_kind":"Deployment","workload_name":dm["name"],"workload_uid":dm["uid"],"container":"api-gateway","image":containers[0]["image"],"port":8443}

def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--kubeconfig", required=True); parser.add_argument("--context", required=True)
    parser.add_argument("--namespace", required=True); parser.add_argument("--namespace-uid", required=True)
    parser.add_argument("--platform-id", required=True); parser.add_argument("--listen-port", required=True, type=int)
    parser.add_argument("--image", required=True)
    parser.add_argument("--ready-file", required=True); parser.add_argument("--lifetime", type=int, default=7200)
    args = parser.parse_args()
    if args.context != "k3d-hakopod-dev" or not UID.fullmatch(args.namespace_uid) or not re.fullmatch(r"[0-9a-f]{32}", args.platform_id) or not re.fullmatch(r"[^\s@]+@sha256:[0-9a-f]{64}",args.image):
        raise RuntimeError("gateway relay binding is invalid")
    base = ["kubectl", "--request-timeout=15s", "--kubeconfig", args.kubeconfig, "--context", args.context]
    state={}
    def kube(*extra):
        result = subprocess.run(base + list(extra), capture_output=True, timeout=20)
        if result.returncode or len(result.stdout) > 2 << 20 or len(result.stderr) > 2 << 20:
            raise RuntimeError("bounded gateway identity lookup failed")
        return json.loads(result.stdout)
    def target():
        return resolve_target(args,kube,state)
    stream=STREAM
    with stream.OwnedPodForward(args.listen_port,target,16,lifetime=args.lifetime) as relay:
        for signum in (signal.SIGTERM,signal.SIGINT,signal.SIGHUP): signal.signal(signum,lambda *_: relay.stop.set())
        target()
        ready=pathlib.Path(args.ready_file)
        with ready.open("x") as output: output.write(str(relay.listen_port)+"\n")
        while not relay.stop.wait(1): relay.check_healthy()
        relay.check_healthy()

if __name__ == "__main__":
    main()
