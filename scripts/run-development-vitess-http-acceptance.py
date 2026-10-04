"""Run the Vitess HTTP vertical acceptance on the named development VM."""

import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import runpy
import selectors
import signal
import subprocess
import sys
import time


MAX_LOG_BYTES = 64 * 1024 * 1024
MAX_RUN_SECONDS = 60 * 60
GIB = 1024 ** 3
NODES = tuple(f"k3d-hakopod-vitess-worker-{index}" for index in range(3))


def bounded_output(command, limit=2 * 1024 * 1024, timeout=20):
    process = subprocess.Popen(command, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL,
                               start_new_session=True)
    data = bytearray()
    deadline = time.monotonic() + timeout
    try:
        with selectors.DefaultSelector() as selector:
            selector.register(process.stdout, selectors.EVENT_READ)
            while selector.get_map():
                if time.monotonic() >= deadline:
                    raise RuntimeError("development environment metadata timed out")
                for key, _ in selector.select(timeout=.5):
                    block = os.read(key.fileobj.fileno(), 64 * 1024)
                    if not block:
                        selector.unregister(key.fileobj)
                    elif len(data) + len(block) > limit:
                        raise RuntimeError("development environment metadata exceeded its bound")
                    else:
                        data.extend(block)
        if process.wait(timeout=max(.1, deadline - time.monotonic())):
            raise RuntimeError("development environment metadata command failed")
        return bytes(data)
    finally:
        try:
            os.killpg(process.pid, signal.SIGKILL)
        except ProcessLookupError:
            pass
        if process.poll() is None:
            process.wait(timeout=5)
        process.stdout.close()


def command_json(command):
    return json.loads(bounded_output(command))


def file_hash(path, limit=MAX_LOG_BYTES):
    if path.is_symlink() or not path.is_file() or path.stat().st_size > limit:
        raise RuntimeError(f"bounded regular file required: {path.name}")
    digest = hashlib.sha256()
    with path.open("rb") as source:
        while block := source.read(1024 * 1024):
            digest.update(block)
    return digest.hexdigest()


def harness_inventory(source):
    paths = [source / "scripts/run-development-vitess-http-acceptance.py"]
    paths.extend(sorted((source / "acceptance/vitess").rglob("*")))
    result = {}
    total = 0
    for path in paths:
        if path.is_dir():
            continue
        if path.is_symlink() or not path.is_file():
            raise RuntimeError("HTTP acceptance source must contain regular files only")
        total += path.stat().st_size
        if len(result) >= 32 or total > 2 * 1024 * 1024:
            raise RuntimeError("HTTP acceptance source inventory exceeded its bound")
        result[str(path.relative_to(source))] = file_hash(path, 2 * 1024 * 1024)
    if "acceptance/vitess/http_live_test.go" not in result:
        raise RuntimeError("Vitess HTTP acceptance test is missing")
    return result


def cpu_milli(value):
    match = re.fullmatch(r"(\d+)(n|u|m)?", value or "")
    if not match:
        raise RuntimeError("Kubernetes CPU request is invalid")
    amount, suffix = int(match.group(1)), match.group(2)
    return ((amount + 999999) // 1000000 if suffix == "n" else
            (amount + 999) // 1000 if suffix == "u" else
            amount if suffix == "m" else amount * 1000)


def preflight(kube, receipt_path, receipt_sha256, fixture, root, pool, storage_class):
    if not re.fullmatch(r"[a-f0-9]{64}", receipt_sha256 or ""):
        raise RuntimeError("exact development cluster receipt SHA-256 is required")
    if receipt_path.is_symlink() or not receipt_path.is_file() or receipt_path.stat().st_size > 256 * 1024:
        raise RuntimeError("bounded development cluster receipt is required")
    raw = receipt_path.read_bytes()
    if hashlib.sha256(raw).hexdigest() != receipt_sha256:
        raise RuntimeError("development cluster receipt SHA-256 differs")
    receipt = json.loads(raw)
    if receipt.get("context") != "k3d-hakopod-dev" or set(receipt.get("node_uids", {})) != set(NODES):
        raise RuntimeError("development cluster receipt does not bind the dedicated Vitess nodes")
    namespace = command_json(kube + ["get", "namespace", "kube-system", "-o", "json"])
    nodes = command_json(kube + ["get", "nodes", *NODES, "-o", "json"]).get("items", [])
    observed = {item.get("metadata", {}).get("name"): item.get("metadata", {}).get("uid") for item in nodes}
    if namespace.get("metadata", {}).get("uid") != receipt.get("cluster_uid") or observed != receipt["node_uids"]:
        raise RuntimeError("live development cluster identity differs from its receipt")
    for node in nodes:
        info = node.get("status", {}).get("nodeInfo", {})
        conditions = {item.get("type"): item.get("status") for item in node.get("status", {}).get("conditions", [])}
        if (info.get("architecture") not in ("amd64", "x86_64") or info.get("operatingSystem") != "linux"
                or node.get("spec", {}).get("unschedulable")
                or conditions.get("Ready") != "True"
                or any(conditions.get(kind) != "False" for kind in ("DiskPressure", "MemoryPressure", "PIDPressure"))):
            raise RuntimeError("dedicated Vitess nodes are not healthy Linux amd64 workers")
        if (node.get("metadata", {}).get("labels", {}).get("hakopod.com/pool") != pool
                or node.get("metadata", {}).get("labels", {}).get("hakopod.com.node-restriction.kubernetes.io/default-runtime") != "runsc"):
            raise RuntimeError("dedicated Vitess node placement labels differ from the trusted policy")
    runtime_class = command_json(kube + ["get", "runtimeclass", "runsc", "-o", "json"])
    storage = command_json(kube + ["get", "storageclass", storage_class, "-o", "json"])
    if runtime_class.get("handler") != "runsc" or storage.get("metadata", {}).get("name") != storage_class:
        raise RuntimeError("trusted Vitess runtime or storage class is unavailable")
    databases = command_json(kube + ["get", "namespaces", "-o", "json"]).get("items", [])
    if any(item.get("metadata", {}).get("name", "").startswith("hdb-") for item in databases):
        raise RuntimeError("an existing managed database fixture owns the acceptance lane")
    pods = command_json(kube + ["get", "pods", "--all-namespaces", "-o", "json"]).get("items", [])
    requested = 0
    for pod in pods:
        if pod.get("spec", {}).get("nodeName") not in NODES or pod.get("status", {}).get("phase") in ("Succeeded", "Failed"):
            continue
        regular = sum(cpu_milli(container.get("resources", {}).get("requests", {}).get("cpu", "0"))
                      for container in pod.get("spec", {}).get("containers", []))
        init = max([cpu_milli(container.get("resources", {}).get("requests", {}).get("cpu", "0"))
                    for container in pod.get("spec", {}).get("initContainers", [])] or [0])
        requested += max(regular, init)
    allocatable = sum(cpu_milli(item.get("status", {}).get("allocatable", {}).get("cpu", "")) for item in nodes)
    if allocatable - requested < 17700:
        raise RuntimeError("dedicated Vitess nodes do not have the required 17700m CPU available")
    stats = os.statvfs(root)
    available = stats.f_bavail * stats.f_frsize
    if available < 12 * GIB:
        raise RuntimeError("acceptance scratch has less than the required 12 GiB reserve")
    if fixture.is_symlink() or not fixture.is_file() or fixture.stat().st_mode & 0o077 or fixture.stat().st_size > 64 * 1024:
        raise RuntimeError("protected bounded Vitess fixture configuration is required")
    return {"context": "k3d-hakopod-dev", "cluster_uid": receipt["cluster_uid"],
            "node_uids": observed, "receipt_sha256": receipt_sha256,
            "available_cpu_milli": allocatable - requested,
            "scratch_available_bytes": available}


def run_bounded(command, source, env, log):
    descriptor = os.open(log, os.O_CREAT | os.O_EXCL | os.O_WRONLY, 0o600)
    process = None
    error = ""
    with os.fdopen(descriptor, "wb") as output:
        process = subprocess.Popen(command, cwd=source, env=env, stdout=subprocess.PIPE,
                                   stderr=subprocess.STDOUT, start_new_session=True)
        deadline = time.monotonic() + MAX_RUN_SECONDS
        size = 0
        try:
            with selectors.DefaultSelector() as selector:
                selector.register(process.stdout, selectors.EVENT_READ)
                while selector.get_map():
                    if time.monotonic() >= deadline:
                        error = "timeout"
                        break
                    for key, _ in selector.select(timeout=1):
                        block = os.read(key.fileobj.fileno(), 64 * 1024)
                        if not block:
                            selector.unregister(key.fileobj)
                            continue
                        size += len(block)
                        if size > MAX_LOG_BYTES:
                            error = "log_limit"
                            break
                        output.write(block)
                    if error:
                        break
        finally:
            if error and process.poll() is None:
                os.killpg(process.pid, signal.SIGTERM)
            try:
                process.wait(timeout=10)
            except subprocess.TimeoutExpired:
                os.killpg(process.pid, signal.SIGKILL)
                process.wait(timeout=10)
            tail = process.stdout.read()
            if size + len(tail) <= MAX_LOG_BYTES:
                output.write(tail)
            elif not error:
                error = "log_limit"
            process.stdout.close()
    return process.returncode, error


def test_events(log):
    events = []
    with log.open("rb") as source:
        for raw in source:
            try:
                event = json.loads(raw)
            except (json.JSONDecodeError, UnicodeDecodeError):
                continue
            if event.get("Test") == "TestVitessHTTPVerticalSlice" and event.get("Action") in ("run", "pass", "fail", "skip"):
                events.append({key: event[key] for key in ("Time", "Action", "Test", "Elapsed") if key in event})
    return events


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--root", type=Path, required=True)
    parser.add_argument("--source", type=Path, required=True)
    parser.add_argument("--fixture", type=Path, required=True)
    parser.add_argument("--kubeconfig", type=Path, required=True)
    parser.add_argument("--kubectl", type=Path, required=True)
    parser.add_argument("--go", type=Path, default=Path("/opt/hakopod-build-go/bin/go"))
    parser.add_argument("--cache-root", type=Path, required=True)
    parser.add_argument("--cluster-receipt", type=Path, required=True)
    parser.add_argument("--cluster-receipt-sha256", required=True)
    parser.add_argument("--pool", required=True)
    parser.add_argument("--storage-class", required=True)
    parser.add_argument("--attempt", type=int, required=True)
    args = parser.parse_args()
    root, source = args.root.resolve(), args.source.resolve()
    fixture, kubeconfig = args.fixture.resolve(), args.kubeconfig.resolve()
    go, kubectl, cache = args.go.resolve(), args.kubectl.resolve(), args.cache_root.resolve()
    scratch = Path("/srv/hakopod-backup-scratch")
    if not 1 <= args.attempt <= 100 or scratch not in root.parents or scratch not in source.parents or scratch not in cache.parents:
        raise RuntimeError("acceptance paths or attempt are outside their bounds")
    if not go.is_file() or not os.access(go, os.X_OK) or not kubectl.is_file() or not os.access(kubectl, os.X_OK):
        raise RuntimeError("Go and kubectl executables are required")
    if not os.environ.get("HAKOPOD_TEST_DATABASE_URL"):
        raise RuntimeError("HAKOPOD_TEST_DATABASE_URL is required")
    if not re.fullmatch(r"[a-z0-9]([-a-z0-9.]*[a-z0-9])?", args.pool) or not re.fullmatch(r"[a-z0-9]([-a-z0-9.]*[a-z0-9])?", args.storage_class):
        raise RuntimeError("trusted pool and storage class names are invalid")
    kube = [str(kubectl), "--kubeconfig", str(kubeconfig), "--context", "k3d-hakopod-dev"]
    if bounded_output(kube + ["config", "current-context"]).decode().strip() != "k3d-hakopod-dev":
        raise RuntimeError("Vitess HTTP acceptance requires k3d-hakopod-dev")
    root.mkdir(mode=0o700, parents=True, exist_ok=True)
    cache.mkdir(mode=0o700, parents=True, exist_ok=True)
    environment = preflight(kube, args.cluster_receipt.resolve(), args.cluster_receipt_sha256, fixture, root, args.pool, args.storage_class)
    verifier = runpy.run_path(str(source / "release/verify-vitess-runtime.py"))
    runtime_before = verifier["source_files"](source)
    harness_before = harness_inventory(source)
    stem = f"vitess-http-v{args.attempt}"
    log, evidence = root / f"{stem}.jsonl", root / f"{stem}.evidence.json"
    if log.exists() or evidence.exists():
        raise RuntimeError("HTTP acceptance evidence for this attempt already exists")
    env = os.environ.copy()
    for key in list(env):
        if key.startswith(("GO", "CGO_", "AWS_", "HAKOPOD_")) or key in ("CC", "CXX", "FC", "PKG_CONFIG"):
            env.pop(key)
    env.update(PATH=str(go.parent) + ":" + str(kubectl.parent) + ":/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
               GOENV="off", GOWORK="off", GOFLAGS="-mod=readonly", GOTOOLCHAIN="local", GOMAXPROCS="1",
               GOCACHE=str(cache / "go-cache"), GOMODCACHE=str(cache / "go-mod"),
               HAKOPOD_TEST_DATABASE_URL=os.environ["HAKOPOD_TEST_DATABASE_URL"],
               HAKOPOD_VITESS_HTTP_ACCEPTANCE_TEST="1", HAKOPOD_TEST_KUBECONFIG=str(kubeconfig),
               HAKOPOD_VITESS_NATIVE_FIXTURE_CONFIG=str(fixture),
               HAKOPOD_VITESS_ACCEPTANCE_POOL=args.pool,
               HAKOPOD_VITESS_ACCEPTANCE_STORAGE_CLASS=args.storage_class)
    started = time.time()
    code, limit_error = run_bounded([str(go), "test", "-p", "1", "./acceptance/vitess",
        "-run", "^TestVitessHTTPVerticalSlice$", "-count=1", "-timeout=55m", "-json"], source, env, log)
    runtime_after = verifier["source_files"](source)
    harness_after = harness_inventory(source)
    events = test_events(log)
    actions = [event["Action"] for event in events]
    passed = code == 0 and not limit_error and runtime_before == runtime_after and harness_before == harness_after and "pass" in actions and "fail" not in actions and "skip" not in actions
    report = {"schema_version": 1, "test": "TestVitessHTTPVerticalSlice", "execution": "http_vertical",
              "environment": environment, "runtime_source_files": runtime_before,
              "runtime_source_files_after": runtime_after, "http_harness_source_files": harness_before,
              "http_harness_source_files_after": harness_after, "log_sha256": file_hash(log),
              "exit_code": code, "limit_error": limit_error, "elapsed_seconds": round(time.time() - started, 3),
              "test_events": events, "passed": passed}
    descriptor = os.open(evidence, os.O_CREAT | os.O_EXCL | os.O_WRONLY, 0o600)
    with os.fdopen(descriptor, "w") as output:
        json.dump(report, output, indent=2)
    print("Vitess HTTP vertical acceptance passed:", passed)
    print("Protected event log:", log)
    print("Protected structural evidence:", evidence)
    return 0 if passed else 1


if __name__ == "__main__":
    try:
        sys.exit(main())
    except Exception as error:
        print("Vitess HTTP acceptance setup failed:", type(error).__name__)
        sys.exit(1)
