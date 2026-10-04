#!/usr/bin/env python3
"""Focused tests for fail-closed Neon qualification tooling."""
import hashlib, importlib.util, json, tempfile, unittest
from pathlib import Path
from unittest import mock

ROOT = Path(__file__).resolve().parent.parent
def load(name, path):
    spec = importlib.util.spec_from_file_location(name, path); module = importlib.util.module_from_spec(spec); spec.loader.exec_module(module); return module
VERIFY = load("verify_neon", ROOT / "release/verify-neon-runtime.py")
RECORD = load("record_neon", ROOT / "release/record-neon-qualification.py")
PRODUCER = load("neon_evidence", ROOT / "examples/neon-native-acceptance/evidence.py")
DRIVER = load("neon_driver", ROOT / "examples/neon-native-acceptance/driver.py")

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
def release_source(root, values, metadata, qualified=False):
    path = Path(root) / "internal/managedplatform/neon_qualification.go"
    path.parent.mkdir(parents=True, exist_ok=True)
    constants = {
        "NeonReleaseQualificationID": "neon-fa504217-pg17.11-linux-amd64",
        "NeonReleaseSourceArchiveSHA256": metadata["source_archive"]["sha256"],
        "NeonReleasePostgresCommit": metadata["postgres_commit"],
        "NeonReleaseConsumerPatchID": metadata["consumer_patch_id"],
    }
    declarations = [f'const {name} = "{value}"' for name, value in constants.items()]
    rows = [f'\t"{name}": "{value}",' for name, value in sorted(values.items())]
    gate = "true" if qualified else "false"
    path.write_text("\n".join(["package managedplatform", *declarations,
        "var neonReleaseImages = map[string]string{", *rows, "}",
        f"func NeonReleaseQualified() bool {{ return {gate} }}", ""]))
    return path

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
        "transport_patch_sha256": "8" * 64,
        "reconfigure_patch_sha256": "9" * 64,
        "placement_patch_sha256": "0" * 64,
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
        "patches": VERIFY.source_patch_hashes(metadata),
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
    def test_build_requires_each_exact_runtime_patch(self):
        for name in ("transport", "reconfigure", "placement"):
            with self.subTest(patch=name):
                evidence, metadata = build()
                VERIFY.validate_build(evidence, metadata)
                del evidence["patches"][name]
                with self.assertRaisesRegex(ValueError, "patch provenance"):
                    VERIFY.validate_build(evidence, metadata)
                evidence["patches"][name] = "a" * 64
                with self.assertRaisesRegex(ValueError, "patch provenance"):
                    VERIFY.validate_build(evidence, metadata)

    def test_release_gate_and_exact_inventory(self):
        evidence, metadata = build()
        with tempfile.TemporaryDirectory() as temporary:
            for qualified in (False, True):
                release_source(temporary, evidence["images"], metadata, qualified)
                flags = VERIFY.capabilities(temporary, evidence["images"], metadata)
                self.assertIs(flags["release_runtime_qualified"], qualified)
                for field in ("cluster_qualified", "development_cluster_qualified",
                              "encrypted_storage_class_qualified", "public_endpoint_qualified",
                              "physical_zones_qualified"):
                    self.assertIs(flags[field], False)
                changed = dict(evidence["images"], proxy=reference("changed", "8"))
                with self.assertRaisesRegex(ValueError, "compiled release inventory"):
                    VERIFY.release_runtime_qualified(temporary, changed, metadata)

    def test_empty_candidate_inventory_cannot_qualify_a_release(self):
        evidence, metadata = build()
        with tempfile.TemporaryDirectory() as temporary:
            path = release_source(temporary, {}, metadata)
            path.write_text(path.read_text().replace("map[string]string{\n}", "map[string]string{}"))
            self.assertFalse(VERIFY.release_runtime_qualified(temporary, evidence["images"], metadata))
            path.write_text(path.read_text().replace("return false", "return true"))
            with self.assertRaisesRegex(ValueError, "complete digest-pinned"):
                VERIFY.release_runtime_qualified(temporary, evidence["images"], metadata)

    def test_release_contract_rejects_changed_provenance_or_duplicate_declarations(self):
        evidence, metadata = build()
        with tempfile.TemporaryDirectory() as temporary:
            path = release_source(temporary, evidence["images"], metadata, True)
            original = path.read_text()
            changes = (
                original.replace(metadata["postgres_commit"], "4" * 40),
                original.replace(metadata["consumer_patch_id"], "5" * 40),
                original.replace(metadata["source_archive"]["sha256"], "8" * 64),
                original + "func NeonReleaseQualified() bool { return false }\n",
                original.replace('"broker":', '"proxy":'),
            )
            for source in changes:
                path.write_text(source)
                with self.assertRaises(ValueError):
                    VERIFY.release_runtime_qualified(temporary, evidence["images"], metadata)

    def test_release_capability_flags_require_exact_booleans(self):
        evidence, metadata = build()
        with tempfile.TemporaryDirectory() as temporary:
            release_source(temporary, evidence["images"], metadata, True)
            expected = VERIFY.capabilities(temporary, evidence["images"], metadata)
            VERIFY.validate_capabilities(expected, expected)
            for key in expected:
                changed = dict(expected)
                changed[key] = int(expected[key])
                with self.assertRaisesRegex(ValueError, "capability boundary"):
                    VERIFY.validate_capabilities(changed, expected)
            changed = dict(expected, cluster_qualified=True)
            with self.assertRaisesRegex(ValueError, "capability boundary"):
                VERIFY.validate_capabilities(changed, expected)

    def test_closed_release_gate_refuses_before_pull_or_output(self):
        with tempfile.TemporaryDirectory() as temporary:
            output = Path(temporary) / "release-output"
            manifest = {"capability": {"release_runtime_qualified": False}}
            with mock.patch.object(VERIFY, "validate_metadata", return_value=manifest), \
                    mock.patch.object(VERIFY, "verify_images") as pull:
                with self.assertRaisesRegex(ValueError, "release gate is closed"):
                    VERIFY.verify(Path(temporary) / "record", output)
                pull.assert_not_called()
                self.assertFalse(output.exists())

    def test_legacy_development_manifest_cannot_be_used_for_release(self):
        legacy = dict.fromkeys(("platform", "source", "source_files", "images",
                               "identities", "tooling", "files", "capability"))
        legacy.update(schema_version=1, platform="linux/amd64")
        with mock.patch.object(VERIFY, "read_json", return_value=legacy):
            with self.assertRaisesRegex(ValueError, "manifest is malformed"):
                VERIFY.validate_metadata(Path("legacy"))

    def test_authorization_sources_are_sealed(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            for name in VERIFY.SOURCE_DIRS:
                (root / name).mkdir(parents=True, exist_ok=True)
            for name in VERIFY.SOURCE_FILES:
                path = root / name
                path.parent.mkdir(parents=True, exist_ok=True)
                path.write_text("fixture source\n")
            authorization = root / "auth/runtime.go"
            authorization.write_text("original authorization\n")
            before = VERIFY.source_files(root)
            authorization.write_text("changed authorization\n")
            after = VERIFY.source_files(root)
            self.assertNotEqual(before["auth/runtime.go"], after["auth/runtime.go"])

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

    def test_tls_requires_authenticated_proxy_query_after_trust_checks(self):
        logical={"broker-auth","compute-auth","controller-auth","controller-database-password","pageserver-auth","proxy-auth","safekeeper-auth"}
        snapshots={name:{} for name in logical}; common={"status":"passed","ca_fingerprint":"same-ca"}
        observation={"run_id":"4"*32,"platform_id":"6"*32,"namespace_uid":"uid-source","client_verification_enforced":True,"server_verified":True,"plaintext_refused":True,"services":["broker","controller-database","storage-controller","pageserver","safekeeper","compute","compute-sql","proxy"],"compute_sql_names":["compute-0","compute-1"],"managed_tls":{"before":dict(common,snapshots=snapshots),"injection":dict(common,transition_journal_complete=True,near_expiry_leaf_fingerprint="old-leaf"),"renewal":dict(common,snapshots=snapshots,all_snapshots_owned_and_valid=True,served_proxy_leaf_fingerprint="new-leaf")},"control_plane_trust":{"correct_issuer_reached_authentication":True,"wrong_issuer_refused":True,"proxy_authenticated_query":True}}
        state={"run_id":"4"*32,"resources":resources()}
        PRODUCER.validate_observation("tls",observation,state)
        for names in (None,[],["compute-0"],["compute-1","compute-0"],["compute-0","compute-0"]):
            changed=dict(observation,compute_sql_names=names)
            with self.assertRaisesRegex(ValueError,"tls"): PRODUCER.validate_observation("tls",changed,state)
        for replacement in (False,None):
            observation["control_plane_trust"]["proxy_authenticated_query"]=replacement
            with self.assertRaisesRegex(ValueError,"authenticated SQL"): PRODUCER.validate_observation("tls",observation,state)

    def test_failure_isolation_and_limit_observations_are_exact(self):
        state={"run_id":"4"*32,"resources":resources()}; common={"run_id":"4"*32,"platform_id":"6"*32,"namespace_uid":"uid-source"}
        target=dict(common,platform_id="7"*32,namespace_uid="uid-recovery_target")
        observations={
            "isolation-authentication":{**common,"target_platform_id":"7"*32,"target_namespace_uid":"uid-recovery_target","source_tenant_id":"1"*32,"source_timeline_id":"2"*32,"target_tenant_id":"3"*32,"target_timeline_id":"4"*32,"source_marker_verified":True,"target_marker_absent":True,"bad_credentials_refused":True,"cross_platform_credentials_refused":True},
            "connection-limits":{**common,"configured_max_connections":64,"attempted_connections":72,"concurrent_connections":60,"refused_connections":12,"backend_pids":list(range(100,160)),"baseline_backend_pids":[1,2,3,4],"superuser_reserved_connections":4,"reserved_connections":0,"role_superuser":True,"role_reserved":False,"effective_connection_limit":64,"held_markers_flushed":True,"sleeping_backends_verified":True,"baseline_unchanged":True,"service_recovered":True},
            "compute-roles":{**common,"primary_compute":"compute-0","replica_compute":"compute-1","primary_writable":True,"replica_read_only":True,"replica_write_refused":True,"replica_caught_up":True},
            "wal-quorum-fencing":{**target,"safekeeper_count":3,"stopped_safekeepers":2,"stopped_statefulsets":{"neon-safekeeper-1":"set-1","neon-safekeeper-2":"set-2"},"stopped_pod_uids":["old-1","old-2"],"replicas_zero_before_write":True,"replicas_zero_after_refusal":True,"replacement_pods_absent":True,"write_refused_without_quorum":True,"fenced_write_absent":True,"quorum_restored":True,"write_recovered":True},
            "controller-recovery":{**target,"controller_pod_uid_before":"old","controller_pod_uid_after":"new","tenant_id":"1"*32,"timeline_id":"2"*32,"tenant_generation":1,"timeline_generation":2,"identity_preserved":True,"service_recovered":True},
            "object-store-outage":{**common,"outage_observed":True,"backup_refused":True,"service_restored":True},
        }
        for case,value in observations.items():
            PRODUCER.validate_observation(case,value,state)
            broken=dict(value); flag=next(key for key in value if key.endswith(("refused","recovered","restored")))
            broken[flag]=False
            with self.assertRaisesRegex(ValueError,case): PRODUCER.validate_observation(case,broken,state)
        for case in ("wal-quorum-fencing","controller-recovery"):
            with self.assertRaisesRegex(ValueError,"exact fixture"): PRODUCER.validate_observation(case,dict(observations[case],**common),state)
        limits=observations["connection-limits"]
        for changed in ({"held_markers_flushed":False},{"sleeping_backends_verified":False},{"backend_pids":[100]*60},{"concurrent_connections":0},{"refused_connections":1},{"baseline_unchanged":False},{"baseline_backend_pids":[1]},{"role_superuser":False},{"effective_connection_limit":60}):
            with self.assertRaisesRegex(ValueError,"connection-limits"): PRODUCER.validate_observation("connection-limits",dict(limits,**changed),state)
        for field in ("replicas_zero_before_write","replicas_zero_after_refusal","replacement_pods_absent","fenced_write_absent"):
            with self.assertRaisesRegex(ValueError,"wal-quorum-fencing"): PRODUCER.validate_observation("wal-quorum-fencing",dict(observations["wal-quorum-fencing"],**{field:False}),state)
        non_superuser=dict(limits,role_superuser=False,effective_connection_limit=60,baseline_backend_pids=[1],concurrent_connections=59,refused_connections=13,backend_pids=list(range(100,159)))
        PRODUCER.validate_observation("connection-limits",non_superuser,state)
        restart={**target,"storage_pod_uids_before":["storage-old"],"storage_pod_uids_after":["storage-new"],"compute_pod_uids_before":["compute-old"],"compute_pod_uids_after":["compute-new"],"active_storage":{"component":"pageserver-1","node_id":2,"tenant_id":"1"*32,"tenant_generation":7},"restarted_components":{"pageserver":"pageserver-1","compute":"compute-0"},"statefulset_uids":{"pageserver":"storage-set","compute":"compute-set"},"old_pods_absent":True,"failure_observed":True,"service_recovered":True,"data_sha256":"3"*64}
        PRODUCER.validate_observation("restart-failure",restart,state)
        for changed in ({"old_pods_absent":False},{"compute_pod_uids_after":["compute-old"]},{"restarted_components":{"pageserver":"pageserver-1","compute":"compute-1"}},{"restarted_components":{"pageserver":"pageserver-0","compute":"compute-0"}},{"active_storage":None},{"active_storage":dict(restart["active_storage"],node_id=1)}):
            with self.assertRaisesRegex(ValueError,"restart-failure"): PRODUCER.validate_observation("restart-failure",dict(restart,**changed),state)

    def test_restart_uses_the_verified_active_pageserver(self):
        probe={"platform_id":"7"*32,"platform_revision":4,"namespace_uid":"owned-namespace","ownership_capability_verified":True,"attached_pageserver":"pageserver-1","attached_pageserver_node_id":2,"tenant_id":"1"*32,"tenant_generation":7}
        result=DRIVER.restart_storage(probe,"7"*32,4,"owned-namespace")
        self.assertEqual(result,{"component":"pageserver-1","node_id":2,"tenant_id":"1"*32,"tenant_generation":7})
        for changes in ({"attached_pageserver":"pageserver-0"},{"attached_pageserver_node_id":0},{"attached_pageserver_node_id":True},{"tenant_generation":0},{"platform_id":"8"*32},{"platform_revision":5},{"namespace_uid":"foreign"},{"ownership_capability_verified":False}):
            with self.assertRaisesRegex(RuntimeError,"active storage"): DRIVER.restart_storage(dict(probe,**changes),"7"*32,4,"owned-namespace")

    def test_tenant_migration_requires_owned_move_and_compute_recovery(self):
        state = {"run_id": "4" * 32, "resources": resources()}
        value = {
            "run_id": state["run_id"], "platform_id": "7" * 32,
            "platform_revision": 4, "namespace_uid": "uid-recovery_target",
            "tenant_id": "1" * 32, "timeline_id": "2" * 32,
            "source_node_id": 1, "destination_node_id": 2,
            "generation_before": 4, "generation_after": 5,
            "tenant_owner_unchanged": True, "controller_move_completed": True,
            "compute_names": ["compute-0", "compute-1"], "compute_routing_verified": True,
            "primary_sql_verified": True, "replica_sql_verified": True, "data_sha256": "3" * 64,
        }
        PRODUCER.validate_observation("tenant-migration", value, state)
        for key, replacement in (
            ("source_node_id", True), ("destination_node_id", 1), ("destination_node_id", 9),
            ("generation_after", 4), ("generation_before", 0), ("generation_after", 4294967296),
            ("platform_revision", True), ("tenant_owner_unchanged", False),
            ("controller_move_completed", False), ("compute_routing_verified", False),
            ("primary_sql_verified", False), ("replica_sql_verified", False),
            ("compute_names", ["compute-0"]), ("compute_names", ["compute-0", "compute-0"]),
            ("tenant_id", "invalid"), ("data_sha256", "invalid"),
        ):
            with self.subTest(key=key, replacement=replacement):
                with self.assertRaisesRegex(ValueError, "tenant-migration"):
                    PRODUCER.validate_observation("tenant-migration", dict(value, **{key: replacement}), state)
        with self.assertRaisesRegex(ValueError, "exact fixture"):
            PRODUCER.validate_observation("tenant-migration", dict(value, platform_id="6" * 32, namespace_uid="uid-source"), state)

    def test_restored_resource_update_evidence_requires_exact_transition_and_live_pod_cpu(self):
        state={"run_id":"4"*32,"resources":resources()}
        value={"run_id":"4"*32,"platform_id":"7"*32,"namespace_uid":"uid-recovery_target","operation_id":"a"*32,"revision_before":3,"revision_after":4,"compute_cpu_millis_before":500,"compute_cpu_millis_after":600,"compute_pods_before":{name:{"pod_uid":name+"-old","cpu_millis":500} for name in ("compute-0","compute-1")},"compute_pods_after":{name:{"pod_uid":name+"-new","cpu_millis":600} for name in ("compute-0","compute-1")},"tenant_id":"1"*32,"timeline_id":"2"*32,"tenant_generation":5,"timeline_generation":6,"identity_preserved":True,"stale_revision_refused":True,"topology_change_refused":True,"primary_compute":"compute-0","replica_compute":"compute-1","primary_writable":True,"replica_read_only":True,"replica_write_refused":True,"replica_caught_up":True,"restored_data_sha256":"3"*64}
        PRODUCER.validate_observation("restored-resource-update",value,state)
        for key,replacement in (("revision_after",5),("compute_cpu_millis_after",500),("identity_preserved",False),("stale_revision_refused",False),("topology_change_refused",False),("replica_read_only",False),("compute_pods_after",value["compute_pods_before"])):
            with self.subTest(key=key):
                with self.assertRaisesRegex(ValueError,"restored-resource-update"): PRODUCER.validate_observation("restored-resource-update",dict(value,**{key:replacement}),state)
        with self.assertRaisesRegex(ValueError,"exact fixture"): PRODUCER.validate_observation("restored-resource-update",dict(value,platform_id="6"*32,namespace_uid="uid-source"),state)
        incomplete=report(); incomplete["test_events"]=[event for event in incomplete["test_events"] if event["case"]!="restored-resource-update"]
        with self.assertRaises(ValueError): self.validate(incomplete)

class QualificationRecordTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.base = Path(self.temporary.name)
        self.root = self.base / "source"
        for name in VERIFY.SOURCE_DIRS:
            (self.root / name).mkdir(parents=True, exist_ok=True)
        for name in (*VERIFY.SOURCE_FILES, *VERIFY.PRODUCER.values()):
            path = self.root / name
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_text("test fixture source\n")
        self.build, self.metadata = build()
        self.archive = self.base / "archive"
        self.archive.write_bytes(b"test fixture source archive")
        archive_metadata = dict(self.metadata["source_archive"],
            sha256=VERIFY.file_hash(self.archive), size_bytes=self.archive.stat().st_size)
        self.metadata["source_archive"] = archive_metadata
        self.build["source_archive"] = archive_metadata
        release_source(self.root, {}, self.metadata)
        self.report = report()
        self.refresh_report_sources()
        self.output = self.base / "record"
        # The source metadata parser has separate tests; all source, archive,
        # acceptance, compiled-gate, recorder and manifest checks run here.
        metadata_patch = mock.patch.object(VERIFY, "source_metadata", return_value=self.metadata)
        metadata_patch.start()
        self.addCleanup(metadata_patch.stop)
        verifier_patch = mock.patch.object(RECORD, "VERIFIER", vars(VERIFY))
        verifier_patch.start()
        self.addCleanup(verifier_patch.stop)

    def refresh_report_sources(self):
        sources = VERIFY.source_files(self.root)
        self.report.update(source_files=sources, source_files_after=dict(sources),
            runner_sha256=VERIFY.file_hash(self.root / VERIFY.PRODUCER["runner_path"]),
            producer_sha256=VERIFY.file_hash(self.root / VERIFY.PRODUCER["producer_path"]))

    def assemble(self):
        build_path, report_path = self.base / "build.json", self.base / "report.json"
        build_path.write_text(json.dumps(self.build))
        report_path.write_text(json.dumps(self.report))
        return RECORD.assemble(self.root, self.archive, build_path, report_path, self.output)

    def test_empty_closed_gate_records_but_cannot_publish(self):
        manifest = self.assemble()
        self.assertFalse(manifest["capability"]["release_runtime_qualified"])
        self.assertEqual(VERIFY.validate_metadata(self.output, self.root), manifest)
        calls = []
        verified = self.base / "verified"
        with self.assertRaisesRegex(ValueError, "release gate is closed"):
            VERIFY.verify(self.output, verified, self.root, lambda *args: calls.append(args))
        self.assertEqual(calls, [])
        self.assertFalse(verified.exists())

    def test_final_source_records_release_without_deployment_approval(self):
        release_source(self.root, self.build["images"], self.metadata, True)
        self.refresh_report_sources()
        manifest = self.assemble()
        self.assertTrue(manifest["capability"]["release_runtime_qualified"])
        self.assertFalse(manifest["capability"]["cluster_qualified"])
        self.assertEqual(VERIFY.validate_metadata(self.output, self.root), manifest)

    def test_opening_gate_requires_new_native_evidence(self):
        self.assemble()
        release_source(self.root, self.build["images"], self.metadata, True)
        with self.assertRaisesRegex(ValueError, "source changed"):
            VERIFY.validate_metadata(self.output, self.root)

    def test_record_cannot_promote_a_closed_gate(self):
        self.assemble()
        path = self.output / "manifest.json"
        manifest = json.loads(path.read_text())
        manifest["capability"]["release_runtime_qualified"] = True
        path.write_text(json.dumps(manifest))
        with self.assertRaisesRegex(ValueError, "capability boundary"):
            VERIFY.validate_metadata(self.output, self.root)


if __name__ == "__main__": unittest.main()
