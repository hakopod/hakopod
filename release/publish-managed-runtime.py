#!/usr/bin/env python3
"""Publish inspected OCI archives without rebuilding or qualifying a runtime."""
import argparse
import hashlib
import json
import os
from pathlib import Path, PurePosixPath
import re
import selectors
import shutil
import subprocess
import tempfile
import time

DIGEST = re.compile(r"sha256:[0-9a-f]{64}")
SHA = re.compile(r"[0-9a-f]{64}")
ASSET = re.compile(r"[a-z0-9][a-z0-9._-]{0,127}\.oci\.tar")
REPOSITORY = re.compile(r"ghcr\.io/hakopod/(?:managed-)?(?:vitess(?:-operator|-runtime|-server)?|neon-(?:storage|compute-tools|compute-runtime|compute-v17))")
SOURCE = "https://github.com/hakopod/hakopod"
MAX_ARCHIVE = 2 * 1024**3
MAX_UNPACKED = 4 * 1024**3
MAX_JSON = 1024**2
MANIFEST_TYPES = {"application/vnd.oci.image.manifest.v1+json", "application/vnd.docker.distribution.manifest.v2+json"}
CONFIG_TYPES = {"application/vnd.oci.image.config.v1+json", "application/vnd.docker.container.image.v1+json"}
LAYER_TYPES = {"application/vnd.oci.image.layer.v1.tar", "application/vnd.oci.image.layer.v1.tar+gzip", "application/vnd.oci.image.layer.v1.tar+zstd", "application/vnd.docker.image.rootfs.diff.tar.gzip"}


def run(args, timeout=300):
    """Keep command output and duration bounded; never print raw diagnostics."""
    process = subprocess.Popen(args, stdin=subprocess.DEVNULL, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
    outputs = {process.stdout: bytearray(), process.stderr: bytearray()}
    deadline = time.monotonic() + timeout
    try:
        with selectors.DefaultSelector() as selector:
            for pipe in outputs:
                selector.register(pipe, selectors.EVENT_READ)
            while selector.get_map():
                if time.monotonic() >= deadline:
                    raise ValueError("Publisher command timed out")
                for key, _ in selector.select(min(0.25, max(0, deadline - time.monotonic()))):
                    chunk = os.read(key.fileobj.fileno(), 65536)
                    if not chunk:
                        selector.unregister(key.fileobj)
                        continue
                    outputs[key.fileobj].extend(chunk)
                    if sum(map(len, outputs.values())) > 256 * 1024:
                        raise ValueError("Publisher command output exceeded its bound")
        code = process.wait(timeout=max(0.001, deadline - time.monotonic()))
        return code, *(bytes(outputs[pipe]).decode("utf-8", errors="replace") for pipe in (process.stdout, process.stderr))
    finally:
        if process.poll() is None:
            process.kill()
        process.wait()
        process.stdout.close()
        process.stderr.close()


def checked(args, timeout=300):
    code, output, _ = run(args, timeout)
    if code:
        raise ValueError("Publisher command failed: " + Path(args[0]).name)
    return output


def digest_file(path, limit):
    if path.is_symlink() or not path.is_file() or not 0 < path.stat().st_size <= limit:
        raise ValueError("Publication file is missing or exceeds its bound")
    with path.open("rb") as source:
        return hashlib.file_digest(source, "sha256").hexdigest()


def read_json(path):
    digest_file(path, MAX_JSON)
    def unique(pairs):
        result = {}
        for name, value in pairs:
            if name in result:
                raise ValueError("Duplicate publication JSON field")
            result[name] = value
        return result
    return json.loads(path.read_bytes(), object_pairs_hook=unique)


def plan(path, expected_sha):
    if not SHA.fullmatch(expected_sha) or digest_file(path, MAX_JSON) != expected_sha:
        raise ValueError("Publication plan checksum mismatch")
    data = read_json(path)
    if not isinstance(data, dict) or set(data) != {"schema_version", "images"} or type(data["schema_version"]) is not int or data["schema_version"] != 1:
        raise ValueError("Invalid publication plan schema")
    if not isinstance(data["images"], list) or not 1 <= len(data["images"]) <= 5:
        raise ValueError("Publication plan needs one to five images")
    seen_assets, seen_repositories = set(), set()
    for image in data["images"]:
        if not isinstance(image, dict) or set(image) != {"archive", "archive_sha256", "image_digest", "repository", "platform", "config_user", "labels"}:
            raise ValueError("Invalid publication image fields")
        if any(not isinstance(image[key], str) for key in image if key != "labels"):
            raise ValueError("Publication image fields must be strings")
        if not ASSET.fullmatch(image["archive"]) or not SHA.fullmatch(image["archive_sha256"]) or not DIGEST.fullmatch(image["image_digest"]) or not REPOSITORY.fullmatch(image["repository"]):
            raise ValueError("Invalid publication image identity")
        if image["archive"] in seen_assets or image["repository"] in seen_repositories:
            raise ValueError("Duplicate publication image")
        seen_assets.add(image["archive"])
        seen_repositories.add(image["repository"])
        if image["platform"] != "linux/amd64" or len(image["config_user"]) > 128:
            raise ValueError("Unsupported publication platform or user")
        labels = image["labels"]
        if not isinstance(labels, dict) or not 1 <= len(labels) <= 32 or labels.get("org.opencontainers.image.source") != SOURCE:
            raise ValueError("Publication must identify the canonical source repository")
        if any(not isinstance(k, str) or not isinstance(v, str) or not 1 <= len(k) <= 128 or len(v) > 512 for k, v in labels.items()):
            raise ValueError("Invalid publication labels")
    return data["images"]


def inspect_archive(archive, image, layout):
    import tarfile
    if digest_file(archive, MAX_ARCHIVE) != image["archive_sha256"]:
        raise ValueError("OCI archive checksum mismatch")
    layout.mkdir(mode=0o700)
    names, total = set(), 0
    with tarfile.open(archive) as source:
        for member in source:
            name = PurePosixPath(member.name)
            if member.name in names or len(names) >= 1024 or name.is_absolute() or ".." in name.parts or not (member.isdir() or member.isfile()):
                raise ValueError("Unsafe or oversized OCI archive inventory")
            names.add(member.name)
            if member.isdir():
                if str(name) not in {".", "blobs", "blobs/sha256"}:
                    raise ValueError("Unexpected OCI directory")
                continue
            blob = len(name.parts) == 3 and name.parts[:2] == ("blobs", "sha256") and SHA.fullmatch(name.name)
            if not blob and str(name) not in {"oci-layout", "index.json"}:
                raise ValueError("Unexpected OCI archive file")
            total += member.size
            if member.size < 0 or total > MAX_UNPACKED or (not blob and member.size > MAX_JSON):
                raise ValueError("OCI archive exceeds its size bound")
            target = layout.joinpath(*name.parts)
            target.parent.mkdir(mode=0o700, parents=True, exist_ok=True)
            with target.open("xb") as output, source.extractfile(member) as content:
                shutil.copyfileobj(content, output, 1024 * 1024)
            if blob and digest_file(target, MAX_UNPACKED) != name.name:
                raise ValueError("OCI blob checksum mismatch")
    if read_json(layout / "oci-layout") != {"imageLayoutVersion": "1.0.0"}:
        raise ValueError("Unsupported OCI layout")
    index = read_json(layout / "index.json")
    if not isinstance(index, dict):
        raise ValueError("OCI index must be an object")
    manifests = index.get("manifests", [])
    if type(index.get("schemaVersion")) is not int or index["schemaVersion"] != 2 or not isinstance(manifests, list) or len(manifests) != 1 or not isinstance(manifests[0], dict) or manifests[0].get("digest") != image["image_digest"]:
        raise ValueError("OCI image digest mismatch")

    def descriptor(item, media_types):
        if not isinstance(item, dict) or not isinstance(item.get("digest"), str) or not DIGEST.fullmatch(item["digest"]) or type(item.get("size")) is not int or item.get("mediaType") not in media_types:
            raise ValueError("Invalid OCI descriptor")
        path = layout / "blobs/sha256" / item["digest"][7:]
        if not path.is_file() or path.stat().st_size != item["size"]:
            raise ValueError("Missing or truncated OCI blob")
        return path

    manifest = read_json(descriptor(manifests[0], MANIFEST_TYPES))
    if not isinstance(manifest, dict) or type(manifest.get("schemaVersion")) is not int or manifest["schemaVersion"] != 2 or manifest.get("mediaType") != manifests[0]["mediaType"] or not isinstance(manifest.get("layers"), list) or not 1 <= len(manifest["layers"]) <= 128:
        raise ValueError("Invalid OCI image manifest")
    for layer in manifest["layers"]:
        descriptor(layer, LAYER_TYPES)
    config = read_json(descriptor(manifest.get("config"), CONFIG_TYPES))
    if not isinstance(config, dict) or config.get("os") != "linux" or config.get("architecture") != "amd64":
        raise ValueError("OCI image platform mismatch")
    settings = config.get("config", {})
    if not isinstance(settings, dict) or not isinstance(settings.get("Labels", {}), dict):
        raise ValueError("Invalid OCI image settings")
    if settings.get("User", "") != image["config_user"] or any(settings.get("Labels", {}).get(key) != value for key, value in image["labels"].items()):
        raise ValueError("OCI image user or source labels mismatch")
    return total


def publish(publisher, image, layout):
    immutable = image["repository"] + "@" + image["image_digest"]
    checked([publisher, "push", str(layout), immutable], 900)
    if checked([publisher, "digest", immutable], 60).strip() != image["image_digest"]:
        raise ValueError("Published image digest mismatch")
    # The tag is derived from the digest. Never replace a different manifest.
    tag = "sha256-" + image["image_digest"][7:]
    reference = image["repository"] + ":" + tag
    code, current, error = run([publisher, "digest", reference], 60)
    if code == 0:
        if current.strip() != image["image_digest"]:
            raise ValueError("Refusing to replace an existing image tag")
    else:
        url = "https://ghcr.io/v2/" + image["repository"][len("ghcr.io/"):] + "/manifests/" + tag
        missing = re.fullmatch(r"Error: (?:fetching manifest |GET )?" + re.escape(reference) + r": GET " + re.escape(url) + r": (?:MANIFEST_UNKNOWN|NAME_UNKNOWN): [^\r\n]+", error.strip())
        if current.strip() or missing is None:
            raise ValueError("Registry did not confirm an absent tag; no tag was changed")
        checked([publisher, "tag", immutable, tag], 60)
    if checked([publisher, "digest", reference], 60).strip() != image["image_digest"]:
        raise ValueError("Published tag digest mismatch")
    return immutable


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--plan", type=Path, required=True)
    parser.add_argument("--plan-sha256", required=True)
    parser.add_argument("--release", required=True)
    parser.add_argument("--publisher", required=True)
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()
    if os.environ.get("GITHUB_REPOSITORY") != "hakopod/hakopod" or os.environ.get("GITHUB_REF") != "refs/heads/main":
        raise ValueError("Managed runtime publication requires canonical main")
    if not re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9._/-]{0,127}", args.release):
        raise ValueError("Invalid archive release")
    images = plan(args.plan, args.plan_sha256)
    receipts = []
    for image in images:
        with tempfile.TemporaryDirectory(prefix="managed-runtime-", dir=os.environ["RUNNER_TEMP"]) as directory:
            root = Path(directory)
            checked(["gh", "release", "download", args.release, "--repo", "hakopod/hakopod", "--pattern", image["archive"], "--dir", str(root)], 900)
            size = inspect_archive(root / image["archive"], image, root / "layout")
            reference = publish(args.publisher, image, root / "layout")
            receipts.append({"image": reference, "archive_sha256": image["archive_sha256"], "unpacked_bytes": size, "platform": image["platform"]})
    args.output.write_text(json.dumps({"schema_version": 1, "plan_sha256": args.plan_sha256, "images": receipts, "native_qualification": False}, indent=2) + "\n")
    print("Published " + str(len(receipts)) + " inspected runtime images. Native qualification and anonymous pull verification remain separate.")


if __name__ == "__main__":
    try:
        main()
    except ValueError as error:
        raise SystemExit("Managed runtime publication stopped: " + str(error)) from None
    except (OSError, KeyError, TypeError, subprocess.TimeoutExpired) as error:
        raise SystemExit("Managed runtime publication stopped: " + type(error).__name__) from None
