#!/usr/bin/env python3
"""Install the pinned Community controller only into k3d-hakopod-dev."""
import hashlib
import os
from pathlib import Path
import subprocess
import tempfile
from urllib.request import urlopen

import yaml

SOURCE = "https://raw.githubusercontent.com/mongodb/mongodb-kubernetes/75fa89bca8c1395a1beb0f244723f255f67f8719/"
CHECKSUMS = {
    "config/crd/bases/mongodb.com_mongodbsearch.yaml": "66bd0337d42b84388a1d7a41ce03fe400350d93130a9f941bc98cc6f77b55e01",
    "config/crd/bases/mongodbcommunity.mongodb.com_mongodbcommunity.yaml": "92e68cbb8c40c8a67c04a7f9d0c2c6fe0f707f0fed94161cd8d0846f975a79c1",
    "helm_chart/Chart.yaml": "4f4ec1dc11e61fd2fa810ff9599ef945fd6463f357e1b05337d464bd77beca49",
    "helm_chart/templates/_helpers.tpl": "7ca373988aa34e96a94d7785e7be6b65326ab8111882a586f6547f2c015fb1e3",
    "helm_chart/templates/database-roles.yaml": "0a834ca7a64149a24ee8906ad94c3befb8ff0481000ecf35f5afd708076086b1",
    "helm_chart/templates/mongodbcommunity_cr_with_tls.yaml": "2a6c3f119a961533c12a4a8e1157bfa2df3531a5e6645d04220192ced805375d",
    "helm_chart/templates/operator-roles-base.yaml": "f79ad6abaf222302d72a51648cf41947d3bb1576866dcd7bcbc4a88b2ce9d25f",
    "helm_chart/templates/operator-roles-clustermongodbroles.yaml": "4dcaed8e0593e92b4f6810aacf28f7e0d68ba1ca20b8cfa60abe5c87df847202",
    "helm_chart/templates/operator-roles-pvc-resize.yaml": "dbf4da952946db0866648525aafb9a4a10def047a673e6d93ee3381992e803df",
    "helm_chart/templates/operator-roles-telemetry.yaml": "f18050f1b22b54c1e112de0ecdfc967a6b8b54be86778f273fa9aa733ff26a23",
    "helm_chart/templates/operator-roles-webhook.yaml": "eeb3bb5def95823444f6c847d757d49416898f053f4050074b352280594805c0",
    "helm_chart/templates/operator-sa.yaml": "d40e51a3af87c60473cd110a47a7259a9fe3c653700623ef075bbabc049232d3",
    "helm_chart/templates/operator.yaml": "08b73ab9d168e70c2f115d2ac31b615350f2435c1d5906911dcf7595dcc682ee",
    "helm_chart/templates/secret-config.yaml": "8c8bca9c959d9684e1b2261611f6afc6fea81674c7d77b66ab7e73e04341052b",
    "helm_chart/values.yaml": "04f2e24d11c23bb91b298446ccdbb4b0b82fdbb0ca9913fa3ab223f0c18db1d4",
}

kube = ["kubectl", "--kubeconfig", os.environ["HAKOPOD_TEST_KUBECONFIG"], "--context", "k3d-hakopod-dev"]
contexts = subprocess.check_output(kube + ["config", "get-contexts", "-o", "name"], timeout=15).decode().splitlines()
if "k3d-hakopod-dev" not in contexts:
    raise SystemExit("The named development cluster is required")

with tempfile.TemporaryDirectory(prefix="mongodb-controller-") as temporary:
    root = Path(temporary)
    for name, expected in CHECKSUMS.items():
        with urlopen(SOURCE + name, timeout=30) as response:
            data = response.read(8 * 1024 * 1024)
        if hashlib.sha256(data).hexdigest() != expected:
            raise SystemExit("MongoDB controller source checksum did not match")
        target = root / name
        target.parent.mkdir(parents=True, exist_ok=True)
        target.write_bytes(data)
    values = {
        "operator": {
            "watchNamespace": "*", "watchedResources": ["mongodbcommunity"],
            "enableClusterMongoDBRoles": False, "createResourcesServiceAccountsAndRoles": False,
            "telemetry": {"enabled": False, "installClusterRole": False, "send": {"enabled": False}},
            "maxConcurrentReconciles": 1,
            "nodeSelector": {"kubernetes.io/arch": "amd64", "kubernetes.io/hostname": "k3d-hakopod-dev-server-0"},
            "resources": {"requests": {"cpu": "100m", "memory": "256Mi"}, "limits": {"cpu": "500m", "memory": "512Mi"}},
            "operator_image_name": "managed-mongodb-operator",
            "version": "1.13.0-hakopod.1@sha256:7ba576ba3115f007fcfc22c38b530ad05c211705a6a958124ec41ea80239a85c",
        },
        "registry": {"operator": "ghcr.io/hakopod"},
        "community": {"agent": {"version": "109.0.0.9285-1@sha256:2d819aff81c5d9be80017791ad9e6f8c0fef828b476c9ed1cba8fe9838b07cd2"}},
        "versionUpgradeHook": {"version": "1.0.10@sha256:65e653bd319022328102bbd4aa8559810fe10282608af359e34e224e98116652"},
        "readinessProbe": {"version": "1.0.24@sha256:dd861b4254ebf0458c460e9445fcda89a5b101ad39f9719fd09c33a99511d928"},
    }
    (root / "values.yaml").write_text(yaml.safe_dump(values))
    rendered = subprocess.check_output(["helm", "template", "mongodb-kubernetes-operator", str(root / "helm_chart"), "--namespace", "mongodb-system", "-f", str(root / "values.yaml")], timeout=30)
    objects = [{"apiVersion": "v1", "kind": "Namespace", "metadata": {"name": "mongodb-system", "labels": {"app.kubernetes.io/managed-by": "hakopod-development"}}}]
    for obj in yaml.safe_load_all(rendered):
        if not obj:
            continue
        if obj["kind"] in ("Role", "ClusterRole"):
            obj["rules"] = [rule for rule in obj["rules"] if not any(group in ("mongodb.com", "ai.mongodb.com") for group in rule.get("apiGroups", []))]
            if obj["metadata"]["name"] == "mongodb-kubernetes-operator":
                # The Community reconciler registers this informer even when
                # the separate Search reconciler is disabled.
                obj["rules"].append({"apiGroups": ["mongodb.com"], "resources": ["mongodbsearch"], "verbs": ["get", "list", "watch"]})
        objects.append(obj)
    (root / "operator.yaml").write_text(yaml.safe_dump_all(objects))
    subprocess.run(kube + ["apply", "--server-side", "-f", str(root / "config/crd/bases/mongodbcommunity.mongodb.com_mongodbcommunity.yaml")], check=True, timeout=60)
    subprocess.run(kube + ["apply", "--server-side", "-f", str(root / "config/crd/bases/mongodb.com_mongodbsearch.yaml")], check=True, timeout=60)
    subprocess.run(kube + ["apply", "--server-side", "-f", str(root / "operator.yaml")], check=True, timeout=60)
    subprocess.run(kube + ["rollout", "status", "deployment/mongodb-kubernetes-operator", "-n", "mongodb-system", "--timeout=240s"], check=True, timeout=250)
