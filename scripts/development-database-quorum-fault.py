#!/usr/bin/env python3
"""Pause only an owned database acceptance container in k3d-hakopod-dev."""
import json
import os
from pathlib import Path
import grp
import pwd
import re
import subprocess
import sys
import time


WATCHDOG_DELAY_SECONDS = 90


def watchdog_unit(database_id, uid):
    return "hakopod-db-fault-" + database_id + "-" + uid


def start_watchdog(flag, kubeconfig, database_id, pod_name, uid):
    # A separate systemd unit survives termination of the test runner's cgroup.
    # Forward only the named context path and the one engine acceptance flag.
    run(["sudo", "systemd-run", "--quiet", "--collect",
         "--unit=" + watchdog_unit(database_id, uid),
         "--property=User=" + pwd.getpwuid(os.getuid()).pw_name,
         "--property=Group=" + grp.getgrgid(os.getgid()).gr_name,
         "--property=CPUQuota=25%", "--property=MemoryMax=256M",
         "--property=TasksMax=32", "--property=Nice=19",
         "--property=RuntimeMaxSec=300s",
         "--setenv=PATH=" + os.environ.get("PATH", "/usr/bin:/bin"),
         "--setenv=HAKOPOD_TEST_KUBECONFIG=" + kubeconfig,
         "--setenv=" + flag + "=1", sys.executable,
         str(Path(__file__).resolve()), "resume-after", database_id, pod_name, uid])


def stop_watchdog(database_id, uid):
    # The exact fixture ID and immutable pod UID also name its rescue unit.
    # It may already have completed; that does not change a verified resume.
    subprocess.run(["sudo", "systemctl", "stop", watchdog_unit(database_id, uid)],
                   stdin=subprocess.DEVNULL, stdout=subprocess.DEVNULL,
                   stderr=subprocess.DEVNULL, timeout=20)


def run(args):
    result = subprocess.run(args, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL,
                            timeout=20, check=True)
    if len(result.stdout) > 2 * 1024 * 1024:
        raise ValueError("development fault inventory exceeds its bound")
    return result.stdout


def main():
    engine = "clickhouse" if os.environ.get("HAKOPOD_CLICKHOUSE_TEST") == "1" else "mongodb" if os.environ.get("HAKOPOD_DATABASE_MONGODB_TEST") == "1" else "mysql"
    flag = "HAKOPOD_CLICKHOUSE_TEST" if engine == "clickhouse" else "HAKOPOD_DATABASE_" + engine.upper() + "_TEST"
    if os.environ.get(flag) != "1":
        raise ValueError("Database acceptance is not enabled")
    resource, kind, server = ("mongodbcommunity", "MongoDBCommunity", "mongod") if engine == "mongodb" else ("innodbcluster", "InnoDBCluster", "mysql")
    if engine == "clickhouse":
        resource, kind, server = "clickhouseinstallation", "ClickHouseInstallation", "keeper"
    action, database_id, pod_name, uid = sys.argv[1:]
    if action not in ("pause", "resume", "resume-after", "verify-paused") or not re.fullmatch(r"[a-f0-9]{32}", database_id):
        raise ValueError("invalid development fault request")
    pattern = r"database-keeper-[0-2]" if engine == "clickhouse" else r"database-[0-6]"
    if not re.fullmatch(pattern, pod_name) or not re.fullmatch(r"[a-f0-9-]{36}", uid):
        raise ValueError("invalid development fault identity")
    kubeconfig = os.environ["HAKOPOD_TEST_KUBECONFIG"]
    kube = ["kubectl", "--kubeconfig", kubeconfig, "--context", "k3d-hakopod-dev"]
    watchdog = action == "resume-after"
    if watchdog:
        time.sleep(WATCHDOG_DELAY_SECONDS)
        action = "resume"
    namespace = "hdb-" + database_id
    ns = json.loads(run(kube + ["get", "namespace", namespace, "-o", "json"]))
    labels = ns["metadata"].get("labels", {})
    if labels.get("hakopod.io/database-id") != database_id or labels.get("app.kubernetes.io/managed-by") != "hakopod":
        raise ValueError("development namespace ownership changed")
    obj = json.loads(run(kube + ["get", resource, "database", "-n", namespace, "-o", "json"]))
    if obj["metadata"].get("labels", {}).get("hakopod.io/database-id") != database_id:
        raise ValueError("development database ownership changed")
    pod = json.loads(run(kube + ["get", "pod", pod_name, "-n", namespace, "-o", "json"]))
    if pod["metadata"]["uid"] != uid or pod["metadata"].get("labels", {}).get("hakopod.io/database-id") != database_id:
        raise ValueError("development member identity changed")
    set_name = "database-keeper" if engine == "clickhouse" else "database"
    statefulset = json.loads(run(kube + ["get", "statefulset", set_name, "-n", namespace, "-o", "json"]))
    if not any(x.get("kind") == "StatefulSet" and x.get("uid") == statefulset["metadata"]["uid"] for x in pod["metadata"].get("ownerReferences", [])):
        raise ValueError("development member owner changed")
    parent_kind, parent_uid = ("Namespace", ns["metadata"]["uid"]) if engine == "clickhouse" else (kind, obj["metadata"]["uid"])
    if not any(x.get("kind") == parent_kind and x.get("uid") == parent_uid for x in statefulset["metadata"].get("ownerReferences", [])):
        raise ValueError("development controller owner changed")
    image = "container-registry.oracle.com/mysql/community-server:8.4.12@sha256:7dcc4add9183664de3a214daf85a50c3ba6cccfd7534f700b6561bf5b41885be"
    if engine == "mongodb":
        image = "quay.io/mongodb/mongodb-community-server:8.0.32-ubi8@sha256:7c905b7efb6d7713ded906b88483d68776fbd37722a47fdaf7444b9ccdb0a86d"
    if engine == "clickhouse":
        image = "docker.io/clickhouse/clickhouse-keeper:26.3.33.24@sha256:3fd59d9efb8c9e9136c3c924ceaa004f65c0b14699f34e4eae4eb63e2f860803"
    if not any(x["name"] == server and x["image"] == image for x in pod["spec"]["containers"]):
        raise ValueError("development member image changed")
    node = pod["spec"]["nodeName"]
    if node not in ("k3d-hakopod-dev-server-0", "k3d-hakopod-database-worker-0"):
        raise ValueError("member is outside the named development nodes")
    node_labels = json.loads(run(["sudo", "docker", "inspect", node, "--format", "{{json .Config.Labels}}"] ))
    if node_labels.get("k3d.cluster") != "hakopod-dev" and node_labels.get("com.hakopod.acceptance") != "database-placement":
        raise ValueError("development node container ownership changed")
    status = next(x for x in pod["status"]["containerStatuses"] if x["name"] == server)
    container_id = status["containerID"].removeprefix("containerd://")
    if not re.fullmatch(r"[a-f0-9]{64}", container_id):
        raise ValueError("invalid development runtime identity")
    ctr = ["sudo", "docker", "exec", node, "ctr", "--namespace", "k8s.io"]
    container = json.loads(run(ctr + ["containers", "info", container_id]))
    runtime_labels = container.get("Labels", {})
    if runtime_labels.get("io.kubernetes.pod.uid") != uid or runtime_labels.get("io.kubernetes.container.name") != server:
        raise ValueError("development runtime owner changed")
    tasks = run(ctr + ["tasks", "list"]).decode().splitlines()
    state = next((x.split()[2] for x in tasks if x.split() and x.split()[0] == container_id), "")
    if action == "pause":
        if state != "RUNNING":
            raise ValueError("development member is not running")
        start_watchdog(flag, kubeconfig, database_id, pod_name, uid)
        run(ctr + ["tasks", "pause", container_id])
    elif action == "verify-paused":
        if state != "PAUSED":
            raise ValueError("development fault ended before its assertion")
    elif state == "PAUSED":
        run(ctr + ["tasks", "resume", container_id])
    elif state != "RUNNING":
        raise ValueError("development task is unavailable")
    tasks = run(ctr + ["tasks", "list"]).decode().splitlines()
    expected = "PAUSED" if action in ("pause", "verify-paused") else "RUNNING"
    if not any(x.split() and x.split()[0] == container_id and x.split()[2] == expected for x in tasks):
        raise ValueError("development runtime did not apply the requested fault state")
    if action == "resume" and not watchdog:
        stop_watchdog(database_id, uid)


if __name__ == "__main__":
    try:
        main()
    except Exception:
        # Never relay raw Kubernetes objects, command stderr or credential data.
        sys.exit("Owned development container fault could not be applied")
