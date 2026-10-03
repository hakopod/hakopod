#!/usr/bin/env python3
"""Verify the exact Neon release runtime and its native evidence."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import resource
import shutil
import signal
import subprocess
import tempfile
import tomllib

ROOT = Path(__file__).resolve().parent.parent
DIGEST = re.compile(r"[0-9a-f]{64}")
IMAGE = re.compile(r"[^@\s]+@sha256:[0-9a-f]{64}")
COMMIT = re.compile(r"[0-9a-f]{40}")
MAX_FILE_BYTES = 128 * 1024 * 1024
MAX_JSON_BYTES = 4 * 1024 * 1024
MAX_SOURCE_FILES = 20000
MAX_SOURCE_BYTES = 512 * 1024 * 1024
COMPONENTS = {"broker", "compute", "compute-tls", "controller-database", "pageserver", "proxy", "safekeeper", "storage-controller"}
IMAGE_STAGES = {"storage", "compute-tools", "compute-runtime"}
MANIFEST_TYPES = {"application/vnd.oci.image.manifest.v1+json", "application/vnd.docker.distribution.manifest.v2+json"}
CONFIG_TYPES = {"application/vnd.oci.image.config.v1+json", "application/vnd.docker.container.image.v1+json"}
STAGE_COMPONENTS = {
    "storage": {"broker", "pageserver", "proxy", "safekeeper", "storage-controller"},
    "compute-runtime": {"compute"},
}
HELPER_IMAGES = {"buildkit": "moby/buildkit@sha256:cec9f139f45e93c5c69c60f8b07cfad9f43f4ef6b6a6cd917527fea5ff2e3dea"}
BINARY_NAMES = {
    "storage": {"pg_sni_router", "pageserver", "pagectl", "safekeeper", "storage_broker", "storage_controller", "proxy", "endpoint_storage", "neon_local", "storage_scrubber"},
    "compute-tools": {"compute_ctl", "fast_import", "local_proxy"},
    "compute-runtime": {"postgres", "compute_ctl", "fast_import", "local_proxy", "pgbouncer", "postgres_exporter", "pgbouncer_exporter", "sql_exporter"},
}
CASE_EVENTS = {
    "ownership-capability": {"mutation-capability-required", "foreign-owner-refused", "deletion-token-required"},
    "tls": {"client-verified", "server-verified", "plaintext-refused"},
    "tenant-timeline-compute-lifecycle": {"tenant-created", "timeline-created", "branch-created", "compute-started", "compute-stopped", "branch-deleted", "timeline-deleted", "tenant-deleted"},
    "backup-recovery": {"backup-completed", "recovery-target-created", "restored-data-verified"},
    "restart-failure": {"storage-restarted", "compute-restarted", "failure-observed", "service-recovered"},
    "revocation-cleanup": {"access-revoked", "revoked-access-refused", "owned-resources-removed", "persistent-volumes-removed"},
    "isolation-authentication": {"tenant-isolated", "source-marker-verified", "target-marker-absent", "bad-credentials-refused", "cross-platform-credentials-refused"},
    "connection-limits": {"connection-limit-observed", "excess-connections-refused", "service-recovered"},
    "compute-roles": {"primary-writable", "replica-read-only", "replica-write-refused", "replica-caught-up"},
    "wal-quorum-fencing": {"quorum-loss-observed", "write-fenced", "quorum-restored", "fenced-write-absent", "write-recovered"},
    "controller-recovery": {"controller-restarted", "identity-preserved", "service-recovered"},
    "tenant-migration": {"owned-destination-verified", "generation-advanced", "ownership-preserved", "compute-routing-updated", "primary-data-verified", "replica-data-verified"},
    "object-store-outage": {"outage-observed", "backup-refused", "service-restored"},
    "restored-resource-update": {"reviewed-update-applied", "revision-advanced-once", "stale-revision-refused", "topology-change-refused", "identity-preserved", "pod-cpu-changed", "compute-roles-preserved", "restored-data-verified"},
}
CASES = set(CASE_EVENTS)
PRODUCER = {"runner_path": "examples/neon-native-acceptance/run.sh", "producer_path": "examples/neon-native-acceptance/evidence.py"}
SOURCE_DIRS = ("internal", "auth", "templates", "cmd", "hack", "scripts", "examples/neon-native-acceptance")
SOURCE_FILES = ("go.mod", "go.sum", "examples/owned-pod-stream.py", "release/record-neon-qualification.py",
                "release/verify-neon-runtime.py", "docs/managed-neon-qualification.md")


def file_hash(path, limit=MAX_FILE_BYTES):
    path = Path(path)
    if path.is_symlink() or not path.is_file() or path.stat().st_size > limit:
        raise ValueError("Missing, symbolic or oversized Neon qualification artifact: " + path.name)
    value = hashlib.sha256()
    with path.open("rb") as stream:
        for block in iter(lambda: stream.read(1024 * 1024), b""):
            value.update(block)
    return value.hexdigest()


def read_json(path, limit=MAX_JSON_BYTES):
    file_hash(path, limit)
    def unique(pairs):
        value = {}
        for key, item in pairs:
            if key in value:
                raise ValueError("Duplicate Neon qualification JSON key: " + key)
            value[key] = item
        return value
    value = json.loads(Path(path).read_text(), object_pairs_hook=unique)
    if not isinstance(value, dict):
        raise ValueError("Neon qualification JSON must be an object")
    return value


def source_files(root):
    root = Path(root)
    if root.is_symlink() or not root.is_dir() or any(parent.is_symlink() for parent in [root, *root.parents]):
        raise ValueError("Neon qualification source root and ancestors must not be symbolic")
    paths = [root / name for name in SOURCE_FILES]
    entries = 0
    for name in SOURCE_DIRS:
        directory = root / name
        if directory.is_symlink() or not directory.is_dir():
            raise ValueError("Missing or symbolic Neon source directory: " + name)
        pending = [directory]
        while pending:
            with os.scandir(pending.pop()) as children:
                for child in children:
                    if child.name in {".git", ".DS_Store", "__pycache__"} or child.name.endswith((".pyc", ".pyo")):
                        continue
                    entries += 1
                    if entries > MAX_SOURCE_FILES:
                        raise ValueError("Neon source traversal exceeded its file limit")
                    if child.is_symlink():
                        raise ValueError("Symbolic Neon qualification source is not permitted: " + child.name)
                    if child.is_dir():
                        pending.append(Path(child.path))
                    elif child.is_file():
                        paths.append(Path(child.path))
    result, total = {}, 0
    for path in sorted(set(paths)):
        relative = path.relative_to(root)
        if any((root / parent).is_symlink() for parent in relative.parents):
            raise ValueError("Symbolic Neon qualification source directory is not permitted")
        result[relative.as_posix()] = file_hash(path)
        total += path.stat().st_size
        if len(result) > MAX_SOURCE_FILES or total > MAX_SOURCE_BYTES:
            raise ValueError("Neon source inventory exceeded its bound")
    return result


def source_metadata(root):
    path = Path(root) / "hack/managed-neon/source-metadata.toml"
    file_hash(path)
    with path.open("rb") as stream:
        data = tomllib.load(stream)
    expected = {"schema_version", "upstream_repository", "upstream_commit", "proxy_patch",
                "proxy_patch_sha256", "ownership_patch", "ownership_patch_sha256",
                "postgres_commit", "postgres_tree", "postgres_patch", "postgres_patch_sha256",
                "consumer_patch_id", "consumer_patch", "consumer_patch_sha256",
                "transport_patch", "transport_patch_sha256", "reconfigure_patch", "reconfigure_patch_sha256",
                "placement_patch", "placement_patch_sha256",
                "frozen_combined_patch_sha256", "combined_candidate_tree", "source_archive",
                "qualification", "rejected_artifacts"}
    if set(data) != expected or type(data["schema_version"]) is not int or data["schema_version"] != 2:
        raise ValueError("Neon source metadata is missing or malformed")
    if data["upstream_repository"] != "https://github.com/neondatabase/neon.git" or not COMMIT.fullmatch(data["upstream_commit"]):
        raise ValueError("Neon upstream identity is invalid")
    for name in ("proxy_patch", "ownership_patch", "postgres_patch", "consumer_patch", "transport_patch", "reconfigure_patch", "placement_patch"):
        patch = Path(root) / data[name]
        if file_hash(patch) != data[name + "_sha256"]:
            raise ValueError("Neon patch identity changed: " + name)
    for name in ("frozen_combined_patch_sha256",):
        if not DIGEST.fullmatch(data[name]):
            raise ValueError("Neon frozen patch identity is invalid")
    if any(not COMMIT.fullmatch(data[name]) for name in ("combined_candidate_tree", "postgres_commit", "postgres_tree", "consumer_patch_id")):
        raise ValueError("Neon candidate tree identity is invalid")
    archive = data["source_archive"]
    if set(archive) != {"sha256", "size_bytes", "member_count", "repeat_comparison", "global_member_order", "location"} or not DIGEST.fullmatch(archive["sha256"]):
        raise ValueError("Neon source archive metadata is malformed")
    if type(archive["size_bytes"]) is not int or archive["size_bytes"] <= 0 or type(archive["member_count"]) is not int or archive["member_count"] <= 0 or archive["repeat_comparison"] != "PASS" or archive["global_member_order"] != "PASS":
        raise ValueError("Neon source archive is not reproducible and globally ordered")
    if data["qualification"] != {"images_built": False, "images_published": False, "cluster_qualified": False}:
        raise ValueError("Neon source metadata must remain pre-qualification")
    rejected = data["rejected_artifacts"]
    if not isinstance(rejected, list) or any(not isinstance(x, dict) or set(x) != {"sha256", "reason"} or not DIGEST.fullmatch(x["sha256"]) or not x["reason"] for x in rejected):
        raise ValueError("Neon rejected artifact inventory is malformed")
    return data


def source_patch_hashes(metadata):
    return {**{name: metadata[name + "_patch_sha256"]
               for name in ("proxy", "ownership", "postgres", "consumer", "transport", "reconfigure", "placement")},
            "combined": metadata["frozen_combined_patch_sha256"]}


def validate_images(images):
    if not isinstance(images, dict) or set(images) != COMPONENTS or any(not isinstance(v, str) or not IMAGE.fullmatch(v) for v in images.values()):
        raise ValueError("Neon requires a complete digest-pinned component image inventory")
    return images


def validate_identities(identities, images):
    if not isinstance(identities, dict) or set(identities) != COMPONENTS:
        raise ValueError("Neon requires a complete runtime identity inventory")
    for component, item in identities.items():
        if not isinstance(item, dict) or set(item) != {"uid", "gid", "image"} or type(item["uid"]) is not int or type(item["gid"]) is not int or item["uid"] < 1 or item["gid"] < 1 or item["image"] != images[component]:
            raise ValueError("Neon runtime identity is malformed or belongs to another image")
    return identities


def valid_lsn(value):
    if not isinstance(value, str) or not re.fullmatch(r"[0-9A-Fa-f]{1,8}/[0-9A-Fa-f]{1,8}", value):
        return False
    high, low = (int(part, 16) for part in value.split("/"))
    return high > 0 or low > 0


def lsn_value(value):
    if not valid_lsn(value):
        raise ValueError("Invalid Neon LSN")
    high, low = (int(part, 16) for part in value.split("/"))
    return high << 32 | low


def validate_events(events, run_id=None, elapsed=None):
    if not isinstance(events, list) or not events or len(events) > 128:
        raise ValueError("Neon native events are missing or oversized")
    seen = set()
    for event in events:
        if not isinstance(event, dict) or set(event) != {"sequence", "case", "run_id", "elapsed_seconds", "evidence_sha256"}:
            raise ValueError("Neon native events must be structural")
        case = event["case"]
        if case not in CASES or case in seen or event["sequence"] != len(seen) + 1 or (run_id is not None and event["run_id"] != run_id) or not DIGEST.fullmatch(event["evidence_sha256"]):
            raise ValueError("Neon native evidence contains failed, skipped, duplicate or malformed events")
        event_elapsed = event["elapsed_seconds"]
        if type(event_elapsed) not in (int, float) or isinstance(event_elapsed, bool) or event_elapsed < 0 or (elapsed is not None and event_elapsed > elapsed):
            raise ValueError("Neon native evidence contains an invalid event time")
        seen.add(case)
    if seen != CASES:
        raise ValueError("Neon native acceptance is incomplete")


def validate_build(build, metadata, archive=None):
    required = {"schema_version", "platform", "upstream_repository", "upstream_commit", "candidate_tree",
                "postgres_commit", "postgres_tree", "consumer_patch_id",
                "source_archive", "patches", "images", "identities", "image_stages", "helper_images"}
    if set(build) != required or type(build["schema_version"]) is not int or build["schema_version"] != 1 or build["platform"] != "linux/amd64":
        raise ValueError("Neon build evidence is malformed")
    if (build["upstream_repository"] != metadata["upstream_repository"] or build["upstream_commit"] != metadata["upstream_commit"] or
            build["candidate_tree"] != metadata["combined_candidate_tree"] or build["postgres_commit"] != metadata["postgres_commit"] or
            build["postgres_tree"] != metadata["postgres_tree"] or build["consumer_patch_id"] != metadata["consumer_patch_id"]):
        raise ValueError("Neon build belongs to another upstream source")
    expected_archive = {k: metadata["source_archive"][k] for k in ("sha256", "size_bytes", "member_count", "repeat_comparison", "global_member_order")}
    if build["source_archive"] != expected_archive:
        raise ValueError("Neon build source archive identity changed")
    if archive is not None and (file_hash(archive, metadata["source_archive"]["size_bytes"]) != expected_archive["sha256"] or Path(archive).stat().st_size != expected_archive["size_bytes"]):
        raise ValueError("Neon build source archive identity changed")
    patches = source_patch_hashes(metadata)
    if build["patches"] != patches:
        raise ValueError("Neon build patch provenance changed")
    images = validate_images(build["images"])
    validate_identities(build["identities"], images)
    stages = build["image_stages"]
    if build["helper_images"] != HELPER_IMAGES:
        raise ValueError("Neon helper image provenance changed")
    if not isinstance(stages, dict) or set(stages) != IMAGE_STAGES:
        raise ValueError("Neon image-stage provenance is incomplete")
    for stage, item in stages.items():
        if not isinstance(item, dict) or set(item) != {"image", "manifest_digest", "config_digest", "binary_sha256", "published"} or not IMAGE.fullmatch(item["image"]) or not re.fullmatch(r"sha256:[0-9a-f]{64}", item["manifest_digest"]) or not re.fullmatch(r"sha256:[0-9a-f]{64}", item["config_digest"]) or item["published"] is not True:
            raise ValueError("Neon image-stage provenance is malformed: " + stage)
        binaries = item["binary_sha256"]
        if not isinstance(binaries, dict) or {Path(k).name for k in binaries} != BINARY_NAMES[stage] or any(not isinstance(k, str) or not k.startswith("/") or not DIGEST.fullmatch(v) for k, v in binaries.items()):
            raise ValueError("Neon image binary inventory is incomplete: " + stage)
        if item["manifest_digest"] != item["image"].rsplit("@", 1)[1]:
            raise ValueError("Neon image stage manifest digest changed: " + stage)
    for stage, components in STAGE_COMPONENTS.items():
        if any(images[component] != stages[stage]["image"] for component in components):
            raise ValueError("Neon component image differs from its built stage: " + stage)
    return images, build["identities"]


def validate_acceptance(report, sources, images, identities):
    required = {"schema_version", "context", "execution", "platform", "passed", "exit_code", "limit_error",
                "source_files", "source_files_after", "images", "identities", "process_observations", "test_events", "failed_cases",
                "public_endpoint_qualified", "physical_zones_qualified", "runner_sha256", "producer_sha256",
                "run_id", "event_file_sha256", "log_sha256", "started_at", "finished_at", "elapsed_seconds", "environment", "cleanup"}
    if set(report) != required or type(report["schema_version"]) is not int or report["schema_version"] != 2 or report["context"] != "k3d-hakopod-dev" or report["execution"] != "native" or report["platform"] != "linux/amd64":
        raise ValueError("Neon native report is missing or malformed")
    if report["passed"] is not True or type(report["exit_code"]) is not int or report["exit_code"] != 0 or report["limit_error"] != "" or report["failed_cases"] != []:
        raise ValueError("A failed, skipped or bounded-out Neon run cannot qualify")
    if report["source_files"] != sources or report["source_files_after"] != sources or report["images"] != images or report["identities"] != identities:
        raise ValueError("Neon native evidence is stale or changed during acceptance")
    if report["public_endpoint_qualified"] is not False or report["physical_zones_qualified"] is not False:
        raise ValueError("Cluster acceptance cannot qualify public endpoints or physical zones")
    if not all(DIGEST.fullmatch(report[name]) for name in ("runner_sha256", "producer_sha256", "event_file_sha256", "log_sha256")) or not re.fullmatch(r"[0-9a-f]{32}", report["run_id"]):
        raise ValueError("Neon native run identity is malformed")
    elapsed = report["elapsed_seconds"]
    if type(elapsed) not in (int, float) or isinstance(elapsed, bool) or not 0 < elapsed <= 7200:
        raise ValueError("Neon native run duration is invalid")
    environment = report["environment"]
    if not isinstance(environment, dict) or set(environment) != {"cpu_limit", "memory_limit_bytes", "cluster_mutation", "cluster_uid", "node_names"} or environment["cpu_limit"] != 1 or environment["memory_limit_bytes"] != 2147483648 or environment["cluster_mutation"] is not True or not environment["cluster_uid"] or not isinstance(environment["node_names"], list) or not 3 <= len(environment["node_names"]) <= 48 or environment["node_names"] != sorted(set(environment["node_names"])):
        raise ValueError("Neon native environment evidence is incomplete")
    cleanup = report["cleanup"]
    if not isinstance(cleanup, dict) or set(cleanup) != {"schema_version", "run_id", "context", "status", "resources", "namespaces_absent", "persistent_volumes_absent"} or cleanup["schema_version"] != 2 or cleanup["run_id"] != report["run_id"] or cleanup["context"] != report["context"] or cleanup["status"] != "verified" or cleanup["namespaces_absent"] is not True or cleanup["persistent_volumes_absent"] is not True or set(cleanup["resources"]) != {"source", "recovery_target", "cancellation_target"}:
        raise ValueError("Neon cleanup evidence is not bound to the native fixture")
    platform_ids = set()
    for resource in cleanup["resources"].values():
        if not isinstance(resource, dict) or set(resource) != {"platform_id", "namespace", "namespace_uid", "create_operation_id"} or not re.fullmatch(r"[0-9a-f]{32}", resource.get("platform_id", "")) or resource.get("namespace") != "managed-platform-" + resource.get("platform_id", "") or not resource.get("namespace_uid") or not re.fullmatch(r"[0-9a-f]{32}", resource.get("create_operation_id", "")):
            raise ValueError("Neon cleanup evidence is not bound to the native fixture")
        platform_ids.add(resource["platform_id"])
    if len(platform_ids) != 3:
        raise ValueError("Neon recovery and cancellation targets must be separate")
    observations = report["process_observations"]
    if not isinstance(observations, dict) or set(observations) != COMPONENTS:
        raise ValueError("Neon process evidence is incomplete")
    for component, items in observations.items():
        if not isinstance(items, list) or not items or len(items) > 8:
            raise ValueError("Neon process evidence is incomplete")
        for item in items:
            if not isinstance(item, dict) or set(item) != {"pod_uid", "process_observation_sha256"} or not item["pod_uid"] or not DIGEST.fullmatch(item["process_observation_sha256"]):
                raise ValueError("Neon process evidence is malformed")
    validate_events(report["test_events"], report["run_id"], elapsed)


def release_runtime_qualified(root, images, metadata):
    path = Path(root) / "internal/managedplatform/neon_qualification.go"
    file_hash(path, 1024 * 1024)
    source = path.read_text()
    gates = re.findall(r'^func NeonReleaseQualified\(\) bool \{ return (true|false) \}$', source, re.MULTILINE)
    tables = re.findall(r'^var neonReleaseImages = map\[string\]string\{(.*?)\}$', source, re.MULTILINE | re.DOTALL)
    expected = {
        "NeonReleaseQualificationID": "neon-fa504217-pg17.11-linux-amd64",
        "NeonReleaseSourceArchiveSHA256": metadata["source_archive"]["sha256"],
        "NeonReleasePostgresCommit": metadata["postgres_commit"],
        "NeonReleaseConsumerPatchID": metadata["consumer_patch_id"],
    }
    if len(gates) != 1 or len(tables) != 1 or any(
        re.findall(r'^const ' + re.escape(name) + r' = "([^"]+)"$', source, re.MULTILINE) != [value]
        for name, value in expected.items()
    ):
        raise ValueError("Neon compiled release contract is missing or malformed")
    compiled = {}
    for line in tables[0].splitlines():
        if not line.strip():
            continue
        match = re.fullmatch(r'\s*"([a-z-]+)":\s*"([^"]+)",', line)
        if not match or match[1] in compiled:
            raise ValueError("Neon compiled image inventory is malformed")
        compiled[match[1]] = match[2]
    if gates[0] == "false" and not compiled:
        return False
    validate_images(compiled)
    if compiled != images:
        raise ValueError("Neon native images differ from the compiled release inventory")
    return gates[0] == "true"


def capabilities(root, images, metadata):
    # A release run does not approve another cluster's storage, routing or zones.
    return {
        "development_evidence_recorded": True,
        "release_runtime_qualified": release_runtime_qualified(root, images, metadata),
        "development_cluster_qualified": False,
        "cluster_qualified": False,
        "encrypted_storage_class_qualified": False,
        "public_endpoint_qualified": False,
        "physical_zones_qualified": False,
    }


def validate_capabilities(value, expected):
    if not isinstance(value, dict) or set(value) != set(expected) or any(
        value[name] is not flag for name, flag in expected.items()
    ):
        raise ValueError("Neon qualification capability boundary is invalid")


def validate_metadata(directory, root=ROOT):
    directory, root = Path(directory), Path(root)
    manifest = read_json(directory / "manifest.json")
    required = {"schema_version", "platform", "source", "source_files", "images", "identities", "tooling", "files", "capability"}
    if set(manifest) != required or type(manifest["schema_version"]) is not int or manifest["schema_version"] != 2 or manifest["platform"] != "linux/amd64":
        raise ValueError("Neon qualification manifest is malformed")
    metadata = source_metadata(root)
    sources = source_files(root)
    if manifest["source_files"] != sources:
        raise ValueError("Neon source changed after native qualification")
    source = manifest["source"]
    expected_source = {"repository": metadata["upstream_repository"], "commit": metadata["upstream_commit"], "candidate_tree": metadata["combined_candidate_tree"],
                       "postgres_commit": metadata["postgres_commit"], "postgres_tree": metadata["postgres_tree"], "consumer_patch_id": metadata["consumer_patch_id"],
                       "archive": {k: metadata["source_archive"][k] for k in ("sha256", "size_bytes", "member_count", "repeat_comparison", "global_member_order")},
                       "patches": source_patch_hashes(metadata)}
    if source != expected_source:
        raise ValueError("Neon qualification source provenance changed")
    images = validate_images(manifest["images"])
    identities = validate_identities(manifest["identities"], images)
    runner = root / "examples/neon-native-acceptance/run.sh"
    tooling = {"recorder_sha256": file_hash(root / "release/record-neon-qualification.py"), "verifier_sha256": file_hash(root / "release/verify-neon-runtime.py"), "runner_sha256": file_hash(runner), "producer_sha256": file_hash(root / PRODUCER["producer_path"])}
    if manifest["tooling"] != tooling:
        raise ValueError("Neon qualification tooling changed")
    validate_capabilities(manifest["capability"], capabilities(root, images, metadata))
    if set(manifest["files"]) != {"build-provenance.json", "native-acceptance.json"}:
        raise ValueError("Neon qualification artifacts are incomplete")
    for name, digest in manifest["files"].items():
        if not DIGEST.fullmatch(digest) or file_hash(directory / name, MAX_JSON_BYTES) != digest:
            raise ValueError("Neon qualification artifact checksum changed: " + name)
    build = read_json(directory / "build-provenance.json")
    sealed_images, sealed_identities = validate_build(build, metadata)
    # The archive bytes are checked by the recorder; the sealed record binds its digest and size.
    if build["source_archive"] != source["archive"] or build["patches"] != source["patches"] or sealed_images != images or sealed_identities != identities:
        raise ValueError("Neon sealed build evidence disagrees with the manifest")
    acceptance = read_json(directory / "native-acceptance.json")
    validate_acceptance(acceptance, sources, images, identities)
    if acceptance["runner_sha256"] != tooling["runner_sha256"] or acceptance["producer_sha256"] != file_hash(root / PRODUCER["producer_path"]):
        raise ValueError("Neon native report used another evidence producer")
    return manifest


def docker(args, config):
    with tempfile.TemporaryFile() as stdout, tempfile.TemporaryFile() as stderr:
        process = subprocess.Popen(["docker", *args], stdout=stdout, stderr=stderr, text=False,
            start_new_session=True, env=dict(os.environ, DOCKER_CONFIG=str(config)))
        # docker cp writes the requested container file itself, so its file-size
        # limit must admit the same bounded binary size accepted by file_hash.
        # Other commands only write the temporary stdout and stderr files.
        file_limit = 512 * 1024 * 1024 if args and args[0] == "cp" else 2 * 1024 * 1024
        resource.prlimit(process.pid, resource.RLIMIT_FSIZE, (file_limit, file_limit))
        try: process.wait(timeout=600)
        except subprocess.TimeoutExpired:
            os.killpg(process.pid, signal.SIGKILL); process.wait(); raise ValueError("Docker verification timed out")
        if stdout.tell() > 2 * 1024 * 1024 or stderr.tell() > 2 * 1024 * 1024:
            raise ValueError("Docker output exceeded its bound")
        stdout.seek(0); stderr.seek(0)
        output = stdout.read().decode()
        if process.returncode: raise ValueError("Docker verification failed")
        return output


def verify_remote_manifest(reference, manifest_digest, config_digest, config, runner=docker):
    raw = runner(["buildx", "imagetools", "inspect", "--raw", reference], config)
    if hashlib.sha256(raw.encode()).hexdigest() != manifest_digest.removeprefix("sha256:"):
        raise ValueError("Neon registry manifest bytes changed")
    try:
        manifest = json.loads(raw)
    except json.JSONDecodeError as error:
        raise ValueError("Neon registry manifest is not JSON") from error
    descriptor = manifest.get("config", {})
    if (manifest.get("schemaVersion") != 2 or manifest.get("mediaType") not in MANIFEST_TYPES or
            not isinstance(descriptor, dict) or descriptor.get("mediaType") not in CONFIG_TYPES or
            descriptor.get("digest") != config_digest or type(descriptor.get("size")) is not int or descriptor["size"] <= 0):
        raise ValueError("Neon registry manifest or config descriptor changed")


def verify_images(images, identities, stages, runner=docker, manifest_verifier=verify_remote_manifest):
    with tempfile.TemporaryDirectory(prefix="neon-release-verification-") as temporary:
        config = Path(temporary) / "anonymous"; config.mkdir()
        for component, reference in sorted(images.items()):
            runner(["pull", "--platform", "linux/amd64", reference], config)
            values = json.loads(runner(["image", "inspect", reference], config))
            repository, digest = reference.rsplit("@", 1)
            if repository.rfind(":") > repository.rfind("/"): repository = repository[:repository.rfind(":")]
            expected_user = str(identities[component]["uid"]) + ":" + str(identities[component]["gid"])
            if not isinstance(values, list) or len(values) != 1 or values[0].get("Os") != "linux" or values[0].get("Architecture") != "amd64" or repository + "@" + digest not in values[0].get("RepoDigests", []) or values[0].get("Config", {}).get("User") != expected_user:
                raise ValueError("Neon image platform or digest changed: " + component)
        for stage, item in sorted(stages.items()):
            runner(["pull", "--platform", "linux/amd64", item["image"]], config)
            values = json.loads(runner(["image", "inspect", item["image"]], config))
            repository, digest = item["image"].rsplit("@", 1)
            if repository.rfind(":") > repository.rfind("/"):
                repository = repository[:repository.rfind(":")]
            if (len(values) != 1 or values[0].get("Os") != "linux" or values[0].get("Architecture") != "amd64" or
                    repository + "@" + digest not in values[0].get("RepoDigests", [])):
                raise ValueError("Neon pulled stage metadata changed: " + stage)
            manifest_verifier(item["image"], item["manifest_digest"], item["config_digest"], config, runner)
            container = runner(["create", "--network", "none", item["image"]], config).strip()
            if not re.fullmatch(r"[0-9a-f]{12,64}", container):
                raise ValueError("Docker returned an invalid temporary container identity")
            try:
                for number, (path, digest) in enumerate(sorted(item["binary_sha256"].items())):
                    destination = Path(temporary) / (stage + "-" + str(number))
                    runner(["cp", container + ":" + path, str(destination)], config)
                    if file_hash(destination, 512 * 1024 * 1024) != digest:
                        raise ValueError("Neon image binary changed: " + stage + path)
            finally:
                runner(["rm", "-f", container], config)


def verify(directory, output, root=ROOT, runner=docker):
    manifest = validate_metadata(directory, root)
    if manifest["capability"]["release_runtime_qualified"] is not True:
        raise ValueError("Neon release gate is closed; candidate evidence cannot qualify a release")
    build = read_json(Path(directory) / "build-provenance.json")
    verify_images(manifest["images"], manifest["identities"], build["image_stages"], runner)
    output = Path(output)
    if output.exists(): raise ValueError("Use a fresh Neon verification output directory")
    output.parent.mkdir(parents=True, exist_ok=True)
    temporary = Path(tempfile.mkdtemp(prefix="." + output.name + "-", dir=output.parent))
    try:
        for name in ("manifest.json", *manifest["files"]): shutil.copyfile(Path(directory) / name, temporary / name)
        (temporary / "release-verification.json").write_text(json.dumps({"schema_version": 2, "platform": "linux/amd64", "anonymous_pull_verified": True, "native_acceptance_reused": True, "release_runtime_qualified": True, "deployment_qualified": False, "images": manifest["images"]}, indent=2, sort_keys=True) + "\n")
        os.replace(temporary, output)
    except Exception:
        shutil.rmtree(temporary, ignore_errors=True)
        raise


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--qualification", type=Path, default=ROOT / "release/managed-neon")
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args(); verify(args.qualification, args.output)
