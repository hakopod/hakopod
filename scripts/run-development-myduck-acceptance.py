#!/usr/bin/env python3
"""Run one MyDuck case in the reserved, named development-cluster lane."""

import argparse
import json
import os
from pathlib import Path
import platform
import re
import runpy
import stat

ROOT = Path(__file__).resolve().parents[1]
VERIFIER = runpy.run_path(str(ROOT / "release/verify-myduck-runtime.py"))
RUNNER = runpy.run_path(str(ROOT / "scripts/run-development-vitess-acceptance.py"))
CAPACITY = runpy.run_path(str(ROOT / "scripts/run-development-oracle-free-acceptance.py"))
NODES = {"k3d-hakopod-dev-server-0", "k3d-hakopod-database-worker-0", "k3d-hakopod-database-worker-1"}
GIB = 1024 ** 3


def metadata(kube, args):
    return RUNNER["command_json"]([*kube, *args, "-o", "json"])


def inventory(kube, docker):
    namespaces = metadata(kube, ["get", "namespaces", "--chunk-size=0"])
    volumes = metadata(kube, ["get", "persistentvolumes", "--chunk-size=0"])
    for value in (namespaces, volumes):
        if not isinstance(value.get("items"), list) or len(value["items"]) > 1024 or value.get("metadata", {}).get("continue"):
            raise ValueError("MyDuck preserved-resource inventory exceeds its bound")
    containers = RUNNER["command_output"]([str(docker), "ps", "--all", "--no-trunc", "--filter", "label=com.hakopod.test=backups", "--format", "{{.ID}}"])
    ids = containers.decode().splitlines()
    if len(ids) > 128 or any(not re.fullmatch(r"[a-f0-9]{64}", value) for value in ids):
        raise ValueError("MyDuck S3 fixture inventory exceeds its bound")
    return {
        "namespaces": {item["metadata"]["name"]: {"uid": item["metadata"]["uid"], "deletion_timestamp": item["metadata"].get("deletionTimestamp")} for item in namespaces["items"]},
        "persistent_volumes": {item["metadata"]["name"]: {"uid": item["metadata"]["uid"], "claim": item.get("spec", {}).get("claimRef"), "reclaim_policy": item.get("spec", {}).get("persistentVolumeReclaimPolicy"), "storage_class": item.get("spec", {}).get("storageClassName"), "deletion_timestamp": item["metadata"].get("deletionTimestamp")} for item in volumes["items"]},
        "s3_containers": sorted(ids),
    }


def environment(kube, nodes, images, case, output, docker, name="capacity-before.json"):
    if not nodes or len(nodes) != len(set(nodes)) or not set(nodes) <= NODES:
        raise ValueError("MyDuck requires distinct named development nodes")
    if RUNNER["command_output"]([*kube, "config", "current-context"]).strip() != b"k3d-hakopod-dev":
        raise ValueError("MyDuck acceptance requires k3d-hakopod-dev")
    system = metadata(kube, ["get", "namespace", "kube-system"])
    pods = metadata(kube, ["get", "pods", "-A", "--chunk-size=0"])
    if not isinstance(pods.get("items"), list) or len(pods["items"]) > 1024 or pods.get("metadata", {}).get("continue"):
        raise ValueError("MyDuck pod inventory exceeds its bound")
    identities, capacity = {}, {}
    for node_name in nodes:
        node = metadata(kube, ["get", "node", node_name])
        status = node.get("status", {})
        conditions = {item["type"]: item["status"] for item in status.get("conditions", [])}
        info = status.get("nodeInfo", {})
        if info.get("architecture") != "amd64" or info.get("operatingSystem") != "linux" or node.get("spec", {}).get("unschedulable") or any(conditions.get(key) != value for key, value in {"Ready": "True", "MemoryPressure": "False", "DiskPressure": "False", "PIDPressure": "False"}.items()):
            raise ValueError("MyDuck native node is not healthy Linux amd64")
        if any(taint.get("effect") in ("NoSchedule", "NoExecute") for taint in node.get("spec", {}).get("taints", [])):
            raise ValueError("MyDuck native fixture does not tolerate reserved node taints")
        cached = {value for image in status.get("images", []) for value in image.get("names", [])}
        if any(reference not in cached for reference in images.values()):
            raise ValueError("MyDuck image must be cached by digest on each selected node")
        capacity[node_name] = CAPACITY["node_capacity"](node, pods["items"], CAPACITY["container_budget"](docker, node_name))
        identities[node_name] = node["metadata"]["uid"]
    count = VERIFIER["FIXTURE_COUNTS"][case]
    # Each cluster fixture is 1 CPU / 1GiB. Its stopped-process storage helper
    # uses less. HTTP fixtures are smaller; reserve the same upper bound.
    remaining = {name: [item["available_cpu_milli"], item["available_memory_bytes"]] for name, item in capacity.items()}
    for _ in range(count):
        candidates = [value for value in remaining.values() if value[0] >= 1000 and value[1] >= GIB]
        if not candidates:
            raise ValueError("MyDuck native fixture lacks per-node CPU or memory headroom")
        selected = max(candidates, key=lambda value: value[1])
        selected[0] -= 1000
        selected[1] -= GIB
    disk = os.statvfs(output)
    free = disk.f_bavail * disk.f_frsize
    if free < (8 + 2 * count) * GIB:
        raise ValueError("MyDuck native fixture lacks its disk reserve")
    (output / name).write_text(json.dumps({"case": case, "available_disk_bytes": free, "nodes": capacity}, sort_keys=True, indent=2) + "\n")
    return {"cluster_uid": system["metadata"]["uid"], "node_uids": identities}, capacity


def fixture_names(log):
    names = set()
    with log.open("rb") as source:
        while line := source.readline(2 * 1024 * 1024 + 1):
            if len(line) > 2 * 1024 * 1024:
                raise ValueError("MyDuck test output exceeded its line bound")
            event = json.loads(line)
            if event.get("Package") in ("github.com/hakopod/hakopod/internal/cluster", "github.com/hakopod/hakopod/internal/api"):
                names.update(re.findall(r"Development MyDuck namespace (hdb-[a-f0-9]{32})\b", event.get("Output", "")))
    return sorted(names)


def cleanup(kube, names, case):
    if len(names) != VERIFIER["FIXTURE_COUNTS"][case]:
        raise ValueError("MyDuck fixture inventory is incomplete")
    for name in names:
        if RUNNER["command_output"]([*kube, "get", "namespace", name, "--ignore-not-found", "-o", "json"]).strip():
            raise ValueError("MyDuck fixture namespace remains")
    volumes = metadata(kube, ["get", "persistentvolumes", "--chunk-size=0"])
    if not isinstance(volumes.get("items"), list) or len(volumes["items"]) > 1024 or volumes.get("metadata", {}).get("continue"):
        raise ValueError("MyDuck volume inventory exceeds its bound")
    if any(volume.get("spec", {}).get("claimRef", {}).get("namespace") in names for volume in volumes["items"]):
        raise ValueError("MyDuck fixture volume remains")
    return {"namespaces": names, "namespaces_absent": True, "persistent_volumes_absent": True}


def protected_dsn(path):
    if path is None or not path.is_absolute() or any(item.is_symlink() for item in (path, *path.parents)):
        raise ValueError("MyDuck HTTP acceptance requires a protected PostgreSQL DSN file")
    status = path.stat()
    if not stat.S_ISREG(status.st_mode) or stat.S_IMODE(status.st_mode) & 0o077 or status.st_uid != os.getuid() or not 1 <= status.st_size <= 8192:
        raise ValueError("MyDuck PostgreSQL fixture credentials have invalid bounds or permissions")
    value = path.read_text().strip()
    if not value.startswith(("postgres://", "postgresql://")) or "\n" in value:
        raise ValueError("MyDuck PostgreSQL fixture DSN is invalid")
    return value


def run(args):
    if platform.system() != "Linux" or platform.machine() != "x86_64":
        raise ValueError("MyDuck native acceptance requires the approved Linux amd64 VM")
    kube = CAPACITY["kubectl_command"](args.kubectl, args.kubeconfig, args.cache)
    args.output.mkdir(mode=0o700, parents=True, exist_ok=False)
    sources, images = VERIFIER["source_files"](ROOT), VERIFIER["source_images"](ROOT)
    identity, capacity = environment(kube, args.nodes.split(","), images, args.case, args.output, args.docker)
    before_inventory = inventory(kube, args.docker)
    (args.output / "inventory-before.json").write_text(json.dumps(before_inventory, sort_keys=True, indent=2) + "\n")
    env = {key: value for key, value in os.environ.items() if not key.startswith(("GO", "CGO_", "HAKOPOD_", "AWS_"))}
    env.update(PATH=str(args.go.parent) + ":" + str(args.kubectl.parent) + ":" + str(args.docker.parent) + ":/usr/local/bin:/usr/bin:/bin",
               GOMAXPROCS="2", GOENV="off", GOWORK="off", GOFLAGS="-mod=readonly -p=1", GOTOOLCHAIN="local",
               CGO_ENABLED="0", GOOS="linux", GOARCH="amd64", GOCACHE=str(args.cache / "go-build"),
               GOMODCACHE=str(args.cache / "go-mod"), GOTMPDIR=str(args.cache / "go-tmp"),
               HAKOPOD_DATABASE_MYDUCK_TEST="1", HAKOPOD_DATABASE_RECOVERY_TEST="1", HAKOPOD_KEEP_DATABASE_FIXTURES="1",
               HAKOPOD_TEST_KUBECONFIG=str(args.kubeconfig), HAKOPOD_DATABASE_FIXTURE_NODES=args.nodes)
    env["TMPDIR"] = str(args.cache / "test-tmp")
    if args.case == "http-api":
        env["HAKOPOD_MYDUCK_API_RECOVERY_TEST"] = "1"
        env["HAKOPOD_TEST_DATABASE_URL"] = protected_dsn(args.database_dsn_file)
    for key in ("GOCACHE", "GOMODCACHE", "GOTMPDIR", "TMPDIR"):
        Path(env[key]).mkdir(parents=True, exist_ok=True)
    test = VERIFIER["TESTS"][args.case]
    package = "github.com/hakopod/hakopod/internal/" + ("api" if args.case == "http-api" else "cluster")
    log = args.output / "native-test.jsonl"
    code, limit = RUNNER["run_bounded"]([str(args.go), "test", "-json", "-count=1", package, "-run", "^" + test + "$", "-timeout=35m"], ROOT, env, log)
    events, valid = RUNNER["structural_events"](log)
    after = VERIFIER["source_files"](ROOT)
    if code != 0 or limit or not valid or after != sources:
        raise ValueError("MyDuck native acceptance failed, exceeded its bound or changed source; evidence retained")
    required = {test}
    if args.case == "http-api":
        required.update(test + "/" + phase for phase in VERIFIER["HTTP_PHASES"])
    if args.case == "recovery":
        required.update(test + "/" + phase for phase in VERIFIER["RECOVERY_PHASES"])
    if args.case == "lifecycle":
        required.update(test + "/" + phase for phase in VERIFIER["LIFECYCLE_PHASES"])
    VERIFIER["accepted_events"](events, required, package)
    after_identity, after_capacity = environment(kube, args.nodes.split(","), images, args.case, args.output, args.docker, "capacity-after.json")
    if after_identity != identity or {name: item["container_id"] for name, item in after_capacity.items()} != {name: item["container_id"] for name, item in capacity.items()}:
        raise ValueError("MyDuck development cluster identity changed during acceptance")
    cleaned = cleanup(kube, fixture_names(log), args.case)
    after_inventory = inventory(kube, args.docker)
    (args.output / "inventory-after.json").write_text(json.dumps(after_inventory, sort_keys=True, indent=2) + "\n")
    if before_inventory != after_inventory:
        raise ValueError("MyDuck acceptance changed unrelated resources or left a host S3 fixture")
    preserved = {"before_sha256": VERIFIER["file_hash"](args.output / "inventory-before.json"),
                 "after_sha256": VERIFIER["file_hash"](args.output / "inventory-after.json"),
                 "namespace_count": len(before_inventory["namespaces"]),
                 "persistent_volume_count": len(before_inventory["persistent_volumes"]),
                 "s3_container_count": len(before_inventory["s3_containers"])}
    attempt = {"case": args.case, "exit_code": code, "source_manifest_sha256": VERIFIER["source_hash"](sources),
               "source_manifest_after_sha256": VERIFIER["source_hash"](after), "log_sha256": VERIFIER["file_hash"](log),
               "test_events": events, **identity, "cleanup": cleaned, "preserved_resources": preserved}
    report = {"schema_version": 1, "context": "k3d-hakopod-dev", "execution": "native", "platform": "linux/amd64",
              "source_files": sources, "images": images, "attempt": attempt}
    (args.output / "report.json").write_text(json.dumps(report, sort_keys=True, indent=2) + "\n")
    print(json.dumps({"case": args.case, "status": "passed", "cleanup_verified": True}))


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--case", choices=VERIFIER["TESTS"], required=True)
    for name in ("kubeconfig", "kubectl", "go", "docker", "cache", "output"):
        parser.add_argument("--" + name, type=Path, required=True)
    parser.add_argument("--database-dsn-file", type=Path)
    parser.add_argument("--nodes", required=True)
    try:
        run(parser.parse_args())
    except (ValueError, RuntimeError, OSError) as error:
        raise SystemExit("MyDuck acceptance stopped: " + str(error)) from None
