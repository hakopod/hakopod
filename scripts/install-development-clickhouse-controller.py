#!/usr/bin/env python3
"""Install the pinned ClickHouse controller in the named development cluster."""
import base64
import hashlib
import os
from pathlib import Path
import secrets
import subprocess
import tempfile
from urllib.request import urlopen

import yaml

SOURCE = "https://raw.githubusercontent.com/Altinity/clickhouse-operator/release-0.27.4/deploy/operator/clickhouse-operator-install-bundle.yaml"
CHECKSUM = "ec58d6c9215d4c8a6edbd8d55e720d7a3eb9643d7eda6fca1c62df286a8856b3"
IMAGE = "docker.io/altinity/clickhouse-operator:0.27.4@sha256:c60c872fedd85017f843167dfdd2b900bb06b668a3deaf02b21782e59af859e3"
NAMESPACE = "clickhouse-operator"
kube = ["kubectl", "--kubeconfig", os.environ["HAKOPOD_TEST_KUBECONFIG"], "--context", "k3d-hakopod-dev"]
contexts = subprocess.check_output(kube + ["config", "get-contexts", "-o", "name"], timeout=15).decode().splitlines()
if "k3d-hakopod-dev" not in contexts:
    raise SystemExit("The named development cluster is required")
with urlopen(SOURCE, timeout=30) as response:
    raw = response.read(8 * 1024 * 1024)
if hashlib.sha256(raw).hexdigest() != CHECKSUM:
    raise SystemExit("ClickHouse operator source checksum did not match")
objects = [{"apiVersion": "v1", "kind": "Namespace", "metadata": {"name": NAMESPACE, "labels": {"app.kubernetes.io/managed-by": "hakopod-development"}}}]
for obj in yaml.safe_load_all(raw):
    if not obj:
        continue
    meta = obj["metadata"]
    if meta.get("namespace") == "kube-system":
        meta["namespace"] = NAMESPACE
    for subject in obj.get("subjects", []):
        if subject.get("namespace") == "kube-system":
            subject["namespace"] = NAMESPACE
    if obj["kind"] == "Secret":
        # Preserve the existing controller credential on idempotent installs.
        found = subprocess.run(kube + ["get", "secret", meta["name"], "-n", NAMESPACE, "-o", "name"], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, timeout=15)
        if found.returncode == 0:
            continue
        obj.pop("stringData", None)
        obj["data"] = {"username": base64.b64encode(b"clickhouse_operator").decode(), "password": base64.b64encode(secrets.token_hex(32).encode()).decode()}
    if obj["kind"] == "ConfigMap" and "config.yaml" in (obj.get("data") or {}):
        config = yaml.safe_load(obj["data"]["config.yaml"])
        config["watch"]["namespaces"]["include"] = ["^hdb-[a-f0-9]{32}$"]
        config["clickhouse"]["access"].update({"scheme": "https", "port": 8443})
        config["reconcile"]["runtime"].update({"reconcileCHIsThreadsNumber": 2, "reconcileCHKsThreadsNumber": 1, "reconcileShardsThreadsNumber": 1})
        config["logger"].update({"v": "0", "stderrthreshold": "ERROR"})
        config["security"]["clickhouse"]["tls"].update({"verify": "Strict", "minVersion": "1.2"})
        obj["data"]["config.yaml"] = yaml.safe_dump(config)
    if obj["kind"] == "Deployment":
        template = obj["spec"]["template"]
        template["metadata"].setdefault("annotations", {})["hakopod.io/clickhouse-security"] = "strict-tls-v1"
        template["metadata"]["annotations"]["hakopod.io/clickhouse-watch"] = "database-namespaces-v1"
        template["metadata"]["annotations"]["hakopod.io/clickhouse-reconcile"] = "two-databases-v1"
        container = next(item for item in template["spec"]["containers"] if item["name"] == "clickhouse-operator")
        container["image"] = IMAGE
        container["imagePullPolicy"] = "IfNotPresent"
        container["resources"] = {"requests": {"cpu": "100m", "memory": "128Mi"}, "limits": {"cpu": "500m", "memory": "384Mi"}}
        template["spec"]["containers"] = [container]
    objects.append(obj)
with tempfile.TemporaryDirectory(prefix="clickhouse-controller-") as temporary:
    path = Path(temporary) / "operator.yaml"
    path.write_text(yaml.safe_dump_all(objects))
    path.chmod(0o600)
    subprocess.run(kube + ["apply", "--server-side", "-f", str(path)], check=True, timeout=90)
    subprocess.run(kube + ["rollout", "status", "deployment/clickhouse-operator", "-n", NAMESPACE, "--timeout=240s"], check=True, timeout=250)
