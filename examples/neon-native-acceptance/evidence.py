#!/usr/bin/env python3
"""Produce source-bound Neon evidence from fixed native observation schemas."""
import argparse, hashlib, json, os, platform, re, runpy, secrets, subprocess, sys, tempfile, time
from datetime import datetime, timezone
from pathlib import Path

HERE = Path(__file__).resolve().parent
ROOT = HERE.parent.parent
VERIFY = runpy.run_path(str(ROOT / "release/verify-neon-runtime.py"))
PARTIAL = runpy.run_path(str(HERE / "partial-create.py"))
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
    subject = target if case in {"restart-failure", "wal-quorum-fencing", "controller-recovery", "restored-resource-update", "tenant-migration"} else source
    common = {"run_id", "platform_id", "namespace_uid"}
    if not common <= set(observation) or observation["run_id"] != value["run_id"] or observation["platform_id"] != subject.get("platform_id") or observation["namespace_uid"] != subject.get("namespace_uid"):
        raise ValueError("Neon observation is not bound to its exact fixture")
    if case == "partial-create-cleanup":
        PARTIAL["validate_observation"](observation, value["resources"])
        valid = True
    elif case == "ownership-capability":
        required = common | {"mutation_capability_required", "foreign_owner_refused", "deletion_token_required", "owner_operation_id", "resource_intent_ids"}
        valid = set(observation) == required and all(observation[k] is True for k in ("mutation_capability_required", "foreign_owner_refused", "deletion_token_required")) and ID.fullmatch(observation["owner_operation_id"]) and 3 <= len(observation["resource_intent_ids"]) <= 16 and all(ID.fullmatch(x) for x in observation["resource_intent_ids"])
    elif case == "tls":
        required = common | {"client_verification_enforced", "server_verified", "plaintext_refused", "services", "compute_sql_names", "managed_tls", "control_plane_trust"}
        managed = observation.get("managed_tls", {}); before, injection, renewal = managed.get("before", {}), managed.get("injection", {}), managed.get("renewal", {})
        logical = {"broker-auth", "compute-auth", "controller-auth", "controller-database-password", "pageserver-auth", "proxy-auth", "safekeeper-auth"}
        trust = observation.get("control_plane_trust", {})
        if trust.get("proxy_authenticated_query") is not True:
            raise ValueError("Neon proxy authenticated SQL evidence is missing")
        sql_names = observation.get("compute_sql_names")
        sql_valid = isinstance(sql_names,list) and 2<=len(sql_names)<=6 and sql_names==["compute-"+str(i) for i in range(len(sql_names))]
        valid = sql_valid and set(observation) == required and all(observation[k] is True for k in ("client_verification_enforced", "server_verified", "plaintext_refused")) and set(observation["services"]) == {"broker", "controller-database", "storage-controller", "pageserver", "safekeeper", "compute", "compute-sql", "proxy"} and set(managed) == {"before", "injection", "renewal"} and all(item.get("status") == "passed" for item in (before, injection, renewal)) and set(before.get("snapshots", {})) == logical and set(renewal.get("snapshots", {})) == logical and injection.get("transition_journal_complete") is True and renewal.get("all_snapshots_owned_and_valid") is True and before.get("ca_fingerprint") == injection.get("ca_fingerprint") == renewal.get("ca_fingerprint") and injection.get("near_expiry_leaf_fingerprint") != renewal.get("served_proxy_leaf_fingerprint") and trust == {"correct_issuer_reached_authentication": True, "wrong_issuer_refused": True, "proxy_authenticated_query": True}
    elif case == "tenant-timeline-compute-lifecycle":
        required = common | {"tenant_id", "timeline_id", "tenant_generation", "timeline_generation", "compute_names", "created", "stopped", "deleted", "branch_created", "branch_deleted"}
        names = observation.get("compute_names")
        valid = set(observation) == required and ID.fullmatch(observation["tenant_id"]) and ID.fullmatch(observation["timeline_id"]) and type(observation["tenant_generation"]) is int and observation["tenant_generation"] > 0 and type(observation["timeline_generation"]) is int and observation["timeline_generation"] > 0 and isinstance(names, list) and names == ["compute-" + str(i) for i in range(len(names))] and 1 <= len(names) <= 6 and all(observation[key] is True for key in ("created","stopped","deleted","branch_created","branch_deleted"))
    elif case == "backup-recovery":
        required = common | {"target_platform_id", "target_namespace_uid", "format", "parts", "tenant_id", "timeline_id", "tenant_generation", "timeline_generation", "commit_lsn", "pageserver_remote_consistent_lsns", "source_object_prefix", "object_inventory_sha256", "object_count", "object_bytes", "restored_data_sha256", "isolated_target"}
        lsns = observation.get("pageserver_remote_consistent_lsns", {})
        valid = set(observation) == required and observation["target_platform_id"] == target.get("platform_id") and observation["target_namespace_uid"] == target.get("namespace_uid") and observation["format"] == "hakopod-neon-recovery-v1" and observation["parts"] == ["tenant.json", "timeline.json", "remote-storage.tar"] and ID.fullmatch(observation["tenant_id"]) and ID.fullmatch(observation["timeline_id"]) and type(observation["tenant_generation"]) is int and observation["tenant_generation"] > 0 and type(observation["timeline_generation"]) is int and observation["timeline_generation"] > 0 and VERIFY["valid_lsn"](observation["commit_lsn"]) and isinstance(lsns, dict) and 1 <= len(lsns) <= 8 and all(VERIFY["valid_lsn"](x) and VERIFY["lsn_value"](x) >= VERIFY["lsn_value"](observation["commit_lsn"]) for x in lsns.values()) and isinstance(observation["source_object_prefix"], str) and observation["source_object_prefix"] and not observation["source_object_prefix"].startswith("/") and ".." not in observation["source_object_prefix"] and re.fullmatch(r"[0-9a-f]{64}", observation["object_inventory_sha256"]) and type(observation["object_count"]) is int and 1 <= observation["object_count"] <= 100000 and type(observation["object_bytes"]) is int and 1 <= observation["object_bytes"] <= 64 << 30 and re.fullmatch(r"[0-9a-f]{64}", observation["restored_data_sha256"]) and observation["isolated_target"] is True
    elif case == "restart-failure":
        required = common | {"storage_pod_uids_before", "storage_pod_uids_after", "compute_pod_uids_before", "compute_pod_uids_after", "active_storage", "restarted_components", "statefulset_uids", "old_pods_absent", "failure_observed", "service_recovered", "data_sha256"}
        lists=[observation.get(key) for key in ("storage_pod_uids_before","storage_pod_uids_after","compute_pod_uids_before","compute_pod_uids_after")]; owners=observation.get("statefulset_uids",{})
        active=observation.get("active_storage",{})
        if not isinstance(active,dict): raise ValueError("restart-failure active storage is invalid")
        node=active.get("node_id"); generation=active.get("tenant_generation")
        active_valid = isinstance(active,dict) and set(active)=={"component","node_id","tenant_id","tenant_generation"} and type(node) is int and 1<=node<=8 and active.get("component")=="pageserver-"+str(node-1) and ID.fullmatch(str(active.get("tenant_id"))) and type(generation) is int and generation>0
        valid = set(observation) == required and active_valid and all(isinstance(items,list) and len(items)==1 and isinstance(items[0],str) and items[0] for items in lists) and len({items[0] for items in lists})==4 and observation["restarted_components"]=={"pageserver":active["component"],"compute":"compute-0"} and isinstance(owners,dict) and set(owners)=={"pageserver","compute"} and all(isinstance(uid,str) and uid for uid in owners.values()) and len(set(owners.values()))==2 and all(observation[key] is True for key in ("old_pods_absent","failure_observed","service_recovered")) and re.fullmatch(r"[0-9a-f]{64}", observation["data_sha256"])
    elif case == "revocation-cleanup":
        cancellation = value["resources"].get("cancellation_target", {})
        receipt = {"schema_version", "restore_operation_id", "project", "environment", "source_platform_id", "source_revision", "target_platform_id", "target_revision", "artifact_id", "manifest_sha256", "tenant_id", "timeline_id", "tenant_generation", "timeline_generation", "journal_entries", "journal_phase_counts", "cleanup_pending", "operation_authority_refused", "cancellation_target_namespace_uid", "deployment_count", "statefulset_count", "workload_replicas_zero", "pods_absent", "staging_prefix_empty"}
        required = common | receipt | {"foreign_resources_preserved"}
        counts = observation.get("journal_phase_counts", {})
        valid = set(observation) == required and observation["source_platform_id"] == source.get("platform_id") and observation["target_platform_id"] == cancellation.get("platform_id") and observation["cancellation_target_namespace_uid"] == cancellation.get("namespace_uid") and ID.fullmatch(observation["restore_operation_id"]) and ID.fullmatch(observation["artifact_id"]) and re.fullmatch(r"[0-9a-f]{64}", observation["manifest_sha256"]) and ID.fullmatch(observation["tenant_id"]) and ID.fullmatch(observation["timeline_id"]) and isinstance(counts, dict) and set(counts) == {"complete", "empty_complete", "untouched_complete"} and all(type(count) is int and count >= 0 for count in counts.values()) and type(observation["journal_entries"]) is int and observation["journal_entries"] > 0 and sum(counts.values()) == observation["journal_entries"] and type(observation["deployment_count"]) is int and observation["deployment_count"] == 1 and type(observation["statefulset_count"]) is int and observation["statefulset_count"] >= 6 and observation["cleanup_pending"] is False and all(observation[k] is True for k in ("operation_authority_refused", "workload_replicas_zero", "pods_absent", "staging_prefix_empty", "foreign_resources_preserved"))
    elif case == "isolation-authentication":
        required=common|{"target_platform_id","target_namespace_uid","source_tenant_id","source_timeline_id","target_tenant_id","target_timeline_id","source_marker_verified","target_marker_absent","bad_credentials_refused","cross_platform_credentials_refused"}
        valid=set(observation)==required and observation["target_platform_id"]==target.get("platform_id") and observation["target_namespace_uid"]==target.get("namespace_uid") and observation["target_platform_id"]!=observation["platform_id"] and observation["target_namespace_uid"]!=observation["namespace_uid"] and all(ID.fullmatch(observation[key]) for key in ("source_tenant_id","source_timeline_id","target_tenant_id","target_timeline_id")) and observation["source_tenant_id"]!=observation["target_tenant_id"] and observation["source_timeline_id"]!=observation["target_timeline_id"] and all(observation[key] is True for key in ("source_marker_verified","target_marker_absent","bad_credentials_refused","cross_platform_credentials_refused"))
    elif case == "connection-limits":
        required=common|{"configured_max_connections","attempted_connections","concurrent_connections","refused_connections","backend_pids","baseline_backend_pids","superuser_reserved_connections","reserved_connections","role_superuser","role_reserved","effective_connection_limit","held_markers_flushed","sleeping_backends_verified","baseline_unchanged","service_recovered"}
        pids=observation.get("backend_pids",[]); baseline=observation.get("baseline_backend_pids",[]); maximum=observation.get("configured_max_connections")
        capacity_valid=type(maximum) is int and 10<=maximum<=120 and all(type(observation.get(key)) is int and 0<=observation[key]<maximum for key in ("superuser_reserved_connections","reserved_connections")) and all(type(observation.get(key)) is bool for key in ("role_superuser","role_reserved"))
        effective=maximum if capacity_valid and observation["role_superuser"] else maximum-observation["superuser_reserved_connections"]-(0 if observation["role_reserved"] else observation["reserved_connections"]) if capacity_valid else 0
        valid=set(observation)==required and capacity_valid and type(observation["effective_connection_limit"]) is int and observation["effective_connection_limit"]==effective and isinstance(baseline,list) and 1<=len(baseline)<=8 and all(type(pid) is int and pid>0 for pid in baseline) and len(set(baseline))==len(baseline) and type(observation["attempted_connections"]) is int and observation["attempted_connections"]==maximum+8 and type(observation["concurrent_connections"]) is int and 1<=observation["concurrent_connections"]==effective-len(baseline) and type(observation["refused_connections"]) is int and observation["refused_connections"]>=1 and observation["refused_connections"]+observation["concurrent_connections"]==observation["attempted_connections"] and isinstance(pids,list) and all(type(pid) is int and pid>0 for pid in pids) and len(pids)==len(set(pids))==observation["concurrent_connections"] and not set(pids)&set(baseline) and all(observation[key] is True for key in ("held_markers_flushed","sleeping_backends_verified","baseline_unchanged","service_recovered"))
    elif case == "compute-roles":
        required=common|{"primary_compute","replica_compute","primary_writable","replica_read_only","replica_write_refused","replica_caught_up"}
        valid=set(observation)==required and observation["primary_compute"]=="compute-0" and observation["replica_compute"]=="compute-1" and all(observation[key] is True for key in ("primary_writable","replica_read_only","replica_write_refused","replica_caught_up"))
    elif case == "wal-quorum-fencing":
        required=common|{"safekeeper_count","stopped_safekeepers","stopped_statefulsets","stopped_pod_uids","replicas_zero_before_write","replicas_zero_after_refusal","replacement_pods_absent","write_refused_without_quorum","fenced_write_absent","quorum_restored","write_recovered"}
        sets=observation.get("stopped_statefulsets",{}); pods=observation.get("stopped_pod_uids",[])
        valid=set(observation)==required and type(observation["safekeeper_count"]) is int and observation["safekeeper_count"]==3 and type(observation["stopped_safekeepers"]) is int and observation["stopped_safekeepers"]==2 and isinstance(sets,dict) and set(sets)=={"neon-safekeeper-1","neon-safekeeper-2"} and all(isinstance(uid,str) and uid for uid in sets.values()) and len(set(sets.values()))==2 and isinstance(pods,list) and all(isinstance(uid,str) and uid for uid in pods) and len(pods)==len(set(pods))==2 and all(observation[key] is True for key in ("replicas_zero_before_write","replicas_zero_after_refusal","replacement_pods_absent","write_refused_without_quorum","fenced_write_absent","quorum_restored","write_recovered"))
    elif case == "restored-resource-update":
        identity={"tenant_id","timeline_id","tenant_generation","timeline_generation"}
        flags={"identity_preserved","stale_revision_refused","topology_change_refused","primary_writable","replica_read_only","replica_write_refused","replica_caught_up"}
        required=common|identity|flags|{"operation_id","revision_before","revision_after","compute_cpu_millis_before","compute_cpu_millis_after","compute_pods_before","compute_pods_after","primary_compute","replica_compute","restored_data_sha256"}
        before,after=observation.get("compute_pods_before",{}),observation.get("compute_pods_after",{})
        cpu_before,cpu_after=observation.get("compute_cpu_millis_before"),observation.get("compute_cpu_millis_after")
        pods_valid=isinstance(before,dict) and isinstance(after,dict) and set(before)==set(after)=={"compute-0","compute-1"}
        if pods_valid:
            pods_valid=all(isinstance(pods[name],dict) and set(pods[name])=={"pod_uid","cpu_millis"} and isinstance(pods[name]["pod_uid"],str) and pods[name]["pod_uid"] and type(pods[name]["cpu_millis"]) is int and pods[name]["cpu_millis"]==cpu for pods,cpu in ((before,cpu_before),(after,cpu_after)) for name in pods) and len({pod["pod_uid"] for pod in [*before.values(),*after.values()]})==4
        valid=set(observation)==required and ID.fullmatch(str(observation["operation_id"])) and all(ID.fullmatch(str(observation[key])) for key in ("tenant_id","timeline_id")) and all(type(observation[key]) is int and observation[key]>0 for key in ("tenant_generation","timeline_generation","revision_before","revision_after")) and observation["revision_after"]==observation["revision_before"]+1 and type(cpu_before) is int and 100<=cpu_before<=15900 and type(cpu_after) is int and cpu_after==cpu_before+100 and pods_valid and all(observation[key] is True for key in flags) and observation["primary_compute"]=="compute-0" and observation["replica_compute"]=="compute-1" and re.fullmatch(r"[0-9a-f]{64}",str(observation["restored_data_sha256"]))
    elif case == "tenant-migration":
        flags = {"tenant_owner_unchanged", "controller_move_completed", "compute_routing_verified", "primary_sql_verified", "replica_sql_verified"}
        required = common | flags | {"platform_revision", "tenant_id", "timeline_id", "source_node_id", "destination_node_id", "generation_before", "generation_after", "compute_names", "data_sha256"}
        nodes = [observation.get(key) for key in ("source_node_id", "destination_node_id")]
        generations = [observation.get(key) for key in ("generation_before", "generation_after")]
        names = observation.get("compute_names")
        valid = (
            set(observation) == required
            and type(observation["platform_revision"]) is int and observation["platform_revision"] > 0
            and all(isinstance(observation[key], str) and ID.fullmatch(observation[key]) for key in ("tenant_id", "timeline_id"))
            and all(type(node) is int and 1 <= node <= 8 for node in nodes) and nodes[0] != nodes[1]
            and all(type(generation) is int and 1 <= generation <= 4294967295 for generation in generations)
            and generations[1] > generations[0]
            and isinstance(names, list) and 2 <= len(names) <= 6
            and names == ["compute-" + str(i) for i in range(len(names))]
            and all(observation[key] is True for key in flags)
            and isinstance(observation["data_sha256"], str) and re.fullmatch(r"[0-9a-f]{64}", observation["data_sha256"])
        )
    elif case == "controller-recovery":
        required=common|{"controller_pod_uid_before","controller_pod_uid_after","tenant_id","timeline_id","tenant_generation","timeline_generation","identity_preserved","service_recovered"}
        valid=set(observation)==required and observation["controller_pod_uid_before"] and observation["controller_pod_uid_after"] and observation["controller_pod_uid_before"]!=observation["controller_pod_uid_after"] and ID.fullmatch(observation["tenant_id"]) and ID.fullmatch(observation["timeline_id"]) and type(observation["tenant_generation"]) is int and observation["tenant_generation"]>0 and type(observation["timeline_generation"]) is int and observation["timeline_generation"]>0 and observation["identity_preserved"] is True and observation["service_recovered"] is True
    elif case == "object-store-outage":
        required=common|{"outage_observed","backup_refused","service_restored"}
        valid=set(observation)==required and all(observation[key] is True for key in ("outage_observed","backup_refused","service_restored"))
    else:
        raise ValueError("Unknown Neon acceptance case")
    if not valid:
        raise ValueError("Neon native observation is malformed for " + case)


def record(args):
    value = state(args.state); stream = load(args.events); observation = load(args.observation)
    if stream.get("run_id") != value["run_id"] or any(item.get("case") == args.case for item in stream.get("events", [])):
        raise ValueError("Neon event stream is stale or duplicate")
    validate_observation(args.case, observation, value)
    if args.case == "partial-create-cleanup":
        PARTIAL["validate_supporting_files"](args.observation, observation)
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
    partial_path = Path(args.events).parent / "partial-create-cleanup.json"
    partial = load(partial_path)
    partial_event = next(item for item in stream["events"] if item["case"] == "partial-create-cleanup")
    if VERIFY["file_hash"](partial_path) != partial_event["evidence_sha256"]:
        raise ValueError("Partial-create cleanup evidence changed after recording")
    validate_observation("partial-create-cleanup", partial, value)
    PARTIAL["validate_supporting_files"](partial_path, partial)
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
