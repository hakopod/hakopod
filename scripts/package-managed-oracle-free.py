#!/usr/bin/env python3
"""Package a source-tested Oracle Free controller as an inspected OCI archive.

This uses the VM's existing Docker builder for the fixed scratch image. It does
not publish an image, install a controller, or enable database provisioning.
"""

import argparse
import hashlib
import importlib.util
import json
from pathlib import Path, PurePosixPath
import platform
import re
import shutil
import tarfile

ROOT = Path(__file__).resolve().parents[1]
SPEC = importlib.util.spec_from_file_location("managed_runtime_publisher", ROOT / "release/publish-managed-runtime.py")
PUBLISHER = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(PUBLISHER)
SHA = re.compile(r"[0-9a-f]{64}")
LABELS = {
    "org.opencontainers.image.source": "https://github.com/hakopod/hakopod",
    "io.hakopod.oracle.operator-upstream": "ff6f9178c1650df30afbf203ebdb633e9b80760a",
    "io.hakopod.oracle.patch": "hakopod-oracle-free-tcps-v1",
}


def checked_file(path, expected, limit):
    if not SHA.fullmatch(expected) or PUBLISHER.digest_file(path, limit) != expected:
        raise ValueError("Oracle Free source or build checksum changed")


def verified_build(base, source_sha, artifact_sha):
    evidence, source, build = base / "evidence", base / "source", base / "upstream-build"
    manifest = evidence / "formatted-source-sha256.json"
    checked_file(manifest, source_sha, 1024 * 1024)
    if (evidence / "exit-code.txt").read_bytes() != b"0\n":
        raise ValueError("Oracle Free source build did not pass")
    hashes = PUBLISHER.read_json(manifest)
    required = {
        "Dockerfile.oracle-free-operator", "scripts/apply-managed-oracle-patches.py",
        "scripts/build-managed-oracle-free.sh", "scripts/export-oracle-free-crd.py",
        "installer/oracle_free_controller.py",
        "patches/oracle-operator/hakopod_free_policy.go.txt",
        "patches/oracle-operator/hakopod_free_resource.go.txt",
        "patches/oracle-operator/hakopod_free_health.go.txt",
    }
    if not isinstance(hashes, dict) or not required.issubset(hashes) or len(hashes) > 256:
        raise ValueError("Oracle Free source receipt is incomplete")
    for name, digest in hashes.items():
        path = PurePosixPath(name)
        if path.is_absolute() or ".." in path.parts or not isinstance(digest, str):
            raise ValueError("Oracle Free source receipt contains an unsafe path")
        checked_file(source / name, digest, 4 * 1024 * 1024)
    artifact_file = build / "artifact-sha256.txt"
    checked_file(artifact_file, artifact_sha, 4096)
    expected_paths = {build / "image/bin/manager", build / "image/upstream.patch", build / "image/crds/singleinstancedatabases.yaml"}
    found = set()
    for row in artifact_file.read_text().splitlines():
        digest, name = row.split("  ", 1)
        path = Path(name)
        if path not in expected_paths or path in found:
            raise ValueError("Oracle Free build artifact inventory changed")
        checked_file(path, digest, 512 * 1024 * 1024)
        found.add(path)
    if found != expected_paths:
        raise ValueError("Oracle Free build artifacts are incomplete")
    checked_file(build / "image/Dockerfile", hashes["Dockerfile.oracle-free-operator"], 4096)
    license_sha = PUBLISHER.digest_file(build / "source/LICENSE.txt", 64 * 1024)
    checked_file(build / "image/LICENSE.txt", license_sha, 64 * 1024)
    return build / "image"


def archive_digest(archive):
    with tarfile.open(archive) as bundle:
        entries = [entry for entry in bundle if entry.name == "index.json"]
        if len(entries) != 1 or not entries[0].isfile() or entries[0].size > 1024 * 1024:
            raise ValueError("Oracle Free OCI index is missing or oversized")
        value = json.load(bundle.extractfile(entries[0]))
    manifests = value.get("manifests", [])
    if len(manifests) != 1 or not PUBLISHER.DIGEST.fullmatch(manifests[0].get("digest", "")):
        raise ValueError("Oracle Free OCI archive requires one exact image")
    return manifests[0]["digest"]


def package(base, output, source_sha, artifact_sha):
    image_source = verified_build(base, source_sha, artifact_sha)
    output.mkdir(mode=0o700, exist_ok=False)
    context = output / "context"
    (context / "bin").mkdir(parents=True, mode=0o700)
    for name in ("Dockerfile", "LICENSE.txt", "bin/manager"):
        shutil.copyfile(image_source / name, context / name)
    archive = output / "managed-oracle-free-operator.oci.tar"
    labels = dict(LABELS, **{"io.hakopod.oracle.source-sha256": source_sha})
    PUBLISHER.checked([
        "docker", "buildx", "build", "--builder", "default", "--platform", "linux/amd64",
        "--provenance=false", "--sbom=false", "--network=none", "--no-cache", "--progress=plain",
        "--label", "io.hakopod.oracle.source-sha256=" + source_sha,
        "--output", "type=oci,compression=uncompressed,dest=" + str(archive), str(context),
    ], 600)
    image = {
        "archive": archive.name, "archive_sha256": PUBLISHER.digest_file(archive, PUBLISHER.MAX_ARCHIVE),
        "image_digest": archive_digest(archive), "repository": "ghcr.io/hakopod/managed-oracle-free-operator",
        "platform": "linux/amd64", "config_user": "65532:65532", "labels": labels,
    }
    PUBLISHER.inspect_archive(archive, image, output / "inspected-oci")
    plan_path = output / "managed-runtime-publication.json"
    plan_path.write_text(json.dumps({"schema_version": 1, "images": [image]}, indent=2) + "\n")
    plan_path.chmod(0o600)
    plan_sha = hashlib.sha256(plan_path.read_bytes()).hexdigest()
    PUBLISHER.plan(plan_path, plan_sha)
    receipt = {
        "schema_version": 1, "status": "packaged_not_published", "source_manifest_sha256": source_sha,
        "build_artifacts_sha256": artifact_sha, "publication_plan_sha256": plan_sha, "image": image,
    }
    (output / "packaging-receipt.json").write_text(json.dumps(receipt, indent=2) + "\n")
    return receipt


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--build-root", type=Path, required=True)
    parser.add_argument("--source-manifest-sha256", required=True)
    parser.add_argument("--artifact-checksums-sha256", required=True)
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()
    if platform.system() != "Linux" or platform.machine() != "x86_64":
        raise SystemExit("Oracle Free packaging requires the approved linux/amd64 VM")
    try:
        result = package(args.build_root.resolve(), args.output.resolve(), args.source_manifest_sha256, args.artifact_checksums_sha256)
    except (OSError, ValueError, KeyError, TypeError, json.JSONDecodeError) as error:
        raise SystemExit("Oracle Free packaging failed: " + str(error)) from None
    print(json.dumps({"status": result["status"], "image_digest": result["image"]["image_digest"], "publication_plan_sha256": result["publication_plan_sha256"]}))


if __name__ == "__main__":
    main()
