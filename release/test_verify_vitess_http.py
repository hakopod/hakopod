"""Fail-closed checks for Vitess HTTP release evidence."""

import copy
import json
from pathlib import Path
import runpy
import tempfile
import unittest
from unittest.mock import patch


MODULE = runpy.run_path(str(Path(__file__).with_name("verify-vitess-http.py")))
STATE = MODULE["validate_report"].__globals__
DIGEST = "a" * 64


class VitessHTTPReleaseEvidenceTest(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name) / "source"
        self.root.mkdir()
        self.output = Path(self.temporary.name) / "output"
        self.output.mkdir()
        self.runtime_sources = {"internal/cluster/database_vitess.go": DIGEST}
        self.harness_sources = {"acceptance/vitess/http_live_test.go": DIGEST}
        self.environment = {"context": "k3d-hakopod-dev", "cluster_uid": "cluster", "receipt_sha256": DIGEST,
                            "node_uids": {f"k3d-hakopod-vitess-worker-{index}": f"uid-{index}" for index in range(3)},
                            "available_cpu_milli": 17700, "scratch_available_bytes": 12 * 1024 ** 3,
                            "qualified_native_preflight": {"fixture": "validated", "cluster": {
                                "uid": "cluster", "receipt_sha256": DIGEST,
                                "node_uids": {f"k3d-hakopod-vitess-worker-{index}": f"uid-{index}" for index in range(3)}}}}
        self.report = {"schema_version": 1, "test": "TestVitessHTTPVerticalSlice", "execution": "http_vertical",
                       "environment": self.environment, "runtime_source_files": self.runtime_sources,
                       "runtime_source_files_after": self.runtime_sources, "http_harness_source_files": self.harness_sources,
                       "http_harness_source_files_after": self.harness_sources, "log_sha256": DIGEST,
                       "exit_code": 0, "limit_error": "", "elapsed_seconds": 10.0,
                       "test_events": [{"Time": "2026-10-05T00:00:00Z", "Action": "run", "Test": "TestVitessHTTPVerticalSlice"},
                                       {"Time": "2026-10-05T00:00:10Z", "Action": "pass", "Test": "TestVitessHTTPVerticalSlice", "Elapsed": 10.0}],
                       "passed": True}
        self.evidence = Path(self.temporary.name) / "evidence.json"
        self.native = {"DIGEST": __import__("re").compile(r"[a-f0-9]{64}"),
                       "source_boolean": lambda root, name: True,
                       "source_files": lambda root: self.runtime_sources,
                       "source_constant": lambda root, name: name + "@sha256:" + DIGEST,
                       "validate_native_environment": lambda environment, case, images: environment}
        self.state = patch.dict(STATE, NATIVE=self.native)
        self.inventory = patch.dict(STATE, harness_inventory=lambda root: self.harness_sources)
        self.state.start(); self.inventory.start()
        self.addCleanup(self.state.stop); self.addCleanup(self.inventory.stop)

    def write(self, report=None):
        self.evidence.write_text(json.dumps(self.report if report is None else report))

    def test_accepts_and_copies_exact_successful_evidence(self):
        self.write()
        MODULE["verify"](self.evidence, self.output, self.root)
        self.assertEqual(json.loads((self.output / "http-acceptance.json").read_text()), self.report)

    def test_closed_gate_does_not_require_evidence(self):
        self.native["source_boolean"] = lambda root, name: False
        self.assertFalse(MODULE["verify"](self.evidence, self.output, self.root))
        self.assertEqual(list(self.output.iterdir()), [])

    def test_open_gate_requires_committed_evidence(self):
        with self.assertRaisesRegex(ValueError, "missing"):
            MODULE["verify"](self.evidence, self.output, self.root)

    def test_rejects_skipped_failed_or_incomplete_events(self):
        for action in ("skip", "fail"):
            changed = copy.deepcopy(self.report)
            changed["test_events"][1]["Action"] = action
            changed["passed"] = action != "fail"
            with self.subTest(action=action), self.assertRaises(ValueError):
                MODULE["validate_report"](changed, self.root)
        changed = copy.deepcopy(self.report)
        changed["test_events"] = changed["test_events"][:1]
        with self.assertRaises(ValueError):
            MODULE["validate_report"](changed, self.root)

    def test_rejects_mismatched_or_changed_source(self):
        for field in ("runtime_source_files", "runtime_source_files_after",
                      "http_harness_source_files", "http_harness_source_files_after"):
            changed = copy.deepcopy(self.report)
            changed[field] = {"changed": DIGEST}
            with self.subTest(field=field), self.assertRaises(ValueError):
                MODULE["validate_report"](changed, self.root)

    def test_rejects_wrong_cluster_or_image_environment(self):
        changed = copy.deepcopy(self.report)
        changed["environment"]["context"] = "production"
        with self.assertRaises(ValueError):
            MODULE["validate_report"](changed, self.root)
        changed = copy.deepcopy(self.report)
        changed["environment"]["qualified_native_preflight"]["cluster"]["uid"] = "another-cluster"
        with self.assertRaisesRegex(ValueError, "different clusters"):
            MODULE["validate_report"](changed, self.root)
        self.native["validate_native_environment"] = lambda environment, case, images: (_ for _ in ()).throw(ValueError("image mismatch"))
        with self.assertRaisesRegex(ValueError, "image mismatch"):
            MODULE["validate_report"](self.report, self.root)


if __name__ == "__main__":
    unittest.main()
