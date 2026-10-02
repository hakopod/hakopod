#!/usr/bin/env python3
"""Build and inspect the bounded public Supavisor OCI candidate twice."""
from __future__ import annotations

import argparse
import hashlib
import json
import os
from pathlib import Path, PurePosixPath
import selectors
import shutil
import signal
import subprocess
import tarfile
import time

HERE = Path(__file__).resolve().parent
DOCKERFILE = HERE / "Dockerfile.supavisor"
PATCH = HERE / "supavisor-v2.9.12-hakopod-tls.patch"
DOCKERFILE_SHA256 = "19506707ec464c8cb57e3748f70961266f00b223497db6325a1bdefadb1b6257"
PATCH_SHA256 = "0e32aa27bfbc8b8a59be2ce2b71ea71a2edb3bf69b13e0ac6762b01598724082"
BUILDKIT_IMAGE = "moby/buildkit@sha256:cec9f139f45e93c5c69c60f8b07cfad9f43f4ef6b6a6cd917527fea5ff2e3dea"
BUILDER_CONTAINER = "hakopod-supavisor-buildkit"
BUILDER_NAME = "hakopod-supavisor-bounded"
BUILDER_ADDRESS = "tcp://127.0.0.1:12349"
TAG = "ghcr.io/hakopod/managed-supabase-pooler:v2.9.12-hakopod.2"
SOURCE_LABEL = "https://github.com/hakopod/hakopod"
UPSTREAM_REPOSITORY = "https://github.com/supabase/supavisor"
UPSTREAM_COMMIT = "e8719d9ac5a43604c46d2d9e15bb34c76dea8f12"
UPSTREAM_SOURCE_SHA256 = "8b883bd6cc72db080ac4e6abfa90f11ba433bae41e3e8e5e4618ff49199ab098"
RUSTUP_INIT_SHA256 = "6aeece6993e902708983b209d04c0d1dbb14ebb405ddb87def578d41f920f56d"
SOURCE_DATE_EPOCH = "1787668490"
CPU_QUOTA = 400000
CPU_PERIOD = 100000
MEMORY_BYTES = 12 * 1024**3
MINIMUM_FREE_BYTES = 20 * 1024**3
MAX_LOG_BYTES = 32 * 1024**2
MAX_ARCHIVE_BYTES = 2 * 1024**3
MAX_UNPACKED_BYTES = 5 * 1024**3


def sha256(path: Path) -> str:
    with path.open("rb") as stream:
        return hashlib.file_digest(stream, "sha256").hexdigest()


def require(value: object, message: str) -> None:
    if not value:
        raise RuntimeError(message)


def run(args: list[str], timeout: int = 120) -> bytes:
    process = subprocess.run(args, stdin=subprocess.DEVNULL, stdout=subprocess.PIPE,
                             stderr=subprocess.STDOUT, timeout=timeout)
    require(process.returncode == 0 and len(process.stdout) <= 4 * 1024**2,
            "bounded command failed: " + Path(args[0]).name)
    return process.stdout


def docker(*args: str, timeout: int = 120) -> bytes:
    return run(["sudo", "docker", *args], timeout=timeout)


def free_bytes(path: Path) -> int:
    return shutil.disk_usage(path).free


def bounded_build(args: list[str], log: Path, output: Path, timeout: int = 7200) -> None:
    require(not log.exists() and not output.exists(), "build output already exists")
    deadline = time.monotonic() + timeout
    with log.open("xb") as stream:
        os.chmod(stream.fileno(), 0o600)
        process = subprocess.Popen(args, stdin=subprocess.DEVNULL, stdout=stream,
                                   stderr=subprocess.STDOUT, start_new_session=True)
        try:
            while process.poll() is None:
                require(time.monotonic() < deadline, "Supavisor build timed out")
                require(log.stat().st_size <= MAX_LOG_BYTES, "Supavisor build log exceeded its bound")
                require(free_bytes(output.parent) >= MINIMUM_FREE_BYTES,
                        "Supavisor build would breach disk reserve")
                time.sleep(5)
            require(process.returncode == 0, "Supavisor build failed")
        except BaseException:
            if process.poll() is None:
                os.killpg(process.pid, signal.SIGTERM)
                try:
                    process.wait(timeout=20)
                except subprocess.TimeoutExpired:
                    os.killpg(process.pid, signal.SIGKILL)
                    process.wait(timeout=10)
            raise
    require(output.is_file() and 0 < output.stat().st_size <= MAX_ARCHIVE_BYTES,
            "Supavisor OCI output is missing or oversized")


def blob(archive: tarfile.TarFile, members: dict[str, tarfile.TarInfo], descriptor: dict,
         allowed: set[str], maximum: int) -> bytes:
    require(isinstance(descriptor, dict) and descriptor.get("mediaType") in allowed,
            "OCI descriptor media type differs")
    digest = descriptor.get("digest", "")
    size = descriptor.get("size")
    require(isinstance(digest, str) and digest.startswith("sha256:") and len(digest) == 71 and
            type(size) is int and 0 < size <= maximum, "OCI descriptor differs")
    name = "blobs/sha256/" + digest[7:]
    require(name in members and members[name].isfile() and members[name].size == size,
            "OCI descriptor blob is missing")
    raw = archive.extractfile(members[name]).read(maximum + 1)
    require(len(raw) == size and hashlib.sha256(raw).hexdigest() == digest[7:],
            "OCI blob digest differs")
    return raw


def inspect_archive(path: Path) -> dict:
    require(path.is_file() and not path.is_symlink() and path.stat().st_size <= MAX_ARCHIVE_BYTES,
            "OCI archive identity differs")
    with tarfile.open(path, "r:") as archive:
        entries = archive.getmembers()
        require(1 < len(entries) <= 512 and len({entry.name for entry in entries}) == len(entries),
                "OCI archive inventory differs")
        members = {entry.name: entry for entry in entries}
        aggregate = 0
        for entry in entries:
            name = PurePosixPath(entry.name)
            require(not name.is_absolute() and ".." not in name.parts and
                    (entry.isfile() or entry.isdir()), "unsafe OCI archive entry")
            aggregate += entry.size
            require(aggregate <= MAX_UNPACKED_BYTES, "OCI archive exceeds unpacked bound")
            if entry.isfile() and entry.name.startswith("blobs/sha256/"):
                require(hashlib.file_digest(archive.extractfile(entry), "sha256").hexdigest() ==
                        entry.name.rsplit("/", 1)[1], "OCI blob name differs from content")
        layout = json.load(archive.extractfile(members["oci-layout"]))
        index = json.load(archive.extractfile(members["index.json"]))
        require(layout == {"imageLayoutVersion": "1.0.0"} and index.get("schemaVersion") == 2 and
                len(index.get("manifests", [])) == 1, "OCI index differs")
        descriptor = index["manifests"][0]
        manifest_raw = blob(archive, members, descriptor,
                            {"application/vnd.oci.image.manifest.v1+json"}, 2 * 1024**2)
        manifest = json.loads(manifest_raw)
        config_raw = blob(archive, members, manifest.get("config"),
                          {"application/vnd.oci.image.config.v1+json"}, 2 * 1024**2)
        config = json.loads(config_raw)
        layers = manifest.get("layers")
        require(isinstance(layers, list) and 1 <= len(layers) <= 128, "OCI layers differ")
        layer_digests = []
        for layer in layers:
            blob(archive, members, layer, {
                "application/vnd.oci.image.layer.v1.tar",
                "application/vnd.oci.image.layer.v1.tar+gzip",
                "application/vnd.oci.image.layer.v1.tar+zstd",
            }, 2 * 1024**3)
            layer_digests.append(layer["digest"])
        settings = config.get("config", {})
        labels = settings.get("Labels", {})
        require(config.get("os") == "linux" and config.get("architecture") == "amd64" and
                settings.get("User") == "65534:65534" and
                settings.get("Entrypoint") == ["/usr/bin/tini", "-s", "-g", "--", "/app/limits.sh"] and
                settings.get("Cmd") == ["/app/bin/server"], "OCI runtime contract differs")
        require(labels.get("org.opencontainers.image.source") == SOURCE_LABEL and
                labels.get("org.opencontainers.image.version") == "v2.9.12-hakopod.2" and
                labels.get("io.hakopod.supavisor.upstream-repository") == UPSTREAM_REPOSITORY and
                labels.get("io.hakopod.supavisor.upstream-commit") == UPSTREAM_COMMIT and
                labels.get("io.hakopod.supavisor.upstream-source-sha256") == UPSTREAM_SOURCE_SHA256 and
                labels.get("io.hakopod.supavisor.patch-sha256") == PATCH_SHA256 and
                labels.get("io.hakopod.supavisor.rustup-init-sha256") == RUSTUP_INIT_SHA256,
                "OCI provenance labels differ")
        return {
            "archive": path.name,
            "archive_bytes": path.stat().st_size,
            "archive_sha256": sha256(path),
            "manifest_digest": descriptor["digest"],
            "config_digest": manifest["config"]["digest"],
            "layer_digests": layer_digests,
            "platform": "linux/amd64",
            "user": settings["User"],
            "labels": {key: labels[key] for key in sorted(labels)},
            "unpacked_bytes": aggregate,
        }


def prepare_builder(root: Path) -> None:
    state = root / "buildkit-state"
    state.mkdir(mode=0o700)
    names = docker("ps", "-aq", "--filter", "name=^/" + BUILDER_CONTAINER + "$").decode().split()
    require(not names, "bounded Supavisor builder already exists")
    docker("run", "--detach", "--name", BUILDER_CONTAINER, "--privileged", "--network", "host",
           "--memory", str(MEMORY_BYTES), "--memory-swap", str(MEMORY_BYTES),
           "--cpu-period", str(CPU_PERIOD), "--cpu-quota", str(CPU_QUOTA),
           "--volume", str(state) + ":/var/lib/buildkit", BUILDKIT_IMAGE,
           "--addr", BUILDER_ADDRESS)
    docker("buildx", "create", "--name", BUILDER_NAME, "--driver", "remote", BUILDER_ADDRESS)
    docker("buildx", "inspect", "--bootstrap", BUILDER_NAME, timeout=300)
    value = json.loads(docker("inspect", BUILDER_CONTAINER))[0]
    host = value["HostConfig"]
    require(host["Memory"] == MEMORY_BYTES and host["MemorySwap"] == MEMORY_BYTES and
            host["CpuPeriod"] == CPU_PERIOD and host["CpuQuota"] == CPU_QUOTA and
            host["NetworkMode"] == "host", "bounded builder limits differ")


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--output", type=Path, required=True)
    parser.add_argument("--self-check", action="store_true")
    args = parser.parse_args()
    require(sha256(DOCKERFILE) == DOCKERFILE_SHA256 and sha256(PATCH) == PATCH_SHA256,
            "reviewed build inputs differ")
    if args.self_check:
        print(json.dumps({"dockerfile_sha256": DOCKERFILE_SHA256, "patch_sha256": PATCH_SHA256,
                          "tag": TAG}, sort_keys=True))
        return
    root = args.output.resolve()
    require(root.is_absolute() and not root.exists(), "fresh absolute output directory required")
    require(os.cpu_count() is not None and os.cpu_count() >= 32, "resized build host is required")
    root.mkdir(mode=0o700)
    require(free_bytes(root) >= MINIMUM_FREE_BYTES, "insufficient build disk reserve")
    prepare_builder(root)
    try:
        receipts = []
        for attempt in (1, 2):
            archive = root / f"supavisor-v2.9.12-hakopod.2-linux-amd64-attempt{attempt}.oci.tar"
            log = root / f"build-attempt{attempt}.log"
            bounded_build([
                "sudo", "docker", "buildx", "build", "--builder", BUILDER_NAME,
                "--platform", "linux/amd64", "--provenance=false", "--sbom=false",
                "--build-arg", "SOURCE_DATE_EPOCH=" + SOURCE_DATE_EPOCH,
                "--tag", TAG, "--file", str(DOCKERFILE),
                "--output", "type=oci,dest=" + str(archive) + ",rewrite-timestamp=true",
                str(HERE),
            ], log, archive)
            receipts.append(inspect_archive(archive))
        require(receipts[0]["manifest_digest"] == receipts[1]["manifest_digest"] and
                receipts[0]["config_digest"] == receipts[1]["config_digest"] and
                receipts[0]["layer_digests"] == receipts[1]["layer_digests"],
                "independent OCI build outputs are not reproducible")
        receipt = {
            "schema_version": 1,
            "candidate": TAG,
            "dockerfile_sha256": DOCKERFILE_SHA256,
            "patch_sha256": PATCH_SHA256,
            "buildkit_image": BUILDKIT_IMAGE,
            "source_date_epoch": int(SOURCE_DATE_EPOCH),
            "resource_limits": {"cpu": 4, "memory_bytes": MEMORY_BYTES,
                                "minimum_free_bytes": MINIMUM_FREE_BYTES},
            "attempts": receipts,
            "deterministic_cached_reexport": True,
            "independent_reproducibility_build": False,
            "native_qualification": False,
        }
        target = root / "build-receipt.json"
        with target.open("x") as stream:
            json.dump(receipt, stream, indent=2, sort_keys=True)
            stream.write("\n")
        os.chmod(target, 0o600)
        print(json.dumps({"receipt": str(target), "receipt_sha256": sha256(target),
                          "manifest_digest": receipts[0]["manifest_digest"]}, sort_keys=True))
    finally:
        try:
            docker("buildx", "rm", BUILDER_NAME)
        finally:
            docker("rm", "--force", BUILDER_CONTAINER)


if __name__ == "__main__":
    try:
        main()
    except (OSError, RuntimeError, subprocess.TimeoutExpired, KeyError, ValueError, json.JSONDecodeError) as error:
        raise SystemExit("Supavisor build stopped: " + str(error)) from None
