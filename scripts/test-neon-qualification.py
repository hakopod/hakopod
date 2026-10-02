#!/usr/bin/env python3
"""Focused tests for fail-closed Neon qualification tooling."""
import importlib.util, tempfile, unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
def load(name, path):
    spec = importlib.util.spec_from_file_location(name, path); module = importlib.util.module_from_spec(spec); spec.loader.exec_module(module); return module
VERIFY = load("verify_neon", ROOT / "release/verify-neon-runtime.py")
RECORD = load("record_neon", ROOT / "release/record-neon-qualification.py")
PRODUCER = load("neon_evidence", ROOT / "examples/neon-native-acceptance/evidence.py")

def images(): return {name: "registry.example/neon-" + name + "@sha256:" + "1" * 64 for name in VERIFY.COMPONENTS}
def identities(values): return {name: {"uid": 10001, "gid": 10001, "image": value} for name, value in values.items()}
def resources():
    return {role: {"platform_id": digit * 32, "namespace": "managed-platform-" + digit * 32, "namespace_uid": "uid-" + role, "create_operation_id": ("a" if role == "source" else "b") * 32} for role, digit in (("source", "6"), ("recovery_target", "7"))}
def events(run="4" * 32):
    return [{"sequence": n, "case": case, "run_id": run, "elapsed_seconds": n, "evidence_sha256": "5" * 64} for n, case in enumerate(sorted(VERIFY.CASES), 1)]
def report():
    values = images(); ids = identities(values); run = "4" * 32; sources = {"go.mod": "2" * 64}
    return {"schema_version": 2, "context": "k3d-hakopod-dev", "execution": "native", "platform": "linux/amd64", "passed": True, "exit_code": 0, "limit_error": "", "started_at": "2026-10-02T00:00:00Z", "finished_at": "2026-10-02T00:01:00Z", "elapsed_seconds": 60, "environment": {"cpu_limit": 1, "memory_limit_bytes": 2147483648, "cluster_mutation": True, "cluster_uid": "cluster", "node_names": ["a", "b", "c"]}, "source_files": sources, "source_files_after": sources, "images": values, "identities": ids, "process_observations": {name: [{"pod_uid": "uid-" + name, "process_observation_sha256": "8" * 64}] for name in values}, "test_events": events(run), "failed_cases": [], "runner_sha256": "3" * 64, "producer_sha256": "9" * 64, "event_file_sha256": "5" * 64, "run_id": run, "log_sha256": "5" * 64, "cleanup": {"schema_version": 2, "run_id": run, "context": "k3d-hakopod-dev", "status": "verified", "resources": resources(), "namespaces_absent": True, "persistent_volumes_absent": True}, "public_endpoint_qualified": False, "physical_zones_qualified": False}

class Tests(unittest.TestCase):
    def validate(self, value): VERIFY.validate_acceptance(value, value["source_files"], value["images"], value["identities"])
    def test_complete(self): self.validate(report())
    def test_extra_field(self):
        value=report(); value["invented"]=True
        with self.assertRaisesRegex(ValueError,"missing or malformed"): self.validate(value)
    def test_stale_source(self):
        value=report(); value["source_files_after"]={"x":"3"*64}
        with self.assertRaisesRegex(ValueError,"stale or changed"): self.validate(value)
    def test_failed(self):
        value=report(); value["passed"]=False
        with self.assertRaisesRegex(ValueError,"cannot qualify"): self.validate(value)
    def test_bounded_out(self):
        value=report(); value["limit_error"]="memory"
        with self.assertRaisesRegex(ValueError,"cannot qualify"): self.validate(value)
    def test_missing_case(self):
        with self.assertRaisesRegex(ValueError,"incomplete"): VERIFY.validate_events(events()[:-1],"4"*32,60)
    def test_duplicate_case(self):
        value=events(); value.append(dict(value[0]))
        with self.assertRaisesRegex(ValueError,"duplicate"): VERIFY.validate_events(value)
    def test_event_binding(self):
        value=events(); value[0]["run_id"]="7"*32
        with self.assertRaises(ValueError): VERIFY.validate_events(value,"4"*32,60)
    def test_mutable_image(self):
        value=images(); value["proxy"]="registry/neon:latest"
        with self.assertRaisesRegex(ValueError,"digest-pinned"): VERIFY.validate_images(value)
    def test_identity_image(self):
        value=images(); ids=identities(value); ids["proxy"]["image"]=value["compute"]
        with self.assertRaisesRegex(ValueError,"another image"): VERIFY.validate_identities(ids,value)
    def test_process_inventory(self):
        value=report(); value["process_observations"].pop("proxy")
        with self.assertRaisesRegex(ValueError,"process evidence"): self.validate(value)
    def test_three_nodes(self):
        value=report(); value["environment"]["node_names"]=["a","b"]
        with self.assertRaisesRegex(ValueError,"environment"): self.validate(value)
    def test_separate_target(self):
        value=report(); value["cleanup"]["resources"]["recovery_target"]=dict(value["cleanup"]["resources"]["source"])
        with self.assertRaisesRegex(ValueError,"separate"): self.validate(value)
    def test_claims_stay_false(self):
        value=report(); value["public_endpoint_qualified"]=True
        with self.assertRaisesRegex(ValueError,"cannot qualify"): self.validate(value)
    def test_lsn(self):
        self.assertEqual(VERIFY.lsn_value("1/2"),(1<<32)+2)
        with self.assertRaises(ValueError): VERIFY.lsn_value("0/0")
    def test_revocation_schema(self):
        value={"run_id":"4"*32,"platform_id":"6"*32,"namespace_uid":"uid-source","restore_operation_id":"a"*32,"restore_token_sha256":"b"*64,"access_revoked":True,"revoked_access_refused":True,"compute_closed":True,"proxy_closed":True,"readers_closed":True,"cleanup_replayed":True,"owned_resources_removed":True,"staging_prefix_removed":True,"foreign_resources_preserved":True}
        PRODUCER.validate_observation("revocation-cleanup",value,{"run_id":"4"*32,"resources":resources()}); value["readers_closed"]=False
        with self.assertRaisesRegex(ValueError,"revocation-cleanup"): PRODUCER.validate_observation("revocation-cleanup",value,{"run_id":"4"*32,"resources":resources()})
    def test_missing_reports(self):
        with tempfile.TemporaryDirectory() as temporary:
            output=Path(temporary)/"out"
            with self.assertRaises((FileNotFoundError,ValueError)): RECORD.assemble(ROOT,Path(temporary)/"archive",Path(temporary)/"build",Path(temporary)/"report",output)
            self.assertFalse(output.exists())

if __name__ == "__main__": unittest.main()
