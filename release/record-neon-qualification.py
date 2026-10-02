#!/usr/bin/env python3
"""Record complete native Neon evidence; refuse partial qualification."""
import argparse
import json
from pathlib import Path
import runpy
import shutil
import tempfile
import os

VERIFIER = runpy.run_path(str(Path(__file__).with_name("verify-neon-runtime.py")))


def assemble(source, source_archive, build_path, report_path, output):
    source, output = Path(source), Path(output)
    if output.exists():
        raise ValueError("Use a fresh Neon qualification output directory")
    metadata = VERIFIER["source_metadata"](source)
    sources = VERIFIER["source_files"](source)
    build = VERIFIER["read_json"](build_path)
    images, identities = VERIFIER["validate_build"](build, metadata, source_archive)
    report = VERIFIER["read_json"](report_path)
    VERIFIER["validate_acceptance"](report, sources, images, identities)
    runner = source / "examples/neon-native-acceptance/run.sh"
    runner_hash = VERIFIER["file_hash"](runner)
    if report["runner_sha256"] != runner_hash:
        raise ValueError("Neon native report used another evidence producer")
    output.parent.mkdir(parents=True, exist_ok=True)
    temporary = Path(tempfile.mkdtemp(prefix="." + output.name + "-", dir=output.parent))
    archive = {k: metadata["source_archive"][k] for k in ("sha256", "size_bytes", "member_count", "repeat_comparison", "global_member_order")}
    manifest = {
        "schema_version": 1, "platform": "linux/amd64",
        "source": {"repository": metadata["upstream_repository"], "commit": metadata["upstream_commit"], "candidate_tree": metadata["combined_candidate_tree"], "archive": archive, "patches": {"proxy": metadata["proxy_patch_sha256"], "ownership": metadata["ownership_patch_sha256"], "combined": metadata["frozen_combined_patch_sha256"]}},
        "source_files": sources, "images": images, "identities": identities,
        "tooling": {"recorder_sha256": VERIFIER["file_hash"](source / "release/record-neon-qualification.py"), "verifier_sha256": VERIFIER["file_hash"](source / "release/verify-neon-runtime.py"), "runner_sha256": runner_hash, "producer_sha256": VERIFIER["file_hash"](source / VERIFIER["PRODUCER"]["producer_path"])},
        "files": {},
        "capability": {"development_evidence_recorded": True, "cluster_qualified": False, "public_endpoint_qualified": False, "physical_zones_qualified": False},
    }
    try:
        shutil.copyfile(build_path, temporary / "build-provenance.json")
        shutil.copyfile(report_path, temporary / "native-acceptance.json")
        manifest["files"] = {name: VERIFIER["file_hash"](temporary / name, VERIFIER["MAX_JSON_BYTES"]) for name in ("build-provenance.json", "native-acceptance.json")}
        (temporary / "manifest.json").write_text(json.dumps(manifest, indent=2, sort_keys=True) + "\n")
        VERIFIER["validate_metadata"](temporary, source)
        os.replace(temporary, output)
    except Exception:
        shutil.rmtree(temporary, ignore_errors=True)
        raise
    return manifest


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--source", type=Path, required=True)
    parser.add_argument("--source-archive", type=Path, required=True)
    parser.add_argument("--build-report", type=Path, required=True)
    parser.add_argument("--report", type=Path, required=True)
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args(); assemble(args.source, args.source_archive, args.build_report, args.report, args.output)
