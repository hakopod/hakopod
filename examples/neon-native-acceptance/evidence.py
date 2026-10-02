#!/usr/bin/env python3
"""Produce source-bound Neon evidence from fixed native observation schemas."""
import argparse, hashlib, json, os, platform, re, runpy, secrets, subprocess, sys, tempfile, time
from datetime import datetime, timezone
from pathlib import Path

HERE = Path(__file__).resolve().parent
ROOT = HERE.parent.parent
VERIFY = runpy.run_path(str(ROOT / "release/verify-neon-runtime.py"))
ID = re.compile(r"[0-9a-f]{32}")
MAX_OUTPUT = 2 * 1024 * 1024


def now():
    return datetime.now(timezone.utc).isoformat(timespec="milliseconds").replace("+00:00", "Z")


def atomic(path, value, exclusive=False):
    path = Path(path)
    if path.is_symlink() or any(parent.is_symlink() for parent in path.parents):
        raise ValueError("Neon evidence paths must not be symbolic")
    path.parent.mkdir(parents=True, exist_ok=True)
    fd, name = tempfile.mkstemp(prefix="." + path.name + "-", dir=path.parent)
    temporary = Path(name)
    try:
        os.fchmod(fd, 0o600)
        with os.fdopen(fd, "w") as output:
            json.dump(value, output, sort_keys=True, separators=(",", ":"))
            output.write("\n"); output.flush(); os.fsync(output.fileno())
        if exclusive:
            os.link(temporary, path); temporary.unlink()
        else:
            temporary.replace(path)
    except Exception:
        temporary.unlink(missing_ok=True)
        raise


def load(path):
    return VERIFY["read_json"](path)


def command(args, timeout=30):
    result = subprocess.run(args, stdin=subprocess.DEVNULL, stdout=subprocess.PIPE,
                            stderr=subprocess.PIPE, timeout=timeout, check=False)
    if len(result.stdout) > MAX_OUTPUT or len(result.stderr) > MAX_OUTPUT:
        raise ValueError("Neon observation command exceeded its output bound")
    if result.returncode:
        raise ValueError("Neon observation command failed; raw output was withheld")
    return result.stdout.decode()


def kubectl(context, *args):
    return command(["kubectl", "--request-timeout=15s", "--context", context, *args])


def state(path):
    value = load(path)
    required = {"schema_version", "run_id", "started_at", "started_monotonic", "context",
                "cluster_uid", "node_names", "source_files", "images", "expected_identities", "identities", "process_observations", "resources", "failures"}
    if set(value) != required or value.get("schema_version") != 2 or not ID.fullmatch(value.get("run_id", "")):
        raise ValueError("Neon evidence state is malformed")
    return value


def begin(args):
    if Path(args.state).exists() or Path(args.events).exists():
        raise ValueError("Use fresh Neon evidence paths")
    if sys.platform != "linux" or platform.machine() not in ("x86_64", "amd64"):
        raise ValueError("Neon native evidence requires Linux AMD64")
    current = command(["kubectl", "config", "current-context"]).strip()
    if args.context != "k3d-hakopod-dev" or current != args.context:
        raise ValueError("Neon evidence requires the active k3d-hakopod-dev context")
    namespace = json.loads(kubectl(args.context, "get", "namespace", "kube-system", "-o", "json"))
    nodes = json.loads(kubectl(args.context, "get", "nodes", "--chunk-size=32", "-o", "json"))
    names = sorted(item.get("metadata", {}).get("name", "") for item in nodes.get("items", []))
    cluster_uid = namespace.get("metadata", {}).get("uid", "")
    if not cluster_uid or len(names) < 3 or len(names) > 48 or any(not name for name in names):
        raise ValueError("Neon requires a bounded development cluster with at least three nodes")
    value = {"schema_version": 2, "run_id": secrets.token_hex(16), "started_at": now(),
             "started_monotonic": time.monotonic(), "context": args.context,
             "cluster_uid": cluster_uid, "node_names": names,
             "source_files": VERIFY["source_files"](args.source),
             "images": VERIFY["validate_images"](load(args.images)), "resources": {}, "failures": []}
    value["expected_identities"] = VERIFY["validate_identities"](load(args.identities), value["images"])
    value["identities"] = {}
    value["process_observations"] = {}
    atomic(args.state, value)
    atomic(args.events, {"schema_version": 2, "run_id": value["run_id"], "events": []})
    print(value["run_id"])


def bind(args):
    value = state(args.state)
    if args.role in value["resources"]:
        raise ValueError("Neon resource role is already bound")
    operation = load(args.operation)
    required = {"id", "platform_id", "kind", "status"}
    if set(operation) != required or operation["kind"] != "create" or operation["status"] != "succeeded" or not ID.fullmatch(operation["platform_id"]):
        raise ValueError("Neon create operation observation is malformed")
    namespace_name = "managed-platform-" + operation["platform_id"]
    namespace = json.loads(kubectl(value["context"], "get", "namespace", namespace_name, "-o", "json"))
    metadata, labels = namespace.get("metadata", {}), namespace.get("metadata", {}).get("labels", {})
    if metadata.get("uid", "") == "" or labels.get("app.kubernetes.io/managed-by") != "hakopod" or labels.get("hakopod.io/managed-platform-id") != operation["platform_id"] or labels.get("hakopod.io/owner-operation-id") != operation["id"]:
        raise ValueError("Neon namespace ownership is invalid")
    value["resources"][args.role] = {"platform_id": operation["platform_id"], "namespace": namespace_name,
        "namespace_uid": metadata["uid"], "create_operation_id": operation["id"]}
    atomic(args.state, value)


def observe_identities(args):
    value = state(args.state); resource = value["resources"].get("source")
    if not resource or value["identities"]:
        raise ValueError("Neon source resource is unbound or identities were already observed")
    pods = json.loads(kubectl(value["context"], "-n", resource["namespace"], "get", "pods", "--chunk-size=32", "-l", "hakopod.io/managed-platform-id=" + resource["platform_id"], "-o", "json"))
    observed = {}
    for pod in pods.get("items", []):
        metadata, spec = pod.get("metadata", {}), pod.get("spec", {})
        component = metadata.get("labels", {}).get("hakopod.io/neon-role", "")
        if component == "compute":
            containers = {container.get("name"): container for container in spec.get("containers", [])}
            components = [("compute", containers.get("compute", {})), ("compute-tls", containers.get("compute-tls", {}))]
        else:
            components = [(component, spec.get("containers", [])[0] if spec.get("containers") else {})]
        for logical, container in components:
            if logical not in value["images"] or container.get("image") != value["images"][logical]:
                raise ValueError("Neon pod image inventory is incomplete")
            process = kubectl(value["context"], "-n", resource["namespace"], "exec", "pod/" + metadata.get("name", ""), "-c", container.get("name", ""), "--", "/bin/sh", "-ceu", "printf '%s:%s\\n' \"$(id -u)\" \"$(id -g)\"; awk '/^(CapInh|CapPrm|CapEff|CapBnd|CapAmb|NoNewPrivs|Seccomp):/{print}' /proc/1/status")
            lines = process.splitlines(); identity = value["expected_identities"][logical]
            if not lines or lines[0] != str(identity["uid"]) + ":" + str(identity["gid"]):
                raise ValueError("Neon live process identity differs from its reviewed image")
            expected = {"CapInh:": "0000000000000000", "CapPrm:": "0000000000000000", "CapEff:": "0000000000000000", "CapBnd:": "0000000000000000", "CapAmb:": "0000000000000000", "NoNewPrivs:": "1", "Seccomp:": "2"}
            status = {parts[0]: parts[1] for parts in (line.split() for line in lines[1:]) if len(parts) == 2}
            if status != expected:
                raise ValueError("Neon live process security state is incomplete")
            digest = hashlib.sha256(json.dumps({"pod_uid": metadata.get("uid"), "component": logical, "image": container["image"], "status": status}, sort_keys=True, separators=(",", ":")).encode()).hexdigest()
            observed.setdefault(logical, []).append({"pod_uid": metadata.get("uid"), "process_observation_sha256": digest})
    if set(observed) != VERIFY["COMPONENTS"]:
        raise ValueError("Neon live process inventory is incomplete")
    value["identities"] = value["expected_identities"]
    value["process_observations"] = {name: sorted(items, key=lambda item: item["pod_uid"]) for name, items in observed.items()}
    atomic(args.state, value)


def validate_observation(case, observation, value):
    source = value["resources"].get("source", {})
    target = value["resources"].get("recovery_target", {})
    common = {"run_id", "platform_id", "namespace_uid"}
    if not common <= set(observation) or observation["run_id"] != value["run_id"] or observation["platform_id"] != source.get("platform_id") or observation["namespace_uid"] != source.get("namespace_uid"):
        raise ValueError("Neon observation is not bound to the source fixture")
    if case == "ownership-capability":
        required = common | {"mutation_capability_required", "foreign_owner_refused", "deletion_token_required", "owner_operation_id", "resource_intent_ids"}
        valid = set(observation) == required and all(observation[k] is True for k in ("mutation_capability_required", "foreign_owner_refused", "deletion_token_required")) and ID.fullmatch(observation["owner_operation_id"]) and 3 <= len(observation["resource_intent_ids"]) <= 16 and all(ID.fullmatch(x) for x in observation["resource_intent_ids"])
    elif case == "tls":
        required = common | {"client_verification_enforced", "server_verified", "plaintext_refused", "services"}
        valid = set(observation) == required and all(observation[k] is True for k in ("client_verification_enforced", "server_verified", "plaintext_refused")) and set(observation["services"]) == {"storage-controller", "pageserver", "safekeeper", "compute", "proxy"}
    elif case == "tenant-timeline-compute-lifecycle":
        required = common | {"tenant_id", "timeline_id", "tenant_generation", "timeline_generation", "compute_names", "created", "stopped", "deleted"}
        names = observation.get("compute_names")
        valid = set(observation) == required and ID.fullmatch(observation["tenant_id"]) and ID.fullmatch(observation["timeline_id"]) and type(observation["tenant_generation"]) is int and observation["tenant_generation"] > 0 and type(observation["timeline_generation"]) is int and observation["timeline_generation"] > 0 and isinstance(names, list) and names == ["compute-" + str(i) for i in range(len(names))] and 1 <= len(names) <= 6 and observation["created"] is True and observation["stopped"] is True and observation["deleted"] is True
    elif case == "backup-recovery":
        required = common | {"target_platform_id", "target_namespace_uid", "format", "parts", "tenant_id", "timeline_id", "tenant_generation", "timeline_generation", "commit_lsn", "pageserver_remote_consistent_lsns", "source_object_prefix", "object_inventory_sha256", "object_count", "object_bytes", "restored_data_sha256", "isolated_target"}
        lsns = observation.get("pageserver_remote_consistent_lsns", {})
        valid = set(observation) == required and observation["target_platform_id"] == target.get("platform_id") and observation["target_namespace_uid"] == target.get("namespace_uid") and observation["format"] == "hakopod-neon-recovery-v1" and observation["parts"] == ["tenant.json", "timeline.json", "remote-storage.tar"] and ID.fullmatch(observation["tenant_id"]) and ID.fullmatch(observation["timeline_id"]) and type(observation["tenant_generation"]) is int and observation["tenant_generation"] > 0 and type(observation["timeline_generation"]) is int and observation["timeline_generation"] > 0 and VERIFY["valid_lsn"](observation["commit_lsn"]) and isinstance(lsns, dict) and 2 <= len(lsns) <= 8 and all(VERIFY["valid_lsn"](x) and VERIFY["lsn_value"](x) >= VERIFY["lsn_value"](observation["commit_lsn"]) for x in lsns.values()) and isinstance(observation["source_object_prefix"], str) and observation["source_object_prefix"] and not observation["source_object_prefix"].startswith("/") and ".." not in observation["source_object_prefix"] and re.fullmatch(r"[0-9a-f]{64}", observation["object_inventory_sha256"]) and type(observation["object_count"]) is int and 1 <= observation["object_count"] <= 100000 and type(observation["object_bytes"]) is int and 1 <= observation["object_bytes"] <= 64 << 30 and re.fullmatch(r"[0-9a-f]{64}", observation["restored_data_sha256"]) and observation["isolated_target"] is True
    elif case == "restart-failure":
        required = common | {"storage_pod_uids_before", "storage_pod_uids_after", "compute_pod_uids_before", "compute_pod_uids_after", "failure_observed", "service_recovered", "data_sha256"}
        valid = set(observation) == required and observation["storage_pod_uids_before"] != observation["storage_pod_uids_after"] and observation["compute_pod_uids_before"] != observation["compute_pod_uids_after"] and observation["failure_observed"] is True and observation["service_recovered"] is True and re.fullmatch(r"[0-9a-f]{64}", observation["data_sha256"])
    elif case == "revocation-cleanup":
        cancellation = value["resources"].get("cancellation_target", {})
        receipt = {"schema_version", "restore_operation_id", "project", "environment", "source_platform_id", "source_revision", "target_platform_id", "target_revision", "artifact_id", "manifest_sha256", "tenant_id", "timeline_id", "tenant_generation", "timeline_generation", "journal_entries", "journal_phase_counts", "cleanup_pending", "operation_authority_refused", "cancellation_target_namespace_uid", "deployment_count", "statefulset_count", "workload_replicas_zero", "pods_absent", "staging_prefix_empty"}
        required = common | receipt | {"foreign_resources_preserved"}
        counts = observation.get("journal_phase_counts", {})
        valid = set(observation) == required and observation["source_platform_id"] == source.get("platform_id") and observation["target_platform_id"] == cancellation.get("platform_id") and observation["cancellation_target_namespace_uid"] == cancellation.get("namespace_uid") and ID.fullmatch(observation["restore_operation_id"]) and ID.fullmatch(observation["artifact_id"]) and re.fullmatch(r"[0-9a-f]{64}", observation["manifest_sha256"]) and ID.fullmatch(observation["tenant_id"]) and ID.fullmatch(observation["timeline_id"]) and isinstance(counts, dict) and set(counts) == {"complete", "empty_complete", "untouched_complete"} and all(type(count) is int and count >= 0 for count in counts.values()) and type(observation["journal_entries"]) is int and observation["journal_entries"] > 0 and sum(counts.values()) == observation["journal_entries"] and type(observation["deployment_count"]) is int and observation["deployment_count"] == 1 and type(observation["statefulset_count"]) is int and observation["statefulset_count"] >= 6 and observation["cleanup_pending"] is False and all(observation[k] is True for k in ("operation_authority_refused", "workload_replicas_zero", "pods_absent", "staging_prefix_empty", "foreign_resources_preserved"))
    else:
        raise ValueError("Unknown Neon acceptance case")
    if not valid:
        raise ValueError("Neon native observation is malformed for " + case)


def record(args):
    value = state(args.state); stream = load(args.events); observation = load(args.observation)
    if stream.get("run_id") != value["run_id"] or any(item.get("case") == args.case for item in stream.get("events", [])):
        raise ValueError("Neon event stream is stale or duplicate")
    validate_observation(args.case, observation, value)
    stream["events"].append({"sequence": len(stream["events"]) + 1, "case": args.case,
        "run_id": value["run_id"], "elapsed_seconds": round(time.monotonic() - value["started_monotonic"], 3),
        "evidence_sha256": VERIFY["file_hash"](args.observation)})
    atomic(args.events, stream)


def fail(args):
    value = state(args.state)
    if not re.fullmatch(r"[a-z0-9-]{1,128}", args.code):
        raise ValueError("Neon failure code is malformed")
    value["failures"].append({"code": args.code, "at": now()}); atomic(args.state, value)


def absent(value, resource):
    if kubectl(value["context"], "get", "namespace", resource["namespace"], "--ignore-not-found", "-o", "name").strip():
        raise ValueError("Neon namespace cleanup is incomplete")
    pvs = json.loads(kubectl(value["context"], "get", "pv", "--chunk-size=128", "-o", "json"))
    if len(pvs.get("items", [])) > 512 or any(item.get("spec", {}).get("claimRef", {}).get("namespace") == resource["namespace"] for item in pvs.get("items", [])):
        raise ValueError("Neon persistent-volume cleanup is incomplete")


def finalize(args):
    value = state(args.state); stream = load(args.events)
    if value["failures"] or set(value["resources"]) != {"source", "recovery_target", "cancellation_target"} or set(value["identities"]) != VERIFY["COMPONENTS"] or len({resource["platform_id"] for resource in value["resources"].values()}) != 3:
        raise ValueError("Neon resource evidence is incomplete")
    for resource in value["resources"].values(): absent(value, resource)
    elapsed = round(time.monotonic() - value["started_monotonic"], 3)
    VERIFY["validate_events"](stream.get("events", []), value["run_id"], elapsed)
    sources_after = VERIFY["source_files"](args.source)
    if sources_after != value["source_files"]: raise ValueError("Neon source changed during acceptance")
    cleanup = {"schema_version": 2, "run_id": value["run_id"], "context": value["context"], "status": "verified", "resources": value["resources"], "namespaces_absent": True, "persistent_volumes_absent": True}
    report = {"schema_version": 2, "context": value["context"], "execution": "native", "platform": "linux/amd64", "passed": True, "exit_code": 0, "limit_error": "", "started_at": value["started_at"], "finished_at": now(), "elapsed_seconds": elapsed, "environment": {"cpu_limit": 1, "memory_limit_bytes": 2147483648, "cluster_mutation": True, "cluster_uid": value["cluster_uid"], "node_names": value["node_names"]}, "source_files": value["source_files"], "source_files_after": sources_after, "images": value["images"], "identities": value["identities"], "process_observations": value["process_observations"], "test_events": stream["events"], "failed_cases": [], "runner_sha256": VERIFY["file_hash"](Path(args.source) / "examples/neon-native-acceptance/run.sh"), "producer_sha256": VERIFY["file_hash"](Path(args.source) / "examples/neon-native-acceptance/evidence.py"), "run_id": value["run_id"], "event_file_sha256": VERIFY["file_hash"](args.events), "log_sha256": VERIFY["file_hash"](args.sanitized_log), "cleanup": cleanup, "public_endpoint_qualified": False, "physical_zones_qualified": False}
    VERIFY["validate_acceptance"](report, sources_after, value["images"], value["expected_identities"])
    atomic(args.cleanup, cleanup, exclusive=True); atomic(args.report, report, exclusive=True)


def parser():
    root = argparse.ArgumentParser(description=__doc__); sub = root.add_subparsers(dest="command", required=True)
    p = sub.add_parser("begin"); p.add_argument("--source", type=Path, required=True); p.add_argument("--images", type=Path, required=True); p.add_argument("--identities", type=Path, required=True); p.add_argument("--state", type=Path, required=True); p.add_argument("--events", type=Path, required=True); p.add_argument("--context", default="k3d-hakopod-dev"); p.set_defaults(func=begin)
    p = sub.add_parser("bind-resource"); p.add_argument("--state", type=Path, required=True); p.add_argument("--role", choices=("source", "recovery_target", "cancellation_target"), required=True); p.add_argument("--operation", type=Path, required=True); p.set_defaults(func=bind)
    p = sub.add_parser("observe-identities"); p.add_argument("--state", type=Path, required=True); p.set_defaults(func=observe_identities)
    p = sub.add_parser("record"); p.add_argument("--state", type=Path, required=True); p.add_argument("--events", type=Path, required=True); p.add_argument("--case", choices=sorted(VERIFY["CASES"]), required=True); p.add_argument("--observation", type=Path, required=True); p.set_defaults(func=record)
    p = sub.add_parser("fail"); p.add_argument("--state", type=Path, required=True); p.add_argument("--code", required=True); p.set_defaults(func=fail)
    p = sub.add_parser("finalize"); p.add_argument("--source", type=Path, required=True); p.add_argument("--state", type=Path, required=True); p.add_argument("--events", type=Path, required=True); p.add_argument("--sanitized-log", type=Path, required=True); p.add_argument("--report", type=Path, required=True); p.add_argument("--cleanup", type=Path, required=True); p.set_defaults(func=finalize)
    return root


if __name__ == "__main__":
    try:
        args = parser().parse_args(); args.func(args)
    except (ValueError, OSError, subprocess.SubprocessError):
        raise SystemExit("Neon evidence rejected")
