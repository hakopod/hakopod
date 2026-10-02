#!/usr/bin/env python3
"""Focused tests for fail-closed Neon qualification tooling."""
import hashlib, importlib.util, json, tempfile, unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
def load(name, path):
    spec = importlib.util.spec_from_file_location(name, path); module = importlib.util.module_from_spec(spec); spec.loader.exec_module(module); return module
VERIFY = load("verify_neon", ROOT / "release/verify-neon-runtime.py")
RECORD = load("record_neon", ROOT / "release/record-neon-qualification.py")
PRODUCER = load("neon_evidence", ROOT / "examples/neon-native-acceptance/evidence.py")

def reference(name, digit): return "registry.example/neon-" + name + "@sha256:" + digit * 64
def images():
    storage = reference("storage", "1")
    return {
        "broker": storage,
        "compute": reference("compute-v17", "3"),
        "compute-tls": reference("compute-tls", "4"),
        "controller-database": reference("controller-database", "5"),
        "pageserver": storage,
        "proxy": storage,
        "safekeeper": storage,
        "storage-controller": storage,
    }
def identities(values): return {name: {"uid": 10001, "gid": 10001, "image": value} for name, value in values.items()}
def build():
    values = images()
    stage_images = {"storage": values["storage-controller"], "compute-tools": reference("compute-tools", "2"), "compute-runtime": values["compute"]}
    stages = {
        stage: {
            "image": image,
            "manifest_digest": image.rsplit("@", 1)[1],
            "config_digest": "sha256:" + str(index + 6) * 64,
            "binary_sha256": {"/bin/" + name: "9" * 64 for name in VERIFY.BINARY_NAMES[stage]},
            "published": True,
        }
        for index, (stage, image) in enumerate(sorted(stage_images.items()))
    }
    metadata = {
        "upstream_repository": "https://github.com/neondatabase/neon.git",
        "upstream_commit": "a" * 40,
        "combined_candidate_tree": "b" * 40,
        "postgres_commit": "1" * 40,
        "postgres_tree": "2" * 40,
        "consumer_patch_id": "3" * 40,
        "source_archive": {"sha256": "c" * 64, "size_bytes": 1, "member_count": 1, "repeat_comparison": "PASS", "global_member_order": "PASS"},
        "proxy_patch_sha256": "d" * 64,
        "ownership_patch_sha256": "e" * 64,
        "postgres_patch_sha256": "6" * 64,
        "consumer_patch_sha256": "7" * 64,
        "frozen_combined_patch_sha256": "f" * 64,
    }
    evidence = {
        "schema_version": 1,
        "platform": "linux/amd64",
        "upstream_repository": metadata["upstream_repository"],
        "upstream_commit": metadata["upstream_commit"],
        "candidate_tree": metadata["combined_candidate_tree"],
        "postgres_commit": metadata["postgres_commit"],
        "postgres_tree": metadata["postgres_tree"],
        "consumer_patch_id": metadata["consumer_patch_id"],
        "source_archive": dict(metadata["source_archive"]),
        "patches": {"proxy": metadata["proxy_patch_sha256"], "ownership": metadata["ownership_patch_sha256"], "postgres": metadata["postgres_patch_sha256"], "consumer": metadata["consumer_patch_sha256"], "combined": metadata["frozen_combined_patch_sha256"]},
        "images": values,
        "identities": identities(values),
        "image_stages": stages,
        "helper_images": dict(VERIFY.HELPER_IMAGES),
    }
    return evidence, metadata
def resources():
    return {role: {"platform_id": digit * 32, "namespace": "managed-platform-" + digit * 32, "namespace_uid": "uid-" + role, "create_operation_id": operation * 32} for role, digit, operation in (("source", "6", "a"), ("recovery_target", "7", "b"), ("cancellation_target", "8", "c"))}
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
    def test_build_binds_storage_components_to_storage_stage(self):
        value, metadata=build(); value["images"]["proxy"]=reference("unrelated-proxy","8"); value["identities"]=identities(value["images"])
        with self.assertRaisesRegex(ValueError,"differs from its built stage: storage"): VERIFY.validate_build(value,metadata)
    def test_build_binds_compute_component_to_runtime_stage(self):
        value, metadata=build(); value["images"]["compute"]=reference("unrelated-compute","8"); value["identities"]=identities(value["images"])
        with self.assertRaisesRegex(ValueError,"differs from its built stage: compute-runtime"): VERIFY.validate_build(value,metadata)
    def test_verifier_pulls_each_stage_digest_before_inspection(self):
        value, metadata=build(); VERIFY.validate_build(value,metadata); calls=[]
        stage_by_image={item["image"]:item for item in value["image_stages"].values()}
        def runner(args,config,stdout_limit=None):
            calls.append(tuple(args))
            if args[0]=="pull" or args[0]=="rm": return ""
            if args[:2]==["image","inspect"]:
                reference_value=args[2]; repository,digest=reference_value.rsplit("@",1)
                item={"Os":"linux","Architecture":"amd64","RepoDigests":[repository+"@"+digest],"Config":{"User":"10001:10001"}}
                if reference_value in stage_by_image: item["Id"]=stage_by_image[reference_value]["config_digest"]
                return json.dumps([item])
            if args[0]=="create": return "a"*12
            if args[0]=="cp":
                Path(args[2]).write_bytes(b"binary"); return ""
            raise AssertionError(args)
        stages={name:{**item,"binary_sha256":{path:hashlib.sha256(b"binary").hexdigest() for path in item["binary_sha256"]}} for name,item in value["image_stages"].items()}
        manifests=[]
        VERIFY.verify_images(value["images"],value["identities"],stages,runner,lambda *args: manifests.append(args[:3]))
        self.assertEqual(len(manifests),3)
        for item in stages.values():
            pull=("pull","--platform","linux/amd64",item["image"]); inspect=("image","inspect",item["image"])
            pull_indexes=[index for index,call in enumerate(calls) if call==pull]
            inspect_indexes=[index for index,call in enumerate(calls) if call==inspect]
            self.assertTrue(pull_indexes); self.assertTrue(inspect_indexes)
            self.assertLess(pull_indexes[-1],inspect_indexes[-1])
    def test_remote_manifest_binds_exact_config_descriptor(self):
        config_digest="sha256:"+"b"*64
        manifest={"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json","config":{"mediaType":"application/vnd.oci.image.config.v1+json","digest":config_digest,"size":123},"layers":[]}
        raw=json.dumps(manifest,separators=(",",":")); manifest_digest="sha256:"+hashlib.sha256(raw.encode()).hexdigest()
        runner=lambda args,config: raw
        VERIFY.verify_remote_manifest("registry.example/image@"+manifest_digest,manifest_digest,config_digest,Path("unused"),runner)
        for changed_manifest,changed_config in (("sha256:"+"a"*64,config_digest),(manifest_digest,"sha256:"+"c"*64)):
            with self.assertRaises(ValueError): VERIFY.verify_remote_manifest("registry.example/image@"+manifest_digest,changed_manifest,changed_config,Path("unused"),runner)
    def test_process_inventory(self):
        value=report(); value["process_observations"].pop("proxy")
        with self.assertRaisesRegex(ValueError,"process evidence"): self.validate(value)
    def test_three_nodes(self):
        value=report(); value["environment"]["node_names"]=["a","b"]
        with self.assertRaisesRegex(ValueError,"environment"): self.validate(value)
    def test_separate_target(self):
        value=report(); value["cleanup"]["resources"]["recovery_target"]=dict(value["cleanup"]["resources"]["source"])
        with self.assertRaisesRegex(ValueError,"separate"): self.validate(value)
    def test_separate_cancellation_target(self):
        value=report(); value["cleanup"]["resources"]["cancellation_target"]=dict(value["cleanup"]["resources"]["source"])
        with self.assertRaisesRegex(ValueError,"separate"): self.validate(value)
    def test_claims_stay_false(self):
        value=report(); value["public_endpoint_qualified"]=True
        with self.assertRaisesRegex(ValueError,"cannot qualify"): self.validate(value)
    def test_lsn(self):
        self.assertEqual(VERIFY.lsn_value("1/2"),(1<<32)+2)
        with self.assertRaises(ValueError): VERIFY.lsn_value("0/0")
    def test_revocation_schema(self):
        value={"run_id":"4"*32,"platform_id":"6"*32,"namespace_uid":"uid-source","schema_version":1,"restore_operation_id":"a"*32,"project":"native-neon","environment":"development","source_platform_id":"6"*32,"source_revision":1,"target_platform_id":"8"*32,"target_revision":1,"artifact_id":"b"*32,"manifest_sha256":"c"*64,"tenant_id":"d"*32,"timeline_id":"e"*32,"tenant_generation":1,"timeline_generation":1,"journal_entries":2,"journal_phase_counts":{"complete":2,"empty_complete":0,"untouched_complete":0},"cleanup_pending":False,"operation_authority_refused":True,"cancellation_target_namespace_uid":"uid-cancellation_target","deployment_count":1,"statefulset_count":6,"workload_replicas_zero":True,"pods_absent":True,"staging_prefix_empty":True,"foreign_resources_preserved":True}
        PRODUCER.validate_observation("revocation-cleanup",value,{"run_id":"4"*32,"resources":resources()}); value["pods_absent"]=False
        with self.assertRaisesRegex(ValueError,"revocation-cleanup"): PRODUCER.validate_observation("revocation-cleanup",value,{"run_id":"4"*32,"resources":resources()})
    def test_missing_reports(self):
        with tempfile.TemporaryDirectory() as temporary:
            output=Path(temporary)/"out"
            with self.assertRaises((FileNotFoundError,ValueError)): RECORD.assemble(ROOT,Path(temporary)/"archive",Path(temporary)/"build",Path(temporary)/"report",output)
            self.assertFalse(output.exists())

if __name__ == "__main__": unittest.main()
