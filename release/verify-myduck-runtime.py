#!/usr/bin/env python3
"""Verify the pinned MyDuck build and native lifecycle/API recovery evidence."""

import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import runpy
import shutil
import subprocess
import tempfile

ROOT = Path(__file__).resolve().parents[1]
GATES = runpy.run_path(str(ROOT / "release/managed-runtime-availability.py"))
GATE_PATH = "internal/database/myduck_qualification.go"
UPSTREAM = "6e3427591fd8895df9585969e7256f958fb639bb"
PACKAGE = "ghcr.io/hakopod/managed-myduck"
RUNTIME_VERSION = "0.1.0-hakopod.2"
SHA = re.compile(r"[a-f0-9]{64}")
UUID = re.compile(r"[a-f0-9]{8}(?:-[a-f0-9]{4}){3}-[a-f0-9]{12}")
TESTS = {"lifecycle": "TestManagedMyDuckLive", "recovery": "TestManagedMyDuckColdRecoveryLive",
         "http-api": "TestManagedMyDuckHTTPLive"}
HTTP_PHASES = ("authorization", "lifecycle", "backup_restore", "worker_loss", "cancellation", "deletion")
BUILD_INPUTS = {"Dockerfile.myduck", "scripts/apply-managed-myduck-patches.py", "scripts/build-managed-myduck.sh",
                "patches/myduck/0001-harden-managed-runtime.patch", "cmd/hakopod-myduck-storage/main.go"}
FILES = {"source-build-manifest.json", "packaging-receipt.json", "native-acceptance.json"}
BINARIES = {"/usr/local/bin/myduckserver", "/usr/local/bin/hakopod-myduck-storage"}


def image_labels(sources):
    return {"io.hakopod.myduck.patch-sha256": sources["patches/myduck/0001-harden-managed-runtime.patch"],
            "io.hakopod.myduck.upstream": UPSTREAM,
            "org.opencontainers.image.source": "https://github.com/hakopod/hakopod",
            "org.opencontainers.image.title": "Hakopod-managed-MyDuck",
            "org.opencontainers.image.version": RUNTIME_VERSION}


def file_hash(path, limit=64 * 1024 * 1024, *, allow_empty=False):
    if path.is_symlink() or not path.is_file() or not (0 if allow_empty else 1) <= path.stat().st_size <= limit:
        raise ValueError("Missing, symbolic or oversized MyDuck artifact: " + path.name)
    with path.open("rb") as source:
        return hashlib.file_digest(source, "sha256").hexdigest()


def read_json(path):
    file_hash(path, 2 * 1024 * 1024)
    def unique(pairs):
        value = {}
        for key, item in pairs:
            if key in value:
                raise ValueError("Duplicate MyDuck evidence field")
            value[key] = item
        return value
    return json.loads(path.read_bytes(), object_pairs_hook=unique)


def gate_hash(path):
    value = GATES["_gate"](path, "MyDuckRuntimeQualified", "const")
    expected = ["package", "database", ";", "const", "MyDuckRuntimeQualified", "=", "true" if value else "false", ";"]
    if GATES["_tokens"](path.read_text()) != expected:
        raise ValueError("MyDuck qualification gate must contain only its boolean declaration")
    raw = path.read_bytes()
    matches = list(re.finditer(rb"(?m)^const MyDuckRuntimeQualified = (true|false)$", raw))
    if len(matches) != 1:
        raise ValueError("MyDuck qualification gate is not canonical")
    match = matches[0]
    return hashlib.sha256(raw[:match.start(1)] + b"false" + raw[match.end(1):]).hexdigest()


def source_files(root):
    root = Path(root)
    paths = {root / name for name in ("go.mod", "go.sum", *BUILD_INPUTS,
        "release/verify-myduck-runtime.py", "release/record-myduck-qualification.py",
        "release/managed-runtime-availability.py", "scripts/run-development-myduck-acceptance.py",
        "scripts/run-development-oracle-free-acceptance.py", "release/verify-oracle-free-runtime.py",
        "scripts/run-development-vitess-acceptance.py")}
    for name in ("api", "auth", "cmd/hakopod-server", "cmd/hakopod-myduck-storage", "internal", "templates", "patches/myduck"):
        directory = root / name
        if directory.is_symlink() or not directory.is_dir():
            raise ValueError("Missing or symbolic MyDuck source directory")
        pending, count = [directory], 0
        while pending:
            with os.scandir(pending.pop()) as entries:
                for entry in entries:
                    if entry.name in (".git", ".DS_Store", "__pycache__"):
                        continue
                    count += 1
                    if count > 4096 or len(paths) > 4096 or entry.is_symlink():
                        raise ValueError("MyDuck source inventory exceeds its bound or contains a link")
                    if entry.is_dir():
                        pending.append(Path(entry.path))
                    elif entry.is_file():
                        paths.add(Path(entry.path))
    total, result = 0, {}
    for path in sorted(paths):
        if any((root / parent).is_symlink() for parent in path.relative_to(root).parents):
            raise ValueError("MyDuck source directory is symbolic")
        relative = path.relative_to(root).as_posix()
        digest = file_hash(path, allow_empty=True)
        result[relative] = gate_hash(path) if relative == GATE_PATH else digest
        total += path.stat().st_size
        if total > 128 * 1024 * 1024:
            raise ValueError("MyDuck source inventory exceeds its byte bound")
    return result


def source_images(root):
    path = Path(root) / "internal/cluster/database_myduck.go"
    file_hash(path)
    values = re.findall(r'^const myduckServerImage = "([^"\n]+)"$', path.read_text(), re.M)
    if len(values) != 1 or not re.fullmatch(re.escape(PACKAGE) + r"@sha256:[a-f0-9]{64}", values[0]):
        raise ValueError("MyDuck requires one pinned managed image")
    return {"runtime": values[0]}


def source_hash(sources):
    return hashlib.sha256(json.dumps(sources, sort_keys=True, separators=(",", ":")).encode()).hexdigest()


def accepted_events(events, required, package):
    if not isinstance(events, list) or not 1 <= len(events) <= 512:
        raise ValueError("MyDuck native test events are missing or oversized")
    running, passed = set(), set()
    for event in events:
        if not isinstance(event, dict) or set(event) != {"Action", "Package", "Test"}:
            raise ValueError("MyDuck evidence must contain only structural test events")
        name, action = event["Test"], event["Action"]
        if event["Package"] != package or not isinstance(name, str) or not re.fullmatch(r"TestManagedMyDuck[A-Za-z0-9_/.-]{0,160}", name):
            raise ValueError("MyDuck evidence belongs to another test")
        if action == "run" and name not in running and name not in passed:
            running.add(name)
        elif action == "pass" and name in running:
            running.remove(name)
            passed.add(name)
        else:
            raise ValueError("MyDuck acceptance failed, skipped, duplicated or is incomplete")
    if running or not required <= passed:
        raise ValueError("MyDuck native coverage is incomplete")
    return passed


def validate_acceptance(acceptance, sources, images):
    fields = {"schema_version", "context", "execution", "platform", "source_files", "images", "attempts"}
    if not isinstance(acceptance, dict) or set(acceptance) != fields or type(acceptance["schema_version"]) is not int or acceptance["schema_version"] != 1:
        raise ValueError("MyDuck native evidence schema is invalid")
    if acceptance["context"] != "k3d-hakopod-dev" or acceptance["execution"] != "native" or acceptance["platform"] != "linux/amd64" or acceptance["source_files"] != sources or acceptance["images"] != images:
        raise ValueError("MyDuck evidence belongs to another source, image or cluster")
    attempts = acceptance["attempts"]
    if not isinstance(attempts, list) or len(attempts) != len(TESTS):
        raise ValueError("MyDuck requires lifecycle, recovery and HTTP/API acceptance")
    seen = set()
    for attempt in attempts:
        fields = {"case", "exit_code", "source_manifest_sha256", "source_manifest_after_sha256", "log_sha256",
                  "test_events", "cluster_uid", "node_uids", "cleanup", "preserved_resources"}
        if not isinstance(attempt, dict) or set(attempt) != fields:
            raise ValueError("MyDuck attempt schema is invalid")
        case = attempt["case"]
        if not isinstance(case, str) or case not in TESTS or case in seen:
            raise ValueError("MyDuck native cases are unknown or duplicated")
        seen.add(case)
        if type(attempt["exit_code"]) is not int or attempt["exit_code"] != 0 or attempt["source_manifest_sha256"] != source_hash(sources) or attempt["source_manifest_after_sha256"] != source_hash(sources) or not isinstance(attempt["log_sha256"], str) or not SHA.fullmatch(attempt["log_sha256"]):
            raise ValueError("MyDuck native attempt failed or source changed")
        if not isinstance(attempt["cluster_uid"], str) or not UUID.fullmatch(attempt["cluster_uid"]):
            raise ValueError("MyDuck native cluster identity is missing")
        nodes = attempt["node_uids"]
        allowed = {"k3d-hakopod-dev-server-0", "k3d-hakopod-database-worker-0", "k3d-hakopod-database-worker-1"}
        if not isinstance(nodes, dict) or not 1 <= len(nodes) <= 3 or not set(nodes) <= allowed or any(not isinstance(uid, str) or not UUID.fullmatch(uid) for uid in nodes.values()):
            raise ValueError("MyDuck native node identity is missing or foreign")
        preserved = attempt["preserved_resources"]
        counts = {"namespace_count", "persistent_volume_count", "s3_container_count"}
        if not isinstance(preserved, dict) or set(preserved) != {"before_sha256", "after_sha256", *counts} or not isinstance(preserved["before_sha256"], str) or not SHA.fullmatch(preserved["before_sha256"]) or preserved["after_sha256"] != preserved["before_sha256"] or any(type(preserved[key]) is not int or not 0 <= preserved[key] <= 1024 for key in counts):
            raise ValueError("MyDuck unrelated-resource preservation is unverified")
        cleanup = attempt["cleanup"]
        if not isinstance(cleanup, dict) or set(cleanup) != {"namespaces", "namespaces_absent", "persistent_volumes_absent"} or cleanup["namespaces_absent"] is not True or cleanup["persistent_volumes_absent"] is not True:
            raise ValueError("MyDuck native fixture cleanup is unverified")
        names = cleanup["namespaces"]
        count = 1 if case == "lifecycle" else 2
        if not isinstance(names, list) or len(names) != count or len(set(names)) != count or any(not isinstance(name, str) or not re.fullmatch(r"hdb-[a-f0-9]{32}", name) for name in names):
            raise ValueError("MyDuck fixture inventory is incomplete")
        required = {TESTS[case]}
        if case == "http-api":
            required.update(TESTS[case] + "/" + phase for phase in HTTP_PHASES)
        package = "github.com/hakopod/hakopod/internal/" + ("api" if case == "http-api" else "cluster")
        passed = accepted_events(attempt["test_events"], required, package)
        if any(name != TESTS[case] and not name.startswith(TESTS[case] + "/") for name in passed):
            raise ValueError("MyDuck attempt includes another case")


def validate_metadata(directory, root=ROOT):
    manifest = read_json(directory / "manifest.json")
    if not isinstance(manifest, dict) or set(manifest) != {"schema_version", "platform", "source", "source_files", "images", "files"} or type(manifest["schema_version"]) is not int or manifest["schema_version"] != 1 or manifest["platform"] != "linux/amd64":
        raise ValueError("MyDuck manifest schema is invalid")
    if not GATES["_gate"](Path(root) / GATE_PATH, "MyDuckRuntimeQualified", "const"):
        raise ValueError("MyDuck release gate is closed")
    sources, images = source_files(root), source_images(root)
    if manifest["source_files"] != sources or manifest["source"] != {"repository": "https://github.com/apecloud/myduckserver", "revision": UPSTREAM}:
        raise ValueError("MyDuck source changed after qualification")
    if not isinstance(manifest["files"], dict) or set(manifest["files"]) != FILES or any(manifest["files"][name] != file_hash(directory / name) for name in FILES):
        raise ValueError("MyDuck evidence changed after qualification")
    items = manifest["images"]
    if not isinstance(items, dict) or set(items) != {"runtime"} or not isinstance(items["runtime"], dict) or set(items["runtime"]) != {"reference", "binaries"} or items["runtime"]["reference"] != images["runtime"]:
        raise ValueError("MyDuck qualified image changed")
    binaries = items["runtime"]["binaries"]
    if not isinstance(binaries, dict) or set(binaries) != BINARIES or any(not isinstance(value, str) or not SHA.fullmatch(value) for value in binaries.values()):
        raise ValueError("MyDuck binary inventory changed")
    build = read_json(directory / "source-build-manifest.json")
    if not isinstance(build, dict) or set(build) != {"schema_version", "upstream_repository", "upstream_commit", "files"} or type(build["schema_version"]) is not int or build["schema_version"] != 1 or build["upstream_repository"] != "https://github.com/apecloud/myduckserver" or build["upstream_commit"] != UPSTREAM or not isinstance(build["files"], dict) or not BUILD_INPUTS <= set(build["files"]) or any(build["files"][name] != sources[name] for name in BUILD_INPUTS):
        raise ValueError("MyDuck image build source changed")
    receipt = read_json(directory / "packaging-receipt.json")
    receipt_fields = {"schema_version", "image_reference", "archive_sha256", "platform", "config_user",
                      "entrypoint", "labels", "binaries", "source_build_manifest_sha256"}
    if (not isinstance(receipt, dict) or set(receipt) != receipt_fields
            or type(receipt["schema_version"]) is not int or receipt["schema_version"] != 1
            or receipt["image_reference"] != images["runtime"] or receipt["binaries"] != binaries
            or receipt["source_build_manifest_sha256"] != file_hash(directory / "source-build-manifest.json")
            or receipt["platform"] != "linux/amd64" or receipt["config_user"] != "1000:1000"
            or receipt["entrypoint"] != ["/usr/local/bin/myduckserver"]
            or receipt["labels"] != image_labels(sources)
            or not isinstance(receipt["archive_sha256"], str) or not SHA.fullmatch(receipt["archive_sha256"])):
        raise ValueError("MyDuck packaging provenance differs from qualification")
    validate_acceptance(read_json(directory / "native-acceptance.json"), sources, images)
    return manifest


def docker(args, config):
    result = subprocess.run(["docker", *args], env=dict(os.environ, DOCKER_CONFIG=str(config)), capture_output=True, timeout=600)
    if result.returncode or len(result.stdout) > 2 * 1024 * 1024:
        raise ValueError("MyDuck anonymous image verification failed")
    return result.stdout.decode()


def verify_images(manifest, runner=docker):
    item = manifest["images"]["runtime"]
    reference = item["reference"]
    with tempfile.TemporaryDirectory(prefix="myduck-release-") as temporary:
        base = Path(temporary)
        config = base / "anonymous"
        config.mkdir()
        runner(["pull", "--platform", "linux/amd64", reference], config)
        values = json.loads(runner(["image", "inspect", reference], config))
        if not isinstance(values, list) or len(values) != 1 or values[0].get("Os") != "linux" or values[0].get("Architecture") != "amd64" or reference not in values[0].get("RepoDigests", []):
            raise ValueError("MyDuck pulled image platform or digest changed")
        settings = values[0].get("Config", {})
        if (settings.get("User") != "1000:1000" or settings.get("Entrypoint") != ["/usr/local/bin/myduckserver"]
                or settings.get("Cmd") not in (None, []) or settings.get("Labels") != image_labels(manifest["source_files"])):
            raise ValueError("MyDuck execution configuration changed")
        container = runner(["create", "--network", "none", "--entrypoint", "/usr/local/bin/myduckserver", reference], config).strip()
        if not re.fullmatch(r"[a-f0-9]{64}", container):
            raise ValueError("MyDuck inspection container identity is invalid")
        try:
            for name, digest in item["binaries"].items():
                target = base / Path(name).name
                runner(["cp", container + ":" + name, str(target)], config)
                if file_hash(target, 512 * 1024 * 1024) != digest:
                    raise ValueError("MyDuck binary differs from the qualified build")
        finally:
            runner(["rm", "-v", container], config)


def verify(directory, output, root=ROOT):
    manifest = validate_metadata(directory, root)
    verify_images(manifest)
    output.mkdir(parents=True, exist_ok=False)
    for name in ("manifest.json", *manifest["files"]):
        shutil.copyfile(directory / name, output / name)
    report = {"schema_version": 1, "platform": "linux/amd64", "anonymous_pull_verified": True,
              "image_binary_hashes_verified": True, "native_acceptance_reused": True,
              "images": {kind: item["reference"] for kind, item in manifest["images"].items()}}
    (output / "release-verification.json").write_text(json.dumps(report, indent=2) + "\n")


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--qualification", type=Path, default=ROOT / "release/managed-myduck")
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()
    verify(args.qualification, args.output)
