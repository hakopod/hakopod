"""Fail-closed regression tests for chained historical runtime evidence."""

import copy
import hashlib
import json
import os
from pathlib import Path
import runpy
import shutil
import subprocess
import tempfile
import unittest
from unittest import mock
import zipfile


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

    def test_alpha62_preserves_exact_anchors_and_rejects_tampering(self):
        self.check_published_review("NEXT_CHAIN_PREDECESSORS", "source-compatibility-alpha62.json",
                                    ["source-compatibility-alpha59.json", "source-compatibility-alpha61.json"])

    def test_alpha63_preserves_exact_anchors_and_rejects_tampering(self):
        self.check_published_review("ALPHA63_CHAIN_PREDECESSORS", "source-compatibility-alpha63.json",
                                    ["source-compatibility-alpha59.json", "source-compatibility-alpha61.json",
                                     "source-compatibility-alpha62.json"])

    def test_alpha64_preserves_exact_anchors_and_rejects_tampering(self):
        self.check_published_review("ALPHA64_CHAIN_PREDECESSORS", "source-compatibility-alpha64.json",
                                    ["source-compatibility-alpha59.json", "source-compatibility-alpha61.json",
                                     "source-compatibility-alpha62.json", "source-compatibility-alpha63.json"])

    def test_alpha65_preserves_exact_anchors_and_rejects_tampering(self):
        self.check_published_review("ALPHA65_CHAIN_PREDECESSORS", "source-compatibility-alpha65.json",
                                    ["source-compatibility-alpha59.json", "source-compatibility-alpha61.json",
                                     "source-compatibility-alpha62.json", "source-compatibility-alpha63.json",
                                     "source-compatibility-alpha64.json"])

    def test_alpha66_preserves_exact_anchors_and_rejects_tampering(self):
        self.check_published_review("ALPHA66_CHAIN_PREDECESSORS", "source-compatibility-alpha66.json",
                                    ["source-compatibility-alpha59.json", "source-compatibility-alpha61.json",
                                     "source-compatibility-alpha62.json", "source-compatibility-alpha63.json",
                                     "source-compatibility-alpha64.json", "source-compatibility-alpha65.json"])

    def test_alpha67_preserves_exact_anchors_and_rejects_tampering(self):
        self.check_published_review("ALPHA67_CHAIN_PREDECESSORS", "source-compatibility-alpha67.json",
                                    ["source-compatibility-alpha59.json", "source-compatibility-alpha61.json",
                                     "source-compatibility-alpha62.json", "source-compatibility-alpha63.json",
                                     "source-compatibility-alpha64.json", "source-compatibility-alpha65.json",
                                     "source-compatibility-alpha66.json"])

    def test_alpha68_preserves_exact_anchors_and_rejects_tampering(self):
        self.check_published_review("ALPHA68_CHAIN_PREDECESSORS", "source-compatibility-alpha68.json",
                                    ["source-compatibility-alpha59.json", "source-compatibility-alpha61.json",
                                     "source-compatibility-alpha62.json", "source-compatibility-alpha63.json",
                                     "source-compatibility-alpha64.json", "source-compatibility-alpha65.json",
                                     "source-compatibility-alpha66.json", "source-compatibility-alpha67.json"])

    def test_alpha69_preserves_exact_anchors_and_rejects_tampering(self):
        self.check_published_review("ALPHA69_CHAIN_PREDECESSORS", "source-compatibility-alpha69.json",
                                    ["source-compatibility-alpha59.json", "source-compatibility-alpha61.json",
                                     "source-compatibility-alpha62.json", "source-compatibility-alpha63.json",
                                     "source-compatibility-alpha64.json", "source-compatibility-alpha65.json",
                                     "source-compatibility-alpha66.json", "source-compatibility-alpha67.json",
                                     "source-compatibility-alpha68.json"])

    def test_alpha70_preserves_exact_anchors_and_rejects_tampering(self):
        self.check_published_review("ALPHA70_CHAIN_PREDECESSORS", "source-compatibility-alpha70.json",
                                    ["source-compatibility-alpha59.json", "source-compatibility-alpha61.json",
                                     "source-compatibility-alpha62.json", "source-compatibility-alpha63.json",
                                     "source-compatibility-alpha64.json", "source-compatibility-alpha65.json",
                                     "source-compatibility-alpha66.json", "source-compatibility-alpha67.json",
                                     "source-compatibility-alpha68.json", "source-compatibility-alpha69.json"])

    def test_alpha71_preserves_exact_anchors_and_rejects_tampering(self):
        self.check_published_review("ALPHA71_CHAIN_PREDECESSORS", "source-compatibility-alpha71.json",
                                    ["source-compatibility-alpha59.json", "source-compatibility-alpha61.json",
                                     "source-compatibility-alpha62.json", "source-compatibility-alpha63.json",
                                     "source-compatibility-alpha64.json", "source-compatibility-alpha65.json",
                                     "source-compatibility-alpha66.json", "source-compatibility-alpha67.json",
                                     "source-compatibility-alpha68.json", "source-compatibility-alpha69.json",
                                     "source-compatibility-alpha70.json"])

    def test_alpha72_preserves_exact_anchors_and_rejects_tampering(self):
        self.check_published_review("ALPHA72_CHAIN_PREDECESSORS", "source-compatibility-alpha72.json",
                                    ["source-compatibility-alpha59.json", "source-compatibility-alpha61.json",
                                     "source-compatibility-alpha62.json", "source-compatibility-alpha63.json",
                                     "source-compatibility-alpha64.json", "source-compatibility-alpha65.json",
                                     "source-compatibility-alpha66.json", "source-compatibility-alpha67.json",
                                     "source-compatibility-alpha68.json", "source-compatibility-alpha69.json",
                                     "source-compatibility-alpha70.json", "source-compatibility-alpha71.json"])

    def test_alpha73_preserves_exact_anchors_and_rejects_tampering(self):
        self.check_published_review("ALPHA73_CHAIN_PREDECESSORS", "source-compatibility-alpha73.json",
                                    ["source-compatibility-alpha59.json", "source-compatibility-alpha61.json",
                                     "source-compatibility-alpha62.json", "source-compatibility-alpha63.json",
                                     "source-compatibility-alpha64.json", "source-compatibility-alpha65.json",
                                     "source-compatibility-alpha66.json", "source-compatibility-alpha67.json",
                                     "source-compatibility-alpha68.json", "source-compatibility-alpha69.json",
                                     "source-compatibility-alpha70.json", "source-compatibility-alpha71.json",
                                     "source-compatibility-alpha72.json"])

    def test_alpha74_preserves_exact_anchors_and_rejects_tampering(self):
        self.check_published_review("ALPHA74_CHAIN_PREDECESSORS", "source-compatibility-alpha74.json",
                                    ["source-compatibility-alpha59.json", "source-compatibility-alpha61.json",
                                     "source-compatibility-alpha62.json", "source-compatibility-alpha63.json",
                                     "source-compatibility-alpha64.json", "source-compatibility-alpha65.json",
                                     "source-compatibility-alpha66.json", "source-compatibility-alpha67.json",
                                     "source-compatibility-alpha68.json", "source-compatibility-alpha69.json",
                                     "source-compatibility-alpha70.json", "source-compatibility-alpha71.json",
                                     "source-compatibility-alpha72.json", "source-compatibility-alpha73.json"])

    def test_alpha75_preserves_exact_anchors_and_rejects_tampering(self):
        self.check_published_review("ALPHA75_CHAIN_PREDECESSORS", "source-compatibility-alpha75.json",
                                    ["source-compatibility-alpha59.json", "source-compatibility-alpha61.json",
                                     "source-compatibility-alpha62.json", "source-compatibility-alpha63.json",
                                     "source-compatibility-alpha64.json", "source-compatibility-alpha65.json",
                                     "source-compatibility-alpha66.json", "source-compatibility-alpha67.json",
                                     "source-compatibility-alpha68.json", "source-compatibility-alpha69.json",
                                     "source-compatibility-alpha70.json", "source-compatibility-alpha71.json",
                                     "source-compatibility-alpha72.json", "source-compatibility-alpha73.json",
                                     "source-compatibility-alpha74.json"])

    def test_alpha76_preserves_exact_anchors_and_rejects_tampering(self):
        self.check_published_review("ALPHA76_CHAIN_PREDECESSORS", "source-compatibility-alpha76.json",
                                    ["source-compatibility-alpha59.json", "source-compatibility-alpha61.json",
                                     "source-compatibility-alpha62.json", "source-compatibility-alpha63.json",
                                     "source-compatibility-alpha64.json", "source-compatibility-alpha65.json",
                                     "source-compatibility-alpha66.json", "source-compatibility-alpha67.json",
                                     "source-compatibility-alpha68.json", "source-compatibility-alpha69.json",
                                     "source-compatibility-alpha70.json", "source-compatibility-alpha71.json",
                                     "source-compatibility-alpha72.json", "source-compatibility-alpha73.json",
                                     "source-compatibility-alpha74.json", "source-compatibility-alpha75.json"])

    def test_alpha77_preserves_exact_anchors_and_rejects_tampering(self):
        self.check_published_review("ALPHA77_CHAIN_PREDECESSORS", "source-compatibility-alpha77.json",
                                    ["source-compatibility-alpha59.json", "source-compatibility-alpha61.json",
                                     "source-compatibility-alpha62.json", "source-compatibility-alpha63.json",
                                     "source-compatibility-alpha64.json", "source-compatibility-alpha65.json",
                                     "source-compatibility-alpha66.json", "source-compatibility-alpha67.json",
                                     "source-compatibility-alpha68.json", "source-compatibility-alpha69.json",
                                     "source-compatibility-alpha70.json", "source-compatibility-alpha71.json",
                                     "source-compatibility-alpha72.json", "source-compatibility-alpha73.json",
                                     "source-compatibility-alpha74.json", "source-compatibility-alpha75.json",
                                     "source-compatibility-alpha76.json"])

    def test_alpha78_preserves_exact_anchors_and_rejects_tampering(self):
        self.check_published_review("ALPHA78_CHAIN_PREDECESSORS", "source-compatibility-alpha78.json",
                                    ["source-compatibility-alpha59.json", "source-compatibility-alpha61.json",
                                     "source-compatibility-alpha62.json", "source-compatibility-alpha63.json",
                                     "source-compatibility-alpha64.json", "source-compatibility-alpha65.json",
                                     "source-compatibility-alpha66.json", "source-compatibility-alpha67.json",
                                     "source-compatibility-alpha68.json", "source-compatibility-alpha69.json",
                                     "source-compatibility-alpha70.json", "source-compatibility-alpha71.json",
                                     "source-compatibility-alpha72.json", "source-compatibility-alpha73.json",
                                     "source-compatibility-alpha74.json", "source-compatibility-alpha75.json",
                                     "source-compatibility-alpha76.json", "source-compatibility-alpha77.json"])

    def test_alpha79_preserves_exact_anchors_and_rejects_tampering(self):
        self.check_published_review("ALPHA79_CHAIN_PREDECESSORS", "source-compatibility-alpha79.json",
                                    ["source-compatibility-alpha59.json", "source-compatibility-alpha61.json",
                                     "source-compatibility-alpha62.json", "source-compatibility-alpha63.json",
                                     "source-compatibility-alpha64.json", "source-compatibility-alpha65.json",
                                     "source-compatibility-alpha66.json", "source-compatibility-alpha67.json",
                                     "source-compatibility-alpha68.json", "source-compatibility-alpha69.json",
                                     "source-compatibility-alpha70.json", "source-compatibility-alpha71.json",
                                     "source-compatibility-alpha72.json", "source-compatibility-alpha73.json",
                                     "source-compatibility-alpha74.json", "source-compatibility-alpha75.json",
                                     "source-compatibility-alpha76.json", "source-compatibility-alpha77.json",
                                     "source-compatibility-alpha78.json"])

    def test_alpha80_preserves_exact_anchors_and_rejects_tampering(self):
        self.check_published_review("ALPHA80_CHAIN_PREDECESSORS", "source-compatibility-alpha80.json",
                                    ["source-compatibility-alpha59.json", "source-compatibility-alpha61.json",
                                     "source-compatibility-alpha62.json", "source-compatibility-alpha63.json",
                                     "source-compatibility-alpha64.json", "source-compatibility-alpha65.json",
                                     "source-compatibility-alpha66.json", "source-compatibility-alpha67.json",
                                     "source-compatibility-alpha68.json", "source-compatibility-alpha69.json",
                                     "source-compatibility-alpha70.json", "source-compatibility-alpha71.json",
                                     "source-compatibility-alpha72.json", "source-compatibility-alpha73.json",
                                     "source-compatibility-alpha74.json", "source-compatibility-alpha75.json",
                                     "source-compatibility-alpha76.json", "source-compatibility-alpha77.json",
                                     "source-compatibility-alpha78.json", "source-compatibility-alpha79.json"])

    def test_alpha81_preserves_exact_anchors_and_rejects_tampering(self):
        self.check_published_review("ALPHA81_CHAIN_PREDECESSORS", "source-compatibility-alpha81.json",
                                    ["source-compatibility-alpha59.json", "source-compatibility-alpha61.json",
                                     "source-compatibility-alpha62.json", "source-compatibility-alpha63.json",
                                     "source-compatibility-alpha64.json", "source-compatibility-alpha65.json",
                                     "source-compatibility-alpha66.json", "source-compatibility-alpha67.json",
                                     "source-compatibility-alpha68.json", "source-compatibility-alpha69.json",
                                     "source-compatibility-alpha70.json", "source-compatibility-alpha71.json",
                                     "source-compatibility-alpha72.json", "source-compatibility-alpha73.json",
                                     "source-compatibility-alpha74.json", "source-compatibility-alpha75.json",
                                     "source-compatibility-alpha76.json", "source-compatibility-alpha77.json",
                                     "source-compatibility-alpha78.json", "source-compatibility-alpha79.json",
                                     "source-compatibility-alpha80.json"])

    def test_alpha82_preserves_exact_anchors_and_rejects_tampering(self):
        self.check_published_review("ALPHA82_CHAIN_PREDECESSORS", "source-compatibility-alpha82.json",
                                    ["source-compatibility-alpha59.json", "source-compatibility-alpha61.json",
                                     "source-compatibility-alpha62.json", "source-compatibility-alpha63.json",
                                     "source-compatibility-alpha64.json", "source-compatibility-alpha65.json",
                                     "source-compatibility-alpha66.json", "source-compatibility-alpha67.json",
                                     "source-compatibility-alpha68.json", "source-compatibility-alpha69.json",
                                     "source-compatibility-alpha70.json", "source-compatibility-alpha71.json",
                                     "source-compatibility-alpha72.json", "source-compatibility-alpha73.json",
                                     "source-compatibility-alpha74.json", "source-compatibility-alpha75.json",
                                     "source-compatibility-alpha76.json", "source-compatibility-alpha77.json",
                                     "source-compatibility-alpha78.json", "source-compatibility-alpha79.json",
                                     "source-compatibility-alpha80.json", "source-compatibility-alpha81.json"])

    def test_alpha83_preserves_exact_anchors_and_rejects_tampering(self):
        self.check_published_review("ALPHA83_CHAIN_PREDECESSORS", "source-compatibility-alpha83.json",
                                    ["source-compatibility-alpha59.json", "source-compatibility-alpha61.json",
                                     "source-compatibility-alpha62.json", "source-compatibility-alpha63.json",
                                     "source-compatibility-alpha64.json", "source-compatibility-alpha65.json",
                                     "source-compatibility-alpha66.json", "source-compatibility-alpha67.json",
                                     "source-compatibility-alpha68.json", "source-compatibility-alpha69.json",
                                     "source-compatibility-alpha70.json", "source-compatibility-alpha71.json",
                                     "source-compatibility-alpha72.json", "source-compatibility-alpha73.json",
                                     "source-compatibility-alpha74.json", "source-compatibility-alpha75.json",
                                     "source-compatibility-alpha76.json", "source-compatibility-alpha77.json",
                                     "source-compatibility-alpha78.json", "source-compatibility-alpha79.json",
                                     "source-compatibility-alpha80.json", "source-compatibility-alpha81.json",
                                     "source-compatibility-alpha82.json"])

    def test_alpha84_preserves_exact_anchors_and_rejects_tampering(self):
        self.check_published_review("ALPHA84_CHAIN_PREDECESSORS", "source-compatibility-alpha84.json",
                                    ["source-compatibility-alpha59.json", "source-compatibility-alpha61.json",
                                     "source-compatibility-alpha62.json", "source-compatibility-alpha63.json",
                                     "source-compatibility-alpha64.json", "source-compatibility-alpha65.json",
                                     "source-compatibility-alpha66.json", "source-compatibility-alpha67.json",
                                     "source-compatibility-alpha68.json", "source-compatibility-alpha69.json",
                                     "source-compatibility-alpha70.json", "source-compatibility-alpha71.json",
                                     "source-compatibility-alpha72.json", "source-compatibility-alpha73.json",
                                     "source-compatibility-alpha74.json", "source-compatibility-alpha75.json",
                                     "source-compatibility-alpha76.json", "source-compatibility-alpha77.json",
                                     "source-compatibility-alpha78.json", "source-compatibility-alpha79.json",
                                     "source-compatibility-alpha80.json", "source-compatibility-alpha81.json",
                                     "source-compatibility-alpha82.json", "source-compatibility-alpha83.json"])

    def test_alpha85_preserves_exact_anchors_and_rejects_tampering(self):
        self.check_published_review("ALPHA85_CHAIN_PREDECESSORS", "source-compatibility-alpha85.json",
                                    ["source-compatibility-alpha59.json", "source-compatibility-alpha61.json",
                                     "source-compatibility-alpha62.json", "source-compatibility-alpha63.json",
                                     "source-compatibility-alpha64.json", "source-compatibility-alpha65.json",
                                     "source-compatibility-alpha66.json", "source-compatibility-alpha67.json",
                                     "source-compatibility-alpha68.json", "source-compatibility-alpha69.json",
                                     "source-compatibility-alpha70.json", "source-compatibility-alpha71.json",
                                     "source-compatibility-alpha72.json", "source-compatibility-alpha73.json",
                                     "source-compatibility-alpha74.json", "source-compatibility-alpha75.json",
                                     "source-compatibility-alpha76.json", "source-compatibility-alpha77.json",
                                     "source-compatibility-alpha78.json", "source-compatibility-alpha79.json",
                                     "source-compatibility-alpha80.json", "source-compatibility-alpha81.json",
                                     "source-compatibility-alpha82.json", "source-compatibility-alpha83.json",
                                     "source-compatibility-alpha84.json"])

    def test_alpha86_preserves_exact_anchors_and_rejects_tampering(self):
        self.check_published_review("ALPHA86_CHAIN_PREDECESSORS", "source-compatibility-alpha86.json",
                                    ["source-compatibility-alpha59.json", "source-compatibility-alpha61.json",
                                     "source-compatibility-alpha62.json", "source-compatibility-alpha63.json",
                                     "source-compatibility-alpha64.json", "source-compatibility-alpha65.json",
                                     "source-compatibility-alpha66.json", "source-compatibility-alpha67.json",
                                     "source-compatibility-alpha68.json", "source-compatibility-alpha69.json",
                                     "source-compatibility-alpha70.json", "source-compatibility-alpha71.json",
                                     "source-compatibility-alpha72.json", "source-compatibility-alpha73.json",
                                     "source-compatibility-alpha74.json", "source-compatibility-alpha75.json",
                                     "source-compatibility-alpha76.json", "source-compatibility-alpha77.json",
                                     "source-compatibility-alpha78.json", "source-compatibility-alpha79.json",
                                     "source-compatibility-alpha80.json", "source-compatibility-alpha81.json",
                                     "source-compatibility-alpha82.json", "source-compatibility-alpha83.json",
                                     "source-compatibility-alpha84.json",
                                     "source-compatibility-alpha85.json"])

    def test_alpha89_preserves_exact_anchors_and_rejects_tampering(self):
        self.check_published_review("ALPHA89_CHAIN_PREDECESSORS", "source-compatibility.json",
                                    ["source-compatibility-alpha59.json", "source-compatibility-alpha61.json",
                                     "source-compatibility-alpha62.json", "source-compatibility-alpha63.json",
                                     "source-compatibility-alpha64.json", "source-compatibility-alpha65.json",
                                     "source-compatibility-alpha66.json", "source-compatibility-alpha67.json",
                                     "source-compatibility-alpha68.json", "source-compatibility-alpha69.json",
                                     "source-compatibility-alpha70.json", "source-compatibility-alpha71.json",
                                     "source-compatibility-alpha72.json", "source-compatibility-alpha73.json",
                                     "source-compatibility-alpha74.json", "source-compatibility-alpha75.json",
                                     "source-compatibility-alpha76.json", "source-compatibility-alpha77.json",
                                     "source-compatibility-alpha78.json", "source-compatibility-alpha79.json",
                                     "source-compatibility-alpha80.json", "source-compatibility-alpha81.json",
                                     "source-compatibility-alpha82.json", "source-compatibility-alpha83.json",
                                     "source-compatibility-alpha84.json",
                                     "source-compatibility-alpha85.json",
                                     "source-compatibility-alpha86.json"])

    def check_published_review(self, anchors, record_file, chain_files):
        for runtime, anchor in CHAIN[anchors].items():
            with self.subTest(runtime=runtime):
                temporary = tempfile.TemporaryDirectory()
                self.addCleanup(temporary.cleanup)
                root = Path(temporary.name)
                directory = root / "release" / ("managed-" + runtime)
                shutil.copytree(HERE / ("managed-" + runtime), directory)
                predecessor_path = directory / chain_files[-1]
                predecessor = json.loads(predecessor_path.read_text())
                self.assertEqual(CHAIN["canonical_hash"](predecessor), anchor)
                record = json.loads((directory / record_file).read_text())
                manifest = json.loads((directory / "manifest.json").read_text())
                recorded = manifest["source_files"]
                def delta(review):
                    return review["scopes"]["runtime"] if runtime == "vitess" else review["delta"]
                previous = recorded
                for name in chain_files:
                    previous = CHAIN["apply_chain_delta"](previous, delta(json.loads((directory / name).read_text())))
                current = CHAIN["apply_chain_delta"](previous, delta(record))
                manifest_key = "native_manifest_sha256" if runtime == "vitess" else "manifest_sha256"
                artifact = record["qualified_release"][manifest_key]
                def validate(review, inventory=current, artifact_hash=artifact):
                    return CHAIN["validate_chain"](
                        root, runtime, review, recorded, inventory,
                        lambda path: path.startswith("internal/runtime/"), artifact_hash)
                self.assertTrue(validate(record))
                changed = copy.deepcopy(record)
                changed["predecessor"]["review_sha256"] = A
                with self.assertRaisesRegex(ValueError, "predecessor identity"):
                    validate(changed)
                changed = copy.deepcopy(record)
                changed["qualified_release"][manifest_key] = B
                with self.assertRaisesRegex(ValueError, "historical native evidence identity"):
                    validate(changed)
                with self.assertRaisesRegex(ValueError, "another native manifest"):
                    validate(record, artifact_hash=B)
                with self.assertRaisesRegex(ValueError, "outside the chained review"):
                    validate(record, dict(current, **{"internal/api/unreviewed.go": A}))
                changed = copy.deepcopy(record)
                protected_current = dict(previous, **{"internal/runtime/controller.go": C})
                protected_delta = self.delta(previous, protected_current, "internal/runtime/controller.go")
                if runtime == "vitess":
                    changed["scopes"]["runtime"] = protected_delta
                else:
                    changed["delta"] = protected_delta
                with self.assertRaisesRegex(ValueError, "require new qualification"):
                    validate(changed, protected_current)
                predecessor["control_plane_commit"] = "5" * 40
                predecessor_path.write_text(json.dumps(predecessor))
                with self.assertRaisesRegex(ValueError, "predecessor review changed"):
                    validate(record)

    def synthetic(self, runtime="myduck", root=None, extra=None, commit="1" * 40):
        if root is None:
            temporary = tempfile.TemporaryDirectory()
            self.addCleanup(temporary.cleanup)
            root = Path(temporary.name)
        directory = root / "release" / ("managed-" + runtime)
        directory.mkdir(parents=True)
        trust = "release/runtime-source-compatibility.py"
        recorded = {"internal/api/shared.go": A, trust: A, **(extra or {})}
        previous = {**recorded, "internal/api/shared.go": B}
        current = {**previous, trust: C}
        predecessor = {
            "qualified_release": {
                "tag": "v0.1.0-alpha.58",
                "commit": commit,
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
            "reviewed_release": CHAIN["CHAIN_RELEASE"],
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
                "reviewed_release": CHAIN["CHAIN_RELEASE"],
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
    def delta(before, after, *changed):
        return {
            "baseline_sha256": CHAIN["canonical_hash"](before),
            "current_sha256": CHAIN["canonical_hash"](after),
            "changes": [{
                "path": path,
                "before": before.get(path),
                "after": after.get(path),
                "reason": "The exact reviewed source change leaves the historical native runtime evidence unchanged.",
            } for path in changed],
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

    def go_module(self):
        """A tiny repository whose qualified commit builds cmd/bin against example.com/dep."""
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        base = Path(temporary.name)
        proxy, cache, repo = base / "proxy", base / "cache", base / "repo"
        for name, versions in (("dep", ("v1.0.0", "v1.1.0")), ("other", ("v1.0.0", "v1.1.0")), ("extra", ("v1.0.0",))):
            directory = proxy / "example.com" / name / "@v"
            directory.mkdir(parents=True)
            (directory / "list").write_text("\n".join(versions) + "\n")
            for version in versions:
                module = "module example.com/" + name + "\n\ngo 1.21\n"
                (directory / (version + ".info")).write_text(json.dumps({"Version": version, "Time": "2024-01-01T00:00:00Z"}))
                (directory / (version + ".mod")).write_text(module)
                with zipfile.ZipFile(directory / (version + ".zip"), "w") as archive:
                    prefix = "example.com/" + name + "@" + version + "/"
                    archive.writestr(prefix + "go.mod", module)
                    archive.writestr(prefix + name + ".go", "package " + name + "\n\nconst Version = \"" + version + "\"\n")
        environment = {"GOPROXY": proxy.as_uri(), "GOSUMDB": "off", "GOMODCACHE": str(cache),
                       "GOTOOLCHAIN": "local", "GOFLAGS": "-mod=mod", "GOWORK": "off",
                       "GIT_CONFIG_GLOBAL": os.devnull, "GIT_CONFIG_NOSYSTEM": "1"}
        patcher = mock.patch.dict(os.environ, environment)
        patcher.start()
        self.addCleanup(patcher.stop)
        self.addCleanup(subprocess.run, ["go", "clean", "-modcache"], capture_output=True)
        (repo / "cmd/bin").mkdir(parents=True)
        (repo / "internal/other").mkdir(parents=True)
        (repo / "cmd/bin/main.go").write_text('package main\n\nimport "example.com/dep"\n\nfunc main() { println(dep.Version) }\n')
        (repo / "internal/other/other.go").write_text('package other\n\nimport "example.com/other"\n\nconst Version = other.Version\n')
        self.go_mod(repo, "require (\n\texample.com/dep v1.0.0\n\texample.com/other v1.0.0\n)\n")
        def git(*args):
            return subprocess.run(["git", *args], cwd=repo, check=True, capture_output=True, text=True).stdout.strip()
        git("init", "-q")
        git("add", "-A")
        git("-c", "user.name=release", "-c", "user.email=release@example.invalid", "commit", "-qm", "qualified")
        globals_ = CHAIN["validate_chain"].__globals__
        old_packages = globals_["GO_BINARY_PACKAGES"]
        globals_["GO_BINARY_PACKAGES"] = {"myduck": ("./cmd/bin",)}
        self.addCleanup(lambda: globals_.update(GO_BINARY_PACKAGES=old_packages))
        return repo, git

    @staticmethod
    def go_mod(repo, body, go="1.21"):
        (repo / "go.mod").write_text("module example.com/app\n\ngo " + go + "\n\n" + body)
        subprocess.run(["go", "mod", "tidy"], cwd=repo, check=True, capture_output=True)

    def go_change(self, repo, commit, named=None, runtime="myduck"):
        """Review the working tree's go.mod and go.sum against the qualified commit."""
        names = ("go.mod", "go.sum")
        qualified = {name: hashlib.sha256(subprocess.run(["git", "show", commit + ":" + name], cwd=repo, check=True,
                                                         capture_output=True).stdout).hexdigest() for name in names}
        root, record, recorded, current = self.synthetic(runtime, repo, qualified, named or commit)
        previous = dict(current, **{"release/runtime-source-compatibility.py": A})
        current = dict(current, **{name: hashlib.sha256((repo / name).read_bytes()).hexdigest() for name in names})
        changed = [name for name in names if current[name] != previous[name]]
        record["delta"] = self.delta(previous, current, "release/runtime-source-compatibility.py", *changed)
        def validate():
            return CHAIN["validate_chain"](root, runtime, record, recorded, current,
                                           lambda path: path in names or path.startswith("internal/runtime/"), A)
        return validate

    @unittest.skipUnless(shutil.which("go") and shutil.which("git"), "Go and Git are required")
    def test_go_module_changes_outside_runtime_binaries_are_reviewable(self):
        repo, git = self.go_module()
        commit = git("rev-parse", "HEAD")
        self.go_mod(repo, "require (\n\texample.com/dep v1.0.0\n\texample.com/other v1.1.0\n)\n")
        self.assertTrue(self.go_change(repo, commit)())
        shutil.rmtree(repo / "release")
        (repo / "internal/extra").mkdir()
        (repo / "internal/extra/extra.go").write_text('package extra\n\nimport "example.com/extra"\n\nconst Version = extra.Version\n')
        self.go_mod(repo, "require (\n\texample.com/dep v1.0.0\n\texample.com/extra v1.0.0\n\texample.com/other v1.1.0\n)\n")
        self.assertTrue(self.go_change(repo, commit)())

    @unittest.skipUnless(shutil.which("go") and shutil.which("git"), "Go and Git are required")
    def test_go_module_changes_inside_runtime_binaries_require_qualification(self):
        repo, git = self.go_module()
        commit = git("rev-parse", "HEAD")
        for name, body, go in (
                ("bumped dependency", "require (\n\texample.com/dep v1.1.0\n\texample.com/other v1.0.0\n)\n", "1.21"),
                ("replaced dependency", "require (\n\texample.com/dep v1.0.0\n\texample.com/other v1.0.0\n)\n\n"
                                        "replace example.com/dep v1.0.0 => example.com/dep v1.1.0\n", "1.21"),
                ("language version", "require (\n\texample.com/dep v1.0.0\n\texample.com/other v1.0.0\n)\n", "1.22")):
            self.go_mod(repo, body, go)
            shutil.rmtree(repo / "release", ignore_errors=True)
            with self.subTest(change=name), self.assertRaisesRegex(ValueError, "require new qualification"):
                self.go_change(repo, commit)()

    @unittest.skipUnless(shutil.which("go") and shutil.which("git"), "Go and Git are required")
    def test_go_module_review_fails_closed_without_go_or_qualified_bytes(self):
        repo, git = self.go_module()
        commit = git("rev-parse", "HEAD")
        self.go_mod(repo, "require (\n\texample.com/dep v1.1.0\n\texample.com/other v1.0.0\n)\n")
        git("add", "go.mod", "go.sum")
        git("-c", "user.name=release", "-c", "user.email=release@example.invalid", "commit", "-qm", "rewritten")
        # The rewritten commit already carries the bump, so it cannot stand in for the qualified bytes.
        validate = self.go_change(repo, commit, git("rev-parse", "HEAD"))
        with self.assertRaisesRegex(ValueError, "require new qualification"):
            validate()
        shutil.rmtree(repo / "release")
        self.go_mod(repo, "require (\n\texample.com/dep v1.0.0\n\texample.com/other v1.1.0\n)\n")
        validate = self.go_change(repo, commit)
        self.assertTrue(validate())
        tools = repo / "git-only"
        tools.mkdir()
        (tools / "git").symlink_to(shutil.which("git"))
        with mock.patch.dict(os.environ, {"PATH": str(tools)}), \
                self.assertRaisesRegex(ValueError, "require new qualification"):
            validate()
        # Repository packages are not pinned by the native manifest, so importing one fails closed.
        (repo / "cmd/bin/main.go").write_text('package main\n\nimport "example.com/app/internal/other"\n\n'
                                              'func main() { println(other.Version) }\n')
        with self.assertRaisesRegex(ValueError, "require new qualification"):
            validate()

    def test_go_module_changes_are_reviewable_for_runtimes_without_repository_binaries(self):
        root, record, recorded, current = self.synthetic("oracle-free", extra={"go.mod": A})
        previous = dict(current, **{"release/runtime-source-compatibility.py": A})
        current = dict(current, **{"go.mod": B})
        record["delta"] = self.delta(previous, current, "release/runtime-source-compatibility.py", "go.mod")
        with mock.patch.dict(os.environ, {"PATH": str(root / "no-tools")}):
            self.assertTrue(CHAIN["validate_chain"](root, "oracle-free", record, recorded, current,
                                                    lambda path: path in ("go.mod", "go.sum"), A))

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
            "reviewed_release": CHAIN["CHAIN_RELEASE"],
            "control_plane_commit": "4" * 40,
            "regression_evidence": {"url": "https://github.com/hakopod/hakopod/actions/runs/123456",
                                    "tests": ["TestManagedVitessRuntimeCompatibility"]},
            "predecessor": {"tag": "v0.1.0-alpha.59",
                            "commit": "869cb4186566199d8611c4ffb30212d42d0daa1d",
                            "review_sha256": CHAIN["CHAIN_PREDECESSORS"]["vitess"]},
            "bootstrap": {"tag": "v0.1.0-alpha.59",
                          "commit": "869cb4186566199d8611c4ffb30212d42d0daa1d",
                          "reviewed_release": CHAIN["CHAIN_RELEASE"],
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
