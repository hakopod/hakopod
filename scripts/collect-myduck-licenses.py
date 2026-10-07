#!/usr/bin/env python3
"""Collect bounded license and notice files for a built MyDuck source tree."""

import argparse
import json
from pathlib import Path
import shutil
import subprocess

NAMES = ("LICENSE", "LICENSE.txt", "LICENSE.md", "NOTICE", "NOTICE.txt", "NOTICE.md", "COPYING")


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("source", type=Path)
    parser.add_argument("output", type=Path)
    args = parser.parse_args()
    args.output.mkdir(parents=True, exist_ok=False)
    process = subprocess.run(
        ["go", "list", "-deps", "-json", "."], cwd=args.source, check=True,
        stdout=subprocess.PIPE, text=True,
    )
    decoder, offset, modules = json.JSONDecoder(), 0, {}
    while offset < len(process.stdout):
        while offset < len(process.stdout) and process.stdout[offset].isspace():
            offset += 1
        if offset == len(process.stdout):
            break
        package, offset = decoder.raw_decode(process.stdout, offset)
        module = package.get("Module")
        if module and module.get("Dir") and module.get("Path"):
            modules[module["Path"]] = Path(module["Dir"])
    modules["github.com/apecloud/myduckserver"] = args.source
    copied = 0
    for module, directory in sorted(modules.items()):
        target = args.output / module.replace("/", "_")
        for name in NAMES:
            source = directory / name
            if not source.is_file():
                continue
            if source.stat().st_size > 1024 * 1024:
                raise ValueError(f"license file exceeds bound: {source}")
            target.mkdir(parents=True, exist_ok=True)
            shutil.copyfile(source, target / name)
            copied += 1
            if copied > 512:
                raise ValueError("license inventory exceeds bound")
    if not (args.output / "github.com_apecloud_myduckserver" / "LICENSE").is_file():
        raise ValueError("upstream MyDuck LICENSE is missing")


if __name__ == "__main__":
    main()
