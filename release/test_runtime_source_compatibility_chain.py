"""Fail-closed regression tests for chained historical runtime evidence."""

import copy
import json
from pathlib import Path
import runpy
import shutil
import tempfile
import unittest


HERE = Path(__file__).resolve().parent
CHAIN = runpy.run_path(str(HERE / "runtime-source-compatibility.py"))
VITESS = runpy.run_path(str(HERE / "verify-vitess-runtime.py"))
A = "a" * 64
B = "b" * 64
C = "c" * 64


class ChainedRuntimeCompatibilityTest(unittest.TestCase):
    def test_published_predecessor_copies_match_exact_anchors(self):
        for runtime, expected in CHAIN["CHAIN_PREDECESSORS"].items():
            path = HERE / ("managed-" + runtime) / "source-compatibility-alpha59.json"
            self.assertTrue(path.is_file())
            self.assertFalse(path.is_symlink())
            self.assertEqual(CHAIN["canonical_hash"](json.loads(path.read_text())), expected)

    def test_predecessors_reconstruct_from_retained_native_manifests(self):
        for runtime in CHAIN["CHAIN_PREDECESSORS"]:
            predecessor = json.loads(
                (HERE / ("managed-" + runtime) / "source-compatibility-alpha59.json").read_text())
            manifest = json.loads((HERE / ("managed-" + runtime) / "manifest.json").read_text())
            recorded = manifest["source_files"]
            delta = (predecessor["scopes"]["runtime"] if runtime == "vitess"
                     else predecessor["delta"])
            with self.subTest(runtime=runtime):
                self.assertEqual(CHAIN["canonical_hash"](recorded), delta["baseline_sha256"])
                reconstructed = CHAIN["apply_chain_delta"](recorded, delta)
                self.assertEqual(CHAIN["canonical_hash"](reconstructed), delta["current_sha256"])

    def synthetic(self, runtime="myduck"):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        root = Path(temporary.name)
        directory = root / "release" / ("managed-" + runtime)
        directory.mkdir(parents=True)
        trust = "release/runtime-source-compatibility.py"
        recorded = {"internal/api/shared.go": A, trust: A}
        previous = {"internal/api/shared.go": B, trust: A}
        current = {"internal/api/shared.go": B, trust: C}
        predecessor = {
            "qualified_release": {
                "tag": "v0.1.0-alpha.58",
                "commit": "1" * 40,
                "manifest_sha256": A,
            },
            "delta": self.delta(recorded, previous, "internal/api/shared.go"),
        }
        predecessor_hash = CHAIN["canonical_hash"](predecessor)
        (directory / "source-compatibility-alpha59.json").write_text(json.dumps(predecessor))
        record = {
            "schema_version": 2,
            "runtime": runtime,
            "qualified_release": copy.deepcopy(predecessor["qualified_release"]),
            "reviewed_release": "v0.1.0-alpha.60",
            "control_plane_commit": "2" * 40,
            "regression_evidence": {
                "url": "https://github.com/hakopod/hakopod/actions/runs/123456",
                "tests": ["TestHistoricalRuntimeCompatibility"],
            },
            "predecessor": {
                "tag": "v0.1.0-alpha.59",
                "commit": "869cb4186566199d8611c4ffb30212d42d0daa1d",
                "review_sha256": predecessor_hash,
            },
            "bootstrap": {
                "tag": "v0.1.0-alpha.59",
                "commit": "869cb4186566199d8611c4ffb30212d42d0daa1d",
                "reviewed_release": "v0.1.0-alpha.60",
                "trust_anchor": "authenticated-git-review",
                "trust_paths": [trust],
            },
            "delta": self.delta(previous, current, trust),
        }
        globals_ = CHAIN["validate_chain"].__globals__
        old_predecessors = globals_["CHAIN_PREDECESSORS"]
        old_trust = globals_["CHAIN_TRUST_PATHS"]
        globals_["CHAIN_PREDECESSORS"] = {runtime: predecessor_hash}
        globals_["CHAIN_TRUST_PATHS"] = {runtime: {trust}}
        self.addCleanup(lambda: globals_.update(
            CHAIN_PREDECESSORS=old_predecessors, CHAIN_TRUST_PATHS=old_trust))
        return root, record, recorded, current

    @staticmethod
    def delta(before, after, changed):
        return {
            "baseline_sha256": CHAIN["canonical_hash"](before),
            "current_sha256": CHAIN["canonical_hash"](after),
            "changes": [{
                "path": changed,
                "before": before.get(changed),
                "after": after.get(changed),
                "reason": "The exact reviewed source change leaves the historical native runtime evidence unchanged.",
            }],
        }

    def validate(self, root, record, recorded, current, artifact=A):
        return CHAIN["validate_chain"](
            root, "myduck", record, recorded, current,
            lambda path: path.startswith("internal/runtime/"), artifact)

    def test_chain_retains_historical_identity_and_exact_current_map(self):
        root, record, recorded, current = self.synthetic()
        self.assertTrue(self.validate(root, record, recorded, current))
        self.assertEqual(record["qualified_release"]["tag"], "v0.1.0-alpha.58")
        changed = dict(current, **{"internal/api/unreviewed.go": A})
        with self.assertRaisesRegex(ValueError, "outside the chained review"):
            self.validate(root, record, recorded, changed)
        with self.assertRaisesRegex(ValueError, "another native manifest"):
            self.validate(root, record, recorded, current, B)

    def test_predecessor_anchor_and_content_drift_are_rejected(self):
        root, record, recorded, current = self.synthetic()
        for field, value in (("tag", "v0.1.0-alpha.58"), ("commit", "3" * 40),
                             ("review_sha256", B)):
            changed = copy.deepcopy(record)
            changed["predecessor"][field] = value
            with self.subTest(field=field), self.assertRaisesRegex(ValueError, "predecessor identity"):
                self.validate(root, changed, recorded, current)
        path = root / "release/managed-myduck/source-compatibility-alpha59.json"
        predecessor = json.loads(path.read_text())
        predecessor["delta"]["changes"][0]["reason"] += " changed"
        path.write_text(json.dumps(predecessor))
        with self.assertRaisesRegex(ValueError, "predecessor review changed"):
            self.validate(root, record, recorded, current)

    def test_protected_paths_and_unlisted_trust_exemptions_are_rejected(self):
        root, record, recorded, current = self.synthetic()
        previous = CHAIN["apply_chain_delta"](
            recorded,
            json.loads((root / "release/managed-myduck/source-compatibility-alpha59.json").read_text())["delta"],
        )
        for path in ("internal/runtime/controller.go", "release/verify-myduck-runtime.py"):
            changed_current = dict(previous, **{path: C})
            changed = copy.deepcopy(record)
            changed["delta"] = self.delta(previous, changed_current, path)
            with self.subTest(path=path), self.assertRaisesRegex(ValueError, "require new qualification"):
                self.validate(root, changed, recorded, changed_current)
        changed = copy.deepcopy(record)
        changed["bootstrap"]["trust_paths"] = []
        with self.assertRaisesRegex(ValueError, "bootstrap changed"):
            self.validate(root, changed, recorded, current)

    def test_vitess_loader_accepts_only_exact_published_predecessor(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        root = Path(temporary.name)
        directory = root / "release/managed-vitess"
        directory.mkdir(parents=True)
        predecessor_path = HERE / "managed-vitess/source-compatibility-alpha59.json"
        shutil.copyfile(predecessor_path, directory / predecessor_path.name)
        predecessor = json.loads(predecessor_path.read_text())
        empty = {"baseline_sha256": CHAIN["canonical_hash"]({}),
                 "current_sha256": CHAIN["canonical_hash"]({}), "changes": []}
        record = {
            "schema_version": 3,
            "runtime": "vitess",
            "qualified_release": predecessor["qualified_release"],
            "reviewed_release": "v0.1.0-alpha.60",
            "control_plane_commit": "4" * 40,
            "regression_evidence": {"url": "https://github.com/hakopod/hakopod/actions/runs/123456",
                                    "tests": ["TestManagedVitessRuntimeCompatibility"]},
            "predecessor": {"tag": "v0.1.0-alpha.59",
                            "commit": "869cb4186566199d8611c4ffb30212d42d0daa1d",
                            "review_sha256": CHAIN["CHAIN_PREDECESSORS"]["vitess"]},
            "bootstrap": {"tag": "v0.1.0-alpha.59",
                          "commit": "869cb4186566199d8611c4ffb30212d42d0daa1d",
                          "reviewed_release": "v0.1.0-alpha.60",
                          "trust_anchor": "authenticated-git-review",
                          "trust_paths": sorted(CHAIN["CHAIN_TRUST_PATHS"]["vitess"])},
            "scopes": {"runtime": empty, "http_harness": empty},
        }
        path = directory / "source-compatibility.json"
        path.write_text(json.dumps(record))
        self.assertEqual(VITESS["load_source_compatibility"](root), record)
        changed_predecessor = copy.deepcopy(predecessor)
        changed_predecessor["control_plane_commit"] = "5" * 40
        (directory / predecessor_path.name).write_text(json.dumps(changed_predecessor))
        with self.assertRaisesRegex(ValueError, "predecessor review changed"):
            VITESS["load_source_compatibility"](root)


if __name__ == "__main__":
    unittest.main()
