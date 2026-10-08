#!/usr/bin/env python3
"""Qualify Rabbit Secret SubPath files in a fresh, owned hakopod-dev cluster."""

import argparse
import base64
import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import signal
import socket
import subprocess
import time
import uuid


OWNER = "io.hakopod.rabbit-mount-owner"
HELPER_CLUSTER = "io.hakopod.fixture-cluster"
CLUSTER = "hakopod-dev"
CONTEXT = "k3d-hakopod-dev"
K3S = "rancher/k3s:v1.35.8-k3s1@sha256:59fe491fd3b73204e499e40b325240d85c42c7189c3ae50150d37b78243f3b32"
TOOLS = "ghcr.io/k3d-io/k3d-tools:5.9.0@sha256:f18c1e21aba123fe6ff315c514a0feffc005f587aa3db773e89489c2118842dd"
IDENTIFIER = re.compile(r"[a-f0-9]{64}")


def require(value, message):
    if not value:
        raise RuntimeError(message)


class Fixture:
    def __init__(self, root, k3d):
        self.root, self.k3d = Path(root), str(k3d)
        self.owner = uuid.uuid4().hex
        self.network = "rabbit-mount-" + self.owner
        self.deadline = time.monotonic() + 600
        self.cluster_attempted = False
        self.network_attempted = False
        self.report = {"owner": self.owner, "accepted": False, "cleanup_ok": False}

    def run(self, args, *, data=None, timeout=60, cleanup=False, cluster_diagnostics=False):
        budget = timeout if cleanup else min(timeout, max(0.1, self.deadline - time.monotonic()))
        result = subprocess.run(args, input=data, capture_output=True, timeout=budget)
        if result.returncode != 0 and cluster_diagnostics:
            # Only cluster creation uses this path; never retain kubeconfig or Secret input.
            self.report["cluster_creation_output"] = (result.stdout + result.stderr).decode(errors="replace")[-8192:]
        require(result.returncode == 0, Path(args[0]).name + " command failed with exit " + str(result.returncode))
        return result.stdout.decode()

    def save(self):
        pending = self.root / "report.pending"
        pending.write_text(json.dumps(self.report, indent=2) + "\n")
        pending.replace(self.root / "report.json")

    def docker_objects(self, kind, *, cleanup=False):
        flag = ["--all"] if kind == "container" else []
        references = self.run(["docker", kind, "ls", *flag, "--filter", "label=" + OWNER + "=" + self.owner, "--quiet", "--no-trunc"], cleanup=cleanup).split()
        return [json.loads(self.run(["docker", kind, "inspect", ref], cleanup=cleanup))[0] for ref in references]

    def verify_absent(self):
        clusters = json.loads(self.run([self.k3d, "cluster", "list", "-o", "json"]))
        require(not any(item["name"] == CLUSTER for item in clusters), "Existing development cluster must remain untouched")
        existing = self.run(["docker", "container", "ls", "--all", "--filter", "label=k3d.cluster=" + CLUSTER, "--quiet"])
        require(not existing.strip(), "Existing development node must remain untouched")
        with socket.socket() as probe:
            probe.bind(("127.0.0.1", 16443))

    def verify_node(self, identifier, *, helper=False):
        require(IDENTIFIER.fullmatch(identifier), "Development node identity is not exact")
        objects = json.loads(self.run(["docker", "container", "inspect", identifier]))
        require(len(objects) == 1, "Development node inspection is ambiguous")
        node = objects[0]
        labels = node.get("Config", {}).get("Labels", {})
        name = "/k3d-hakopod-dev-tools" if helper else "/k3d-hakopod-dev-server-0"
        cluster_label = HELPER_CLUSTER if helper else "k3d.cluster"
        require(node.get("Id") == identifier and node.get("Name") == name and
                labels.get(OWNER) == self.owner and labels.get(cluster_label) == CLUSTER and
                node.get("State", {}).get("Running") is True, "Development node identity or owner changed")
        network = node.get("NetworkSettings", {}).get("Networks", {}).get(self.network, {})
        require(network.get("NetworkID") == self.report.get("network_id"), "Development node network changed")
        if helper:
            require(node.get("Config", {}).get("Image") == TOOLS and not node.get("Mounts"),
                    "Development helper image or mounts changed")
        return node

    def verify_cluster(self):
        self.verify_node(self.report["node_id"])
        namespace = json.loads(self.kube(["get", "namespace", "kube-system", "-o", "json"]))
        require(namespace["metadata"]["uid"] == self.report["cluster_uid"], "Development cluster identity changed")

    def start(self):
        self.verify_absent()
        self.network_attempted = True
        self.save()
        identifier = self.run(["docker", "network", "create", "--label", OWNER + "=" + self.owner, self.network]).strip()
        require(IDENTIFIER.fullmatch(identifier), "Docker network identity was not exact")
        self.report["network_id"] = identifier
        # k3d 5.9 always needs a tools node, even without an image volume. Own a
        # bounded helper before cluster creation; no Docker socket is required.
        helper = self.run(["docker", "run", "--detach", "--name", "k3d-hakopod-dev-tools", "--network", self.network,
                           "--label", OWNER + "=" + self.owner, "--label", "app=k3d", "--label", HELPER_CLUSTER + "=" + CLUSTER,
                           "--cpus", "0.1", "--memory", "64m", "--memory-swap", "64m", "--pids-limit", "32",
                           "--user", "65532:65532", "--cap-drop=ALL", "--security-opt=no-new-privileges", "--read-only",
                           TOOLS, "noop"], timeout=90).strip()
        self.report["helper_id"] = helper
        self.verify_node(helper, helper=True)
        self.cluster_attempted = True
        self.save()
        self.run([self.k3d, "cluster", "create", CLUSTER, "--servers", "1", "--agents", "0", "--image", K3S,
                  "--servers-memory", "2g", "--network", self.network, "--no-lb", "--no-image-volume", "--no-rollback",
                  "--api-port", "127.0.0.1:16443", "--runtime-label", OWNER + "=" + self.owner + "@server:0",
                  "--k3s-arg", "--disable=traefik,servicelb,local-storage,metrics-server,coredns@server:0",
                  "--k3s-arg", "--secrets-encryption@server:0", "--k3s-arg", "--kubelet-arg=pod-max-pids=128@server:0",
                  "--kubeconfig-update-default=false", "--kubeconfig-switch-context=false", "--wait", "--timeout", "180s"], timeout=210, cluster_diagnostics=True)
        nodes = self.docker_objects("container")
        require(len(nodes) == 1 and nodes[0]["Config"]["Labels"].get("k3d.cluster") == CLUSTER,
                "Created node ownership is ambiguous")
        self.report["node_id"] = nodes[0]["Id"]
        self.verify_node(nodes[0]["Id"])
        self.run(["docker", "update", "--cpus", "2", "--memory", "2g", "--memory-swap", "2g", nodes[0]["Id"]])
        config = self.run([self.k3d, "kubeconfig", "get", CLUSTER])
        descriptor = os.open(self.root / "kubeconfig", os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
        with os.fdopen(descriptor, "w") as output:
            output.write(config)
        require(self.kube(["config", "current-context"]).strip() == CONTEXT, "Wrong disposable cluster context")
        self.kube(["wait", "--for=condition=Ready", "node", "--all", "--timeout=120s"], timeout=130)
        namespace = json.loads(self.kube(["get", "namespace", "kube-system", "-o", "json"]))
        self.report["cluster_uid"] = namespace["metadata"]["uid"]
        self.save()

    def kube(self, args, **kwargs):
        return self.run(["kubectl", "--kubeconfig", str(self.root / "kubeconfig"), "--context", CONTEXT, *args], **kwargs)

    def check(self, binary):
        material = self.root / "secret-material"
        material.mkdir(mode=0o700)
        environment = dict(os.environ, RABBIT_PRIVATE_MOUNT_GENERATE=str(material))
        result = subprocess.run([str(binary), "-test.run=^TestPrivateMountedSecretConfiguration$", "-test.v"], env=environment, capture_output=True, timeout=30)
        require(result.returncode == 0, "Synthetic certificate generation failed")
        names = {"server.key", "authority.key", "world.key", "server.crt", "authority.crt", "ca.crt", "operator.yml", "default-group.yml", "wrong-group.yml", "world-key.yml"}
        require({path.name for path in material.iterdir()} == names, "Unexpected synthetic Secret payload")
        build = self.root / "image"
        build.mkdir(mode=0o700)
        shutil.copyfile(binary, build / "mount.test")
        (build / "mount.test").chmod(0o755)
        (build / "Dockerfile").write_text("FROM scratch\nCOPY mount.test /probe/mount.test\nENTRYPOINT [\"/probe/mount.test\"]\n")
        image = "rabbit-private-mount:" + self.owner
        self.run(["docker", "build", "--network=none", "--label", OWNER + "=" + self.owner, "--tag", image, str(build)], timeout=60)
        image_info = json.loads(self.run(["docker", "image", "inspect", image]))[0]
        self.report["probe_image_id"] = image_info["Id"]
        self.report["probe_binary_sha256"] = hashlib.sha256(Path(binary).read_bytes()).hexdigest()
        self.verify_cluster()
        self.run([self.k3d, "image", "import", "--mode", "direct", "--cluster", CLUSTER, image], timeout=90)
        namespace = "rabbit-mount-" + self.owner[:12]
        label = {OWNER: self.owner}
        objects = [{"apiVersion": "v1", "kind": "Namespace", "metadata": {"name": namespace, "labels": label}},
                   {"apiVersion": "v1", "kind": "Secret", "metadata": {"name": "fixture", "namespace": namespace, "labels": label}, "type": "Opaque",
                    "data": {name: base64.b64encode((material / name).read_bytes()).decode() for name in sorted(names)}}]
        mounts = [{"name": "fixture", "mountPath": "/etc/rabbit/fixture/" + name, "subPath": name, "readOnly": True} for name in sorted(names)]
        items = [{"key": name, "path": name, "mode": 0o440 if name in ("server.key", "authority.key") else 0o444} for name in sorted(names)]
        pod = {"apiVersion": "v1", "kind": "Pod", "metadata": {"name": "probe", "namespace": namespace, "labels": label}, "spec": {
            "automountServiceAccountToken": False, "restartPolicy": "Never", "activeDeadlineSeconds": 90,
            "securityContext": {"runAsNonRoot": True, "runAsUser": 65532, "runAsGroup": 65532, "fsGroup": 65532, "seccompProfile": {"type": "RuntimeDefault"}},
            "containers": [{"name": "probe", "image": image, "imagePullPolicy": "Never", "args": ["-test.run=^TestPrivateMountedSecretConfiguration$", "-test.v"],
                            "env": [{"name": "RABBIT_PRIVATE_MOUNT_ROOT", "value": "/etc/rabbit/fixture"}, {"name": "GOMAXPROCS", "value": "1"}],
                            "securityContext": {"allowPrivilegeEscalation": False, "readOnlyRootFilesystem": True, "capabilities": {"drop": ["ALL"]}},
                            "resources": {"requests": {"cpu": "100m", "memory": "64Mi"}, "limits": {"cpu": "500m", "memory": "256Mi"}}, "volumeMounts": mounts}],
            "volumes": [{"name": "fixture", "secret": {"secretName": "fixture", "items": items}}]}}
        objects.append(pod)
        self.verify_cluster()
        self.kube(["create", "-f", "-"], data=json.dumps({"apiVersion": "v1", "kind": "List", "items": objects}).encode())
        deadline = min(self.deadline, time.monotonic() + 100)
        while time.monotonic() < deadline:
            actual = json.loads(self.kube(["get", "pod", "probe", "-n", namespace, "-o", "json"]))
            require(actual["metadata"]["labels"].get(OWNER) == self.owner, "Probe pod owner changed")
            if actual["status"].get("phase") in ("Succeeded", "Failed"):
                break
            time.sleep(1)
        logs = self.kube(["logs", "probe", "-n", namespace], timeout=15)
        (self.root / "probe.log").write_text(logs)
        require(actual["status"].get("phase") == "Succeeded", "Mounted Secret probe did not succeed")
        status = actual["status"].get("containerStatuses", [])
        require(len(status) == 1 and status[0].get("restartCount") == 0 and
                status[0].get("state", {}).get("terminated", {}).get("exitCode") == 0,
                "Probe restarted or did not exit successfully")
        node = self.verify_node(self.report["node_id"])
        require(not node["State"].get("OOMKilled"), "Development node reported an out-of-memory exit")
        require("RABBIT_PRIVATE_MOUNT_ACCEPTED" in logs and "--- PASS: TestPrivateMountedSecretConfiguration" in logs and "--- SKIP:" not in logs,
                "Mounted Secret acceptance did not execute all checks")
        self.report.update(accepted=True, namespace=namespace, pod_uid=actual["metadata"]["uid"], mount_count=len(mounts),
                           pod_image_id=status[0].get("imageID"), security_context=actual["spec"]["securityContext"])
        self.save()

    def cleanup(self):
        failures = []
        for kind in ("container", "network"):
            try:
                objects = self.docker_objects(kind, cleanup=True)
                for item in objects:
                    identifier = item["Id"]
                    labels = item["Config"]["Labels"] if kind == "container" else item["Labels"]
                    require(IDENTIFIER.fullmatch(identifier) and labels.get(OWNER) == self.owner, "Cleanup owner changed")
                    if kind == "container":
                        cluster_label = HELPER_CLUSTER if item["Name"] == "/k3d-hakopod-dev-tools" else "k3d.cluster"
                        require(item["Name"] in ("/k3d-hakopod-dev-server-0", "/k3d-hakopod-dev-tools") and
                                labels.get(cluster_label) == CLUSTER, "Unexpected owned container")
                        self.run(["docker", "container", "rm", "--force", "--volumes", identifier], timeout=30, cleanup=True)
                    else:
                        require(item["Name"] == self.network and not item.get("Containers"), "Owned network still has attached resources")
                        self.run(["docker", "network", "rm", identifier], timeout=20, cleanup=True)
                require(not self.docker_objects(kind, cleanup=True), "Owned resources remain after cleanup")
            except Exception as error:
                failures.append(str(error))
        self.report["cleanup_errors"] = failures
        self.report["cleanup_ok"] = not failures
        self.save()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--root", required=True, type=Path)
    parser.add_argument("--k3d", required=True, type=Path)
    parser.add_argument("--binary", required=True, type=Path)
    args = parser.parse_args()
    require(os.environ.get("GITHUB_ACTIONS") == "true" and os.environ.get("GITHUB_REPOSITORY") == "hakopod/hakopod", "Only the repository's disposable CI runner may execute this fixture")
    args.root.mkdir(mode=0o700)
    fixture = Fixture(args.root, args.k3d)
    def interrupted(_number, _frame):
        raise RuntimeError("Fixture interrupted")
    signal.signal(signal.SIGTERM, interrupted)
    signal.signal(signal.SIGINT, interrupted)
    try:
        fixture.start()
        fixture.check(args.binary)
    except Exception as error:
        fixture.report["error"] = str(error)
    finally:
        fixture.cleanup()
    print(json.dumps({"accepted": fixture.report["accepted"], "cleanup_ok": fixture.report["cleanup_ok"]}))
    raise SystemExit(0 if fixture.report["accepted"] and fixture.report["cleanup_ok"] else 1)


if __name__ == "__main__":
    main()
