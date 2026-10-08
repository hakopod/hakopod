#!/usr/bin/env python3
"""One-use, evidence-preserving Oracle HTTP v2 qualification assembler.

This program is intentionally bound to the retained acceptance-VM paths and
digests below. It never runs a test, builds or pulls an image, or changes the
cluster. It accepts the original mixed Go-download/JSON log as immutable raw
evidence and records the derivation separately.
"""

import hashlib
import json
import os
from pathlib import Path
import re
import runpy
import shutil
import subprocess
import tempfile

SOURCE = Path("/srv/hakopod-backup-scratch/oracle-final-metadata-4d5ff51-v1/engine")
V2 = Path("/srv/hakopod-backup-scratch/oracle-final-http-0cef64c-v2")
CANON = Path("/srv/hakopod-backup-scratch/oracle-canonical-b253812-v1")
BUILD = Path("/srv/hakopod-backup-scratch/oracle-free-operator-v13")
OUTPUT = Path("/srv/hakopod-backup-scratch/oracle-final-metadata-4d5ff51-v1/managed-oracle-free")
KUBECONFIG = Path("/srv/hakopod-backup-scratch/vitess-native-6e7028e-v54/development-kubeconfig")
KUBECTL = Path("/srv/hakopod-backup-scratch/k3d-hakopod-dev/kubectl")
DOCKER = Path("/usr/bin/docker")
NODES = ("k3d-hakopod-dev-server-0", "k3d-hakopod-database-worker-0")
FIXTURES = ("hdb-108079ae0e77bbe91c9115ac696ea6ba", "hdb-c58e1df817c9f5dfb7af2751d0192f7d", "hp-bd1098f28b9fce3563b5c83ffe93c743")
PACKAGE = "github.com/hakopod/hakopod/internal/api"

INPUTS = {
    V2 / "baseline.json": "7732b4c0b81229504c3a218fdaa1c0591950094dedf95b099036eae161066499",
    V2 / "evidence/native-test.jsonl": "08b338106875520a2dbc5bca5f86f4091cafcd1b76fdc6f5e790286c8526042a",
    V2 / "evidence/failed-attempt.json": "8758868204fa32018ea4ef70131bec9bfc3f99eaa256f92e76ad98b9db366c47",
    V2 / "evidence/host-fixture-receipt.json": "fb1c7a7dee9b8312d06f10a0a6ba8e22b2948cf93cc07753c381bc7797cb903b",
    V2 / "evidence/capacity-before.json": "f36633f863cd9cbe70d076bb1f8d34288a54aa7fbadda115ecaabfc7aebe6016",
    V2 / "evidence/host-capacity-before.json": "d0e6771158ee453488aeead82d5bf15297a7a88e768e44d07d33bc56460196d3",
    V2 / "postcondition-v1/receipt.json": "05f5bd3fbdafa1d28520cddc0cfde94abaea2190458cc3c9eff37d8a99029dc9",
    V2 / "postcondition-v1/capacity-after.json": "61d8e1e478b038cac7efdf9a9e3c650251a19d3a4576173de59bcb10b9d198a1",
    CANON / "evidence/lifecycle/report.json": "7d86731456c5f346c1f42037a789a62721fe4c6dc0b78fe43421ef3a19428c37",
    CANON / "evidence/lifecycle/native-test.jsonl": "b425665692b7df29e5fc38b609167a0f366cc7444b5c86a99cee6065763110f5",
    CANON / "evidence/recovery/report.json": "0cae01f7815666cd9e3c9224f76d334f922c67b145af9c91aced6cf1ec6c4a61",
    CANON / "evidence/recovery/native-test.jsonl": "d658e5ec3e39c613600c0599ef604812b6c6c5527b3c782dc010abf0fdcc8c58",
    CANON / "evidence/controller-loss/report.json": "5b93fca281c645e12aad17cd0d70466e582491ac0e0be2bd3c681d39d58c97c3",
    CANON / "evidence/controller-loss/native-test.jsonl": "67cd658ae95625821e1dfe4084240e3d64a17e27c7a59d13b2e83e60862cfa15",
    CANON / "managed-oracle-free/manifest.json": "b15ba8f99e9f046e8ec63bde1413f800632433de69e747140ed98d6d8f6302fb",
    BUILD / "upstream-build/image/upstream.patch": "e95475af11f972045b264d4936352cc0f5e783f3dab084fd209bdd7043eb56ac",
    BUILD / "upstream-build/image/crds/singleinstancedatabases.yaml": "8bc95dc17a6be6c01c059064c04be32334c49d61dfb226e30e8a2d58cff98cde",
    BUILD / "upstream-build/artifact-sha256.txt": "b17fae19625af49ad337e3b18eee355661416e7a6c95bb741948b8d6b48b51b4",
    BUILD / "evidence/formatted-source-sha256.json": "5cc446b4637f69997fac80fa1c0bc467ad0cba371fc038928b6ae229a08725cc",
    BUILD / "upstream-build/image/bin/manager": "8a64742fb2327f720488f5b7e494aff2ba84aec3b82e0edb2f10c58e076bfe01",
    CANON / "managed-oracle-free/packaging-receipt.json": "a052e01c448ad077e822c8ef111e9b322979982797f884009226c993fa5c7a5f",
    Path("/srv/hakopod-backup-scratch/oracle-http-readonly-preflight-0cef64c-v1/evidence/preflight.json"): "53fb9518ffc91330beeab6457b2a1adc7ddb39c99f2206e43479b539b4b7ec13",
    KUBECONFIG: "fef7c25be3850b48351d424a99276dc681838e9644728122a07f47b0292007cb",
    KUBECTL: "fbecbfd375b3686002c2e81d51c390172f5ffba3d6b47920d55342cb03f557af",
    DOCKER: "5fbf1d65d05315a4e89f561fee89731cec1f95094b86a56fac47e240edbf7bac",
    SOURCE / "release/verify-oracle-free-runtime.py": "a2aefadb58b8660825d99b49ee924bfaabadb98f92622a0732d2314447d00be1",
    SOURCE / "release/record-oracle-free-qualification.py": "5c8f9116a302530e73bd7ceebf2a735772e22218d136bbdb550a1dc48aa95cf3",
    SOURCE / "scripts/run-development-vitess-acceptance.py": "4e2abf106177bfa113b70f8f75d088f6115cb6912807cf74cea6dc20d8f3f578",
    SOURCE / "scripts/run-development-oracle-free-acceptance.py": "d5389a1f5231b1d12690072f7b48ba45f62472d005c41503b42e479dd0ca29c7",
    SOURCE / "scripts/run-development-oracle-free-http-acceptance.py": "e33672127a23d20e8275de147dcbb7a8b0204ef90807c999860678a2422bb73f",
}
PREAMBLE_SHA256 = "6ff952178744d42de19ac98dd911a4b3c52e29e042347b9b77632f5f7fe2ce7a"
JSON_SUFFIX_SHA256 = "5cec2155a202f3d5dcdff6d346f664b43a9350eb2f53f856a328277d675d486b"
SOURCE_MAP_SHA256 = "0c8747b6c12b391afaaa65fe1e2c8caa26fdbeb1ee5e63f08b27b19a9e130244"
SOURCE_COMMIT = "4d5ff51a2da727e8ecaadcfd92f3fae614c4d47c"


def digest(path, limit=512 * 1024 * 1024):
    if path.is_symlink() or not path.is_file() or not 0 < path.stat().st_size <= limit:
        raise ValueError("missing, symbolic, empty, or oversized input: " + str(path))
    with path.open("rb") as stream:
        return hashlib.file_digest(stream, "sha256").hexdigest()


def command(argv):
    result = subprocess.run(argv, stdin=subprocess.DEVNULL, capture_output=True, timeout=30, check=False)
    if result.returncode or len(result.stdout) > 4 * 1024 * 1024:
        raise ValueError("read-only command failed: " + " ".join(map(str, argv)))
    return result.stdout


def kubectl(*args):
    return command([str(KUBECTL), "--kubeconfig", str(KUBECONFIG), "--context", "k3d-hakopod-dev", *args])


def parse_raw_log(verifier):
    raw = (V2 / "evidence/native-test.jsonl").read_bytes()
    lines = raw.splitlines(keepends=True)
    if len(lines) != 182 or any(len(line) > 2 * 1024 * 1024 for line in lines):
        raise ValueError("raw v2 log line inventory differs")
    preamble, suffix = b"".join(lines[:81]), b"".join(lines[81:])
    if hashlib.sha256(preamble).hexdigest() != PREAMBLE_SHA256 or hashlib.sha256(suffix).hexdigest() != JSON_SUFFIX_SHA256:
        raise ValueError("raw v2 log partitions differ")
    download = re.compile(rb"go: downloading [^\s]+ [^\s]+\n")
    if any(not download.fullmatch(line) for line in lines[:81]):
        raise ValueError("raw v2 preamble is not exactly 81 Go download notices")
    decoded = []
    for line in lines[81:]:
        event = json.loads(line)
        if not isinstance(event, dict):
            raise ValueError("raw v2 JSON suffix contains a non-object")
        decoded.append(event)
    if any(event.get("Action") in ("fail", "skip") for event in decoded):
        raise ValueError("raw v2 JSON suffix contains fail or skip")
    terminal = [event for event in decoded if event.get("Action") == "pass" and event.get("Package") == PACKAGE and "Test" not in event]
    structural = [event for event in decoded if event.get("Action") in ("run", "pass", "fail", "skip") and event.get("Test")]
    if (len(terminal) != 1 or decoded[-1] != terminal[0] or len(structural) != 12
            or sum(event.get("Action") == "run" for event in structural) != 6
            or sum(event.get("Action") == "pass" for event in structural) != 6
            or any(event.get("Package") != PACKAGE for event in decoded)):
        raise ValueError("raw v2 JSON suffix lacks its unique terminal package pass")
    events = [{key: event.get(key) for key in ("Action", "Package", "Test")} for event in structural]
    test = verifier["HTTP_TEST"]
    required = {test, *(test + "/" + phase for phase in verifier["HTTP_PHASES"])}
    verifier["accepted_events"](events, required, PACKAGE)
    names = sorted({name for event in decoded if event.get("Package") == PACKAGE
                    for name in re.findall(r"Oracle HTTP owned namespace ((?:hdb|hp)-[a-f0-9]{32})\b", event.get("Output", ""))})
    if tuple(names) != tuple(sorted(FIXTURES)):
        raise ValueError("raw v2 fixture namespace inventory differs")
    return events, names, terminal[0]


def live_postconditions(verifier, names):
    if command(["git", "-C", str(SOURCE), "rev-parse", "HEAD"]).decode().strip() != SOURCE_COMMIT:
        raise ValueError("metadata source commit differs")
    if command(["git", "-C", str(SOURCE), "status", "--porcelain=v1"]).strip():
        raise ValueError("metadata source worktree is dirty")
    submodules = command(["git", "-C", str(SOURCE), "submodule", "status", "--recursive"]).decode().splitlines()
    expected_submodules = [
        "-659c6807375ef407149c8f2f7f158238c2350695 packages/ui",
        "-0742114e40c7e2d5bc94da90854da2064c141c0b private/license-issuer",
        " 07cda41b2ca8e93f463f41c81990b4fd01cf78d5 templates (07cda41)",
    ]
    if submodules != expected_submodules:
        raise ValueError("metadata source submodule identity differs")
    sources = verifier["source_files"](SOURCE)
    encoded = json.dumps(sources, sort_keys=True, separators=(",", ":")).encode()
    if len(sources) != 1748 or hashlib.sha256(encoded).hexdigest() != SOURCE_MAP_SHA256:
        raise ValueError("metadata source map differs")
    if kubectl("config", "current-context").decode().strip() != "k3d-hakopod-dev":
        raise ValueError("cluster context differs")
    preflight = verifier["read_json"](Path("/srv/hakopod-backup-scratch/oracle-http-readonly-preflight-0cef64c-v1/evidence/preflight.json"))
    baseline = verifier["read_json"](V2 / "baseline.json")
    system = json.loads(kubectl("get", "namespace", "kube-system", "-o", "json"))
    crd = json.loads(kubectl("get", "crd", "singleinstancedatabases.database.oracle.com", "-o", "json"))
    nodes = {}
    container_ids = {}
    for name in NODES:
        node = json.loads(kubectl("get", "node", name, "-o", "json"))
        nodes[name] = node["metadata"]["uid"]
        container_ids[name] = json.loads(command([str(DOCKER), "inspect", "--format", "{{json .Id}}", name]))
    identity = {"cluster_uid": system["metadata"]["uid"], "sidb_crd_uid": crd["metadata"]["uid"], "node_uids": nodes}
    capacity = verifier["read_json"](V2 / "evidence/capacity-before.json")
    expected_containers = {name: capacity["nodes"][name]["container_id"] for name in NODES}
    if identity != preflight.get("identity") or container_ids != expected_containers:
        raise ValueError("live cluster or node-container identity differs from preflight")
    for name in names:
        if kubectl("get", "namespace", name, "--ignore-not-found", "-o", "name").strip():
            raise ValueError("v2 fixture namespace remains: " + name)
    namespace_items = json.loads(kubectl("get", "namespaces", "--chunk-size=0", "-o", "json"))
    pvs = json.loads(kubectl("get", "persistentvolumes", "--chunk-size=0", "-o", "json"))
    if (len(namespace_items.get("items", [])) > 1024 or namespace_items.get("metadata", {}).get("continue")
            or len(pvs.get("items", [])) > 1024 or pvs.get("metadata", {}).get("continue")):
        raise ValueError("namespace or persistent-volume inventory is unbounded")
    if any(item.get("spec", {}).get("claimRef", {}).get("namespace") in names for item in pvs["items"]):
        raise ValueError("v2 fixture persistent volume remains")
    namespaces = {item["metadata"]["name"]: {"uid": item["metadata"]["uid"],
        "deleting": item["metadata"].get("deletionTimestamp")} for item in namespace_items["items"]}
    volumes = {item["metadata"]["name"]: {"uid": item["metadata"]["uid"],
        "deleting": item["metadata"].get("deletionTimestamp"), "claimRef": item.get("spec", {}).get("claimRef"),
        "phase": item.get("status", {}).get("phase")} for item in pvs["items"]}
    retained_ns = "hdb-e66199f03ed69a30b0495bb04c4d4906"
    retained_cr = json.loads(kubectl("-n", retained_ns, "get", "singleinstancedatabase.database.oracle.com", "database", "-o", "json"))
    retained_secret = json.loads(kubectl("-n", retained_ns, "get", "secret", "database-credentials", "-o", "json"))
    retained = {"cr_uid": retained_cr["metadata"]["uid"], "secret_uid": retained_secret["metadata"]["uid"],
        "secret_resource_version": retained_secret["metadata"]["resourceVersion"],
        "secret_hashes": {key: hashlib.sha256(value.encode()).hexdigest() for key, value in retained_secret["data"].items()}}
    if {"namespaces": namespaces, "pvs": volumes, "retained": retained} != baseline:
        raise ValueError("full post-run cluster baseline differs")
    receipt = verifier["read_json"](V2 / "evidence/host-fixture-receipt.json")
    flags = {key: receipt.get(key) for key in ("postgres_container_absent", "s3_container_absent", "credential_files_absent")}
    if any(value is not True for value in flags.values()):
        raise ValueError("v2 host cleanup receipt is incomplete")
    removed = receipt.get("removed")
    if (not isinstance(removed, list) or len(removed) != 2
            or {item.get("component") for item in removed} != {"s3", "postgres"}
            or any(set(item) not in ({"component", "container_id", "image", "name"},
                                     {"already_absent", "component", "container_id", "image", "name"}) for item in removed)):
        raise ValueError("v2 host cleanup receipt inventory differs")
    live = command([str(DOCKER), "ps", "-a", "--no-trunc", "--format", "{{.ID}} {{.Names}}"]).decode().splitlines()
    forbidden = {item["container_id"] for item in removed} | {item["name"] for item in removed}
    if any(forbidden.intersection(line.split()) for line in live):
        raise ValueError("v2 host fixture container remains")
    return sources, verifier["source_images"](SOURCE), identity, flags


def main():
    if OUTPUT.exists():
        raise ValueError("fresh output path already exists")
    for path, expected in INPUTS.items():
        if digest(path) != expected:
            raise ValueError("fixed input digest differs: " + str(path))
    failed_before = (V2 / "evidence/failed-attempt.json").read_bytes()
    verifier = runpy.run_path(str(SOURCE / "release/verify-oracle-free-runtime.py"))
    recorder = runpy.run_path(str(SOURCE / "release/record-oracle-free-qualification.py"))
    events, names, terminal = parse_raw_log(verifier)
    sources, images, identity, host_flags = live_postconditions(verifier, names)
    historical_paths = [CANON / "evidence/lifecycle/report.json", CANON / "evidence/recovery/report.json",
                        CANON / "evidence/controller-loss/report.json"]
    reports = [verifier["read_json"](path) for path in historical_paths]
    qualified = reports[0]["source_files"]
    if any(report["source_files"] != qualified or report["images"] != images for report in reports):
        raise ValueError("historical report identity differs")
    old_manifest_path = CANON / "managed-oracle-free/manifest.json"
    recorder["validate_qualified_manifest"](old_manifest_path, qualified, images)
    source_sha = hashlib.sha256(json.dumps(sources, sort_keys=True, separators=(",", ":")).encode()).hexdigest()
    attempt = {"case": "http-api", "exit_code": 0, "source_manifest_sha256": source_sha,
               "source_manifest_after_sha256": source_sha, "log_sha256": INPUTS[V2 / "evidence/native-test.jsonl"],
               "test_events": events, **identity,
               "cleanup": {"namespaces": names, "namespaces_absent": True, "persistent_volumes_absent": True,
                           "host_fixtures": host_flags},
               "host_fixture_receipt_sha256": INPUTS[V2 / "evidence/host-fixture-receipt.json"],
               "source_files": sources}
    attempts = [{**report["attempt"], "source_files": report["source_files"]} for report in reports] + [attempt]
    acceptance = {"schema_version": 2, "context": "k3d-hakopod-dev", "execution": "native", "platform": "linux/amd64",
                  "source_files": sources, "qualified_source_files": qualified,
                  "qualified_manifest_sha256": INPUTS[old_manifest_path], "images": images, "attempts": attempts}
    verifier["validate_acceptance"](acceptance, sources, images, SOURCE)
    temporary = Path(tempfile.mkdtemp(prefix=OUTPUT.name + ".staging-", dir=OUTPUT.parent))
    copies = ((BUILD / "upstream-build/image/upstream.patch", "operator-upstream.patch"),
              (BUILD / "upstream-build/image/crds/singleinstancedatabases.yaml", "sidb-v4.yaml"),
              (BUILD / "upstream-build/artifact-sha256.txt", "build-artifacts.txt"),
              (BUILD / "evidence/formatted-source-sha256.json", "source-build-manifest.json"),
              (CANON / "managed-oracle-free/packaging-receipt.json", "packaging-receipt.json"))
    for before, after in copies:
        shutil.copyfile(before, temporary / after)
    (temporary / "native-acceptance.json").write_text(json.dumps(acceptance, indent=2, sort_keys=True) + "\n")
    manifest = {"schema_version": 1, "platform": "linux/amd64",
        "source": {"repository": "https://github.com/oracle/oracle-database-operator", "revision": verifier["UPSTREAM"]},
        "source_files": sources,
        "images": {"operator": {"reference": images["operator"], "binaries": {"/manager": INPUTS[BUILD / "upstream-build/image/bin/manager"]}},
                   "database": {"reference": images["database"], "binaries": {}}},
        "files": {name: verifier["file_hash"](temporary / name) for name in verifier["FILES"]}}
    (temporary / "manifest.json").write_text(json.dumps(manifest, indent=2, sort_keys=True) + "\n")
    verifier["validate_metadata"](temporary, SOURCE)
    output_hashes = {path.name: digest(path) for path in sorted(temporary.iterdir()) if path.is_file()}
    sidecar = {"schema_version": 1, "kind": "oracle-http-v2-evidence-derivation",
        "raw_log_preserved": True, "raw_log_sha256": INPUTS[V2 / "evidence/native-test.jsonl"],
        "preamble_lines": 81, "preamble_sha256": PREAMBLE_SHA256, "json_suffix_sha256": JSON_SUFFIX_SHA256,
        "derived_exit_code": 0,
        "exit_code_basis": "Exit code 0 is inferred from the terminal package pass and the fact run_test returned and post-run structural validation was reached.",
        "os_exit_status_separately_persisted": False,
        "terminal_package_pass": terminal, "failed_attempt_preserved": True,
        "inputs": {str(path): value for path, value in INPUTS.items()}, "output_hashes": output_hashes,
        "source_commit": SOURCE_COMMIT, "source_map_sha256": SOURCE_MAP_SHA256,
        "postconditions": {"source_identity": True, "cluster_identity": True, "fixture_namespaces_absent": True,
                           "fixture_persistent_volumes_absent": True, "host_cleanup": True}}
    (temporary / "oracle-http-v2-derivation.json").write_text(json.dumps(sidecar, indent=2, sort_keys=True) + "\n")
    if (V2 / "evidence/failed-attempt.json").read_bytes() != failed_before:
        raise ValueError("original failed-attempt changed during assembly")
    os.rename(temporary, OUTPUT)
    print(json.dumps({"output": str(OUTPUT), "manifest_sha256": digest(OUTPUT / "manifest.json"),
                      "derivation_sha256": digest(OUTPUT / "oracle-http-v2-derivation.json")}, sort_keys=True))


if __name__ == "__main__":
    main()
