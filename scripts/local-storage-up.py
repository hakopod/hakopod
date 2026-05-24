#!/usr/bin/env python3
"""Enable the optional pinned local-volume provisioner on hakopod-dev only."""
import json
from pathlib import Path
import subprocess

root = Path(__file__).resolve().parent.parent
kube = ["kubectl", "--kubeconfig", str(root / ".local/kubeconfig"), "--context", "k3d-hakopod-dev"]
manifest = str(root / "deploy/storage/local-path.yaml")
def read(*args):
    output = subprocess.check_output([*kube, *args, "-o", "json"], timeout=20)
    return json.loads(output) if output.strip() else {"items": []}
config = read("config", "view", "--minify")
if config.get("current-context") != "k3d-hakopod-dev":
    raise SystemExit("Refusing storage changes outside the named development cluster")
existing = read("get", "-f", manifest, "--ignore-not-found")
for item in existing.get("items", []):
    if item["metadata"].get("labels", {}).get("app.kubernetes.io/managed-by") != "hakopod":
        raise SystemExit("Refusing to replace an unowned storage resource")
classes = read("get", "storageclasses")
for item in classes.get("items", []):
    if item["metadata"]["name"] != "hakopod-local-path" and item["metadata"].get("annotations", {}).get("storageclass.kubernetes.io/is-default-class") == "true":
        raise SystemExit("Another default storage class is already configured; no changes made")
subprocess.run([*kube, "apply", "-f", manifest], timeout=30, check=True)
subprocess.run([*kube, "rollout", "status", "deployment/local-path-provisioner", "-n", "hakopod-storage", "--timeout=180s"], timeout=190, check=True)
print("Optional development storage is ready. Data is local to its worker; back up databases separately.")
