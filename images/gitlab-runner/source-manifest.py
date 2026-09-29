#!/usr/bin/env python3
"""Bind native image qualification to the complete transport source and toolchain."""
import argparse
import hashlib
import json
from pathlib import Path
import subprocess


def sha256(path):
    with path.open("rb") as source:
        return hashlib.file_digest(source, "sha256").hexdigest()


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--go", required=True, type=Path)
    parser.add_argument("--output", required=True, type=Path)
    args = parser.parse_args()
    root = Path(__file__).resolve().parent
    compiler = args.go.resolve(strict=True)
    version = subprocess.check_output([str(compiler), "version"], timeout=10).decode().strip()
    if version not in ("go version go1.26.8 linux/amd64", "go version go1.26.8 linux/arm64"):
        raise SystemExit("Use the qualified Go 1.26.8 Linux toolchain")
    files = [root / name for name in (
        "source.lock.json", "transport.patch", "cache.patch", "build-native.sh", "source-manifest.py", "Dockerfile", "LICENSE.upstream",
    )]
    files.extend((root / "overlay").rglob("*.go.in"))
    manifest = {
        "schema_version": 1,
        "source_commit": json.loads((root / "source.lock.json").read_text())["commit"],
        "toolchain": {"version": version, "go_binary_sha256": sha256(compiler)},
        "files": {str(path.relative_to(root)): sha256(path) for path in sorted(files)},
    }
    # The source digest is SHA256 of these exact canonical UTF-8 JSON bytes.
    encoded = (json.dumps(manifest, sort_keys=True, separators=(",", ":")) + "\n").encode()
    args.output.write_bytes(encoded)
    print(hashlib.sha256(encoded).hexdigest())


if __name__ == "__main__":
    main()
