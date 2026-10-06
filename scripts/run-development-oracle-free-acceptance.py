#!/usr/bin/env python3
"""Run one Oracle case after the caller reserves the native development lane.

This neither installs controllers nor publishes images. The caller provides a
bounded Linux VM unit, the pinned images, and the named development cluster.
"""

import argparse
from decimal import Decimal, ROUND_CEILING
import hashlib
import json
import os
from pathlib import Path
import platform
import re
import runpy

ROOT = Path(__file__).resolve().parents[1]
VERIFIER = runpy.run_path(str(ROOT / "release/verify-oracle-free-runtime.py"))
RUNNER = runpy.run_path(str(ROOT / "scripts/run-development-vitess-acceptance.py"))
NODES = {"k3d-hakopod-dev-server-0", "k3d-hakopod-database-worker-0"}
GIB = 1024 ** 3
NODE_MEMORY_RESERVE = GIB


def memory_bytes(value):
    match = re.fullmatch(r"(\d{1,16}(?:\.\d{1,9})?)([KMGTPE]i|[kKMGTPE]|m)?", str(value))
    if not match:
        raise ValueError("Kubernetes memory quantity is invalid")
    suffix = match[2] or ""
    multipliers = {"": 1, "m": Decimal("0.001"), "k": 1000}
    multipliers.update({unit: 1000 ** index for index, unit in enumerate("KMGTPE", 1)})
    multipliers.update({unit + "i": 1024 ** index for index, unit in enumerate("KMGTPE", 1)})
    return int((Decimal(match[1]) * multipliers[suffix]).to_integral_value(rounding=ROUND_CEILING))


def pod_resource(pod, resource, kind):
    parse = RUNNER["cpu_milli"] if resource == "cpu" else memory_bytes
    spec = pod.get("spec", {})
    def amount(container):
        resources = container.get("resources", {})
        value = resources.get(kind, {}).get(resource)
        if value is None:
            value = resources.get("requests" if kind == "limits" else "limits", {}).get(resource, "0")
        return parse(value)
    regular = sum(amount(container) for container in spec.get("containers", []))
    sidecars, peak = 0, 0
    for container in spec.get("initContainers", []):
        request = amount(container)
        peak = max(peak, sidecars + request)
        if container.get("restartPolicy") == "Always":
            sidecars += request
    total = max(regular + sidecars, peak)
    pod_level = spec.get("resources", {}).get(kind, {}).get(resource)
    if pod_level is not None:
        total = max(total, parse(pod_level))
    return total + parse(spec.get("overhead", {}).get(resource, "0"))


def container_budget(docker, name):
    # Inspect only identity and limits. Docker's full response includes tokens.
    fields = {"id": ".Id", "name": ".Name", "running": ".State.Running", "pid": ".State.Pid",
              "memory": ".HostConfig.Memory", "swap": ".HostConfig.MemorySwap",
              "nano_cpus": ".HostConfig.NanoCpus", "quota": ".HostConfig.CpuQuota", "period": ".HostConfig.CpuPeriod"}
    template = "{" + ",".join(json.dumps(key) + ":{{json " + value + "}}" for key, value in fields.items()) + "}"
    value = RUNNER["command_json"]([str(docker), "inspect", "--format", template, name])
    if (value.get("name") != "/" + name or value.get("running") is not True
            or not re.fullmatch(r"[a-f0-9]{64}", value.get("id", ""))
            or any(type(value.get(key)) is not int for key in ("pid", "memory", "swap", "nano_cpus", "quota", "period"))
            or value["pid"] <= 0 or value["memory"] <= 0 or value["swap"] != value["memory"]):
        raise ValueError("Oracle node container requires a bounded, running identity with no swap")
    cpu = []
    if value["nano_cpus"] > 0:
        cpu.append(value["nano_cpus"] // 1000000)
    if value["quota"] > 0 and value["period"] > 0:
        cpu.append(value["quota"] * 1000 // value["period"])
    groups = Path("/proc", str(value["pid"]), "cgroup").read_text().splitlines()
    if len(groups) != 1 or not groups[0].startswith("0::/") or ".." in groups[0].split("/"):
        raise ValueError("Oracle node requires a unified bounded cgroup")
    group = Path("/sys/fs/cgroup") / groups[0][4:]
    roots = [path for path in (group, *group.parents) if path.name == "docker-" + value["id"] + ".scope"
             or path.name == value["id"] and path.parent.name == "docker"]
    if len(roots) != 1:
        raise ValueError("Oracle node cgroup does not match its Docker container")
    group = roots[0]
    memory, headroom, effective_cpu = [], [], []
    current = group.joinpath("memory.current").read_text().strip()
    if not current.isdecimal() or group.joinpath("memory.swap.max").read_text().strip() != "0":
        raise ValueError("Oracle node cgroup memory or swap boundary is invalid")
    # k3d's init process lives in a child cgroup. Its unbounded /init values do
    # not describe the node. Read the whole Docker scope and its ancestors.
    for path in (group, *group.parents):
        if path == Path("/sys/fs/cgroup"):
            break
        limit = path.joinpath("memory.max").read_text().strip()
        usage = path.joinpath("memory.current").read_text().strip()
        quota, period = path.joinpath("cpu.max").read_text().split()
        if (limit != "max" and not limit.isdecimal() or not usage.isdecimal()
                or quota != "max" and not quota.isdecimal() or not period.isdecimal() or int(period) <= 0):
            raise ValueError("Oracle node cgroup limit is malformed")
        if limit != "max":
            memory.append(int(limit))
            headroom.append(max(0, int(limit) - int(usage)))
        if quota != "max":
            effective_cpu.append(int(quota) * 1000 // int(period))
    if not memory or not effective_cpu:
        raise ValueError("Oracle node cgroup is not explicitly bounded")
    cpu.extend(effective_cpu)
    return {"container_id": value["id"], "cpu_milli": min(cpu),
            "memory_bytes": min(value["memory"], *memory), "memory_current_bytes": int(current),
            "ancestor_memory_headroom_bytes": min(headroom)}


def node_capacity(node, pods, container):
    allocatable = node["status"]["allocatable"]
    active = [pod for pod in pods if pod.get("spec", {}).get("nodeName") == node["metadata"]["name"]
              and pod.get("status", {}).get("phase") not in ("Succeeded", "Failed")]
    requests = {kind: sum(pod_resource(pod, kind, "requests") for pod in active) for kind in ("cpu", "memory")}
    limits = {kind: sum(max(pod_resource(pod, kind, "requests"), pod_resource(pod, kind, "limits")) for pod in active) for kind in ("cpu", "memory")}
    cpu = min(RUNNER["cpu_milli"](allocatable["cpu"]), container["cpu_milli"])
    memory = min(memory_bytes(allocatable["memory"]), container["memory_bytes"])
    return {**container, "allocatable_cpu_milli": cpu, "allocatable_memory_bytes": memory,
            "requested_cpu_milli": requests["cpu"], "requested_memory_bytes": requests["memory"],
            "reserved_cpu_milli": limits["cpu"], "reserved_memory_bytes": limits["memory"],
            "available_cpu_milli": max(0, cpu - limits["cpu"]),
            "available_memory_bytes": max(0, min(memory - max(limits["memory"], container["memory_current_bytes"]),
                container.get("ancestor_memory_headroom_bytes", memory)) - NODE_MEMORY_RESERVE)}


def feasible_placement(capacity, case):
    # live_database_oracle_test.go uses 1 CPU/4Gi databases. Each also needs its
    # 100m/256Mi operator. Recovery binds two services on server-0; reserve all
    # four old/new Pods at their 300m/512Mi limits during rolling replacement.
    count = 2 if case in ("recovery", "http-api") else 1
    workloads = [(1000, 4 * GIB, None)] * count + [(100, 256 * 1024 ** 2, None)] * count
    if case in ("recovery", "http-api"):
        workloads = [(300, 512 * 1024 ** 2, "k3d-hakopod-dev-server-0")] * 4 + workloads
    available = {name: [item["available_cpu_milli"], item["available_memory_bytes"]] for name, item in capacity.items()}
    def place(index):
        if index == len(workloads):
            return True
        cpu, memory, pinned = workloads[index]
        for name, remaining in available.items():
            if (pinned is None or pinned == name) and remaining[0] >= cpu and remaining[1] >= memory:
                remaining[0] -= cpu
                remaining[1] -= memory
                if place(index + 1):
                    return True
                remaining[0] += cpu
                remaining[1] += memory
        return False
    if not place(0):
        raise ValueError("Oracle acceptance lacks per-node CPU or memory capacity for its fixed fixture placement")


def source_hash(sources):
    return hashlib.sha256(json.dumps(sources, sort_keys=True, separators=(",", ":")).encode()).hexdigest()


def metadata(kube, arguments):
    return RUNNER["command_json"]([*kube, *arguments, "-o", "json"])


def environment(kube, nodes, images, crd, case, scratch, docker, evidence_name="capacity-before.json"):
    import yaml
    if not nodes or len(nodes) != len(set(nodes)) or not set(nodes) <= NODES:
        raise ValueError("Oracle acceptance requires distinct dedicated development nodes")
    if RUNNER["command_output"]([*kube, "config", "current-context"]).strip() != b"k3d-hakopod-dev":
        raise ValueError("Oracle acceptance requires k3d-hakopod-dev")
    system = metadata(kube, ["get", "namespace", "kube-system"])
    installed = metadata(kube, ["get", "crd", "singleinstancedatabases.database.oracle.com"])
    if VERIFIER["file_hash"](crd) != VERIFIER["SIDB_SHA256"] or installed.get("spec") != yaml.safe_load(crd.read_bytes())["spec"]:
        raise ValueError("Oracle acceptance requires the exact pinned v4 SIDB definition")
    pods = metadata(kube, ["get", "pods", "-A", "--chunk-size=0"])
    if not isinstance(pods.get("items"), list) or len(pods["items"]) > 1024 or pods.get("metadata", {}).get("continue"):
        raise ValueError("Oracle pod inventory exceeds its bound")
    node_uids, capacity = {}, {}
    for name in nodes:
        node = metadata(kube, ["get", "node", name])
        status = node.get("status", {})
        info = status.get("nodeInfo", {})
        conditions = {item["type"]: item["status"] for item in status.get("conditions", [])}
        if info.get("architecture") != "amd64" or info.get("operatingSystem") != "linux" or node.get("spec", {}).get("unschedulable") or any(conditions.get(key) != value for key, value in {"Ready": "True", "MemoryPressure": "False", "DiskPressure": "False", "PIDPressure": "False"}.items()):
            raise ValueError("Oracle acceptance node is not healthy Linux amd64")
        if any(taint.get("effect") in ("NoSchedule", "NoExecute") for taint in node.get("spec", {}).get("taints", [])):
            raise ValueError("Oracle acceptance does not tolerate reserved node taints")
        cached = {value for item in status.get("images", []) for value in item.get("names", [])}
        if any(RUNNER["canonical_image"](reference) not in cached for reference in images.values()):
            raise ValueError("Oracle acceptance images must be cached by digest on each selected node")
        capacity[name] = node_capacity(node, pods["items"], container_budget(docker, name))
        node_uids[name] = node["metadata"]["uid"]
    count = 2 if case in ("recovery", "http-api") else 1
    disk = os.statvfs(scratch)
    (scratch / evidence_name).write_text(json.dumps({"case": case, "node_memory_reserve_bytes": NODE_MEMORY_RESERVE,
        "required_free_disk_bytes": (12 + 20 * count) * GIB, "available_disk_bytes": disk.f_bavail * disk.f_frsize,
        "nodes": capacity}, sort_keys=True, indent=2) + "\n")
    if disk.f_bavail * disk.f_frsize < (12 + 20 * count) * GIB:
        raise ValueError("Oracle acceptance lacks its disk reserve")
    feasible_placement(capacity, case)
    return {"cluster_uid": system["metadata"]["uid"], "sidb_crd_uid": installed["metadata"]["uid"], "node_uids": node_uids}, capacity


def fixture_names(log):
    names = set()
    with log.open("rb") as source:
        while line := source.readline(2 * 1024 * 1024 + 1):
            if len(line) > 2 * 1024 * 1024:
                raise ValueError("Oracle test output line exceeded its bound")
            event = json.loads(line)
            if event.get("Package") == "github.com/hakopod/hakopod/internal/cluster":
                names.update(re.findall(r"Development Oracle namespace (hdb-[a-f0-9]{32})\b", event.get("Output", "")))
    return sorted(names)


def cleanup(kube, namespaces, case):
    if len(namespaces) != (2 if case == "recovery" else 1):
        raise ValueError("Oracle fixture inventory is incomplete")
    for name in namespaces:
        value = RUNNER["command_output"]([*kube, "get", "namespace", name, "--ignore-not-found", "-o", "json"])
        if value.strip():
            raise ValueError("Oracle native fixture namespace remains")
    volumes = metadata(kube, ["get", "persistentvolumes", "--chunk-size=0"])
    if not isinstance(volumes.get("items"), list) or len(volumes["items"]) > 1024 or volumes.get("metadata", {}).get("continue"):
        raise ValueError("Oracle native volume inventory exceeds its bound")
    if any(volume.get("spec", {}).get("claimRef", {}).get("namespace") in namespaces for volume in volumes["items"]):
        raise ValueError("Oracle native fixture volume remains")
    return {"namespaces": namespaces, "namespaces_absent": True, "persistent_volumes_absent": True}


def run(args):
    if platform.system() != "Linux" or platform.machine() != "x86_64":
        raise ValueError("Oracle acceptance requires the approved Linux amd64 VM")
    args.output.mkdir(mode=0o700, parents=True, exist_ok=False)
    sources, images = VERIFIER["source_files"](ROOT), VERIFIER["source_images"](ROOT)
    kube = [str(args.kubectl), "--kubeconfig", str(args.kubeconfig), "--context", "k3d-hakopod-dev"]
    identity, capacity = environment(kube, args.nodes.split(","), images, args.sidb_crd, args.case, args.output, args.docker)
    env = {key: value for key, value in os.environ.items() if not key.startswith(("GO", "CGO_", "HAKOPOD_", "AWS_"))}
    env.update(PATH=str(args.go.parent) + ":" + str(args.kubectl.parent) + ":/usr/local/bin:/usr/bin:/bin",
               GOMAXPROCS="2", GOENV="off", GOWORK="off", GOFLAGS="-mod=readonly -p=1", GOTOOLCHAIN="local",
               CGO_ENABLED="0", GOOS="linux", GOARCH="amd64", GOCACHE=str(args.cache / "go-build"),
               GOMODCACHE=str(args.cache / "go-mod"), GOTMPDIR=str(args.cache / "go-tmp"),
               HAKOPOD_ORACLE_TEST="1", HAKOPOD_KEEP_DATABASE_FIXTURES="1",
               HAKOPOD_TEST_KUBECONFIG=str(args.kubeconfig), HAKOPOD_DATABASE_FIXTURE_NODES=args.nodes)
    for key in ("GOCACHE", "GOMODCACHE", "GOTMPDIR"):
        Path(env[key]).mkdir(parents=True, exist_ok=True)
    log = args.output / "native-test.jsonl"
    test = VERIFIER["TESTS"][args.case]
    code, limit = RUNNER["run_bounded"]([str(args.go), "test", "-json", "-count=1", "./internal/cluster", "-run", "^" + test + "$", "-timeout=85m"], ROOT, env, log)
    events, valid = RUNNER["structural_events"](log)
    after = VERIFIER["source_files"](ROOT)
    if code != 0 or limit or not valid or after != sources:
        raise ValueError("Oracle native acceptance failed, exceeded its bound or changed source; evidence is retained")
    VERIFIER["accepted_events"](events, {test})
    after_identity, after_capacity = environment(kube, args.nodes.split(","), images, args.sidb_crd, args.case, args.output, args.docker, "capacity-after.json")
    if after_identity != identity or {name: item["container_id"] for name, item in after_capacity.items()} != {name: item["container_id"] for name, item in capacity.items()}:
        raise ValueError("Oracle development cluster identity changed during native acceptance")
    attempt = {"case": args.case, "exit_code": code, "source_manifest_sha256": source_hash(sources),
               "source_manifest_after_sha256": source_hash(after), "log_sha256": VERIFIER["file_hash"](log),
               "test_events": events, **identity, "cleanup": cleanup(kube, fixture_names(log), args.case)}
    report = {"schema_version": 1, "context": "k3d-hakopod-dev", "execution": "native", "platform": "linux/amd64",
              "source_files": sources, "images": images, "attempt": attempt}
    (args.output / "report.json").write_text(json.dumps(report, sort_keys=True, indent=2) + "\n")
    print(json.dumps({"case": args.case, "status": "passed", "cleanup_verified": True}))


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--case", choices=VERIFIER["TESTS"], required=True)
    parser.add_argument("--kubeconfig", type=Path, required=True)
    parser.add_argument("--kubectl", type=Path, required=True)
    parser.add_argument("--go", type=Path, required=True)
    parser.add_argument("--docker", type=Path, required=True)
    parser.add_argument("--cache", type=Path, required=True)
    parser.add_argument("--sidb-crd", type=Path, required=True)
    parser.add_argument("--nodes", required=True)
    parser.add_argument("--output", type=Path, required=True)
    try:
        run(parser.parse_args())
    except (ValueError, RuntimeError, OSError) as error:
        raise SystemExit("Oracle acceptance stopped: " + str(error)) from None
