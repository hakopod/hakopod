"""Only a qualified, pinned Oracle SIDB definition may enter installation."""

import copy
import hashlib
import json
from pathlib import Path
import tempfile
import unittest
from unittest import mock

import database_controllers as controllers
import oracle_free_controller as oracle


class OracleFreeController(unittest.TestCase):
    def setUp(self):
        self.definition = {"apiVersion": "apiextensions.k8s.io/v1", "kind": "CustomResourceDefinition",
            "metadata": {"name": "singleinstancedatabases.database.oracle.com"},
            "spec": {"group": "database.oracle.com", "scope": "Namespaced", "conversion": {"strategy": "None"},
                     "versions": [{"name": "v4", "served": True, "storage": True, "schema": {"openAPIV3Schema": {"type": "object"}}}]}}
        digest = hashlib.sha256(json.dumps(self.definition["spec"], sort_keys=True, separators=(",", ":")).encode()).hexdigest()
        patcher = mock.patch.object(oracle, "CRD_SPEC_SHA256", digest)
        patcher.start()
        self.addCleanup(patcher.stop)

    def test_exact_definition_is_validated_without_a_controller(self):
        oracle.validate_objects([self.definition])
        for objects in ([], [self.definition, self.definition], [{"apiVersion": "apps/v1", "kind": "Deployment", "metadata": {"name": "controller"}}]):
            with self.subTest(objects=objects), self.assertRaises(ValueError):
                oracle.validate_objects(objects)

    def test_conversion_or_schema_changes_are_rejected(self):
        for field, value in (("conversion", {"strategy": "Webhook"}), ("scope", "Cluster"),
                             ("versions", [{"name": "v4", "served": True, "storage": True, "schema": {"openAPIV3Schema": {"type": "string"}}}])):
            changed = copy.deepcopy(self.definition)
            changed["spec"][field] = value
            with self.subTest(field=field), self.assertRaises(ValueError):
                oracle.validate_objects([changed])

    def test_render_removes_other_versions_and_upstream_conversion(self):
        upstream = copy.deepcopy(self.definition)
        upstream["spec"]["versions"].insert(0, {"name": "v2", "served": True, "storage": False})
        upstream["spec"]["conversion"] = {"strategy": "Webhook"}
        upstream["metadata"]["annotations"] = {"unsafe-conversion": "fixture"}
        raw = json.dumps(upstream).encode()
        with mock.patch.object(oracle, "CRD_SHA256", hashlib.sha256(raw).hexdigest()):
            self.assertEqual(oracle.render_bytes(raw), [self.definition])
            with self.assertRaisesRegex(ValueError, "checksum"):
                oracle.render_bytes(raw + b" ")

    def test_qualified_renderer_stops_before_downloads_on_failed_evidence(self):
        fetch = mock.Mock()
        with self.assertRaisesRegex(ValueError, "missing evidence"):
            oracle.render(Path("source"), Path("scratch"), fetch,
                validate_qualification=mock.Mock(side_effect=ValueError("missing evidence")))
        fetch.assert_not_called()

    def test_legacy_bundles_cannot_enable_oracle(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            raw = json.dumps({"apiVersion": "v1", "kind": "List", "items": [self.definition]}).encode()
            (root / "oracle-free.json").write_bytes(raw)
            (root / "manifest.json").write_text(json.dumps({"schema_version": 1, "files": {"oracle-free.json": hashlib.sha256(raw).hexdigest()}}))
            with self.assertRaisesRegex(ValueError, "schema 3"):
                controllers.load_bundle(root, ["oracle-free"])

    def test_schema3_requires_exact_gate_and_payload(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            files = {}
            for name in ("postgresql", "redis", "mysql", "mongodb", "clickhouse", "oracle-free"):
                raw = json.dumps({"apiVersion": "v1", "kind": "List", "items": [self.definition] if name == "oracle-free" else []}).encode()
                (root / (name + ".json")).write_bytes(raw)
                files[name + ".json"] = hashlib.sha256(raw).hexdigest()
            manifest = {"schema_version": 3, "source_revision": "a" * 40,
                        "managed_runtimes": {"vitess": False, "supabase": False, "neon": False, "oracle-free": True}, "files": files}
            (root / "manifest.json").write_text(json.dumps(manifest))
            self.assertEqual(controllers.load_bundle(root, ["oracle-free"])[1], [self.definition])
            manifest["managed_runtimes"]["oracle-free"] = False
            (root / "manifest.json").write_text(json.dumps(manifest))
            with self.assertRaisesRegex(ValueError, "files differ"):
                controllers.load_bundle(root, ["oracle-free"])


if __name__ == "__main__":
    unittest.main()
