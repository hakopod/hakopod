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
            tests = [test] + ([test + "/" + phase for phase in MODULE["HTTP_PHASES"]] if case == "http-api" else [])
            events = [{"Action": "run", "Package": package, "Test": name} for name in tests]
            events += [{"Action": "pass", "Package": package, "Test": name} for name in reversed(tests)]
            attempts.append({"case": case, "exit_code": 0,
                "source_manifest_sha256": MODULE["source_hash"](self.sources), "source_manifest_after_sha256": MODULE["source_hash"](self.sources),
                "log_sha256": "b" * 64, "test_events": events, "cluster_uid": "11111111-1111-1111-1111-111111111111",
                "node_uids": {"k3d-hakopod-dev-server-0": "22222222-2222-2222-2222-222222222222"},
                "cleanup": {"namespaces": ["hdb-" + "a" * 32] + (["hdb-" + "b" * 32] if case != "lifecycle" else []),
                            "namespaces_absent": True, "persistent_volumes_absent": True}})
        self.acceptance = {"schema_version": 1, "context": "k3d-hakopod-dev", "execution": "native", "platform": "linux/amd64",
                           "source_files": self.sources, "images": self.images, "attempts": attempts}
        self.json("native-acceptance.json", self.acceptance)
        self.json("source-build-manifest.json", {"schema_version": 1, "upstream_repository": "https://github.com/apecloud/myduckserver",
            "upstream_commit": MODULE["UPSTREAM"], "files": {name: self.sources[name] for name in MODULE["BUILD_INPUTS"]}})
        self.binaries = {name: "c" * 64 for name in MODULE["BINARIES"]}
        self.json("packaging-receipt.json", {"image": self.reference, "binaries": self.binaries, "platform": "linux/amd64",
            "source_manifest_sha256": MODULE["file_hash"](self.output / "source-build-manifest.json")})
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
        for name in ("internal/cluster/database_myduck.go", "cmd/hakopod-myduck-storage/main.go"):
            path = self.root / name
            old = path.read_text()
            path.write_text(old + "// changed\n")
            with self.assertRaisesRegex(ValueError, "source changed"):
                MODULE["validate_metadata"](self.output, self.root)
            path.write_text(old)
        self.write("internal/new_runtime.go", "package internal\n")
        with self.assertRaisesRegex(ValueError, "source changed"):
            MODULE["validate_metadata"](self.output, self.root)

    def test_skips_incomplete_cases_forged_success_and_residue_are_rejected(self):
        mutations = [lambda value: value["attempts"].pop(),
            lambda value: value["attempts"][0].update(exit_code=False),
            lambda value: value["attempts"][0]["test_events"][-1].update(Action="skip"),
            lambda value: value["attempts"][0]["test_events"].pop(),
            lambda value: value["attempts"][0]["cleanup"].update(persistent_volumes_absent=False),
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

    def test_anonymous_image_binary_and_execution_verification(self):
        manifest = copy.deepcopy(self.manifest)
        manifest["images"]["runtime"]["binaries"] = {name: hashlib.sha256(b"fixture binary").hexdigest() for name in MODULE["BINARIES"]}
        calls = []
        def runner(args, config):
            calls.append(args)
            if args[0:2] == ["image", "inspect"]:
                return json.dumps([{"Os": "linux", "Architecture": "amd64", "RepoDigests": [self.reference],
                    "Config": {"User": "1000:1000", "Entrypoint": ["/usr/local/bin/myduckserver"]}}])
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


if __name__ == "__main__":
    unittest.main()
