#!/usr/bin/env python3
"""Create a deterministic numeric-user OCI image without changing its layers."""
import argparse
import hashlib
import json
import os
from pathlib import Path, PurePosixPath
import re
import tarfile
import tempfile

DIGEST = re.compile(r"sha256:[0-9a-f]{64}")
USER = re.compile(r"[1-9][0-9]{0,9}:[1-9][0-9]{0,9}")
MAX_JSON = 1024 * 1024
MAX_ARCHIVE = 2 * 1024**3
MAX_UNPACKED = 4 * 1024**3
SOURCE = "https://github.com/hakopod/hakopod"


def encoded(value):
    return json.dumps(value, sort_keys=True, separators=(",", ":")).encode()


def read_json(source, name):
    member = source.getmember(name)
    if not member.isfile() or not 0 < member.size <= MAX_JSON:
        raise ValueError("invalid OCI JSON member")
    return json.load(source.extractfile(member))


def read_blob_json(source, descriptor):
    digest = descriptor.get("digest")
    name = blob_name(digest)
    member = source.getmember(name)
    if not member.isfile() or member.size != descriptor.get("size") or not 0 < member.size <= MAX_JSON:
        raise ValueError("invalid OCI JSON descriptor")
    value = source.extractfile(member).read(MAX_JSON + 1)
    if "sha256:" + hashlib.sha256(value).hexdigest() != digest:
        raise ValueError("OCI JSON blob checksum mismatch")
    return json.loads(value)


def blob_name(digest):
    if not isinstance(digest, str) or not DIGEST.fullmatch(digest):
        raise ValueError("invalid OCI digest")
    return "blobs/sha256/" + digest[7:]


def add_bytes(output, name, value):
    import io
    member = tarfile.TarInfo(name)
    member.size = len(value)
    member.mode = 0o644
    member.mtime = member.uid = member.gid = 0
    output.addfile(member, io.BytesIO(value))


class HashingReader:
    def __init__(self, stream):
        self.stream, self.digest = stream, hashlib.sha256()

    def read(self, size=-1):
        value = self.stream.read(size)
        self.digest.update(value)
        return value


def wrap(source_path, output_path, user, labels):
    source_path, output_path = Path(source_path), Path(output_path)
    if source_path.is_symlink() or not source_path.is_file() or not 0 < source_path.stat().st_size <= MAX_ARCHIVE or output_path.exists() or output_path.is_symlink():
        raise ValueError("input must be a file and output must be fresh")
    if not USER.fullmatch(user):
        raise ValueError("runtime user must be a positive numeric UID:GID")
    if labels.get("org.opencontainers.image.source") != SOURCE:
        raise ValueError("runtime wrapper must identify the canonical source")
    if not 1 <= len(labels) <= 32 or any(not isinstance(k, str) or not isinstance(v, str) or not k or len(k) > 128 or len(v) > 512 for k, v in labels.items()):
        raise ValueError("invalid runtime labels")
    temporary = None
    try:
        with tarfile.open(source_path, "r:*") as source:
            names, total = set(), 0
            for member in source.getmembers():
                name = PurePosixPath(member.name)
                if member.name in names or len(names) >= 1024 or name.is_absolute() or ".." in name.parts or not (member.isdir() or member.isfile()):
                    raise ValueError("unsafe or oversized OCI inventory")
                names.add(member.name)
                total += member.size
                if member.size < 0 or total > MAX_UNPACKED:
                    raise ValueError("OCI inventory exceeds its size bound")
            layout = read_json(source, "oci-layout")
            index = read_json(source, "index.json")
            manifests = index.get("manifests", [])
            if layout != {"imageLayoutVersion": "1.0.0"} or index.get("schemaVersion") != 2 or len(manifests) != 1:
                raise ValueError("runtime wrapper requires one OCI image manifest")
            base_manifest_digest = manifests[0].get("digest")
            manifest = read_blob_json(source, manifests[0])
            base_config_digest = manifest.get("config", {}).get("digest")
            config = read_blob_json(source, manifest.get("config", {}))
            if config.get("os") != "linux" or config.get("architecture") != "amd64" or not isinstance(config.get("config"), dict):
                raise ValueError("runtime wrapper requires linux/amd64")
            settings = config["config"]
            settings["User"] = user
            current_labels = settings.get("Labels", {})
            if not isinstance(current_labels, dict):
                raise ValueError("invalid OCI config labels")
            current_labels.update(labels)
            current_labels["io.hakopod.managed-runtime.base-manifest"] = base_manifest_digest
            current_labels["io.hakopod.managed-runtime.base-config"] = base_config_digest
            if len(current_labels) > 32:
                raise ValueError("wrapped OCI config has too many labels")
            settings["Labels"] = current_labels
            config_bytes = encoded(config)
            config_digest = "sha256:" + hashlib.sha256(config_bytes).hexdigest()
            manifest["config"]["digest"], manifest["config"]["size"] = config_digest, len(config_bytes)
            manifest_bytes = encoded(manifest)
            manifest_digest = "sha256:" + hashlib.sha256(manifest_bytes).hexdigest()
            manifests[0]["digest"], manifests[0]["size"] = manifest_digest, len(manifest_bytes)
            index_bytes = encoded(index)
            handle = tempfile.NamedTemporaryFile(prefix="." + output_path.name + ".", dir=output_path.parent, delete=False)
            temporary = Path(handle.name)
            handle.close()
            with tarfile.open(temporary, "w", format=tarfile.PAX_FORMAT) as output:
                for directory in ("blobs", "blobs/sha256"):
                    member = tarfile.TarInfo(directory)
                    member.type, member.mode, member.mtime = tarfile.DIRTYPE, 0o755, 0
                    output.addfile(member)
                add_bytes(output, "oci-layout", encoded(layout))
                add_bytes(output, "index.json", index_bytes)
                add_bytes(output, blob_name(manifest_digest), manifest_bytes)
                add_bytes(output, blob_name(config_digest), config_bytes)
                layers, seen_layers = manifest.get("layers", []), set()
                if not isinstance(layers, list) or not 1 <= len(layers) <= 128:
                    raise ValueError("invalid OCI layer inventory")
                for layer in layers:
                    name = blob_name(layer.get("digest"))
                    if name in seen_layers:
                        continue
                    seen_layers.add(name)
                    member = source.getmember(name)
                    if not member.isfile() or member.size != layer.get("size"):
                        raise ValueError("missing OCI layer")
                    target = tarfile.TarInfo(name)
                    target.size, target.mode, target.mtime = member.size, 0o644, 0
                    stream = HashingReader(source.extractfile(member))
                    output.addfile(target, stream)
                    if "sha256:" + stream.digest.hexdigest() != layer["digest"]:
                        raise ValueError("OCI layer checksum mismatch")
        if temporary.stat().st_size > MAX_ARCHIVE:
            raise ValueError("wrapped OCI archive exceeds its size bound")
        os.link(temporary, output_path)
        temporary.unlink()
        temporary = None
        return manifest_digest, config_digest
    except Exception:
        if temporary is not None:
            temporary.unlink(missing_ok=True)
        raise


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--input", type=Path, required=True)
    parser.add_argument("--output", type=Path, required=True)
    parser.add_argument("--user", required=True)
    parser.add_argument("--label", action="append", default=[])
    args = parser.parse_args()
    label_values = {}
    for item in args.label:
        key, separator, value = item.partition("=")
        if not separator or key in label_values:
            raise SystemExit("invalid or duplicate label")
        label_values[key] = value
    try:
        print(*wrap(args.input, args.output, args.user, label_values))
    except (OSError, KeyError, TypeError, ValueError, json.JSONDecodeError, tarfile.TarError) as error:
        raise SystemExit("Runtime OCI wrapper stopped: " + str(error)) from None
