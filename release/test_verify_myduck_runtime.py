"""MyDuck publication must reject stale source, incomplete tests and image drift."""

import copy
import hashlib
import json
from pathlib import Path
import runpy
import tempfile
import unittest

MODULE = runpy.run_path(str(Path(__file__).with_name("verify-myduck-runtime.py")))


class MyDuckQualificationTest(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name) / "source"
        self.output = Path(temporary.name) / "qualification"
        self.output.mkdir()
        paths = {"go.mod", "go.sum", *MODULE["BUILD_INPUTS"], "release/verify-myduck-runtime.py",
            "release/record-myduck-qualification.py", "release/managed-runtime-availability.py",
            "release/runtime-source-compatibility.py",
            "scripts/run-development-myduck-acceptance.py", "scripts/run-development-vitess-acceptance.py",
            "scripts/run-development-oracle-free-acceptance.py", "release/verify-oracle-free-runtime.py"}
        for name in paths:
            self.write(name, "development test fixture\n")
        for name in ("api", "auth", "cmd/hakopod-server", "internal", "templates", "patches/myduck"):
            (self.root / name).mkdir(parents=True, exist_ok=True)
        self.write(MODULE["GATE_PATH"], "package database\n\nconst MyDuckRuntimeQualified = true\n")
        self.reference = MODULE["PACKAGE"] + "@sha256:" + "a" * 64
        self.write("internal/cluster/database_myduck.go", 'package cluster\nconst myduckServerImage = "' + self.reference + '"\n')
        self.sources = MODULE["source_files"](self.root)
        self.images = {"runtime": self.reference}
        attempts = []
        for case, test in MODULE["TESTS"].items():
            package = "github.com/hakopod/hakopod/internal/" + ("api" if case == "http-api" else "cluster")
            phases = MODULE["HTTP_PHASES"] if case == "http-api" else MODULE["RECOVERY_PHASES"] if case == "recovery" else MODULE["LIFECYCLE_PHASES"]
            tests = [test] + [test + "/" + phase for phase in phases]
            events = [{"Action": "run", "Package": package, "Test": name} for name in tests]
            events += [{"Action": "pass", "Package": package, "Test": name} for name in reversed(tests)]
            attempts.append({"case": case, "exit_code": 0,
                "source_manifest_sha256": MODULE["source_hash"](self.sources), "source_manifest_after_sha256": MODULE["source_hash"](self.sources),
                "log_sha256": "b" * 64, "test_events": events, "cluster_uid": "11111111-1111-1111-1111-111111111111",
                "node_uids": {"k3d-hakopod-dev-server-0": "22222222-2222-2222-2222-222222222222"},
                "preserved_resources": {"before_sha256": "d" * 64, "after_sha256": "d" * 64,
                    "namespace_count": 4, "persistent_volume_count": 0, "s3_container_count": 0},
                "cleanup": {"namespaces": ["hdb-" + letter * 32 for letter in "abcde"[:MODULE["FIXTURE_COUNTS"][case]]],
                            "namespaces_absent": True, "persistent_volumes_absent": True}})
        self.acceptance = {"schema_version": 1, "context": "k3d-hakopod-dev", "execution": "native", "platform": "linux/amd64",
                           "source_files": self.sources, "images": self.images, "attempts": attempts}
        self.json("native-acceptance.json", self.acceptance)
        self.json("source-build-manifest.json", {"schema_version": 1, "upstream_repository": "https://github.com/apecloud/myduckserver",
            "upstream_commit": MODULE["UPSTREAM"], "files": {name: self.sources[name] for name in MODULE["BUILD_INPUTS"]}})
        self.binaries = {name: "c" * 64 for name in MODULE["BINARIES"]}
        self.receipt = {"schema_version": 1, "image_reference": self.reference, "binaries": self.binaries,
            "platform": "linux/amd64", "archive_sha256": "e" * 64, "config_user": "1000:1000",
            "entrypoint": ["/usr/local/bin/myduckserver"], "labels": MODULE["image_labels"](self.sources),
            "source_build_manifest_sha256": MODULE["file_hash"](self.output / "source-build-manifest.json")}
        self.json("packaging-receipt.json", self.receipt)
        self.manifest = {"schema_version": 1, "platform": "linux/amd64",
            "source": {"repository": "https://github.com/apecloud/myduckserver", "revision": MODULE["UPSTREAM"]},
            "source_files": self.sources, "images": {"runtime": {"reference": self.reference, "binaries": self.binaries}},
            "files": {name: MODULE["file_hash"](self.output / name) for name in MODULE["FILES"]}}
        self.json("manifest.json", self.manifest)

    def write(self, name, text):
        path = self.root / name
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(text)

    def json(self, name, value):
        (self.output / name).write_text(json.dumps(value))

    def compatibility(self, current, changes, bootstrap=None):
        path = self.root / "release/managed-myduck/source-compatibility.json"
        path.parent.mkdir(parents=True, exist_ok=True)
        qualified_tag = bootstrap["base_release"] if bootstrap is not None else "v0.1.0-alpha.56"
        qualified_commit = bootstrap["base_commit"] if bootstrap is not None else "a" * 40
        path.write_text(json.dumps({"schema_version": 1, "runtime": "myduck",
            "qualified_release": {"tag": qualified_tag, "commit": qualified_commit,
                "manifest_sha256": MODULE["file_hash"](self.output / "manifest.json")},
            "reviewed_release": "v0.1.0-alpha.57", "control_plane_commit": "b" * 40,
            "regression_evidence": {"url": "https://github.com/hakopod/hakopod/actions/runs/123",
                                    "tests": ["TestManagedMyDuckAPICompatibility"]},
            "bootstrap": bootstrap,
            "delta": {"baseline_sha256": MODULE["COMPAT"]["canonical_hash"](self.sources),
                      "current_sha256": MODULE["COMPAT"]["canonical_hash"](current), "changes": changes}}))

    def test_valid_native_metadata_and_exact_gate_normalization(self):
        self.assertEqual(MODULE["validate_metadata"](self.output, self.root), self.manifest)
        gate = self.root / MODULE["GATE_PATH"]
        before = MODULE["gate_hash"](gate)
        gate.write_text(gate.read_text().replace("= true", "= false"))
        self.assertEqual(MODULE["gate_hash"](gate), before)
        with self.assertRaisesRegex(ValueError, "gate is closed"):
            MODULE["validate_metadata"](self.output, self.root)
        gate.write_text(gate.read_text() + "func skipChecks() {}\n")
        with self.assertRaisesRegex(ValueError, "only its boolean"):
            MODULE["gate_hash"](gate)

    def test_source_or_build_drift_invalidates_prior_acceptance(self):
        for name in ("internal/cluster/database_myduck.go", "cmd/hakopod-myduck-storage/main.go",
                     "scripts/package-managed-myduck.py", "scripts/collect-myduck-licenses.py",
                     "patches/myduck/runtime-overlay-manifest.json"):
            path = self.root / name
            old = path.read_text()
            path.write_text(old + "// changed\n")
            with self.assertRaisesRegex(ValueError, "source changed"):
                MODULE["validate_metadata"](self.output, self.root)
            path.write_text(old)
        self.write("internal/new_runtime.go", "package internal\n")
        with self.assertRaisesRegex(ValueError, "source changed"):
            MODULE["validate_metadata"](self.output, self.root)

    def test_exact_reviewed_api_delta_reuses_historical_qualification(self):
        self.write("internal/api/oracle_only.go", "package api\n")
        path = self.root / "internal/api/oracle_only.go"
        current = MODULE["source_files"](self.root)
        self.compatibility(current, [{"path": "internal/api/oracle_only.go", "before": None,
            "after": current["internal/api/oracle_only.go"],
            "reason": "Oracle-only API observation does not alter MyDuck runtime or image behavior."}])
        self.assertEqual(MODULE["validate_metadata"](self.output, self.root), self.manifest)

    def test_compatibility_rejects_protected_and_unmatched_deltas(self):
        path = self.root / "internal/cluster/database_myduck.go"
        before = self.sources["internal/cluster/database_myduck.go"]
        path.write_text(path.read_text() + "// changed\n")
        current = MODULE["source_files"](self.root)
        self.compatibility(current, [{"path": "internal/cluster/database_myduck.go", "before": before,
            "after": current["internal/cluster/database_myduck.go"],
            "reason": "A protected MyDuck runtime change must never reuse historical native evidence."}])
        with self.assertRaisesRegex(ValueError, "require new qualification"):
            MODULE["validate_metadata"](self.output, self.root)
        path.write_text(path.read_text().replace("// changed\n", ""))
        self.write("internal/api/unlisted.go", "package api\n")
        with self.assertRaisesRegex(ValueError, "outside the reviewed delta"):
            MODULE["validate_metadata"](self.output, self.root)

    def test_trust_roots_require_exact_one_time_bootstrap(self):
        changes = []
        for name in sorted(MODULE["COMPAT"]["BOOTSTRAP_CHANGED"]["myduck"]):
            before = self.sources.get(name)
            self.write(name, "reviewed alpha57 trust root " + name + "\n")
            after = MODULE["file_hash"](self.root / name)
            changes.append({"path": name, "before": before, "after": after,
                            "reason": "Authenticated Git review approved this exact alpha57 validator bootstrap."})
        current = MODULE["source_files"](self.root)
        bootstrap = {"base_release": "v0.1.0-alpha.56", "base_commit": "e45bcf285eabec001e9161878c6696ce7814e54f",
                     "reviewed_release": "v0.1.0-alpha.57", "trust_anchor": "authenticated-git-review",
                     "trust_paths": sorted(MODULE["COMPAT"]["BOOTSTRAP_CHANGED"]["myduck"])}
        self.compatibility(current, changes, bootstrap)
        MODULE["COMPAT"]["validate"](self.root, "myduck", self.sources, current,
                                      MODULE["protected_source"], MODULE["file_hash"](self.output / "manifest.json"))
        self.compatibility(current, changes, None)
        with self.assertRaisesRegex(ValueError, "trust-root changes"):
            MODULE["COMPAT"]["validate"](self.root, "myduck", self.sources, current,
                                          MODULE["protected_source"], MODULE["file_hash"](self.output / "manifest.json"))
        for field, value in (("base_commit", "f" * 40), ("reviewed_release", "v0.1.0-alpha.58"),
                             ("trust_paths", bootstrap["trust_paths"][:-1])):
            wrong = copy.deepcopy(bootstrap); wrong[field] = value
            self.compatibility(current, changes, wrong)
            with self.subTest(field=field), self.assertRaisesRegex(ValueError, "bootstrap identity"):
                MODULE["COMPAT"]["validate"](self.root, "myduck", self.sources, current,
                                              MODULE["protected_source"], MODULE["file_hash"](self.output / "manifest.json"))

    def test_myduck_recorder_is_never_a_bootstrap_change(self):
        name = "release/record-myduck-qualification.py"
        before = self.sources[name]
        self.write(name, "changed recorder\n")
        current = MODULE["source_files"](self.root)
        changes = [{"path": name, "before": before, "after": current[name],
                    "reason": "The MyDuck recorder is an unchanged trust root outside this bootstrap."}]
        self.compatibility(current, changes, None)
        with self.assertRaisesRegex(ValueError, "trust-root changes"):
            MODULE["COMPAT"]["validate"](self.root, "myduck", self.sources, current,
                                          MODULE["protected_source"], MODULE["file_hash"](self.output / "manifest.json"))
        bootstrap = {"base_release": "v0.1.0-alpha.56", "base_commit": "e45bcf285eabec001e9161878c6696ce7814e54f",
                     "reviewed_release": "v0.1.0-alpha.57", "trust_anchor": "authenticated-git-review",
                     "trust_paths": sorted(MODULE["COMPAT"]["BOOTSTRAP_CHANGED"]["myduck"])}
        self.compatibility(current, changes, bootstrap)
        with self.assertRaisesRegex(ValueError, "outside the fixed alpha.57 bootstrap"):
            MODULE["COMPAT"]["validate"](self.root, "myduck", self.sources, current,
                                          MODULE["protected_source"], MODULE["file_hash"](self.output / "manifest.json"))

    def test_compatibility_schema_and_paths_are_strict(self):
        self.write("internal/api/oracle_only.go", "package api\n")
        path = self.root / "internal/api/oracle_only.go"
        current = MODULE["source_files"](self.root)
        change = {"path": "internal/api/oracle_only.go", "before": None,
                  "after": current["internal/api/oracle_only.go"],
                  "reason": "Oracle-only API observation leaves MyDuck runtime behavior unchanged."}
        self.compatibility(current, [change])
        record_path = self.root / "release/managed-myduck/source-compatibility.json"
        record = json.loads(record_path.read_text())
        record["schema_version"] = True; record_path.write_text(json.dumps(record))
        with self.assertRaisesRegex(ValueError, "Invalid myduck compatibility"):
            MODULE["COMPAT"]["validate"](self.root, "myduck", self.sources, current,
                                          MODULE["protected_source"], MODULE["file_hash"](self.output / "manifest.json"))
        for unsafe in ("internal/api/" + "x" * 501, "internal/api/bad\nname.go"):
            record["schema_version"] = 1; record["delta"]["changes"][0]["path"] = unsafe
            record_path.write_text(json.dumps(record))
            with self.subTest(unsafe=unsafe), self.assertRaisesRegex(ValueError, "unsafe"):
                MODULE["COMPAT"]["validate"](self.root, "myduck", self.sources, current,
                                              MODULE["protected_source"], MODULE["file_hash"](self.output / "manifest.json"))

    def test_skips_incomplete_cases_forged_success_and_residue_are_rejected(self):
        mutations = [lambda value: value["attempts"].pop(),
            lambda value: value["attempts"][0].update(exit_code=False),
            lambda value: value["attempts"][0]["test_events"][-1].update(Action="skip"),
            lambda value: value["attempts"][0]["test_events"].pop(),
            lambda value: value["attempts"][0]["cleanup"].update(persistent_volumes_absent=False),
            lambda value: value["attempts"][0]["preserved_resources"].update(after_sha256="f" * 64),
            lambda value: value["attempts"][0].update(source_manifest_after_sha256="f" * 64),
            lambda value: value.update(context="customer-cluster"),
            lambda value: value["attempts"][2]["test_events"].__delitem__(slice(1, 3))]
        for mutate in mutations:
            value = copy.deepcopy(self.acceptance)
            mutate(value)
            with self.assertRaises(ValueError):
                MODULE["validate_acceptance"](value, self.sources, self.images)

    def test_artifact_hash_and_duplicate_json_are_rejected(self):
        path = self.output / "native-acceptance.json"
        path.write_text(path.read_text() + " ")
        with self.assertRaisesRegex(ValueError, "evidence changed"):
            MODULE["validate_metadata"](self.output, self.root)
        path.write_text('{"schema_version":1,"schema_version":1}')
        with self.assertRaisesRegex(ValueError, "Duplicate"):
            MODULE["read_json"](path)

    def test_negative_restore_and_alternate_user_evidence_is_required(self):
        for case, phase in (*(('lifecycle', phase) for phase in MODULE["LIFECYCLE_PHASES"]),
                            *(("recovery", phase) for phase in MODULE["RECOVERY_PHASES"])):
            value = copy.deepcopy(self.acceptance)
            attempt = next(item for item in value["attempts"] if item["case"] == case)
            name = MODULE["TESTS"][case] + "/" + phase
            attempt["test_events"] = [event for event in attempt["test_events"] if event["Test"] != name]
            with self.subTest(case=case, phase=phase), self.assertRaises(ValueError):
                MODULE["validate_acceptance"](value, self.sources, self.images)
        value = copy.deepcopy(self.acceptance)
        recovery = next(item for item in value["attempts"] if item["case"] == "recovery")
        recovery["cleanup"]["namespaces"].pop()
        with self.assertRaisesRegex(ValueError, "fixture inventory"):
            MODULE["validate_acceptance"](value, self.sources, self.images)

    def test_packaging_provenance_requires_actual_schema_and_build_identity(self):
        mutations = [lambda value: value.update(schema_version=True),
            lambda value: value.update(config_user="0:0"),
            lambda value: value.update(entrypoint=["python3"]),
            lambda value: value.update(image_reference=MODULE["PACKAGE"] + "@sha256:" + "f" * 64),
            lambda value: value.update(source_build_manifest_sha256="f" * 64),
            lambda value: value.update(archive_sha256="unverified"),
            lambda value: value.update(image=self.reference),
            lambda value: value["labels"].update({"io.hakopod.myduck.patch-sha256": "f" * 64}),
            lambda value: value["labels"].update({"io.hakopod.myduck.upstream": "f" * 40}),
            lambda value: value["labels"].pop("org.opencontainers.image.version")]
        for mutate in mutations:
            receipt = copy.deepcopy(self.receipt)
            mutate(receipt)
            self.json("packaging-receipt.json", receipt)
            self.manifest["files"]["packaging-receipt.json"] = MODULE["file_hash"](self.output / "packaging-receipt.json")
            self.json("manifest.json", self.manifest)
            with self.assertRaisesRegex(ValueError, "packaging provenance"):
                MODULE["validate_metadata"](self.output, self.root)

    def test_anonymous_image_binary_and_execution_verification(self):
        manifest = copy.deepcopy(self.manifest)
        manifest["images"]["runtime"]["binaries"] = {name: hashlib.sha256(b"fixture binary").hexdigest() for name in MODULE["BINARIES"]}
        calls = []
        def runner(args, config):
            calls.append(args)
            if args[0:2] == ["image", "inspect"]:
                return json.dumps([{"Os": "linux", "Architecture": "amd64", "RepoDigests": [self.reference],
                    "Config": {"User": "1000:1000", "Entrypoint": ["/usr/local/bin/myduckserver"],
                               "Labels": MODULE["image_labels"](manifest["source_files"])}}])
            if args[0] == "create":
                return "e" * 64
            if args[0] == "cp":
                Path(args[2]).write_bytes(b"fixture binary")
            return ""
        MODULE["verify_images"](manifest, runner)
        self.assertEqual(sum(args[0] == "cp" for args in calls), 2)
        manifest["images"]["runtime"]["binaries"]["/usr/local/bin/myduckserver"] = "0" * 64
        with self.assertRaisesRegex(ValueError, "binary differs"):
            MODULE["verify_images"](manifest, runner)
        self.assertEqual(calls[-1], ["rm", "-v", "e" * 64])

    def test_pulled_image_labels_and_inherited_command_are_checked(self):
        expected = {"User": "1000:1000", "Entrypoint": ["/usr/local/bin/myduckserver"],
                    "Labels": MODULE["image_labels"](self.sources)}
        for field, replacement in (("User", "root"), ("Cmd", ["python3"]), ("Labels", {})):
            settings = {**expected, field: replacement}
            def runner(args, config):
                if args[0:2] == ["image", "inspect"]:
                    return json.dumps([{"Os": "linux", "Architecture": "amd64", "RepoDigests": [self.reference],
                                        "Config": settings}])
                return ""
            with self.assertRaisesRegex(ValueError, "execution configuration"):
                MODULE["verify_images"](self.manifest, runner)


if __name__ == "__main__":
    unittest.main()
