#!/usr/bin/env python3
"""Pure ownership and network contract tests for the native Neon bridge."""
import copy
import importlib.util
import json
from pathlib import Path
import unittest
from unittest import mock

ROOT = Path(__file__).resolve().parent.parent
SPEC = importlib.util.spec_from_file_location("neon_bridge", ROOT / "examples/neon-native-acceptance/control-plane-bridge.py")
BRIDGE = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(BRIDGE)


class Tests(unittest.TestCase):
    def test_ingress_scopes_proxy_and_controller_to_owned_platforms(self):
        platforms = ["a" * 32, "b" * 32]
        with mock.patch.object(BRIDGE, "protected", return_value=b"fixture"):
            manifest = json.loads(BRIDGE.manifest(platforms))
        policy = next(item for item in manifest["items"] if item["metadata"]["name"] == "hakopod-neon-control-ingress")
        rules = policy["spec"]["ingress"]
        self.assertEqual(len(rules), 1)
        self.assertEqual(rules[0]["ports"], [{"protocol": "TCP", "port": 443}])
        self.assertEqual(rules[0]["from"], [{"namespaceSelector": {"matchExpressions": [{"key": "hakopod.io/managed-platform-id", "operator": "In", "values": platforms}]}, "podSelector": {"matchExpressions": [{"key": "hakopod.io/neon-role", "operator": "In", "values": ["proxy", "storage-controller"]}]}}])

    def proxy(self):
        platform = "a" * 32
        secret = "platform-tls-proxy-" + "b" * 16 + "-r1"
        image = "registry.example/current-neon-proxy@sha256:" + "c" * 64
        deployment = {"metadata": {"name": "neon-proxy", "namespace": "managed-platform-" + platform, "uid": "11111111-2222-3333-4444-555555555555", "labels": {"app.kubernetes.io/managed-by": "hakopod", "hakopod.io/managed-platform-id": platform}}, "spec": {"template": {"metadata": {"labels": {"hakopod.io/managed-platform-id": platform, "hakopod.io/neon-role": "proxy"}}, "spec": {"containers": [{"name": "proxy", "image": image}], "volumes": [{"name": "proxy-auth", "secret": {"secretName": secret}}]}}}}
        return platform, secret, image, deployment

    def test_issuer_probe_uses_current_pinned_owned_proxy_image(self):
        platform, secret, image, deployment = self.proxy()
        self.assertEqual(BRIDGE.owned_proxy_image(deployment, platform, secret), image)

    def test_issuer_probe_refuses_foreign_or_deleting_deployment(self):
        platform, secret, _, deployment = self.proxy()
        for field, value in [("namespace", "managed-platform-foreign"), ("uid", "invalid"), ("deletionTimestamp", "now")]:
            changed = copy.deepcopy(deployment)
            changed["metadata"][field] = value
            with self.assertRaisesRegex(RuntimeError, "ownership"):
                BRIDGE.owned_proxy_image(changed, platform, secret)
        changed = copy.deepcopy(deployment)
        changed["spec"]["template"]["metadata"]["labels"]["hakopod.io/managed-platform-id"] = "d" * 32
        with self.assertRaisesRegex(RuntimeError, "ownership"):
            BRIDGE.owned_proxy_image(changed, platform, secret)

    def test_issuer_probe_refuses_mutable_image_or_foreign_tls_snapshot(self):
        platform, secret, _, deployment = self.proxy()
        changed = copy.deepcopy(deployment)
        changed["spec"]["template"]["spec"]["containers"][0]["image"] = "registry.example/proxy:latest"
        with self.assertRaisesRegex(RuntimeError, "image or TLS"):
            BRIDGE.owned_proxy_image(changed, platform, secret)
        with self.assertRaisesRegex(RuntimeError, "image or TLS"):
            BRIDGE.owned_proxy_image(deployment, platform, "foreign-secret")


if __name__ == "__main__":
    unittest.main()
