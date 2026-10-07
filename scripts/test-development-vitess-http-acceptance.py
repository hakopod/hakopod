import hashlib
import json
from pathlib import Path
import runpy
import tempfile
import unittest
from unittest import mock


RUNNER = runpy.run_path(str(Path(__file__).with_name("run-development-vitess-http-acceptance.py")))


class RetainedInventoryTests(unittest.TestCase):
    def expected(self):
        return {"schema_version": 1, "context": "k3d-hakopod-dev",
                "namespaces": {"hdb-" + "a" * 32: {"uid": "namespace-uid"}},
                "persistent_volumes": {"pvc-retained": {"uid": "volume-uid",
                    "claim_namespace": "hdb-" + "a" * 32, "claim_name": "data"}}}

    def live(self, namespace_uid="namespace-uid", volume_uid="volume-uid", claim_name="data",
             deleting=False, extra=False):
        metadata = {"name": "hdb-" + "a" * 32, "uid": namespace_uid}
        if deleting:
            metadata["deletionTimestamp"] = "2026-10-07T00:00:00Z"
        namespace = {"items": [{"metadata": metadata}]}
        if extra:
            namespace["items"].append({"metadata": {"name": "hdb-" + "b" * 32, "uid": "foreign"}})
        volume = {"items": [{"metadata": {"name": "pvc-retained", "uid": volume_uid},
            "spec": {"claimRef": {"namespace": "hdb-" + "a" * 32, "name": claim_name}},
            "status": {"phase": "Bound"}}]}
        return namespace, volume

    def verify(self, namespace_uid="namespace-uid", volume_uid="volume-uid", claim_name="data",
               deleting=False, extra=False):
        globals_ = RUNNER["verify_retained_inventory"].__globals__
        live = self.live(namespace_uid, volume_uid, claim_name, deleting, extra)
        with mock.patch.dict(globals_, {"command_json": mock.Mock(side_effect=live)}):
            return RUNNER["verify_retained_inventory"](["kubectl"], self.expected())

    def test_exact_uids_and_claim_binding_pass(self):
        self.assertEqual(self.verify()["status"], "verified")

    def test_matching_names_with_changed_namespace_uid_fail(self):
        with self.assertRaisesRegex(RuntimeError, "identity differs"):
            self.verify(namespace_uid="replacement")

    def test_matching_names_with_changed_volume_uid_fail(self):
        with self.assertRaisesRegex(RuntimeError, "identity differs"):
            self.verify(volume_uid="replacement")

    def test_matching_names_with_changed_claim_fail(self):
        with self.assertRaisesRegex(RuntimeError, "identity differs"):
            self.verify(claim_name="other")

    def test_deleting_namespace_fails(self):
        with self.assertRaisesRegex(RuntimeError, "deleting"):
            self.verify(deleting=True)

    def test_foreign_managed_namespace_fails(self):
        with self.assertRaisesRegex(RuntimeError, "identity differs"):
            self.verify(extra=True)

    def test_default_still_rejects_managed_database_residue(self):
        databases = {"metadata": {"name": "hdb-" + "a" * 32}}
        with self.assertRaisesRegex(RuntimeError, "owns the acceptance lane"):
            RUNNER["retained_preflight"](["kubectl"], [databases], Path("/unused"))

    def test_retained_path_and_hash_are_required_together(self):
        with self.assertRaisesRegex(RuntimeError, "supplied together"):
            RUNNER["retained_preflight"](["kubectl"], [], Path("/unused"), Path("/receipt"), None)

    def test_protected_receipt_requires_root_custody_and_exact_hash(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "retained.json"
            raw = (json.dumps(self.expected()) + "\n").encode()
            path.write_bytes(raw)
            path.chmod(0o600)
            value = RUNNER["protected_retained_inventory"](path, hashlib.sha256(raw).hexdigest())
            self.assertEqual(value, self.expected())
            with self.assertRaisesRegex(RuntimeError, "SHA-256 differs"):
                RUNNER["protected_retained_inventory"](path, "0" * 64)


if __name__ == "__main__":
    unittest.main()
