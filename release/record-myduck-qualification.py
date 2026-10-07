#!/usr/bin/env python3
"""Retain MyDuck build identity and structural native acceptance evidence."""

import argparse
import json
from pathlib import Path
import runpy
import shutil

VERIFIER = runpy.run_path(str(Path(__file__).with_name("verify-myduck-runtime.py")))


def assemble(source, build_manifest, receipt, reports, output):
    sources, images = VERIFIER["source_files"](source), VERIFIER["source_images"](source)
    runner = runpy.run_path(str(source / "scripts/run-development-vitess-acceptance.py"))
    if output.exists() or output.is_symlink() or len(reports) != len(VERIFIER["TESTS"]):
        raise ValueError("Use a fresh output and one native report for each MyDuck case")
    attempts = []
    for path in reports:
        report = VERIFIER["read_json"](path)
        if not isinstance(report, dict) or set(report) != {"schema_version", "context", "execution", "platform", "source_files", "images", "attempt"} or type(report["schema_version"]) is not int or report["schema_version"] != 1 or report["context"] != "k3d-hakopod-dev" or report["execution"] != "native" or report["platform"] != "linux/amd64" or report["source_files"] != sources or report["images"] != images:
            raise ValueError("MyDuck report belongs to another source, image or execution")
        log = path.parent / "native-test.jsonl"
        if VERIFIER["file_hash"](log) != report["attempt"].get("log_sha256"):
            raise ValueError("MyDuck protected native log changed after its report")
        events, valid = runner["structural_events"](log)
        if not valid or events != report["attempt"].get("test_events"):
            raise ValueError("MyDuck structural events differ from the native test log")
        preserved = report["attempt"].get("preserved_resources", {})
        before = path.parent / "inventory-before.json"
        after = path.parent / "inventory-after.json"
        if VERIFIER["file_hash"](before) != preserved.get("before_sha256") or VERIFIER["file_hash"](after) != preserved.get("after_sha256") or VERIFIER["read_json"](before) != VERIFIER["read_json"](after):
            raise ValueError("MyDuck unrelated-resource inventory changed after its report")
        attempts.append(report["attempt"])
    acceptance = {"schema_version": 1, "context": "k3d-hakopod-dev", "execution": "native", "platform": "linux/amd64",
                  "source_files": sources, "images": images, "attempts": attempts}
    VERIFIER["validate_acceptance"](acceptance, sources, images)
    package = VERIFIER["read_json"](receipt)
    VERIFIER["file_hash"](build_manifest)
    output.mkdir(parents=True, exist_ok=False)
    shutil.copyfile(build_manifest, output / "source-build-manifest.json")
    shutil.copyfile(receipt, output / "packaging-receipt.json")
    (output / "native-acceptance.json").write_text(json.dumps(acceptance, sort_keys=True, indent=2) + "\n")
    manifest = {"schema_version": 1, "platform": "linux/amd64",
                "source": {"repository": "https://github.com/apecloud/myduckserver", "revision": VERIFIER["UPSTREAM"]},
                "source_files": sources, "images": {"runtime": {"reference": images["runtime"], "binaries": package["binaries"]}},
                "files": {name: VERIFIER["file_hash"](output / name) for name in VERIFIER["FILES"]}}
    (output / "manifest.json").write_text(json.dumps(manifest, sort_keys=True, indent=2) + "\n")
    VERIFIER["validate_metadata"](output, source)


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    for name in ("source", "build-manifest", "receipt", "output"):
        parser.add_argument("--" + name, type=Path, required=True)
    parser.add_argument("--report", type=Path, action="append", required=True)
    args = parser.parse_args()
    assemble(args.source, args.build_manifest, args.receipt, args.report, args.output)
