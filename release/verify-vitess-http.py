#!/usr/bin/env python3
"""Verify and retain the Vitess HTTP acceptance report for an enabled release."""

import argparse
import hashlib
import json
import math
from pathlib import Path
import runpy
import shutil


ROOT = Path(__file__).resolve().parents[1]
REPORT = ROOT / "release/managed-vitess-http/evidence.json"
MAX_REPORT_BYTES = 8 * 1024 * 1024
MAX_HARNESS_FILES = 32
MAX_HARNESS_BYTES = 2 * 1024 * 1024
NODES = {f"k3d-hakopod-vitess-worker-{index}" for index in range(3)}
NATIVE = runpy.run_path(str(ROOT / "release/verify-vitess-runtime.py"))


def _unique_object(pairs):
    result = {}
    for key, value in pairs:
        if key in result:
            raise ValueError("Duplicate key in Vitess HTTP acceptance evidence")
        result[key] = value
    return result


def read_report(path):
    path = Path(path)
    if path.is_symlink() or not path.is_file() or path.stat().st_size > MAX_REPORT_BYTES:
        raise ValueError("Vitess HTTP acceptance evidence is missing, symbolic or oversized")
    try:
        value = json.loads(path.read_bytes(), object_pairs_hook=_unique_object)
    except (json.JSONDecodeError, UnicodeDecodeError) as error:
        raise ValueError("Vitess HTTP acceptance evidence is malformed") from error
    if not isinstance(value, dict):
        raise ValueError("Vitess HTTP acceptance evidence must be an object")
    return value


def _file_hash(path):
    digest = hashlib.sha256()
    with path.open("rb") as source:
        while block := source.read(1024 * 1024):
            digest.update(block)
    return digest.hexdigest()


def harness_inventory(root):
    root = Path(root)
    paths = [root / "scripts/run-development-vitess-http-acceptance.py"]
    paths.extend(sorted((root / "acceptance/vitess").rglob("*")))
    result, total = {}, 0
    for path in paths:
        if path.is_symlink():
            raise ValueError("Vitess HTTP acceptance source must contain regular files only")
        if path.is_dir():
            continue
        if not path.is_file():
            raise ValueError("Vitess HTTP acceptance source must contain regular files only")
        total += path.stat().st_size
        if len(result) >= MAX_HARNESS_FILES or total > MAX_HARNESS_BYTES:
            raise ValueError("Vitess HTTP acceptance source inventory exceeded its bound")
        result[path.relative_to(root).as_posix()] = _file_hash(path)
    if "acceptance/vitess/http_live_test.go" not in result:
        raise ValueError("Vitess HTTP acceptance test is missing")
    return result


def validate_report(report, root=ROOT):
    root = Path(root)
    expected_keys = {"schema_version", "test", "execution", "environment", "runtime_source_files",
                     "runtime_source_files_after", "http_harness_source_files",
                     "http_harness_source_files_after", "log_sha256", "exit_code", "limit_error",
                     "elapsed_seconds", "test_events", "passed"}
    if set(report) != expected_keys or type(report.get("schema_version")) is not int or report["schema_version"] != 1:
        raise ValueError("Vitess HTTP acceptance evidence schema is invalid")
    if (report.get("test") != "TestVitessHTTPVerticalSlice" or report.get("execution") != "http_vertical"
            or type(report.get("passed")) is not bool or report["passed"] is not True
            or type(report.get("exit_code")) is not int or report["exit_code"] != 0
            or report.get("limit_error") != ""):
        raise ValueError("Vitess HTTP acceptance did not complete successfully")
    if not isinstance(report.get("log_sha256"), str) or not NATIVE["DIGEST"].fullmatch(report["log_sha256"]):
        raise ValueError("Vitess HTTP acceptance log digest is invalid")
    if (type(report.get("elapsed_seconds")) not in (int, float)
            or not math.isfinite(report["elapsed_seconds"]) or not 0 < report["elapsed_seconds"] <= 3600):
        raise ValueError("Vitess HTTP acceptance duration is invalid")
    events = report.get("test_events")
    if (not isinstance(events, list) or len(events) != 2 or any(not isinstance(event, dict) for event in events)
            or [event.get("Action") for event in events] != ["run", "pass"]
            or any(set(event) - {"Time", "Action", "Test", "Elapsed"} for event in events)
            or any(event.get("Test") != "TestVitessHTTPVerticalSlice" or not isinstance(event.get("Time"), str) for event in events)
            or type(events[1].get("Elapsed")) not in (int, float)
            or not math.isfinite(events[1]["Elapsed"]) or not 0 < events[1]["Elapsed"] <= 3600):
        raise ValueError("Vitess HTTP acceptance run and pass events are incomplete")
    runtime_sources = NATIVE["source_files"](root)
    harness_sources = harness_inventory(root)
    if report.get("runtime_source_files") != runtime_sources or report.get("runtime_source_files_after") != runtime_sources:
        raise ValueError("Vitess runtime source changed before or after HTTP acceptance")
    if report.get("http_harness_source_files") != harness_sources or report.get("http_harness_source_files_after") != harness_sources:
        raise ValueError("Vitess HTTP harness source changed before or after acceptance")
    environment = report.get("environment")
    outer_keys = {"context", "cluster_uid", "node_uids", "receipt_sha256", "available_cpu_milli",
                  "scratch_available_bytes", "qualified_native_preflight"}
    if (not isinstance(environment, dict) or set(environment) != outer_keys
            or environment.get("context") != "k3d-hakopod-dev"
            or not isinstance(environment.get("node_uids"), dict)
            or set(environment["node_uids"]) != NODES):
        raise ValueError("Vitess HTTP evidence belongs to another cluster")
    images = [NATIVE["source_constant"](root, name) for name in
              ("vitessServerImage", "vitessOperatorImage", "vitessEtcdImage")]
    native_environment = environment["qualified_native_preflight"]
    NATIVE["validate_native_environment"](native_environment, "recovery", images)
    native_cluster = native_environment["cluster"]
    if (native_cluster["uid"] != environment["cluster_uid"]
            or native_cluster["node_uids"] != environment["node_uids"]
            or native_cluster["receipt_sha256"] != environment["receipt_sha256"]):
        raise ValueError("Vitess HTTP preflights belong to different clusters")
    return report


def verify(evidence=REPORT, output=None, root=ROOT):
    root = Path(root)
    if not NATIVE["source_boolean"](root, "vitessReleaseQualified"):
        return False
    report = validate_report(read_report(evidence), root)
    if output is None:
        return report
    output = Path(output)
    if output.is_symlink() or not output.is_dir():
        raise ValueError("Vitess release evidence output directory is unavailable")
    target = output / "vitess-http-acceptance.json"
    if target.exists() or target.is_symlink():
        raise ValueError("Vitess HTTP release evidence already exists")
    shutil.copyfile(evidence, target)
    return report


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--evidence", type=Path, default=REPORT)
    parser.add_argument("--output", type=Path)
    arguments = parser.parse_args()
    verify(arguments.evidence, arguments.output)
