#!/usr/bin/env python3
"""Development fixtures for Neon owned proxy discovery and renewal fencing."""
import copy
import importlib.util
import json
from pathlib import Path
import socket
import tempfile
import time
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location("neon_proxy_stream_test", Path(__file__).with_name("proxy-stream.py"))
MODULE = importlib.util.module_from_spec(spec)
spec.loader.exec_module(MODULE)
PLATFORM = "1" * 32
OPERATION = "2" * 32
NAMESPACE = "managed-platform-" + PLATFORM
NS_UID = "11111111-1111-1111-1111-111111111111"
DEPLOYMENT_UID = "22222222-2222-2222-2222-222222222222"
RS_UID = "33333333-3333-3333-3333-333333333333"
POD_UID = "44444444-4444-4444-4444-444444444444"
NEW_UID = "55555555-5555-5555-5555-555555555555"
IMAGE = "example.invalid/development-proxy@sha256:" + "a" * 64


class ProxyTargetTests(unittest.TestCase):
    def setUp(self):
        labels = {"app.kubernetes.io/managed-by": "hakopod", "app.kubernetes.io/component": "proxy",
                  "hakopod.io/managed-platform-id": PLATFORM, "hakopod.io/owner-operation-id": OPERATION}
        self.namespace = {"metadata": {"name": NAMESPACE, "uid": NS_UID, "labels": labels}}
        runtime = {"runtimeClassName": "runsc", "automountServiceAccountToken": False,
                   "containers": [{"name": "proxy", "image": IMAGE, "ports": [{"containerPort": 5432}]}]}
        self.deployment = {"kind": "Deployment", "metadata": {"name": "neon-proxy", "uid": DEPLOYMENT_UID,
                            "generation": 1, "labels": labels, "ownerReferences": [{"apiVersion": "v1", "kind": "Namespace", "name": NAMESPACE, "uid": NS_UID}]},
                           "spec": {"replicas": 1, "template": {"spec": copy.deepcopy(runtime)}},
                           "status": {"observedGeneration": 1, "replicas": 1, "updatedReplicas": 1, "readyReplicas": 1, "availableReplicas": 1}}
        self.replica = {"kind": "ReplicaSet", "metadata": {"name": "neon-proxy-replica", "uid": RS_UID,
                        "ownerReferences": [{"kind": "Deployment", "name": "neon-proxy", "uid": DEPLOYMENT_UID, "controller": True}]},
                        "spec": {"template": {"spec": copy.deepcopy(runtime)}}}
        self.pod = {"kind": "Pod", "metadata": {"name": "neon-proxy-pod", "uid": POD_UID,
                    "ownerReferences": [{"kind": "ReplicaSet", "name": "neon-proxy-replica", "uid": RS_UID, "controller": True}]},
                    "spec": copy.deepcopy(runtime), "status": {"phase": "Running", "conditions": [{"type": "Ready", "status": "True"}]}}
        self.listing = {"items": [self.deployment, self.replica, self.pod]}

    def kube(self, *args):
        return copy.deepcopy(self.namespace if args[:2] == ("get", "namespace") else self.listing)

    def target(self, probe=False):
        return MODULE.ProxyTarget("/private/development-kubeconfig", PLATFORM, NS_UID, OPERATION, IMAGE, self.kube, probe=probe)

    def test_replacement_pod_is_discovered_under_same_workload(self):
        target = self.target()
        first = target()
        self.pod["metadata"].update(name="neon-proxy-replacement", uid=NEW_UID)
        second = target()
        self.assertEqual(first["workload_uid"], second["workload_uid"])
        self.assertNotEqual(first["pod_uid"], second["pod_uid"])

    def test_controller_stream_preserves_exact_ownership_and_port_checks(self):
        self.deployment["metadata"]["name"] = "neon-storage-controller"
        self.deployment["metadata"]["labels"]["app.kubernetes.io/component"] = "storage-controller"
        self.replica["metadata"]["ownerReferences"][0]["name"] = "neon-storage-controller"
        for runtime in (self.deployment["spec"]["template"]["spec"], self.replica["spec"]["template"]["spec"], self.pod["spec"]):
            runtime["containers"][0]["name"] = "storage-controller"
            runtime["containers"][0]["ports"] = [{"containerPort": 6699}]
        target = MODULE.ProxyTarget("/private/development-kubeconfig", PLATFORM, NS_UID, OPERATION, IMAGE, self.kube, component="storage-controller")
        observed = target()
        self.assertEqual((observed["port"], observed["container"], observed["workload_name"]), (6699, "storage-controller", "neon-storage-controller"))
        self.pod["spec"]["containers"][0]["ports"] = [{"containerPort": 5432}]
        with self.assertRaisesRegex(RuntimeError, "runtime differs"):
            target()

    def test_workload_replacement_cannot_be_adopted(self):
        target = self.target()
        target()
        self.deployment["metadata"]["uid"] = NEW_UID
        with self.assertRaisesRegex(RuntimeError, "workload UID changed"):
            target()

    def test_active_stream_is_fenced_then_new_owned_pod_can_connect(self):
        target = self.target()
        calls = []
        def callback():
            calls.append(True)
            if len(calls) == 2:
                self.pod["metadata"].update(name="neon-proxy-replacement", uid=NEW_UID)
            return target()
        with tempfile.TemporaryDirectory() as root:
            fake = Path(root) / "kubectl"
            fake.write_text("#!/usr/bin/env python3\nimport sys\nsys.stdout.buffer.write(sys.stdin.buffer.read())\n")
            fake.chmod(0o700)
            with MODULE.STREAM.OwnedPodForward(0, callback, 2, lifetime=10, kubectl=str(fake)) as relay:
                self.assertEqual(self.exchange(relay, b"stale"), b"")
                self.await_idle(relay)
                relay.check_healthy()
                self.assertEqual(self.exchange(relay, b"renewed"), b"renewed")
                self.await_idle(relay)

    def test_post_stream_workload_substitution_fails_owner_health(self):
        target = self.target()
        calls = []
        def callback():
            calls.append(True)
            if len(calls) == 3: self.deployment["metadata"]["uid"] = NEW_UID
            return target()
        with tempfile.TemporaryDirectory() as root:
            fake = Path(root) / "kubectl"
            fake.write_text("#!/usr/bin/env python3\nimport sys\nsys.stdout.buffer.write(sys.stdin.buffer.read())\n")
            fake.chmod(0o700)
            relay = MODULE.STREAM.OwnedPodForward(0, callback, 2, lifetime=10, kubectl=str(fake)).start()
            try:
                self.exchange(relay, b"response")
                self.await_idle(relay)
                with self.assertRaisesRegex(RuntimeError, "workload UID changed"):
                    relay.check_healthy()
            finally: relay.close()

    def exchange(self, relay, payload):
        with socket.create_connection(("127.0.0.1", relay.listen_port), timeout=3) as client:
            client.sendall(payload); client.shutdown(socket.SHUT_WR)
            result = b""
            while True:
                try: part = client.recv(1024)
                except ConnectionResetError: break
                if not part: break
                result += part
            return result

    def await_idle(self, relay):
        deadline = time.monotonic() + 3
        while relay.workers and time.monotonic() < deadline: time.sleep(.01)
        self.assertFalse(relay.workers)

    def test_namespace_substitution_and_operation_substitution_fail(self):
        for field in ("uid", "operation"):
            with self.subTest(field=field):
                self.setUp()
                if field == "uid": self.namespace["metadata"]["uid"] = NEW_UID
                else: self.namespace["metadata"]["labels"]["hakopod.io/owner-operation-id"] = "9" * 32
                with self.assertRaisesRegex(RuntimeError, "namespace ownership changed"):
                    self.target()()

    def test_labels_do_not_replace_namespace_ownership(self):
        self.deployment["metadata"]["ownerReferences"][0]["uid"] = NEW_UID
        with self.assertRaisesRegex(RuntimeError, "workload ownership differs"):
            self.target()()

    def test_namespace_owner_reference_must_be_exact(self):
        for field, value in (("apiVersion", "foreign/v1"), ("name", "foreign-namespace"), ("controller", True)):
            with self.subTest(field=field):
                self.setUp()
                self.deployment["metadata"]["ownerReferences"][0][field] = value
                with self.assertRaisesRegex(RuntimeError, "workload ownership differs"):
                    self.target()()

    def test_owned_rollout_is_temporarily_unavailable(self):
        self.deployment["status"]["readyReplicas"] = 0
        with self.assertRaises(MODULE.STREAM.TargetUnavailable):
            self.target()()

    def test_foreign_replica_does_not_supply_an_owned_target(self):
        self.replica["metadata"]["ownerReferences"][0]["uid"] = NEW_UID
        with self.assertRaises(MODULE.STREAM.TargetUnavailable):
            self.target()()

    def test_pinned_image_runtime_and_port_are_required(self):
        changes = (("image", "example.invalid/development-proxy:latest"), ("name", "unowned"), ("ports", [{"containerPort": 9999}]))
        for field, value in changes:
            with self.subTest(field=field):
                self.setUp()
                self.pod["spec"]["containers"][0][field] = value
                with self.assertRaisesRegex(RuntimeError, "Pod runtime differs"):
                    self.target()()

    def test_paginated_inventory_is_refused(self):
        self.listing["metadata"] = {"continue": "not-a-complete-inventory"}
        with self.assertRaisesRegex(RuntimeError, "inventory is incomplete or unbounded"):
            self.target()()

    def probe_setup(self, root):
        self.deployment["metadata"]["name"] = "hakopod-neon-wrong-ca"
        self.deployment["metadata"]["ownerReferences"] = []
        self.deployment["metadata"]["annotations"] = {MODULE.INSTALL_KEY: "6" * 32}
        self.replica["metadata"]["ownerReferences"][0]["name"] = "hakopod-neon-wrong-ca"
        journal = {"platform_id": PLATFORM, "namespace_uid": NS_UID, "ca_kind": "wrong", "install_id": "6" * 32,
                   "uids": {name: DEPLOYMENT_UID if name.startswith("deployment/") else NEW_UID for name in MODULE.PROBE_RESOURCES}}
        path = Path(root) / "issuer-probe-state.json"
        path.write_text(json.dumps(journal)); path.chmod(0o600)
        return path, journal

    def test_probe_requires_exact_journal_uid_and_install_marker(self):
        for change in ("uid", "marker", "journal", "owner"):
            with self.subTest(change=change), tempfile.TemporaryDirectory() as root, patch.object(MODULE, "PROBE_ROOT", Path(root)):
                self.setUp()
                path, journal = self.probe_setup(root)
                target = self.target(probe=True)
                self.assertEqual(target()["workload_uid"], DEPLOYMENT_UID)
                if change == "uid": self.deployment["metadata"]["uid"] = NEW_UID
                elif change == "marker": self.deployment["metadata"]["annotations"][MODULE.INSTALL_KEY] = "7" * 32
                elif change == "owner": self.deployment["metadata"]["ownerReferences"] = [{"kind": "Deployment", "uid": NEW_UID}]
                else:
                    journal["ca_kind"] = "correct"
                    path.write_text(json.dumps(journal))
                with self.assertRaises(RuntimeError): target()

    def test_probe_rejects_incomplete_or_permissive_journal(self):
        with tempfile.TemporaryDirectory() as root, patch.object(MODULE, "PROBE_ROOT", Path(root)):
            path, journal = self.probe_setup(root)
            path.chmod(0o644)
            with self.assertRaisesRegex(RuntimeError, "not protected"):
                self.target(probe=True)
            path.chmod(0o600)
            journal["uids"].pop("networkpolicy/hakopod-neon-wrong-ca-egress")
            path.write_text(json.dumps(journal))
            with self.assertRaisesRegex(RuntimeError, "journal identity differs"):
                self.target(probe=True)


if __name__ == "__main__":
    unittest.main()
