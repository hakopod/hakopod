"""Keep Oracle images and native evidence bound to the reviewed source."""

import copy
import hashlib
import importlib.util
import json
from pathlib import Path
import runpy
import tempfile
import unittest
from unittest import mock

SPEC = importlib.util.spec_from_file_location("oracle_release", Path(__file__).with_name("verify-oracle-free-runtime.py"))
ORACLE = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(ORACLE)
RECORDER = runpy.run_path(str(Path(__file__).with_name("record-oracle-free-qualification.py")))


def sha(raw):
    return hashlib.sha256(raw).hexdigest()


class OracleQualification(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory(prefix="oracle-release-test-")
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name) / "source"
        self.directory = Path(self.tmp.name) / "qualification"
        self.directory.mkdir()
        for name in ("api", "auth", "cmd/hakopod-server", "internal/cluster", "internal/database", "templates", "patches/oracle-operator"):
            (self.root / name).mkdir(parents=True, exist_ok=True)
        for name in ["go.mod", "go.sum", *ORACLE.BUILD_INPUTS,
                     "scripts/export-oracle-free-crd.py", "scripts/package-managed-oracle-free.py",
                     "scripts/run-development-vitess-acceptance.py", "scripts/run-development-oracle-free-acceptance.py",
                     "scripts/run-development-oracle-free-http-acceptance.py",
                     "scripts/oracle-free-http-fixtures.py",
                     "installer/oracle_free_controller.py", "release/verify-oracle-free-runtime.py",
                     "release/managed-runtime-availability.py",
                     "release/runtime-source-compatibility.py",
                     "release/record-oracle-free-qualification.py"]:
            path = self.root / name
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_text("reviewed fixture " + name + "\n")
        self.images = {"operator": ORACLE.PACKAGE + "@sha256:" + "1" * 64,
                       "database": "container-registry.oracle.com/database/free:23.26.3.0@sha256:" + "2" * 64}
        (self.root / "internal/cluster/database_oracle_free_controller.go").write_text('const oracleFreeOperatorImage = "' + self.images["operator"] + '"\n')
        (self.root / "internal/database/oracle.go").write_text('const OracleFreeImage = "' + self.images["database"] + '"\n')
        (self.root / ORACLE.GATE_PATH).write_text('package cluster\n\n// Qualified only after native checks.\nconst oracleFreeReleaseQualified = false\n')
        self.sources = ORACLE.source_files(self.root)
        self.binary, patch, crd = b"native fixture manager", b"reviewed patch\n", b"reviewed v4 schema\n"
        for name, raw in (("operator-upstream.patch", patch), ("sidb-v4.yaml", crd)):
            (self.directory / name).write_bytes(raw)
        source_manifest = self.directory / "source-build-manifest.json"
        source_manifest.write_text(json.dumps(self.sources))
        checksums = self.directory / "build-artifacts.txt"
        checksums.write_text("".join(digest + "  /build/image/" + name + "\n" for digest, name in (
            (sha(self.binary), "bin/manager"), (sha(patch), "upstream.patch"), (sha(crd), "crds/singleinstancedatabases.yaml"))))
        self.receipt = {"schema_version": 1, "status": "packaged_not_published", "source_manifest_sha256": ORACLE.file_hash(source_manifest),
                        "build_artifacts_sha256": ORACLE.file_hash(checksums),
                        "publication_plan_sha256": "4" * 64,
                        "image": {"repository": ORACLE.PACKAGE, "image_digest": self.images["operator"].split("@")[1],
                                  "platform": "linux/amd64", "config_user": "65532:65532",
                                  "archive": "managed-oracle-free-operator.oci.tar", "archive_sha256": "5" * 64,
                                  "labels": {"org.opencontainers.image.source": "https://github.com/hakopod/hakopod",
                                             "io.hakopod.oracle.operator-upstream": ORACLE.UPSTREAM,
                                             "io.hakopod.oracle.patch": "hakopod-oracle-free-tcps-v1",
                                             "io.hakopod.oracle.source-sha256": ORACLE.file_hash(source_manifest)}}}
        self.acceptance = {"schema_version": 1, "context": "k3d-hakopod-dev", "execution": "native", "platform": "linux/amd64",
                           "source_files": self.sources, "images": self.images, "attempts": []}
        source_sha = sha(json.dumps(self.sources, sort_keys=True, separators=(",", ":")).encode())
        for case, test in ORACLE.ALL_TESTS.items():
            package = "github.com/hakopod/hakopod/internal/api" if case == "http-api" else "github.com/hakopod/hakopod/internal/cluster"
            events = [{"Action": "run", "Package": package, "Test": test}]
            if case == "lifecycle":
                events.extend({"Action": action, "Package": events[0]["Package"], "Test": test + "/security"} for action in ("run", "pass"))
            if case == "http-api":
                for phase in ORACLE.HTTP_PHASES:
                    events.extend({"Action": action, "Package": package, "Test": test + "/" + phase} for action in ("run", "pass"))
            events.append(dict(events[0], Action="pass"))
            count = 2 if case in ("recovery", "http-api") else 1
            namespaces = ["hdb-" + str(index) * 32 for index in range(count)]
            if case == "http-api":
                namespaces.append("hp-" + "9" * 32)
            self.acceptance["attempts"].append({"case": case, "exit_code": 0, "source_manifest_sha256": source_sha,
                "source_manifest_after_sha256": source_sha, "log_sha256": "3" * 64, "test_events": events,
                "cluster_uid": "a" * 36, "sidb_crd_uid": "b" * 36, "node_uids": {"k3d-hakopod-database-worker-0": "c" * 36},
                "cleanup": {"namespaces": namespaces,
                            "namespaces_absent": True, "persistent_volumes_absent": True}})
            if case == "http-api":
                self.acceptance["attempts"][-1]["host_fixture_receipt_sha256"] = "8" * 64
                self.acceptance["attempts"][-1]["cleanup"]["host_fixtures"] = {
                    "postgres_container_absent": True, "s3_container_absent": True, "credential_files_absent": True}
        self.manifest = {"schema_version": 1, "platform": "linux/amd64",
            "source": {"repository": "https://github.com/oracle/oracle-database-operator", "revision": ORACLE.UPSTREAM},
            "source_files": self.sources, "images": {"operator": {"reference": self.images["operator"], "binaries": {"/manager": sha(self.binary)}},
                                                      "database": {"reference": self.images["database"], "binaries": {}}}}
        self.write()
        for patcher in (mock.patch.object(ORACLE, "SIDB_SHA256", sha(crd)),
                        mock.patch.object(ORACLE.runpy, "run_path", return_value={"release_availability": lambda root: {"oracle-free": True}})):
            patcher.start()
            self.addCleanup(patcher.stop)

    def write(self):
        (self.directory / "native-acceptance.json").write_text(json.dumps(self.acceptance))
        (self.directory / "packaging-receipt.json").write_text(json.dumps(self.receipt))
        self.manifest["files"] = {name: ORACLE.file_hash(self.directory / name) for name in ORACLE.FILES}
        (self.directory / "manifest.json").write_text(json.dumps(self.manifest))

    def compatibility(self, current, changes):
        path = self.root / "release/managed-oracle-free/source-compatibility.json"
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(json.dumps({"schema_version": 1, "runtime": "oracle-free",
            "qualified_release": {"tag": "v0.1.0-alpha.56", "commit": "a" * 40,
                "manifest_sha256": ORACLE.file_hash(self.directory / "manifest.json")},
            "reviewed_release": "v0.1.0-alpha.57", "control_plane_commit": "b" * 40,
            "regression_evidence": {"url": "https://github.com/hakopod/hakopod/actions/runs/123",
                                    "tests": ["TestManagedOracleFreeHTTPLive"]},
            "bootstrap": None,
            "delta": {"baseline_sha256": ORACLE.COMPAT["canonical_hash"](self.sources),
                      "current_sha256": ORACLE.COMPAT["canonical_hash"](current), "changes": changes}}))

    def test_complete_native_qualification_is_accepted(self):
        self.assertEqual(ORACLE.validate_metadata(self.directory, self.root), self.manifest)

    def test_historical_manifest_is_semantically_bound_to_cluster_reports(self):
        self.assertEqual(RECORDER["validate_qualified_manifest"](
            self.directory / "manifest.json", self.sources, self.images), self.manifest)
        mutations = (
            lambda value: value.update(schema_version=2),
            lambda value: value["source"].update(repository="https://github.com/apecloud/myduckserver"),
            lambda value: value["source"].update(revision="f" * 40),
            lambda value: value.update(source_files={}),
            lambda value: value["images"]["operator"].update(reference=ORACLE.PACKAGE + "@sha256:" + "f" * 64),
            lambda value: value.update(files={}),
        )
        original = (self.directory / "manifest.json").read_text()
        for mutate in mutations:
            value = copy.deepcopy(self.manifest)
            mutate(value)
            (self.directory / "manifest.json").write_text(json.dumps(value))
            with self.subTest(value=value), self.assertRaisesRegex(ValueError, "Historical Oracle manifest"):
                RECORDER["validate_qualified_manifest"](self.directory / "manifest.json", self.sources, self.images)
        (self.directory / "manifest.json").write_text('{"schema_version":1,"schema_version":1}')
        with self.assertRaisesRegex(ValueError, "Duplicate"):
            RECORDER["validate_qualified_manifest"](self.directory / "manifest.json", self.sources, self.images)
        (self.directory / "manifest.json").write_text(original)

    def test_closed_gate_cannot_qualify(self):
        with mock.patch.object(ORACLE.runpy, "run_path", return_value={"release_availability": lambda root: {"oracle-free": False}}):
            with self.assertRaisesRegex(ValueError, "admission remains closed"):
                ORACLE.validate_metadata(self.directory, self.root)

    def test_unqualified_or_enterprise_image_cannot_be_substituted(self):
        self.images["database"] = "container-registry.oracle.com/database/enterprise:23@sha256:" + "2" * 64
        self.write()
        with self.assertRaises(ValueError):
            ORACLE.validate_metadata(self.directory, self.root)

    def test_transitive_source_change_invalidates_qualification(self):
        (self.root / "internal/cluster/new-runtime.go").write_text("changed orchestration\n")
        with self.assertRaisesRegex(ValueError, "source changed"):
            ORACLE.validate_metadata(self.directory, self.root)

    def test_api_package_and_embedded_schema_are_fingerprinted(self):
        for name, content in (("api/schema.go", b"package api\n"), ("api/openapi.json", b'{"openapi":"3.1.0"}\n')):
            path = self.root / name
            path.write_bytes(content)
            try:
                with self.subTest(name=name):
                    self.assertEqual(ORACLE.source_files(self.root)[name], sha(content))
                    with self.assertRaisesRegex(ValueError, "source changed"):
                        ORACLE.validate_metadata(self.directory, self.root)
            finally:
                path.unlink()

    def test_empty_source_files_are_included_in_the_inventory(self):
        names = ("internal/managedplatform/supabase-assets/db/init/data.sql",
                 "internal/managedplatform/supabase-assets/snippets/.gitkeep",
                 "internal/managedplatform/supabase-assets/storage/.gitkeep")
        for name in names:
            path = self.root / name
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_bytes(b"")
        sources = ORACLE.source_files(self.root)
        for name in names:
            with self.subTest(name=name):
                self.assertEqual(sources[name], sha(b""))
        with self.assertRaisesRegex(ValueError, "source changed"):
            ORACLE.validate_metadata(self.directory, self.root)
        (self.root / names[0]).write_bytes(b"SELECT 1;\n")
        self.assertNotEqual(ORACLE.source_files(self.root)[names[0]], sources[names[0]])

    def test_empty_runtime_artifacts_remain_invalid(self):
        for name in ORACLE.FILES:
            path = self.directory / name
            original = path.read_bytes()
            path.write_bytes(b"")
            try:
                with self.subTest(name=name), self.assertRaisesRegex(ValueError, "Missing, symbolic or oversized"):
                    ORACLE.validate_metadata(self.directory, self.root)
            finally:
                path.write_bytes(original)

    def test_only_the_qualification_literal_may_change_after_native_acceptance(self):
        gate = self.root / ORACLE.GATE_PATH
        before = ORACLE.source_files(self.root)
        gate.write_text(gate.read_text().replace('= false', '= true'))
        self.assertEqual(ORACLE.source_files(self.root), before)
        self.assertEqual(ORACLE.validate_metadata(self.directory, self.root), self.manifest)
        gate.write_text(gate.read_text().replace('native checks', 'different checks'))
        self.assertNotEqual(ORACLE.source_files(self.root), before)
        with self.assertRaisesRegex(ValueError, 'source changed'):
            ORACLE.validate_metadata(self.directory, self.root)

    def test_gate_file_cannot_hide_other_declarations(self):
        gate = self.root / ORACLE.GATE_PATH
        for extra in ('\nconst unrelated = true\n', '\nconst oracleFreeReleaseQualified = false\n'):
            original = gate.read_text()
            gate.write_text(original + extra)
            with self.subTest(extra=extra), self.assertRaises(ValueError):
                ORACLE.source_files(self.root)
            gate.write_text(original)

    def test_build_artifact_change_invalidates_qualification(self):
        (self.directory / "operator-upstream.patch").write_text("changed patch\n")
        self.write()
        with self.assertRaisesRegex(ValueError, "artifacts differ"):
            ORACLE.validate_metadata(self.directory, self.root)

    def test_packaged_image_change_invalidates_qualification(self):
        self.receipt["image"]["image_digest"] = "sha256:" + "9" * 64
        self.write()
        with self.assertRaisesRegex(ValueError, "packaging image differs"):
            ORACLE.validate_metadata(self.directory, self.root)

    def test_failed_or_skipped_case_cannot_qualify(self):
        for action in ("fail", "skip"):
            changed = copy.deepcopy(self.acceptance)
            changed["attempts"][0]["test_events"][-1]["Action"] = action
            with self.subTest(action=action), self.assertRaisesRegex(ValueError, "failed, skipped"):
                ORACLE.validate_acceptance(changed, self.sources, self.images)

    def test_lifecycle_must_include_security_probes(self):
        self.acceptance["attempts"][0]["test_events"] = [event for event in self.acceptance["attempts"][0]["test_events"] if "/" not in event["Test"]]
        with self.assertRaisesRegex(ValueError, "security or recovery"):
            ORACLE.validate_acceptance(self.acceptance, self.sources, self.images)

    def test_cluster_only_evidence_cannot_qualify_http_api(self):
        self.acceptance["attempts"].pop()
        with self.assertRaisesRegex(ValueError, "HTTP/API"):
            ORACLE.validate_acceptance(self.acceptance, self.sources, self.images)

    def test_http_requires_all_phases_and_the_api_test_package(self):
        for phase in ORACLE.HTTP_PHASES:
            changed = copy.deepcopy(self.acceptance)
            attempt = changed["attempts"][-1]
            attempt["test_events"] = [event for event in attempt["test_events"] if event["Test"] != ORACLE.HTTP_TEST + "/" + phase]
            with self.subTest(phase=phase), self.assertRaisesRegex(ValueError, "incomplete"):
                ORACLE.validate_acceptance(changed, self.sources, self.images)
        for event in self.acceptance["attempts"][-1]["test_events"]:
            event["Package"] = "github.com/hakopod/hakopod/internal/cluster"
        with self.assertRaisesRegex(ValueError, "another test"):
            ORACLE.validate_acceptance(self.acceptance, self.sources, self.images)

    def test_http_cleanup_requires_the_bound_application(self):
        attempt = self.acceptance["attempts"][-1]
        attempt["cleanup"]["namespaces"][-1] = "hdb-" + "9" * 32
        with self.assertRaisesRegex(ValueError, "bound application"):
            ORACLE.validate_acceptance(self.acceptance, self.sources, self.images)

    def test_http_runner_change_invalidates_qualification(self):
        (self.root / "scripts/run-development-oracle-free-http-acceptance.py").write_text("changed HTTP evidence producer\n")
        with self.assertRaisesRegex(ValueError, "source changed"):
            ORACLE.validate_metadata(self.directory, self.root)

    def test_http_fixture_helper_change_invalidates_qualification(self):
        (self.root / "scripts/oracle-free-http-fixtures.py").write_text("changed fixture ownership\n")
        with self.assertRaisesRegex(ValueError, "source changed"):
            ORACLE.validate_metadata(self.directory, self.root)

    def test_http_requires_host_fixture_receipt(self):
        for value in (None, "", "f" * 63, True):
            changed = copy.deepcopy(self.acceptance)
            changed["attempts"][-1]["host_fixture_receipt_sha256"] = value
            with self.subTest(value=value), self.assertRaisesRegex(ValueError, "receipt"):
                ORACLE.validate_acceptance(changed, self.sources, self.images)
        self.acceptance["attempts"][-1].pop("host_fixture_receipt_sha256")
        with self.assertRaisesRegex(ValueError, "malformed"):
            ORACLE.validate_acceptance(self.acceptance, self.sources, self.images)

    def test_http_requires_all_host_fixtures_removed(self):
        for flag in ("postgres_container_absent", "s3_container_absent", "credential_files_absent"):
            for value in (False, None, 1):
                changed = copy.deepcopy(self.acceptance)
                changed["attempts"][-1]["cleanup"]["host_fixtures"][flag] = value
                with self.subTest(flag=flag, value=value), self.assertRaisesRegex(ValueError, "host fixture cleanup"):
                    ORACLE.validate_acceptance(changed, self.sources, self.images)
        self.acceptance["attempts"][-1]["cleanup"].pop("host_fixtures")
        with self.assertRaisesRegex(ValueError, "cleanup"):
            ORACLE.validate_acceptance(self.acceptance, self.sources, self.images)

    def test_mixed_record_reuses_cluster_cases_only_with_fresh_http_source(self):
        path = self.root / "internal/api/oracle_observation.go"
        path.parent.mkdir(parents=True, exist_ok=True); path.write_text("package api\n")
        current = ORACLE.source_files(self.root)
        self.compatibility(current, [{"path": "internal/api/oracle_observation.go", "before": None,
            "after": current["internal/api/oracle_observation.go"],
            "reason": "Oracle API observation changes only the freshly executed HTTP acceptance phase."}])
        mixed = copy.deepcopy(self.acceptance)
        mixed.update(schema_version=2, source_files=current, qualified_source_files=self.sources,
                     qualified_manifest_sha256=ORACLE.file_hash(self.directory / "manifest.json"))
        for attempt in mixed["attempts"]:
            attempt["source_files"] = current if attempt["case"] == "http-api" else self.sources
            identity = sha(json.dumps(attempt["source_files"], sort_keys=True, separators=(",", ":")).encode())
            attempt["source_manifest_sha256"] = attempt["source_manifest_after_sha256"] = identity
        ORACLE.validate_acceptance(mixed, current, self.images, self.root)
        mixed["attempts"][0]["source_files"] = current
        with self.assertRaisesRegex(ValueError, "source changed"):
            ORACLE.validate_acceptance(mixed, current, self.images, self.root)

    def test_oracle_compatibility_rejects_protected_runtime_delta(self):
        path = self.root / "internal/cluster/database_oracle_free_runtime.go"; path.write_text("package cluster\n")
        current = ORACLE.source_files(self.root)
        self.compatibility(current, [{"path": "internal/cluster/database_oracle_free_runtime.go", "before": None,
            "after": current["internal/cluster/database_oracle_free_runtime.go"],
            "reason": "Protected Oracle runtime changes require fresh cluster qualification evidence."}])
        with self.assertRaisesRegex(ValueError, "require new qualification"):
            ORACLE.COMPAT["validate"](self.root, "oracle-free", self.sources, current,
                                      ORACLE.protected_source, ORACLE.file_hash(self.directory / "manifest.json"))

    def test_retained_manifest_rejects_native_http_harness_changes(self):
        for name in ("internal/api/live_database_oracle_free_http_test.go",
                     "internal/nativeacceptance/oracle_fixture.go",
                     "scripts/run-development-oracle-free-http-acceptance.py",
                     "scripts/oracle-free-http-fixtures.py"):
            path = self.root / name
            before = path.read_text() if path.exists() else None
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_text((before or "") + "reviewed harness change\n")
            current = ORACLE.source_files(self.root)
            self.compatibility(current, [{"path": name, "before": self.sources.get(name),
                "after": current[name],
                "reason": "A native or HTTP harness change cannot reuse the retained Oracle manifest."}])
            with self.subTest(name=name), self.assertRaisesRegex(ValueError, "require new qualification"):
                ORACLE.COMPAT["validate"](self.root, "oracle-free", self.sources, current,
                    ORACLE.retained_manifest_protected_source, ORACLE.file_hash(self.directory / "manifest.json"))
            if before is None:
                path.unlink()
            else:
                path.write_text(before)

    def test_cleanup_and_source_after_are_required(self):
        for field, value in (("source_manifest_after_sha256", "9" * 64), ("exit_code", 1), ("node_uids", {"production": "c" * 36}),
                             ("cleanup", {"namespaces": ["hdb-" + "0" * 32], "namespaces_absent": True, "persistent_volumes_absent": False})):
            changed = copy.deepcopy(self.acceptance)
            changed["attempts"][0][field] = value
            with self.subTest(field=field), self.assertRaises(ValueError):
                ORACLE.validate_acceptance(changed, self.sources, self.images)

    def test_duplicate_or_missing_native_case_cannot_qualify(self):
        self.acceptance["attempts"][1] = copy.deepcopy(self.acceptance["attempts"][0])
        with self.assertRaisesRegex(ValueError, "duplicated"):
            ORACLE.validate_acceptance(self.acceptance, self.sources, self.images)

    def test_anonymous_verification_compares_the_actual_operator_binary(self):
        commands = []
        def runner(args, config):
            commands.append(args)
            self.assertTrue(config.is_dir())
            if args[:2] == ["image", "inspect"]:
                reference = args[-1]
                canonical = reference.split("@")[0].split(":")[0] + "@" + reference.split("@")[1]
                return json.dumps([{"Os": "linux", "Architecture": "amd64", "RepoDigests": [canonical], "Config": {
                    "User": "65532:65532", "Entrypoint": ["/manager"], "Labels": {
                        "org.opencontainers.image.source": "https://github.com/hakopod/hakopod",
                        "io.hakopod.oracle.operator-upstream": ORACLE.UPSTREAM, "io.hakopod.oracle.patch": "hakopod-oracle-free-tcps-v1",
                        "io.hakopod.oracle.source-sha256": self.manifest["files"]["source-build-manifest.json"]}}}])
            if args[0] == "create":
                return "a" * 64
            if args[0] == "cp":
                Path(args[-1]).write_bytes(self.binary)
            return ""
        ORACLE.verify_images(self.manifest, runner)
        self.assertEqual(sum(command[0] == "pull" for command in commands), 2)
        self.assertTrue(any(command[0] == "rm" for command in commands))
        self.binary = b"different unqualified manager"
        with self.assertRaisesRegex(ValueError, "binary differs"):
            ORACLE.verify_images(self.manifest, runner)


if __name__ == "__main__":
    unittest.main()
