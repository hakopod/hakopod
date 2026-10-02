#!/usr/bin/env python3
"""Install the ClickHouse compatibility sandbox only on named development nodes."""
import argparse
import hashlib
import json
from pathlib import Path
import subprocess
import tempfile
import time
import uuid

RUNTIME = "hakopod-clickhouse"
LABEL = "hakopod.com.node-restriction.kubernetes.io/clickhouse-runtime"
PROFILE = "systrap-no-patching-v1"
SERVER = "k3d-hakopod-dev-server-0"
WORKER = "k3d-hakopod-database-worker-0"
EXTRA_WORKER = "k3d-hakopod-database-worker-1"
BINARY_DIGESTS = {
    "runsc": "3e0df2fa28f6ff5430b004f92573b81b75f442f78c780e0c85fdf6c2d572817a",
    "containerd-shim-runsc-v1": "ab441cda2625eee7324a5f991b22852724fdd9144ac3ffaf3aac492dc9e46737",
}
SOURCE_ROOTS = (
    "/usr/local/bin",
    "/opt/hakopod/database-runtime/release-20260907.0",
)


def run(args, **kwargs):
    return subprocess.check_output(args, timeout=90, **kwargs)


def lease_id(value):
    try:
        parsed = uuid.UUID(value)
    except ValueError as exc:
        raise argparse.ArgumentTypeError("lease ID must be a UUID") from exc
    if str(parsed) != value:
        raise argparse.ArgumentTypeError("lease ID must be a canonical lowercase UUID")
    return value


def owned_worker(labels, expected_lease_id):
    if labels.get("com.hakopod.acceptance") == "database-placement":
        return True
    return (
        expected_lease_id is not None
        and labels.get("k3d.cluster") == "hakopod-dev"
        and labels.get("com.hakopod.lease-id") == expected_lease_id
    )


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--kubeconfig", required=True)
    parser.add_argument("--kubectl", required=True)
    parser.add_argument("--lease-id", type=lease_id)
    args = parser.parse_args()
    kube = [args.kubectl, "--kubeconfig", args.kubeconfig, "--context", "k3d-hakopod-dev"]
    if run(kube + ["config", "current-context"]).decode().strip() != "k3d-hakopod-dev":
        raise ValueError("Refusing a non-development kubeconfig")
    nodes = json.loads(run(kube + ["get", "nodes", "-o", "json"]))["items"]
    node_names = {n["metadata"]["name"] for n in nodes}
    if not {SERVER, WORKER} <= node_names or not node_names <= {SERVER, WORKER, EXTRA_WORKER}:
        raise ValueError("Refusing unexpected development nodes")
    if EXTRA_WORKER in node_names:
        extra = next(n for n in nodes if n["metadata"]["name"] == EXTRA_WORKER)
        labels = json.loads(run(["sudo", "docker", "inspect", EXTRA_WORKER, "--format", "{{json .Config.Labels}}"] ))
        if not owned_worker(labels, args.lease_id) or not any(c.get("type") == "Ready" and c.get("status") == "True" for c in extra.get("status", {}).get("conditions", [])):
            raise ValueError("Additional development worker ownership or readiness changed")
    processes = run(["ps", "-eo", "args="]).decode().splitlines()
    if any("/cluster.test " in p or p.startswith("go test ") for p in processes):
        raise ValueError("Stop active development acceptance tests before restarting nodes")
    for node in (SERVER, WORKER):
        labels = json.loads(run(["sudo", "docker", "inspect", node, "--format", "{{json .Config.Labels}}"] ))
        expected = labels.get("k3d.cluster") == "hakopod-dev" if node == SERVER else owned_worker(labels, args.lease_id)
        if not expected:
            raise ValueError("Development container ownership changed")
    with tempfile.TemporaryDirectory() as tmp:
        root = Path(tmp)
        for name, digest in BINARY_DIGESTS.items():
            sources = []
            for source_root in SOURCE_ROOTS:
                source = f"{source_root}/{name}"
                exists = subprocess.run(["sudo", "docker", "exec", SERVER, "test", "-f", source], timeout=20).returncode == 0
                if not exists:
                    continue
                observed = run(["sudo", "docker", "exec", SERVER, "sha256sum", source]).decode().split()[0]
                if observed != digest:
                    raise ValueError("Pinned development sandbox binary changed")
                sources.append(source)
            if not sources:
                raise ValueError("Pinned development sandbox binary is unavailable")
            run(["sudo", "docker", "cp", f"{SERVER}:{sources[0]}", str(root / name)])
            if hashlib.sha256((root / name).read_bytes()).hexdigest() != digest:
                raise ValueError("Pinned development sandbox binary changed")
        profile = root / "runsc-clickhouse.toml"
        profile.write_text('binary_name = "/usr/local/bin/runsc"\n[runsc_config]\n  platform = "systrap"\n  systrap-disable-syscall-patching = "true"\n')
        fragment = root / "hakopod-clickhouse.toml"
        fragment.write_text('[plugins."io.containerd.cri.v1.runtime".containerd.runtimes.hakopod-clickhouse]\n  runtime_type = "io.containerd.runsc.v1"\n[plugins."io.containerd.cri.v1.runtime".containerd.runtimes.hakopod-clickhouse.options]\n  TypeUrl = "io.containerd.runsc.v1.options"\n  ConfigPath = "/etc/runsc-clickhouse.toml"\n')
        for node in (WORKER, SERVER):
            # Install the already verified binaries without changing any default
            # runtime or the Actions profile. Refuse conflicting existing files.
            run(["sudo", "docker", "exec", node, "mkdir", "-p", "/usr/local/bin"])
            for name in BINARY_DIGESTS:
                exists = subprocess.run(["sudo", "docker", "exec", node, "test", "-e", f"/usr/local/bin/{name}"], timeout=20).returncode == 0
                if exists:
                    digest = run(["sudo", "docker", "exec", node, "sha256sum", f"/usr/local/bin/{name}"]).decode().split()[0]
                    if digest != BINARY_DIGESTS[name]:
                        raise ValueError("Refusing to replace an existing runtime")
                else:
                    run(["sudo", "docker", "cp", str(root / name), f"{node}:/usr/local/bin/{name}"])
            run(["sudo", "docker", "exec", node, "mkdir", "-p", "/var/lib/rancher/k3s/agent/etc/containerd/config-v3.toml.d"])
            for source, target in ((profile, "/etc/runsc-clickhouse.toml"), (fragment, "/var/lib/rancher/k3s/agent/etc/containerd/config-v3.toml.d/hakopod-clickhouse.toml")):
                exists = subprocess.run(["sudo", "docker", "exec", node, "test", "-e", target], timeout=20).returncode == 0
                if exists and run(["sudo", "docker", "exec", node, "cat", target]) != source.read_bytes():
                    raise ValueError("Refusing conflicting runtime configuration")
                run(["sudo", "docker", "cp", str(source), f"{node}:{target}"])
            run(["sudo", "docker", "restart", node])
            # Ready may initially reflect an old heartbeat. Verify the process
            # has restarted and the new containerd configuration can be read.
            for _ in range(60):
                try:
                    run(["sudo", "docker", "exec", node, "ctr", "plugins", "list"])
                    run(kube + ["wait", "--for=condition=Ready", "node/" + node, "--timeout=5s"])
                    break
                except subprocess.CalledProcessError:
                    time.sleep(2)
            else:
                raise ValueError("Development runtime did not restart")
            run(kube + ["label", "node", node, LABEL + "=" + PROFILE, "--overwrite"])
            print("Installed ClickHouse sandbox on", node, flush=True)
    existing = json.loads(run(kube + ["get", "runtimeclasses", "-o", "json"]))["items"]
    for r in existing:
        if r["metadata"]["name"] == RUNTIME and r["metadata"].get("labels", {}).get("app.kubernetes.io/managed-by") != "hakopod":
            raise ValueError("Refusing an unowned runtime class")
    runtime = {"apiVersion":"node.k8s.io/v1", "kind":"RuntimeClass", "metadata":{"name":RUNTIME,"labels":{"app.kubernetes.io/managed-by":"hakopod"},"annotations":{LABEL:PROFILE}},"handler":RUNTIME,"scheduling":{"nodeSelector":{LABEL:PROFILE}},"overhead":{"podFixed":{"cpu":"20m","memory":"50Mi"}}}
    run(kube + ["apply", "-f", "-"], input=json.dumps(runtime).encode())


if __name__ == "__main__":
    main()
