"""Fail-closed tests for carrying the alpha.55 Vitess evidence forward."""

import copy
import hashlib
import importlib.util
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch


SPEC = importlib.util.spec_from_file_location(
    "vitess_release", Path(__file__).with_name("verify-vitess-runtime.py"))
VITESS = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(VITESS)
HTTP = __import__("runpy").run_path(str(Path(__file__).with_name("verify-vitess-http.py")))

DIGEST = "a" * 64
BEFORE = "b" * 64
AFTER = "c" * 64


def canonical_hash(value):
    return hashlib.sha256(json.dumps(value, sort_keys=True, separators=(",", ":")).encode()).hexdigest()


class VitessSourceCompatibilityTest(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory(prefix="vitess-compatibility-test-")
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)
        (self.root / "release/managed-vitess").mkdir(parents=True)
        self.baseline = {
            "internal/cluster/shared.go": BEFORE,
            "internal/cluster/removed_test.go": DIGEST,
        }
        self.current = {
            "internal/cluster/shared.go": AFTER,
            "internal/cluster/added_test.go": DIGEST,
        }
        self.report = {
            "schema_version": 1,
            "runtime_source_files": self.baseline,
            "runtime_source_files_after": self.baseline,
            "http_harness_source_files": {"acceptance/vitess/http_live_test.go": BEFORE},
            "http_harness_source_files_after": {"acceptance/vitess/http_live_test.go": BEFORE},
        }
        self.artifact = b"unaltered alpha.55 native manifest\n"
        self.record = {
            "schema_version": 1,
            "qualified_release": {
                "tag": "v0.1.0-alpha.55",
                "commit": "1" * 40,
                "native_manifest_sha256": hashlib.sha256(self.artifact).hexdigest(),
                "http_evidence_sha256": "2" * 64,
                "http_report_sha256": canonical_hash(self.report),
            },
            "reviewed_release": "v0.1.0-alpha.56",
            "control_plane_commit": "3" * 40,
            "regression_evidence": {
                "url": "https://github.com/hakopod/hakopod/actions/runs/123456789",
                "tests": ["TestManagedPostgresSSLSelectionAndVitessRouting"],
            },
            "scopes": {
                "runtime": self.scope(self.baseline, self.current),
                "http_harness": self.scope(
                    self.report["http_harness_source_files"],
                    self.report["http_harness_source_files"]),
            },
        }

    @staticmethod
    def scope(before, after):
        changes = []
        for path in sorted(before.keys() | after.keys()):
            if before.get(path) != after.get(path):
                changes.append({
                    "path": path,
                    "before": before.get(path),
                    "after": after.get(path),
                    "reason": "Reviewed control-plane-only change; the qualified Vitess image and orchestration are unchanged.",
                })
        return {
            "baseline_sha256": canonical_hash(before),
            "current_sha256": canonical_hash(after),
            "changes": changes,
        }

    def write(self, record=None):
        path = self.root / "release/managed-vitess/source-compatibility.json"
        path.write_text(json.dumps(self.record if record is None else record))
        return path

    def candidate_record(self):
        record = copy.deepcopy(self.record)
        baseline = record.pop("qualified_release")
        baseline.pop("tag")
        native = baseline.pop("commit")
        record["schema_version"] = 2
        record["reviewed_release"] = "v0.1.0-alpha.58"
        record["qualified_candidate"] = {**baseline, "version": "0.1.0-alpha.58",
                                         "native_commit": native, "http_commit": "4" * 40}
        return record

    def test_prepublication_candidate_retains_exact_native_and_http_hashes(self):
        record = self.candidate_record()
        self.write(record)
        self.assertTrue(VITESS.validate_source_compatibility(
            self.root, "runtime", self.baseline, self.current,
            artifact_sha256=record["qualified_candidate"]["native_manifest_sha256"]))
        self.assertTrue(VITESS.validate_http_compatibility(
            self.root, self.report, self.current, self.report["http_harness_source_files"]))

    def test_prepublication_candidate_rejects_invented_tag_and_invalid_commits(self):
        for key, value in (("tag", "v0.1.0-alpha.58"), ("native_commit", "bad"),
                           ("http_commit", "bad"), ("version", "v0.1.0-alpha.58")):
            with self.subTest(key=key):
                record = self.candidate_record()
                record["qualified_candidate"][key] = value
                self.write(record)
                with self.assertRaises(ValueError):
                    VITESS.load_source_compatibility(self.root)

    def test_prepublication_candidate_rejects_different_reviewed_version(self):
        record = self.candidate_record()
        record["reviewed_release"] = "v0.1.0-alpha.59"
        self.write(record)
        with self.assertRaises(ValueError):
            VITESS.load_source_compatibility(self.root)

    def test_equal_source_maps_need_no_compatibility_exception(self):
        self.assertFalse(VITESS.validate_source_compatibility(
            self.root, "runtime", self.baseline, self.baseline))

    def test_accepts_only_the_exact_reviewed_modified_added_and_removed_paths(self):
        self.write()
        self.assertTrue(VITESS.validate_source_compatibility(
            self.root, "runtime", self.baseline, self.current,
            hashlib.sha256(self.artifact).hexdigest()))

        for mutation in (
                lambda value: value.__setitem__("internal/cluster/unreviewed.go", DIGEST),
                lambda value: value.__setitem__("internal/cluster/shared.go", DIGEST),
                lambda value: value.__setitem__("internal/cluster/removed_test.go", DIGEST)):
            changed = copy.deepcopy(self.current)
            mutation(changed)
            with self.subTest(changed=changed), self.assertRaises(ValueError):
                VITESS.validate_source_compatibility(self.root, "runtime", self.baseline, changed)

    def test_rejects_stale_duplicate_extra_and_unsafe_delta_records(self):
        mutations = (
            lambda value: value["scopes"]["runtime"]["changes"][0].update(after="d" * 64),
            lambda value: value["scopes"]["runtime"]["changes"].append(
                copy.deepcopy(value["scopes"]["runtime"]["changes"][0])),
            lambda value: value["scopes"]["runtime"]["changes"].append({
                "path": "internal/cluster/unchanged.go", "before": DIGEST, "after": DIGEST,
                "reason": "An unchanged path must never appear in the reviewed source delta record."}),
            lambda value: value["scopes"]["runtime"]["changes"][0].update(path="../outside.go"),
            lambda value: value["scopes"]["runtime"]["changes"][0].update(unexpected=True),
        )
        for mutation in mutations:
            changed = copy.deepcopy(self.record)
            mutation(changed)
            self.write(changed)
            with self.subTest(mutation=mutation), self.assertRaises(ValueError):
                VITESS.validate_source_compatibility(self.root, "runtime", self.baseline, self.current)

    def test_rejects_changes_to_runtime_authority_and_build_inputs(self):
        protected = (
            "go.mod",
            "auth/runtime.go",
            "internal/cluster/database_vitess.go",
            "internal/database/vitess_policy.go",
            "patches/vitess-tablet-hostname-test.go.txt",
            "Dockerfile.vitess-runtime",
            "scripts/build-managed-vitess.sh",
            "scripts/apply-managed-vitess-patches.py",
            "installer/vitess_controller.py",
        )
        for path in protected:
            baseline, current = {path: BEFORE}, {path: AFTER}
            changed = copy.deepcopy(self.record)
            changed["scopes"]["runtime"] = self.scope(baseline, current)
            self.write(changed)
            with self.subTest(path=path), self.assertRaises(ValueError):
                VITESS.validate_source_compatibility(self.root, "runtime", baseline, current)

    def test_loader_rejects_unknown_fields_invalid_identity_and_weak_review_evidence(self):
        mutations = (
            lambda value: value.update(unexpected=True),
            lambda value: value["qualified_release"].update(tag="alpha.55"),
            lambda value: value["qualified_release"].update(commit="1" * 39),
            lambda value: value.update(reviewed_release="0.1.0-alpha.56"),
            lambda value: value["regression_evidence"].update(url="http://example.test/run"),
            lambda value: value["regression_evidence"].update(tests=[]),
            lambda value: value["scopes"]["runtime"]["changes"][0].update(reason="too short"),
        )
        for mutation in mutations:
            changed = copy.deepcopy(self.record)
            mutation(changed)
            self.write(changed)
            with self.subTest(mutation=mutation), self.assertRaises(ValueError):
                VITESS.load_source_compatibility(self.root)

    def test_historical_manifest_hash_is_bound_without_rewriting_evidence(self):
        self.write()
        original = copy.deepcopy(self.record)
        self.assertTrue(VITESS.validate_source_compatibility(
            self.root, "runtime", self.baseline, self.current,
            hashlib.sha256(self.artifact).hexdigest()))
        self.assertEqual(json.loads(self.write().read_text()), original)
        with self.assertRaises(ValueError):
            VITESS.validate_source_compatibility(
                self.root, "runtime", self.baseline, self.current,
                hashlib.sha256(self.artifact + b"changed").hexdigest())

    def test_http_reuse_pins_canonical_report_and_both_source_scopes(self):
        runtime_current = copy.deepcopy(self.current)
        harness_current = copy.deepcopy(self.report["http_harness_source_files"])
        harness_current["scripts/run-development-vitess-http-acceptance.py"] = AFTER
        self.record["scopes"]["http_harness"] = self.scope(
            self.report["http_harness_source_files"], harness_current)
        self.write()
        self.assertTrue(VITESS.validate_http_compatibility(
            self.root, self.report, runtime_current, harness_current))

        changed_report = copy.deepcopy(self.report)
        changed_report["passed"] = True
        with self.assertRaises(ValueError):
            VITESS.validate_http_compatibility(
                self.root, changed_report, runtime_current, harness_current)
        changed_harness = copy.deepcopy(harness_current)
        changed_harness["acceptance/vitess/http_live_test.go"] = DIGEST
        with self.assertRaises(ValueError):
            VITESS.validate_http_compatibility(
                self.root, self.report, runtime_current, changed_harness)

    def test_http_verifier_rejects_byte_changes_to_the_historical_report(self):
        evidence = self.root / "historical-http.json"
        raw = json.dumps(self.report, sort_keys=True).encode()
        evidence.write_bytes(raw)
        self.record["qualified_release"]["http_evidence_sha256"] = hashlib.sha256(raw).hexdigest()
        self.write()
        native = {
            "source_boolean": lambda root, name: True,
            "load_source_compatibility": VITESS.load_source_compatibility,
            "compatibility_baseline": VITESS.compatibility_baseline,
            "canonical_hash": VITESS.canonical_hash,
        }
        state = HTTP["verify"].__globals__
        with patch.dict(state, NATIVE=native), patch.dict(
                state, validate_report=lambda report, root: report):
            self.assertEqual(HTTP["verify"](evidence, root=self.root), self.report)
            evidence.write_text(json.dumps(self.report, sort_keys=True, indent=2))
            with self.assertRaisesRegex(ValueError, "bytes changed"):
                HTTP["verify"](evidence, root=self.root)

    def test_real_legacy_http_schema_requires_the_exact_compatibility_review(self):
        runtime_current = copy.deepcopy(self.current)
        legacy = json.loads((Path(__file__).parent / "managed-vitess-http/evidence.json").read_text())
        legacy["runtime_source_files"] = copy.deepcopy(self.baseline)
        legacy["runtime_source_files_after"] = copy.deepcopy(self.baseline)
        legacy["http_harness_source_files"] = {
            "acceptance/vitess/http_live_test.go": BEFORE}
        legacy["http_harness_source_files_after"] = copy.deepcopy(
            legacy["http_harness_source_files"])
        harness_current = copy.deepcopy(legacy["http_harness_source_files"])
        harness_current["scripts/run-development-vitess-http-acceptance.py"] = AFTER
        self.record["qualified_release"]["http_report_sha256"] = canonical_hash(legacy)
        self.record["scopes"]["http_harness"] = self.scope(
            legacy["http_harness_source_files"], harness_current)
        self.write()
        native = {
            "DIGEST": __import__("re").compile(r"[a-f0-9]{64}"),
            "source_files": lambda root: runtime_current,
            "source_constant": lambda root, name: name + "@sha256:" + DIGEST,
            "validate_native_environment": lambda environment, case, images: environment,
            "validate_http_compatibility": VITESS.validate_http_compatibility,
        }
        state = HTTP["validate_report"].__globals__
        with patch.dict(state, NATIVE=native), patch.dict(
                state, harness_inventory=lambda root: harness_current):
            self.assertEqual(HTTP["validate_report"](legacy, self.root), legacy)

            changed = copy.deepcopy(legacy)
            changed["elapsed_seconds"] += 1
            with self.assertRaisesRegex(ValueError, "historical HTTP report"):
                HTTP["validate_report"](changed, self.root)

            (self.root / "release/managed-vitess/source-compatibility.json").unlink()
            with self.assertRaisesRegex(ValueError, "without an exact compatibility review"):
                HTTP["validate_report"](legacy, self.root)


if __name__ == "__main__":
    unittest.main()
