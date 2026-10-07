#!/usr/bin/env python3
"""Assemble the Hakopod MyDuck OCI archive from frozen, verified VM inputs."""

import argparse
import hashlib
import importlib.util
import json
import os
from pathlib import Path, PurePosixPath
import shutil
import subprocess
import tarfile
import tempfile

BASE_IMAGE = "python@sha256:2b4f19dae3a777dfc3b76730bda1e82e1f66ab2a2686fa93ca78edbfb4f04ffe"
UPSTREAM_COMMIT = "6e3427591fd8895df9585969e7256f958fb639bb"
VERSION = "0.1.0-hakopod.3"
SERVER_SHA256 = "96318a0c924e6801105b7eb3d37cfc660e87a0b404ad8f5efeacec97c761ad59"
HELPER_SHA256 = "32babdbeab26339e270bcd341fe056cfd94fc30e38f9fa97ed833664f8233eda"
WHEELS = {
    "sqlglot-30.17.0-py3-none-any.whl": "84435ac283a60173da31b5fd7d11a725037a1c3fd6ed1e21fb065de74ddb579f",
    "sqlglotc-30.17.0-cp312-cp312-manylinux2014_x86_64.manylinux_2_17_x86_64.manylinux_2_28_x86_64.whl": "24c0df324d3b8cb1ab65b4139b52dc4de2291ba0e3ce4086ef7fcd0b96555ba3",
    "sqlglotrs-0.13.0-py3-none-any.whl": "6b934a244b16f26fca50974328a2ebc7689583c59f06203cebb46e2e6e8d93a7",
}


def sha(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as source:
        for block in iter(lambda: source.read(1024 * 1024), b""):
            digest.update(block)
    return digest.hexdigest()


def blob(source: Path, target: Path) -> tuple[str, int, bytes]:
    data = source.read_bytes()
    digest = hashlib.sha256(data).hexdigest()
    (target / digest).write_bytes(data)
    return digest, len(data), data


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("base", type=Path, help="retained VM scratch root")
    parser.add_argument("manifest", type=Path, help="frozen source-build manifest")
    parser.add_argument("patch", type=Path, help="frozen upstream patch")
    parser.add_argument("overlay_manifest", type=Path, help="complete retained runtime overlay inventory")
    args = parser.parse_args()
    if os.geteuid() != 0:
        raise SystemExit("run with sudo on the approved Linux AMD64 VM")

    base = args.base.resolve()
    image = base / "image-final-v22"
    source_root = base / "image-final-v9" / "rootfs"
    wheels = base / "sqlglot-30.17.0-wheels"
    server = base / "final-bin-v22" / "myduckserver"
    helper = base / "final-bin-v22" / "hakopod-myduck-storage"
    crane = base / "crane" / "crane"
    publisher = base / "publish-managed-runtime.py"

    if image.exists():
        raise SystemExit(f"output already exists: {image}")
    if sha(server) != SERVER_SHA256 or sha(helper) != HELPER_SHA256:
        raise SystemExit("runtime binary hash mismatch")
    for name, expected in WHEELS.items():
        if sha(wheels / name) != expected:
            raise SystemExit(f"wheel hash mismatch: {name}")
    manifest = json.loads(args.manifest.read_text())
    if manifest.get("upstream_commit") != UPSTREAM_COMMIT:
        raise SystemExit("source manifest upstream mismatch")
    patch_sha = sha(args.patch)
    expected_patch = manifest.get("files", {}).get("patches/myduck/0001-harden-managed-runtime.patch")
    if patch_sha != expected_patch:
        raise SystemExit("source manifest patch mismatch")
    if sha(args.overlay_manifest) != manifest.get("files", {}).get("patches/myduck/runtime-overlay-manifest.json"):
        raise SystemExit("source manifest overlay mismatch")
    if sha(Path(__file__)) != manifest.get("files", {}).get("scripts/package-managed-myduck.py"):
        raise SystemExit("source manifest packaging recipe mismatch")

    overlay = json.loads(args.overlay_manifest.read_text())
    actual_paths = {"."}
    for path in source_root.rglob("*"):
        actual_paths.add(path.relative_to(source_root).as_posix())
    expected_paths = {item["path"] for item in overlay.get("entries", [])}
    if actual_paths != expected_paths:
        raise SystemExit("retained runtime overlay path inventory mismatch")
    for item in overlay["entries"]:
        path = source_root if item["path"] == "." else source_root / item["path"]
        info = path.lstat()
        if (format(info.st_mode & 0o7777, "04o"), info.st_uid, info.st_gid) != (item["mode"], item["uid"], item["gid"]):
            raise SystemExit(f"retained runtime overlay metadata mismatch: {item['path']}")
        if item["type"] == "file" and (not path.is_file() or path.is_symlink() or path.stat().st_size != item["size"] or sha(path) != item["sha256"]):
            raise SystemExit(f"retained runtime overlay file mismatch: {item['path']}")
        if item["type"] == "directory" and (not path.is_dir() or path.is_symlink()):
            raise SystemExit(f"retained runtime overlay directory mismatch: {item['path']}")
        if item["type"] == "symlink" and (not path.is_symlink() or os.readlink(path) != item["target"]):
            raise SystemExit(f"retained runtime overlay link mismatch: {item['path']}")

    shutil.copytree(source_root, image / "rootfs", symlinks=True)
    rootfs = image / "rootfs"
    shutil.copyfile(server, rootfs / "usr/local/bin/myduckserver")
    shutil.copyfile(helper, rootfs / "usr/local/bin/hakopod-myduck-storage")
    for path in rootfs.rglob("*"):
        if path.is_symlink():
            continue
        os.chown(path, 0, 0)
        path.chmod(0o755 if path.is_dir() else 0o644)
    for name in ("myduckserver", "hakopod-myduck-storage"):
        (rootfs / "usr/local/bin" / name).chmod(0o555)
    data_dir = rootfs / "var/lib/myduck"
    os.chown(data_dir, 1000, 1000)
    data_dir.chmod(0o700)
    license_files = [p for p in (rootfs / "usr/share/licenses/myduck").rglob("*") if p.is_file()]
    if len(license_files) != 184:
        raise SystemExit(f"unexpected license inventory: {len(license_files)}")

    layer = image / "runtime-layer-owned.tar"
    subprocess.run([
        "tar", "--sort=name", "--mtime=@0", "--numeric-owner", "-cf", str(layer), "."
    ], cwd=rootfs, check=True)
    labels = {
        "org.opencontainers.image.source": "https://github.com/hakopod/hakopod",
        "org.opencontainers.image.title": "Hakopod-managed-MyDuck",
        "org.opencontainers.image.version": VERSION,
        "io.hakopod.myduck.upstream": UPSTREAM_COMMIT,
        "io.hakopod.myduck.patch-sha256": patch_sha,
    }
    docker_archive = image / "managed-myduck-docker.tar"
    command = [str(crane), "mutate", "--platform=linux/amd64", "--append", str(layer),
               "--user=1000:1000", "--entrypoint=/usr/local/bin/myduckserver",
               "--workdir=/var/lib/myduck", "--exposed-ports=3306,5432"]
    command.extend(f"--label={key}={value}" for key, value in labels.items())
    command.extend([f"--output={docker_archive}", BASE_IMAGE])
    subprocess.run(command, check=True)

    unpack, oci = image / "docker", image / "oci"
    unpack.mkdir()
    blobs = oci / "blobs/sha256"
    blobs.mkdir(parents=True)
    with tarfile.open(docker_archive) as archive:
        for member in archive:
            path = PurePosixPath(member.name)
            if path.is_absolute() or ".." in path.parts or not member.isfile():
                continue
            output = unpack.joinpath(*path.parts)
            output.parent.mkdir(parents=True, exist_ok=True)
            with archive.extractfile(member) as source:
                output.write_bytes(source.read())
    entries = json.loads((unpack / "manifest.json").read_text())
    if len(entries) != 1:
        raise SystemExit("unexpected Docker archive manifest count")
    entry = entries[0]
    config_path = unpack / entry["Config"]
    config = json.loads(config_path.read_text())
    config.setdefault("config", {})["Cmd"] = []
    config_bytes = json.dumps(config, separators=(",", ":"), sort_keys=True).encode()
    old_config = entry["Config"]
    new_config = hashlib.sha256(config_bytes).hexdigest() + ".json"
    (unpack / new_config).write_bytes(config_bytes)
    entry["Config"] = new_config
    (unpack / "manifest.json").write_text(json.dumps(entries, separators=(",", ":")))
    config_path.unlink()
    with tarfile.open(docker_archive, "w") as archive:
        for path in sorted(unpack.rglob("*")):
            info = archive.gettarinfo(str(path), str(path.relative_to(unpack)))
            info.mtime = info.uid = info.gid = 0
            info.uname = info.gname = ""
            if path.is_file():
                with path.open("rb") as source:
                    archive.addfile(info, source)
            else:
                archive.addfile(info)
    config_digest, config_size, _ = blob(unpack / entry["Config"], blobs)
    layers = []
    for name in entry["Layers"]:
        digest, size, data = blob(unpack / name, blobs)
        media = "application/vnd.oci.image.layer.v1.tar+gzip" if data[:2] == b"\x1f\x8b" else "application/vnd.oci.image.layer.v1.tar"
        layers.append({"mediaType": media, "digest": "sha256:" + digest, "size": size})
    oci_manifest = {"schemaVersion": 2, "mediaType": "application/vnd.oci.image.manifest.v1+json",
                    "config": {"mediaType": "application/vnd.oci.image.config.v1+json", "digest": "sha256:" + config_digest, "size": config_size},
                    "layers": layers}
    manifest_bytes = json.dumps(oci_manifest, separators=(",", ":"), sort_keys=True).encode()
    image_digest = hashlib.sha256(manifest_bytes).hexdigest()
    (blobs / image_digest).write_bytes(manifest_bytes)
    index = {"schemaVersion": 2, "manifests": [{"mediaType": oci_manifest["mediaType"], "digest": "sha256:" + image_digest,
             "size": len(manifest_bytes), "platform": {"os": "linux", "architecture": "amd64"}}]}
    (oci / "index.json").write_text(json.dumps(index, separators=(",", ":"), sort_keys=True))
    (oci / "oci-layout").write_text('{"imageLayoutVersion":"1.0.0"}')
    archive_path = image / "managed-myduck.oci.tar"
    with tarfile.open(archive_path, "w") as archive:
        for path in sorted(oci.rglob("*")):
            info = archive.gettarinfo(str(path), str(path.relative_to(oci)))
            info.mtime = info.uid = info.gid = 0
            info.uname = info.gname = ""
            if path.is_file():
                with path.open("rb") as source:
                    archive.addfile(info, source)
            else:
                archive.addfile(info)

    digest = "sha256:" + image_digest
    plan = {"schema_version": 1, "images": [{"archive": archive_path.name, "archive_sha256": sha(archive_path),
            "image_digest": digest, "repository": "ghcr.io/hakopod/managed-myduck", "platform": "linux/amd64",
            "config_user": "1000:1000", "labels": labels}]}
    plan_path = image / "managed-runtime-publication.json"
    plan_path.write_text(json.dumps(plan, indent=2, sort_keys=True) + "\n")
    spec = importlib.util.spec_from_file_location("publisher", publisher)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    with tempfile.TemporaryDirectory(dir=image) as temporary:
        module.inspect_archive(archive_path, plan["images"][0], Path(temporary) / "layout")
    receipt = {"schema_version": 1, "image_reference": "ghcr.io/hakopod/managed-myduck@" + digest,
               "archive_sha256": sha(archive_path), "platform": "linux/amd64", "config_user": "1000:1000",
               "entrypoint": ["/usr/local/bin/myduckserver"],
               "labels": labels, "binaries": {"/usr/local/bin/myduckserver": SERVER_SHA256,
               "/usr/local/bin/hakopod-myduck-storage": HELPER_SHA256},
               "source_build_manifest_sha256": sha(args.manifest)}
    receipt_path = image / "packaging-receipt.json"
    receipt_path.write_text(json.dumps(receipt, indent=2, sort_keys=True) + "\n")
    (image / "image-digest.txt").write_text(digest + "\n")
    print(f"image_digest={digest}")
    print(f"archive_sha256={sha(archive_path)}")
    print(f"plan_sha256={sha(plan_path)}")
    print(f"receipt_sha256={sha(receipt_path)}")


if __name__ == "__main__":
    main()
