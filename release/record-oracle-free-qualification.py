#!/usr/bin/env python3
"""Retain structural Oracle native evidence and the reviewed operator build."""

import argparse
import json
from pathlib import Path
import runpy
import shutil

VERIFIER = runpy.run_path(str(Path(__file__).with_name("verify-oracle-free-runtime.py")))


def validate_qualified_manifest(path, sources, images):
    manifest = VERIFIER["read_json"](path)
    fields = {"schema_version", "platform", "source", "source_files", "images", "files"}
    if (not isinstance(manifest, dict) or set(manifest) != fields
            or type(manifest.get("schema_version")) is not int or manifest["schema_version"] != 1
            or manifest.get("platform") != "linux/amd64"
            or manifest.get("source") != {
                "repository": "https://github.com/oracle/oracle-database-operator",
                "revision": VERIFIER["UPSTREAM"]}
            or manifest.get("source_files") != sources):
        raise ValueError("Historical Oracle manifest identity differs from retained cluster reports")
    qualified_images = manifest.get("images")
    if (not isinstance(qualified_images, dict) or set(qualified_images) != {"operator", "database"}
            or any(not isinstance(qualified_images.get(kind), dict)
                   or set(qualified_images[kind]) != {"reference", "binaries"}
                   or qualified_images[kind].get("reference") != images[kind]
                   for kind in images)
            or not isinstance(qualified_images["operator"]["binaries"], dict)
            or set(qualified_images["operator"]["binaries"]) != {"/manager"}
            or not VERIFIER["SHA"].fullmatch(qualified_images["operator"]["binaries"].get("/manager", ""))
            or qualified_images["database"]["binaries"] != {}):
        raise ValueError("Historical Oracle manifest image identity differs from retained reports")
    files = manifest.get("files")
    if (not isinstance(files, dict) or set(files) != VERIFIER["FILES"]
            or any(not isinstance(value, str) or not VERIFIER["SHA"].fullmatch(value)
                   for value in files.values())):
        raise ValueError("Historical Oracle manifest artifact identity is invalid")
    return manifest


def assemble(source, build, package, reports, output, qualified_manifest=None):
    sources, images = VERIFIER["source_files"](source), VERIFIER["source_images"](source)
    runner = runpy.run_path(str(source / "scripts/run-development-vitess-acceptance.py"))
    if output.exists() or len(reports) != len(VERIFIER["ALL_TESTS"]):
        raise ValueError("Use a fresh output and one report for each Oracle native case")
    attempts, report_sources = [], {}
    for path in reports:
        report = VERIFIER["read_json"](path)
        if not isinstance(report, dict) or set(report) != {"schema_version", "context", "execution", "platform", "source_files", "images", "attempt"} or report["schema_version"] != 1 or report["context"] != "k3d-hakopod-dev" or report["execution"] != "native" or report["platform"] != "linux/amd64" or report["images"] != images:
            raise ValueError("Oracle report belongs to another source, image or execution")
        # The protected raw log stays on the VM; retain its checksum and the
        # structural events after checking they still match the successful run.
        if VERIFIER["file_hash"](path.parent / "native-test.jsonl") != report["attempt"].get("log_sha256"):
            raise ValueError("Oracle native test log changed after its report")
        events, valid = runner["structural_events"](path.parent / "native-test.jsonl")
        if not valid or events != report["attempt"].get("test_events"):
            raise ValueError("Oracle structural events differ from the native test log")
        if report["attempt"].get("case") == "http-api" and VERIFIER["file_hash"](path.parent / "host-fixture-receipt.json") != report["attempt"].get("host_fixture_receipt_sha256"):
            raise ValueError("Oracle HTTP host fixture receipt changed after its report")
        case = report["attempt"].get("case")
        report_sources[case] = report["source_files"]
        attempts.append({**report["attempt"], "source_files": report["source_files"]})
    historical = {case: report_sources.get(case) for case in VERIFIER["TESTS"]}
    if any(value is None for value in historical.values()) or len({json.dumps(value, sort_keys=True) for value in historical.values()}) != 1:
        raise ValueError("Oracle historical cluster reports have mixed source identities")
    qualified = next(iter(historical.values()))
    if report_sources.get("http-api") != sources:
        raise ValueError("Oracle HTTP/API report does not belong to current source")
    if qualified == sources:
        acceptance = {"schema_version": 1, "context": "k3d-hakopod-dev", "execution": "native", "platform": "linux/amd64",
                      "source_files": sources, "images": images,
                      "attempts": [{key: value for key, value in attempt.items() if key != "source_files"} for attempt in attempts]}
    else:
        if qualified_manifest is None:
            raise ValueError("Mixed-source Oracle qualification requires the exact historical manifest")
        validate_qualified_manifest(qualified_manifest, qualified, images)
        qualified_sha = VERIFIER["file_hash"](qualified_manifest)
        acceptance = {"schema_version": 2, "context": "k3d-hakopod-dev", "execution": "native", "platform": "linux/amd64",
                      "source_files": sources, "qualified_source_files": qualified,
                      "qualified_manifest_sha256": qualified_sha, "images": images, "attempts": attempts}
    VERIFIER["validate_acceptance"](acceptance, sources, images, source)
    output.mkdir(parents=True, exist_ok=False)
    for before, after in (
        (build / "upstream-build/image/upstream.patch", "operator-upstream.patch"),
        (build / "upstream-build/image/crds/singleinstancedatabases.yaml", "sidb-v4.yaml"),
        (build / "upstream-build/artifact-sha256.txt", "build-artifacts.txt"),
        (build / "evidence/formatted-source-sha256.json", "source-build-manifest.json"),
        (package / "packaging-receipt.json", "packaging-receipt.json"),
    ):
        VERIFIER["file_hash"](before)
        shutil.copyfile(before, output / after)
    (output / "native-acceptance.json").write_text(json.dumps(acceptance, indent=2, sort_keys=True) + "\n")
    binary = VERIFIER["file_hash"](build / "upstream-build/image/bin/manager", 512 * 1024 * 1024)
    manifest = {"schema_version": 1, "platform": "linux/amd64",
                "source": {"repository": "https://github.com/oracle/oracle-database-operator", "revision": VERIFIER["UPSTREAM"]},
                "source_files": sources, "images": {"operator": {"reference": images["operator"], "binaries": {"/manager": binary}},
                                                   "database": {"reference": images["database"], "binaries": {}}},
                "files": {name: VERIFIER["file_hash"](output / name) for name in VERIFIER["FILES"]}}
    (output / "manifest.json").write_text(json.dumps(manifest, sort_keys=True, indent=2) + "\n")
    VERIFIER["validate_metadata"](output, source)


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--source", type=Path, required=True)
    parser.add_argument("--build", type=Path, required=True)
    parser.add_argument("--package", type=Path, required=True)
    parser.add_argument("--report", type=Path, action="append", required=True)
    parser.add_argument("--output", type=Path, required=True)
    parser.add_argument("--qualified-manifest", type=Path)
    args = parser.parse_args()
    assemble(args.source, args.build, args.package, args.report, args.output, args.qualified_manifest)
