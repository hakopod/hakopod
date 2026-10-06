#!/usr/bin/env python3
"""Run the Oracle Free HTTP/API vertical after the caller reserves the native development lane.

This neither installs controllers nor publishes images. The caller provides a
bounded Linux VM unit, the pinned images, and the named development cluster.
"""

import argparse
import hashlib
import json
import os
from pathlib import Path
import platform
import re
import runpy
import selectors
import signal
import subprocess
import sys
import time

ROOT = Path(__file__).resolve().parents[1]
VERIFIER = runpy.run_path(str(ROOT / "release/verify-oracle-free-runtime.py"))
RUNNER = runpy.run_path(str(ROOT / "scripts/run-development-vitess-acceptance.py"))
NATIVE = runpy.run_path(str(ROOT / "scripts/run-development-oracle-free-acceptance.py"))
GIB = 1024 ** 3
HOST_RESERVE = 2 * GIB
GO_MEMORY_LIMIT = 6 * GIB
PG_MEMORY_LIMIT = GIB
S3_MEMORY_LIMIT = 384 * 1024 ** 2
ORACLE_PENDING_MEMORY = 10 * GIB + 512 * 1024 ** 2
MAX_TEST_SECONDS = 110 * 60
GROUP_TERM_SECONDS = 10
GROUP_KILL_SECONDS = 10


class ProcessDrainError(RuntimeError):
    pass


def source_hash(sources):
    return hashlib.sha256(json.dumps(sources, sort_keys=True, separators=(",", ":")).encode()).hexdigest()


def metadata(kube, arguments):
    return RUNNER["command_json"]([*kube, *arguments, "-o", "json"])


def runner_limits():
    entries = Path("/proc/self/cgroup").read_text().splitlines()
    if len(entries) != 1 or not entries[0].startswith("0::/") or ".." in entries[0].split("/"):
        raise ValueError("Oracle HTTP runner requires a bounded unified cgroup")
    scope = Path("/sys/fs/cgroup") / entries[0][4:]
    memory, cpu, tasks, swap = [], [], [], []
    for path in (scope, *scope.parents):
        if path == Path("/sys/fs/cgroup"):
            break
        for filename, limits in (("memory.max", memory), ("pids.max", tasks), ("memory.swap.max", swap)):
            value = (path / filename).read_text().strip()
            if value != "max":
                if not value.isdecimal():
                    raise ValueError("Oracle HTTP runner cgroup limit is malformed")
                limits.append(int(value))
        quota, period = (path / "cpu.max").read_text().split()
        if not period.isdecimal() or int(period) <= 0 or quota != "max" and not quota.isdecimal():
            raise ValueError("Oracle HTTP runner CPU boundary is malformed")
        if quota != "max":
            cpu.append(int(quota) * 1000 // int(period))
    if (not memory or not 0 < min(memory) <= GO_MEMORY_LIMIT or not cpu or not 0 < min(cpu) <= 2000
            or not tasks or not 0 < min(tasks) <= 512 or not swap or min(swap) != 0):
        raise ValueError("Oracle HTTP runner requires at most 2 CPUs, 6GiB, 512 tasks and no swap")
    return {"memory_bytes": min(memory), "cpu_milli": min(cpu), "tasks": min(tasks), "swap_bytes": 0}


def host_capacity(kube, limits, docker):
    memory = {}
    for line in Path("/proc/meminfo").read_text().splitlines():
        parts = line.split()
        if parts[0] in ("MemAvailable:", "MemTotal:") and len(parts) == 3 and parts[1].isdecimal() and parts[2] == "kB":
            memory[parts[0][:-1]] = int(parts[1]) * 1024
    pods = metadata(kube, ["get", "pods", "-A", "--chunk-size=0"])
    if not isinstance(pods.get("items"), list) or len(pods["items"]) > 1024 or pods.get("metadata", {}).get("continue"):
        raise ValueError("Oracle HTTP host pod inventory exceeds its bound")
    active = [pod for pod in pods["items"] if pod.get("status", {}).get("phase") not in ("Succeeded", "Failed")]
    reserved = {kind: sum(max(NATIVE["pod_resource"](pod, kind, "requests"), NATIVE["pod_resource"](pod, kind, "limits"))
        for pod in active) for kind in ("cpu", "memory")}
    nodes = metadata(kube, ["get", "nodes", "--chunk-size=0"])
    if not isinstance(nodes.get("items"), list) or not 1 <= len(nodes["items"]) <= 32 or nodes.get("metadata", {}).get("continue"):
        raise ValueError("Oracle HTTP host node inventory exceeds its bound")
    node_names = {item["metadata"]["name"] for item in nodes["items"]}
    identifiers = RUNNER["command_output"]([str(docker), "ps", "-q", "--no-trunc"]).decode().split()
    if len(identifiers) > 256 or len(set(identifiers)) != len(identifiers) or any(not re.fullmatch(r"[a-f0-9]{64}", item) for item in identifiers):
        raise ValueError("Oracle HTTP host container inventory exceeds its bound")
    foreign_cpu, foreign_memory = 0, 0
    if identifiers:
        fields = {"id": ".Id", "name": ".Name", "memory": ".HostConfig.Memory", "running": ".State.Running",
                  "nano_cpus": ".HostConfig.NanoCpus", "quota": ".HostConfig.CpuQuota", "period": ".HostConfig.CpuPeriod"}
        template = "{" + ",".join(json.dumps(key) + ":{{json " + value + "}}" for key, value in fields.items()) + "}"
        values = [json.loads(line) for line in RUNNER["command_output"]([str(docker), "inspect", "--format", template, *identifiers]).splitlines()]
        if len(values) != len(identifiers) or {item.get("id") for item in values} != set(identifiers):
            raise ValueError("Oracle HTTP host container identities changed")
        for item in values:
            if item.get("running") is not True or not isinstance(item.get("name"), str):
                raise ValueError("Oracle HTTP host container state changed")
            if item["name"].removeprefix("/") in node_names:
                continue
            if any(type(item.get(key)) is not int for key in ("memory", "nano_cpus", "quota", "period")):
                raise ValueError("Oracle HTTP host container limits are malformed")
            cpu = []
            if item["nano_cpus"] > 0:
                cpu.append(item["nano_cpus"] // 1000000)
            if item["quota"] > 0 and item["period"] > 0:
                cpu.append(item["quota"] * 1000 // item["period"])
            if not cpu or item["memory"] <= 0:
                raise ValueError("Oracle HTTP requires bounded non-cluster host containers")
            foreign_cpu += min(cpu)
            foreign_memory += item["memory"]
    required = (limits["memory_bytes"] + PG_MEMORY_LIMIT + S3_MEMORY_LIMIT + ORACLE_PENDING_MEMORY
                + HOST_RESERVE + reserved["memory"] + foreign_memory)
    if memory.get("MemAvailable", 0) < required:
        raise ValueError("Oracle HTTP fixtures lack host memory headroom")
    # Two database instances, their operators and four old/new application pods
    # need 3400m. The runner and two host fixtures have separate CPU envelopes.
    required_cpu = reserved["cpu"] + foreign_cpu + 3400 + limits["cpu_milli"] + 2000 + 1000
    available_cpu = len(os.sched_getaffinity(0)) * 1000
    if available_cpu < required_cpu:
        raise ValueError("Oracle HTTP fixtures lack host CPU headroom")
    return {"available_memory_bytes": memory["MemAvailable"], "required_memory_bytes": required,
            "host_cpu_milli": available_cpu, "required_cpu_milli": required_cpu,
            "existing_pod_reserved_cpu_milli": reserved["cpu"], "noncluster_container_cpu_milli": foreign_cpu,
            "noncluster_container_memory_bytes": foreign_memory, "runner": limits}


def stop_group(process):
    """Drain the owned session even when its original leader exits first."""
    for signum, seconds in ((signal.SIGTERM, GROUP_TERM_SECONDS), (signal.SIGKILL, GROUP_KILL_SECONDS)):
        try:
            os.killpg(process.pid, signum)
        except ProcessLookupError:
            process.wait(timeout=2)
            return
        deadline = time.monotonic() + seconds
        while time.monotonic() < deadline:
            process.poll()
            try:
                os.killpg(process.pid, 0)
            except ProcessLookupError:
                process.wait(timeout=2)
                return
            time.sleep(0.05)
    raise ProcessDrainError("Oracle HTTP test process group did not drain; host fixtures are retained")


def run_test(command, env, log):
    with log.open("xb") as output:
        process = subprocess.Popen(command, cwd=ROOT, env=env, stdout=subprocess.PIPE,
                                   stderr=subprocess.STDOUT, start_new_session=True)
        size, deadline = 0, time.monotonic() + MAX_TEST_SECONDS
        try:
            with selectors.DefaultSelector() as selector:
                selector.register(process.stdout, selectors.EVENT_READ)
                while selector.get_map():
                    if time.monotonic() >= deadline:
                        raise RuntimeError("Oracle HTTP test exceeded its time bound")
                    for key, _ in selector.select(timeout=1):
                        block = os.read(key.fileobj.fileno(), 64 * 1024)
                        if not block:
                            selector.unregister(key.fileobj)
                            continue
                        size += len(block)
                        if size > RUNNER["MAX_LOG_BYTES"]:
                            raise RuntimeError("Oracle HTTP test exceeded its output bound")
                        output.write(block)
                        output.flush()
            process.wait(timeout=max(1, deadline - time.monotonic()))
        finally:
            previous = {sig: signal.signal(sig, signal.SIG_IGN) for sig in (signal.SIGTERM, signal.SIGINT)}
            try:
                stop_group(process)
            finally:
                process.stdout.close()
                for sig, handler in previous.items():
                    signal.signal(sig, handler)
    return process.returncode


def fixture_names(log):
    names = set()
    with log.open("rb") as source:
        while line := source.readline(2 * 1024 * 1024 + 1):
            if len(line) > 2 * 1024 * 1024:
                raise ValueError("Oracle test output line exceeded its bound")
            event = json.loads(line)
            if event.get("Package") == "github.com/hakopod/hakopod/internal/api":
                names.update(re.findall(r"Oracle HTTP owned namespace ((?:hdb|hp)-[a-f0-9]{32})\b", event.get("Output", "")))
    return sorted(names)


def cleanup(kube, namespaces, case):
    if len(namespaces) != (3 if case == "http-api" else (2 if case == "recovery" else 1)):
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
    for path in (args.output, args.cache):
        if not path.is_absolute() or ".." in path.parts or not path.is_relative_to("/srv/hakopod-backup-scratch") or any(
                parent.is_symlink() for parent in (path, *path.parents)):
            raise ValueError("Oracle HTTP output and caches require nonsymbolic managed scratch paths")
    limits = runner_limits()
    args.output.mkdir(mode=0o700, parents=True, exist_ok=False)
    sources, images = VERIFIER["source_files"](ROOT), VERIFIER["source_images"](ROOT)
    kube = NATIVE["kubectl_command"](args.kubectl, args.kubeconfig, args.cache)
    identity, capacity = NATIVE["environment"](kube, args.nodes.split(","), images, args.sidb_crd, "http-api", args.output, args.docker)
    (args.output / "host-capacity-before.json").write_text(json.dumps(host_capacity(kube, limits, args.docker), indent=2, sort_keys=True) + "\n")
    env = {key: value for key, value in os.environ.items() if not key.startswith(("GO", "CGO_", "HAKOPOD_", "AWS_"))}
    env.update(PATH=str(args.go.parent) + ":" + str(args.kubectl.parent) + ":/usr/local/bin:/usr/bin:/bin",
               GOMAXPROCS="2", GOENV="off", GOWORK="off", GOFLAGS="-mod=readonly -p=1", GOTOOLCHAIN="local",
               CGO_ENABLED="0", GOOS="linux", GOARCH="amd64", GOCACHE=str(args.cache / "go-build"),
               GOMODCACHE=str(args.cache / "go-mod"), GOTMPDIR=str(args.cache / "go-tmp"), TMPDIR=str(args.cache / "tmp"),
               HAKOPOD_ORACLE_FREE_HTTP_TEST="1", HAKOPOD_KEEP_DATABASE_FIXTURES="1",
               HAKOPOD_TEST_KUBECONFIG=str(args.kubeconfig), HAKOPOD_ORACLE_FREE_HTTP_NODES=args.nodes)
    for key in ("GOCACHE", "GOMODCACHE", "GOTMPDIR", "TMPDIR"):
        Path(env[key]).mkdir(parents=True, exist_ok=True)
    log = args.output / "native-test.jsonl"
    test = "TestManagedOracleFreeHTTPLive"
    fixture_module = runpy.run_path(str(ROOT / "scripts/oracle-free-http-fixtures.py"))
    fixtures = fixture_module["Fixtures"](args.output / "host-fixtures", args.cache, args.docker)
    failed, drained, result = None, True, None

    def interrupted(signum, frame):
        raise InterruptedError("Oracle HTTP acceptance interrupted")

    handlers = {sig: signal.signal(sig, interrupted) for sig in (signal.SIGTERM, signal.SIGINT)}
    try:
        fixture_env = fixtures.prepare()
        env.update(fixture_env)
        # The fixture shim comes first; preserve the reviewed Go/kubectl paths.
        shim = Path(fixture_env["PATH"].split(":")[0])
        temporary = Path(fixture_env["TMPDIR"])
        if (not shim.is_absolute() or not shim.is_relative_to(args.cache) or shim.is_symlink()
                or not temporary.is_absolute() or not temporary.is_relative_to(args.cache) or temporary.is_symlink()):
            raise ValueError("Oracle HTTP fixture paths escaped managed scratch")
        env["PATH"] = str(shim) + ":" + str(args.go.parent) + ":" + str(args.kubectl.parent) + ":/usr/local/bin:/usr/bin:/bin"
        code = run_test([str(args.go), "test", "-json", "-count=1", "./internal/api", "-run", "^" + test + "$", "-timeout=110m"], env, log)
        events, valid = RUNNER["structural_events"](log)
        after = VERIFIER["source_files"](ROOT)
        if code != 0 or not valid or after != sources:
            raise ValueError("Oracle HTTP test failed, emitted invalid evidence or changed source")
        VERIFIER["accepted_events"](events, {test, *(test + "/" + phase for phase in VERIFIER["HTTP_PHASES"])}, "github.com/hakopod/hakopod/internal/api")
        result = cleanup(kube, fixture_names(log), "http-api")
    except BaseException as error:
        failed = error
        drained = not isinstance(error, ProcessDrainError)
    finally:
        for sig in handlers:
            signal.signal(sig, signal.SIG_IGN)
        try:
            if not drained:
                raise RuntimeError("Host fixtures retained because the test process group did not drain")
            receipt = fixtures.cleanup()
            (args.output / "host-fixture-receipt.json").write_text(json.dumps(receipt, indent=2, sort_keys=True) + "\n")
        except BaseException as error:
            failed = failed or error
            receipt = None
        finally:
            for sig, handler in handlers.items():
                signal.signal(sig, handler)
    if failed is not None:
        (args.output / "failed-attempt.json").write_text(json.dumps({"case": "http-api", "passed": False,
            "failure_type": type(failed).__name__, "process_group_drained": drained,
            "host_cleanup_receipt_written": receipt is not None}, indent=2) + "\n")
        raise RuntimeError("Oracle HTTP acceptance failed; inspect retained nonsecret evidence") from None
    fixture_flags = {key: receipt.get(key) for key in (
        "postgres_container_absent", "s3_container_absent", "credential_files_absent")}
    if any(value is not True for value in fixture_flags.values()):
        raise RuntimeError("Oracle HTTP host fixture cleanup is incomplete")
    result["host_fixtures"] = fixture_flags
    after_identity, after_capacity = NATIVE["environment"](kube, args.nodes.split(","), images, args.sidb_crd, "http-api", args.output, args.docker, "capacity-after.json")
    if after_identity != identity or {name: item["container_id"] for name, item in after_capacity.items()} != {name: item["container_id"] for name, item in capacity.items()}:
        raise ValueError("Oracle development cluster identity changed during native acceptance")
    attempt = {"case": "http-api", "exit_code": code, "source_manifest_sha256": source_hash(sources),
               "source_manifest_after_sha256": source_hash(after), "log_sha256": VERIFIER["file_hash"](log),
               "test_events": events, **identity, "cleanup": result,
               "host_fixture_receipt_sha256": VERIFIER["file_hash"](args.output / "host-fixture-receipt.json")}
    report = {"schema_version": 1, "context": "k3d-hakopod-dev", "execution": "native", "platform": "linux/amd64",
              "source_files": sources, "images": images, "attempt": attempt}
    (args.output / "report.json").write_text(json.dumps(report, sort_keys=True, indent=2) + "\n")
    print(json.dumps({"case": "http-api", "status": "passed", "cleanup_verified": True}))


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
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
