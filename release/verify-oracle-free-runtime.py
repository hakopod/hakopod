#!/usr/bin/env python3
"""Verify the qualified Oracle Free profile without starting a database."""

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
GATE_PARSER = runpy.run_path(str(ROOT / "release/managed-runtime-availability.py"))
GATE_PATH = "internal/cluster/database_oracle_free_qualification.go"
SHA = re.compile(r"[0-9a-f]{64}")
UPSTREAM = "ff6f9178c1650df30afbf203ebdb633e9b80760a"
SIDB_SHA256 = "8bc95dc17a6be6c01c059064c04be32334c49d61dfb226e30e8a2d58cff98cde"
PACKAGE = "ghcr.io/hakopod/managed-oracle-free-operator"
TESTS = {"lifecycle": "TestManagedOracleFreeLive", "recovery": "TestManagedOracleRecoveryLive",
         "controller-loss": "TestManagedOracleFreeControllerLossLive"}
HTTP_TEST = "TestManagedOracleFreeHTTPLive"
HTTP_PHASES = ("authorization", "lifecycle", "backup_restore", "binding_revocation", "deletion")
ALL_TESTS = {**TESTS, "http-api": HTTP_TEST}
BUILD_INPUTS = {
    "Dockerfile.oracle-free-operator", "scripts/apply-managed-oracle-patches.py", "scripts/build-managed-oracle-free.sh",
    *["patches/oracle-operator/hakopod_free_" + name + ".go.txt" for name in
      ("policy", "policy_test", "resource", "resource_test", "health", "health_test")],
}
FILES = {"operator-upstream.patch", "sidb-v4.yaml", "build-artifacts.txt", "source-build-manifest.json",
         "packaging-receipt.json", "native-acceptance.json"}


def file_hash(path, limit=64 * 1024 * 1024):
    if path.is_symlink() or not path.is_file() or not 0 < path.stat().st_size <= limit:
        raise ValueError("Missing, symbolic or oversized Oracle qualification artifact: " + path.name)
    with path.open("rb") as source:
        return hashlib.file_digest(source, "sha256").hexdigest()


def read_json(path):
    file_hash(path, 2 * 1024 * 1024)
    def unique(pairs):
        result = {}
        for key, value in pairs:
            if key in result:
                raise ValueError("Duplicate Oracle qualification JSON field")
            result[key] = value
        return result
    return json.loads(path.read_bytes(), object_pairs_hook=unique)


def source_files(root):
    paths = {root / "go.mod", root / "go.sum", *[root / name for name in BUILD_INPUTS]}
    for name in ("auth", "cmd/hakopod-server", "internal", "templates", "patches/oracle-operator"):
        directory = root / name
        if directory.is_symlink() or not directory.is_dir():
            raise ValueError("Missing or symbolic Oracle qualification source: " + name)
        pending, count = [directory], 0
        while pending:
            with os.scandir(pending.pop()) as entries:
                for entry in entries:
                    if entry.name in (".git", ".DS_Store", "__pycache__"):
                        continue
                    count += 1
                    if count > 4096 or len(paths) >= 4096 or entry.is_symlink():
                        raise ValueError("Oracle source inventory is symbolic or exceeds its bound")
                    if entry.is_dir():
                        pending.append(Path(entry.path))
                    elif entry.is_file():
                        paths.add(Path(entry.path))
    paths.update(root / name for name in (
        "scripts/export-oracle-free-crd.py", "scripts/package-managed-oracle-free.py",
        "scripts/run-development-vitess-acceptance.py",
        "scripts/run-development-oracle-free-acceptance.py", "installer/oracle_free_controller.py",
        "scripts/run-development-oracle-free-http-acceptance.py",
        "scripts/oracle-free-http-fixtures.py",
        "release/managed-runtime-availability.py",
        "release/verify-oracle-free-runtime.py", "release/record-oracle-free-qualification.py"))
    total, result = 0, {}
    for path in sorted(paths):
        if any((root / parent).is_symlink() for parent in path.relative_to(root).parents):
            raise ValueError("Oracle source directory is symbolic")
        relative = path.relative_to(root).as_posix()
        digest = file_hash(path)
        result[relative] = qualification_gate_hash(path) if relative == GATE_PATH else digest
        total += path.stat().st_size
        if total > 128 * 1024 * 1024:
            raise ValueError("Oracle source inventory exceeds its byte bound")
    return result


def qualification_gate_hash(path):
    # Native acceptance runs with production admission closed. Enabling that
    # one reviewed boolean afterward must not invent a different tested build.
    # Validate the entire declaration and hash every byte except its literal.
    value = GATE_PARSER["_gate"](path, "oracleFreeReleaseQualified", "const")
    source = path.read_text()
    expected = ["package", "cluster", ";", "const", "oracleFreeReleaseQualified", "=", "true" if value else "false", ";"]
    if GATE_PARSER["_tokens"](source) != expected:
        raise ValueError("Oracle qualification gate file must contain only its literal declaration")
    raw = path.read_bytes()
    matches = list(re.finditer(rb"(?m)^const oracleFreeReleaseQualified = (true|false)$", raw))
    if len(matches) != 1:
        raise ValueError("Oracle qualification gate requires its canonical declaration")
    match = matches[0]
    return hashlib.sha256(raw[:match.start(1)] + b"false" + raw[match.end(1):]).hexdigest()


def source_image(root, relative, symbol):
    text = (root / relative).read_text()
    found = re.findall(r"^const " + re.escape(symbol) + r' = "([^"\n]+)"$', text, re.M)
    if len(found) != 1:
        raise ValueError("Oracle source image pin is missing or ambiguous")
    return found[0]


def source_images(root):
    return {
        "operator": source_image(root, "internal/cluster/database_oracle_free_controller.go", "oracleFreeOperatorImage"),
        "database": source_image(root, "internal/database/oracle.go", "OracleFreeImage"),
    }


def accepted_events(events, required, package="github.com/hakopod/hakopod/internal/cluster"):
    if not isinstance(events, list) or not 1 <= len(events) <= 512:
        raise ValueError("Oracle native test events are missing or oversized")
    running, passed = set(), set()
    for event in events:
        if not isinstance(event, dict) or set(event) != {"Action", "Package", "Test"}:
            raise ValueError("Oracle evidence must contain only structural Go test events")
        name, action = event["Test"], event["Action"]
        if event["Package"] != package or not isinstance(name, str) or not re.fullmatch(r"TestManagedOracle[A-Za-z0-9_/.-]{0,160}", name):
            raise ValueError("Oracle native event belongs to another test")
        if action == "run" and name not in running and name not in passed:
            running.add(name)
        elif action == "pass" and name in running:
            running.remove(name)
            passed.add(name)
        else:
            raise ValueError("Oracle native acceptance failed, skipped, duplicated or is incomplete")
    if running or not required.issubset(passed):
        raise ValueError("Oracle native lifecycle, security or recovery evidence is incomplete")
    return passed


def validate_acceptance(acceptance, sources, images):
    expected = {"schema_version", "context", "execution", "platform", "source_files", "images", "attempts"}
    if not isinstance(acceptance, dict) or set(acceptance) != expected or type(acceptance["schema_version"]) is not int or acceptance["schema_version"] != 1:
        raise ValueError("Oracle native acceptance schema is invalid")
    if acceptance["context"] != "k3d-hakopod-dev" or acceptance["execution"] != "native" or acceptance["platform"] != "linux/amd64" or acceptance["source_files"] != sources or acceptance["images"] != images:
        raise ValueError("Oracle native evidence belongs to another source, image or cluster")
    attempts = acceptance["attempts"]
    if not isinstance(attempts, list) or len(attempts) != len(ALL_TESTS):
        raise ValueError("Oracle requires three cluster cases and native HTTP/API acceptance")
    seen = set()
    source_sha = hashlib.sha256(json.dumps(sources, sort_keys=True, separators=(",", ":")).encode()).hexdigest()
    for attempt in attempts:
        fields = {"case", "exit_code", "source_manifest_sha256", "source_manifest_after_sha256", "log_sha256",
                  "test_events", "cluster_uid", "node_uids", "sidb_crd_uid", "cleanup"}
        if isinstance(attempt, dict) and attempt.get("case") == "http-api":
            fields.add("host_fixture_receipt_sha256")
        if not isinstance(attempt, dict) or set(attempt) != fields:
            raise ValueError("Oracle native attempt is malformed")
        case = attempt["case"]
        if not isinstance(case, str) or case not in ALL_TESTS or case in seen:
            raise ValueError("Oracle native cases are unknown or duplicated")
        seen.add(case)
        if type(attempt["exit_code"]) is not int or attempt["exit_code"] != 0 or attempt["source_manifest_sha256"] != source_sha or attempt["source_manifest_after_sha256"] != source_sha or not isinstance(attempt["log_sha256"], str) or not SHA.fullmatch(attempt["log_sha256"]):
            raise ValueError("Oracle native attempt failed or its source changed")
        for key in ("cluster_uid", "sidb_crd_uid"):
            if not isinstance(attempt[key], str) or not re.fullmatch(r"[a-f0-9-]{36}", attempt[key]):
                raise ValueError("Oracle native cluster identity is missing")
        nodes = attempt["node_uids"]
        allowed = {"k3d-hakopod-dev-server-0", "k3d-hakopod-database-worker-0", "k3d-hakopod-database-worker-1"}
        if not isinstance(nodes, dict) or not 1 <= len(nodes) <= 3 or not set(nodes) <= allowed or any(not isinstance(uid, str) or not re.fullmatch(r"[a-f0-9-]{36}", uid) for uid in nodes.values()):
            raise ValueError("Oracle native node identity is missing or foreign")
        cleanup = attempt["cleanup"]
        cleanup_fields = {"namespaces", "namespaces_absent", "persistent_volumes_absent"}
        if case == "http-api":
            cleanup_fields.add("host_fixtures")
            receipt = attempt["host_fixture_receipt_sha256"]
            if not isinstance(receipt, str) or not SHA.fullmatch(receipt):
                raise ValueError("Oracle HTTP host fixture receipt is missing")
        if not isinstance(cleanup, dict) or set(cleanup) != cleanup_fields or cleanup["namespaces_absent"] is not True or cleanup["persistent_volumes_absent"] is not True:
            raise ValueError("Oracle native cleanup is unverified")
        if case == "http-api":
            host = cleanup["host_fixtures"]
            flags = {"postgres_container_absent", "s3_container_absent", "credential_files_absent"}
            if not isinstance(host, dict) or set(host) != flags or any(host[key] is not True for key in flags):
                raise ValueError("Oracle HTTP host fixture cleanup is unverified")
        names = cleanup["namespaces"]
        expected_count = 3 if case == "http-api" else 2 if case == "recovery" else 1
        pattern = r"(?:hdb|hp)-[a-f0-9]{32}" if case == "http-api" else r"hdb-[a-f0-9]{32}"
        if not isinstance(names, list) or len(names) != expected_count or any(not isinstance(name, str) or not re.fullmatch(pattern, name) for name in names) or len(set(names)) != len(names):
            raise ValueError("Oracle native fixture inventory changed")
        if case == "http-api" and sum(name.startswith("hdb-") for name in names) != 2:
            raise ValueError("Oracle HTTP/API cleanup requires two databases and the bound application")
        required = {ALL_TESTS[case]}
        if case == "lifecycle":
            required.add(TESTS[case] + "/security")
        if case == "http-api":
            required.update(HTTP_TEST + "/" + phase for phase in HTTP_PHASES)
        package = "github.com/hakopod/hakopod/internal/api" if case == "http-api" else "github.com/hakopod/hakopod/internal/cluster"
        passed = accepted_events(attempt["test_events"], required, package)
        if any(name != ALL_TESTS[case] and not name.startswith(ALL_TESTS[case] + "/") for name in passed):
            raise ValueError("Oracle native attempt contains another case")


def validate_metadata(directory, root=ROOT):
    manifest = read_json(directory / "manifest.json")
    if not isinstance(manifest, dict) or set(manifest) != {"schema_version", "platform", "source", "source_files", "images", "files"} or type(manifest["schema_version"]) is not int or manifest["schema_version"] != 1 or manifest["platform"] != "linux/amd64":
        raise ValueError("Oracle qualification requires the exact native schema")
    gates = runpy.run_path(str(root / "release/managed-runtime-availability.py"))
    if not gates["release_availability"](root)["oracle-free"]:
        raise ValueError("Oracle Free shipping admission remains closed")
    sources = source_files(root)
    if manifest["source_files"] != sources or manifest["source"] != {"repository": "https://github.com/oracle/oracle-database-operator", "revision": UPSTREAM}:
        raise ValueError("Oracle source changed after native qualification")
    if not isinstance(manifest["files"], dict) or set(manifest["files"]) != FILES:
        raise ValueError("Oracle qualification artifact inventory is incomplete")
    for name, digest in manifest["files"].items():
        if not isinstance(digest, str) or not SHA.fullmatch(digest) or file_hash(directory / name) != digest:
            raise ValueError("Oracle qualification artifact changed: " + name)
    images = source_images(root)
    if not re.fullmatch(re.escape(PACKAGE) + r"@sha256:[a-f0-9]{64}", images["operator"]) or not re.fullmatch(r"container-registry\.oracle\.com/database/free:[A-Za-z0-9.-]+@sha256:[a-f0-9]{64}", images["database"]):
        raise ValueError("Oracle qualification requires exact official database and scoped operator pins")
    items = manifest["images"]
    if not isinstance(items, dict) or set(items) != {"operator", "database"} or any(not isinstance(items[kind], dict) or set(items[kind]) != {"reference", "binaries"} or items[kind]["reference"] != value for kind, value in images.items()):
        raise ValueError("Oracle qualified image identity changed")
    binaries = items["operator"]["binaries"]
    if not isinstance(binaries, dict) or set(binaries) != {"/manager"} or not isinstance(binaries["/manager"], str) or not SHA.fullmatch(binaries["/manager"]) or items["database"]["binaries"] != {}:
        raise ValueError("Oracle operator binary inventory changed")
    receipt = read_json(directory / "packaging-receipt.json")
    built_sources = read_json(directory / "source-build-manifest.json")
    if not isinstance(built_sources, dict) or not BUILD_INPUTS.issubset(built_sources) or any(built_sources[name] != sources[name] for name in BUILD_INPUTS):
        raise ValueError("Oracle operator build source changed")
    receipt_fields = {"schema_version", "status", "source_manifest_sha256", "build_artifacts_sha256", "publication_plan_sha256", "image"}
    if not isinstance(receipt, dict) or set(receipt) != receipt_fields or type(receipt["schema_version"]) is not int or receipt["schema_version"] != 1 or not isinstance(receipt["publication_plan_sha256"], str) or not SHA.fullmatch(receipt["publication_plan_sha256"]) or receipt["status"] != "packaged_not_published" or receipt["source_manifest_sha256"] != file_hash(directory / "source-build-manifest.json") or receipt["build_artifacts_sha256"] != file_hash(directory / "build-artifacts.txt"):
        raise ValueError("Oracle packaging provenance changed")
    image = receipt["image"]
    image_fields = {"archive", "archive_sha256", "image_digest", "repository", "platform", "config_user", "labels"}
    expected_labels = {"org.opencontainers.image.source": "https://github.com/hakopod/hakopod",
                       "io.hakopod.oracle.operator-upstream": UPSTREAM, "io.hakopod.oracle.patch": "hakopod-oracle-free-tcps-v1",
                       "io.hakopod.oracle.source-sha256": receipt["source_manifest_sha256"]}
    if not isinstance(image, dict) or set(image) != image_fields or image["archive"] != "managed-oracle-free-operator.oci.tar" or not isinstance(image["archive_sha256"], str) or not SHA.fullmatch(image["archive_sha256"]) or image["repository"] != PACKAGE or not isinstance(image["image_digest"], str) or PACKAGE + "@" + image["image_digest"] != images["operator"] or image["platform"] != "linux/amd64" or image["config_user"] != "65532:65532" or image["labels"] != expected_labels:
        raise ValueError("Oracle packaging image differs from native qualification")
    artifacts = {}
    for row in (directory / "build-artifacts.txt").read_text().splitlines():
        match = re.fullmatch(r"([a-f0-9]{64})  /[^\n]+/image/(bin/manager|upstream.patch|crds/singleinstancedatabases.yaml)", row)
        if not match or match[2] in artifacts:
            raise ValueError("Oracle build artifact identity changed")
        artifacts[match[2]] = match[1]
    expected = {"bin/manager": binaries["/manager"], "upstream.patch": file_hash(directory / "operator-upstream.patch"), "crds/singleinstancedatabases.yaml": SIDB_SHA256}
    if artifacts != expected or file_hash(directory / "sidb-v4.yaml") != SIDB_SHA256:
        raise ValueError("Oracle native operator artifacts differ from its source build")
    validate_acceptance(read_json(directory / "native-acceptance.json"), sources, images)
    return manifest


def docker(args, config):
    result = subprocess.run(["docker", *args], env=dict(os.environ, DOCKER_CONFIG=str(config)), capture_output=True, timeout=600)
    if result.returncode or len(result.stdout) > 2 * 1024 * 1024:
        raise ValueError("Oracle anonymous image verification failed")
    return result.stdout.decode()


def verify_images(manifest, runner=docker):
    with tempfile.TemporaryDirectory(prefix="oracle-free-release-") as temporary:
        base = Path(temporary)
        config = base / "anonymous"; config.mkdir()
        for kind, item in manifest["images"].items():
            reference = item["reference"]
            runner(["pull", "--platform", "linux/amd64", reference], config)
            values = json.loads(runner(["image", "inspect", reference], config))
            canonical = reference.split("@")[0].split(":")[0] + "@" + reference.split("@")[1]
            if not isinstance(values, list) or len(values) != 1 or values[0].get("Os") != "linux" or values[0].get("Architecture") != "amd64" or canonical not in values[0].get("RepoDigests", []):
                raise ValueError("Oracle pulled image platform or digest changed")
            if kind != "operator":
                continue
            settings = values[0].get("Config", {})
            labels = settings.get("Labels", {}) or {}
            if settings.get("User") != "65532:65532" or settings.get("Entrypoint") != ["/manager"] or labels.get("org.opencontainers.image.source") != "https://github.com/hakopod/hakopod" or labels.get("io.hakopod.oracle.operator-upstream") != UPSTREAM or labels.get("io.hakopod.oracle.patch") != "hakopod-oracle-free-tcps-v1" or labels.get("io.hakopod.oracle.source-sha256") != manifest["files"]["source-build-manifest.json"]:
                raise ValueError("Oracle operator image execution or source configuration changed")
            container = runner(["create", "--network", "none", "--entrypoint", "/manager", reference], config).strip()
            if not re.fullmatch(r"[a-f0-9]{64}", container):
                raise ValueError("Oracle image inspection container identity is invalid")
            try:
                target = base / "manager"
                runner(["cp", container + ":/manager", str(target)], config)
                if file_hash(target, 512 * 1024 * 1024) != item["binaries"]["/manager"]:
                    raise ValueError("Oracle operator binary differs from native qualification")
            finally:
                runner(["rm", "-v", container], config)


def verify(directory, output, root=ROOT):
    manifest = validate_metadata(directory, root)
    verify_images(manifest)
    output.mkdir(parents=True, exist_ok=False)
    for name in ["manifest.json", *manifest["files"]]:
        shutil.copyfile(directory / name, output / name)
    report = {"schema_version": 1, "platform": "linux/amd64", "anonymous_pull_verified": True,
              "image_binary_hashes_verified": True, "native_acceptance_reused": True,
              "images": {kind: item["reference"] for kind, item in manifest["images"].items()}}
    (output / "release-verification.json").write_text(json.dumps(report, indent=2) + "\n")


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--qualification", type=Path, default=ROOT / "release/managed-oracle-free")
    parser.add_argument("--output", type=Path, required=True)
    arguments = parser.parse_args()
    verify(arguments.qualification, arguments.output)
