#!/usr/bin/env python3
"""Bind Neon client streams to owned proxy workloads on the development cluster."""
import importlib.util
import json
from pathlib import Path
import re
import threading


def stream_module():
    path = Path(__file__).resolve().parent.parent / "owned-pod-stream.py"
    spec = importlib.util.spec_from_file_location("neon_owned_pod_stream", path)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


STREAM = stream_module()
UID = re.compile(r"^[0-9a-f]{8}(?:-[0-9a-f]{4}){3}-[0-9a-f]{12}$")
PROBE_ROOT = Path("/srv/hakopod-backup-scratch/neon-native-prereq-v1/control-plane-bridge")
INSTALL_KEY = "hakopod.io/native-acceptance-install"
PROBE_RESOURCES = {"configmap/hakopod-neon-wrong-ca", "deployment/hakopod-neon-wrong-ca", "networkpolicy/hakopod-neon-wrong-ca-egress"}


def require(condition, message):
    if not condition:
        raise RuntimeError(message)


def probe_journal(platform_id, namespace_uid):
    path = PROBE_ROOT / "issuer-probe-state.json"
    info = path.lstat()
    require(not path.is_symlink() and path.is_file() and not info.st_mode & 0o077 and
            1 < info.st_size <= 65536, "issuer probe journal is not protected and bounded")
    value = json.loads(path.read_bytes())
    require(set(value) == {"platform_id", "ca_kind", "install_id", "namespace_uid", "uids"} and
            value["platform_id"] == platform_id and value["namespace_uid"] == namespace_uid and
            value["ca_kind"] in ("correct", "wrong") and re.fullmatch(r"[0-9a-f]{32}", str(value["install_id"])) and
            isinstance(value["uids"], dict) and set(value["uids"]) == PROBE_RESOURCES and
            all(UID.fullmatch(str(uid)) for uid in value["uids"].values()), "issuer probe journal identity differs")
    return value


class ProxyTarget:
    def __init__(self, kubeconfig, platform_id, namespace_uid, operation_id, image, kube, probe=False, component="proxy"):
        require(re.fullmatch(r"[0-9a-f]{32}", platform_id) and UID.fullmatch(namespace_uid) and
                re.fullmatch(r"[0-9a-f]{32}", operation_id), "proxy stream platform binding is invalid")
        require(re.fullmatch(r"[^\s@]+@sha256:[0-9a-f]{64}", image), "proxy stream image is not pinned")
        require(component in ("proxy", "storage-controller") and (not probe or component == "proxy"), "stream component is unsupported")
        self.component, self.port = component, 5432 if component == "proxy" else 6699
        self.kubeconfig, self.platform_id = kubeconfig, platform_id
        self.namespace_uid, self.operation_id, self.image = namespace_uid, operation_id, image
        self.namespace, self.kube = "managed-platform-" + platform_id, kube
        self.name = "hakopod-neon-wrong-ca" if probe else "neon-" + component
        self.journal = probe_journal(platform_id, namespace_uid) if probe else None
        self.workload_uid = self.journal["uids"]["deployment/" + self.name] if probe else None
        self.lock = threading.Lock()

    def __call__(self):
        namespace = self.kube("get", "namespace", self.namespace, "-o", "json")
        metadata = namespace["metadata"]
        labels = metadata.get("labels", {})
        require(metadata.get("uid") == self.namespace_uid and not metadata.get("deletionTimestamp") and
                labels.get("hakopod.io/owner-operation-id") == self.operation_id and
                labels.get("hakopod.io/managed-platform-id") == self.platform_id and
                labels.get("app.kubernetes.io/managed-by") == "hakopod", "proxy stream namespace ownership changed")
        if self.journal is not None:
            require(probe_journal(self.platform_id, self.namespace_uid) == self.journal, "issuer probe journal changed")
        listing = self.kube("-n", self.namespace, "get", "deployments,replicasets,pods", "--chunk-size=97", "-l",
                            "hakopod.io/managed-platform-id=" + self.platform_id + ",hakopod.io/neon-role=" + self.component, "-o", "json")
        items = listing.get("items", [])
        require(1 <= len(items) <= 96 and not listing.get("metadata", {}).get("continue"), "proxy stream inventory is incomplete or unbounded")
        matches = [item for item in items if item["kind"] == "Deployment" and item["metadata"]["name"] == self.name]
        require(len(matches) == 1, "proxy stream workload is absent or ambiguous")
        deployment = matches[0]
        dm, ds = deployment["metadata"], deployment["spec"]
        require(UID.fullmatch(str(dm.get("uid"))) and not dm.get("deletionTimestamp"), "proxy stream workload is being replaced")
        with self.lock:
            if self.workload_uid is None:
                self.workload_uid = dm["uid"]
            require(dm["uid"] == self.workload_uid, "proxy stream workload UID changed")
        if self.journal is not None:
            require(dm.get("ownerReferences", []) == [] and dm.get("annotations", {}).get(INSTALL_KEY) == self.journal["install_id"], "issuer probe install ownership changed")
        else:
            refs = dm.get("ownerReferences", [])
            require(refs == [{"apiVersion": "v1", "kind": "Namespace", "name": self.namespace, "uid": self.namespace_uid}] and
                    dm.get("labels", {}).get("app.kubernetes.io/managed-by") == "hakopod" and
                    dm.get("labels", {}).get("app.kubernetes.io/component") == self.component, "proxy stream workload ownership differs")

        def owned(item, kind, uid):
            owners = item["metadata"].get("ownerReferences", [])
            return len(owners) == 1 and owners[0].get("kind") == kind and owners[0].get("uid") == uid and owners[0].get("controller") is True

        replicas = [item for item in items if item["kind"] == "ReplicaSet" and owned(item, "Deployment", dm["uid"]) and not item["metadata"].get("deletionTimestamp")]
        pods = [item for item in items if item["kind"] == "Pod" and
                any(owned(item, "ReplicaSet", replica["metadata"]["uid"]) for replica in replicas) and
                not item["metadata"].get("deletionTimestamp") and item.get("status", {}).get("phase") == "Running" and
                any(condition.get("type") == "Ready" and condition.get("status") == "True" for condition in item.get("status", {}).get("conditions", []))]
        status = deployment.get("status", {})
        if len(pods) != 1 or ds.get("replicas") != 1 or status.get("observedGeneration") != dm.get("generation") or any(status.get(key) != 1 for key in ("replicas", "updatedReplicas", "readyReplicas", "availableReplicas")):
            raise STREAM.TargetUnavailable("owned proxy is rolling out")
        pod = pods[0]
        owner = pod["metadata"]["ownerReferences"][0]
        replica = next(item for item in replicas if item["metadata"]["uid"] == owner["uid"])
        containers = pod["spec"].get("containers", [])
        template = ds["template"]["spec"]
        template_containers = template.get("containers", [])
        require(replica["metadata"]["name"] == owner.get("name") and
                replica["spec"]["template"]["spec"] == template and
                pod["spec"].get("runtimeClassName") == "runsc" and
                pod["spec"].get("automountServiceAccountToken") is False and
                len(containers) == len(template_containers) == 1 and containers[0].get("name") == self.component and
                containers[0].get("image") == template_containers[0].get("image") == self.image and
                any(port.get("containerPort") == self.port for port in containers[0].get("ports", [])), "proxy stream Pod runtime differs")
        return {"kubeconfig": self.kubeconfig, "context": "k3d-hakopod-dev", "namespace": self.namespace,
                "namespace_uid": self.namespace_uid, "pod": pod["metadata"]["name"], "pod_uid": pod["metadata"]["uid"],
                "owner_kind": "ReplicaSet", "owner_name": replica["metadata"]["name"], "owner_uid": replica["metadata"]["uid"],
                "workload_kind": "Deployment", "workload_name": self.name, "workload_uid": dm["uid"],
                "container": self.component, "image": self.image, "port": self.port}
