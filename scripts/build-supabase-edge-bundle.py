#!/usr/bin/env python3
"""Reproduce the reviewed Supabase Edge Runtime bundle with two fresh caches."""

import argparse
import base64
import hashlib
import json
from pathlib import Path
import shutil
import subprocess
import urllib.request

IMAGE = "docker.io/supabase/edge-runtime@sha256:b331c6422f4f4bebd6052357da5f3c87610128c8cb530390ca024eb68cf4bf2e"
JOSE_META = "https://jsr.io/@panva/jose/6.2.12_meta.json"
JOSE_LICENSE = "https://jsr.io/@panva/jose/6.2.12/LICENSE.md"
MAX_DOWNLOAD = 1 << 20


def download(url: str) -> bytes:
    request = urllib.request.Request(url, headers={"User-Agent": "Deno/2.0"})
    with urllib.request.urlopen(request, timeout=30) as response:
        value = response.read(MAX_DOWNLOAD + 1)
    if not value or len(value) > MAX_DOWNLOAD:
        raise RuntimeError(f"invalid bounded download from {url}")
    return value


def digest(value: bytes) -> str:
    return hashlib.sha256(value).hexdigest()


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--repository", type=Path, required=True)
    parser.add_argument("--work", type=Path, required=True)
    args = parser.parse_args()
    repository = args.repository.resolve()
    work = args.work.resolve()
    source = repository / "internal/managedplatform/supabase-assets/functions/main/index.ts"
    if work.exists() or not source.is_file() or source.is_symlink():
        raise RuntimeError("work must be new and the router source must be a regular file")
    if "jsr:@panva/jose@6.2.12" not in source.read_text():
        raise RuntimeError("router does not pin the reviewed JOSE release")
    work.mkdir(mode=0o700)
    source_dir = work / "source/main"
    source_dir.mkdir(parents=True)
    shutil.copyfile(source, source_dir / "index.ts")

    bundles = []
    for attempt in (1, 2):
        output = work / f"output-{attempt}"
        cache = work / f"cache-{attempt}"
        output.mkdir(mode=0o777)
        cache.mkdir(mode=0o777)
        command = [
            "docker", "run", "--rm", "--network", "bridge", "-e", "DENO_DIR=/deno-dir",
            "-v", f"{work / 'source'}:/src:ro", "-v", f"{output}:/out", "-v", f"{cache}:/deno-dir",
            IMAGE, "bundle", "--entrypoint", "/src/main/index.ts", "--output", "/out/main.eszip",
            "--checksum", "sha256", "--timeout", "120",
        ]
        result = subprocess.run(command, capture_output=True, text=True, timeout=180)
        (work / f"bundle-{attempt}.log").write_text(result.stdout + result.stderr)
        if result.returncode:
            raise RuntimeError(f"bundle attempt {attempt} failed")
        bundles.append((output / "main.eszip").read_bytes())
    if bundles[0] != bundles[1]:
        raise RuntimeError("fresh-cache bundle outputs differ")

    metadata = download(JOSE_META)
    license_text = download(JOSE_LICENSE)
    parsed = json.loads(metadata)
    license_entry = parsed.get("manifest", {}).get("/LICENSE.md", {})
    if license_entry.get("checksum") != "sha256-" + digest(license_text):
        raise RuntimeError("JOSE metadata does not bind the downloaded license")

    assets = repository / "internal/managedplatform/supabase-assets/functions"
    (assets / "vendor").mkdir(exist_ok=True)
    (assets / "main.eszip.b64").write_bytes(base64.b64encode(bundles[0]) + b"\n")
    (assets / "vendor/jose-6.2.12-meta.json").write_bytes(metadata)
    (assets / "vendor/jose-6.2.12-LICENSE.md").write_bytes(license_text)
    receipt = {
        "schema_version": 1,
        "image": IMAGE,
        "router_sha256": digest(source.read_bytes()),
        "eszip_sha256": digest(bundles[0]),
        "base64_sha256": digest(base64.b64encode(bundles[0]) + b"\n"),
        "jose_metadata_sha256": digest(metadata),
        "jose_license_sha256": digest(license_text),
        "attempts": 2,
        "fresh_cache_per_attempt": True,
    }
    (work / "receipt.json").write_text(json.dumps(receipt, indent=2, sort_keys=True) + "\n")
    print(json.dumps(receipt, separators=(",", ":"), sort_keys=True))


if __name__ == "__main__":
    main()
