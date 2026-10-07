#!/usr/bin/env python3
import pathlib
import subprocess
import sys

SOURCE_SHA = "6e3427591fd8895df9585969e7256f958fb639bb"
ROOT = pathlib.Path(__file__).resolve().parents[1]
PATCH = ROOT / "patches/myduck/0001-harden-managed-runtime.patch"

if len(sys.argv) != 2:
    raise SystemExit("usage: apply-managed-myduck-patches.py UPSTREAM_CHECKOUT")
checkout = pathlib.Path(sys.argv[1]).resolve()

def git(*args: str) -> str:
    return subprocess.check_output(["git", "-C", str(checkout), *args], text=True).strip()

if git("rev-parse", "HEAD") != SOURCE_SHA:
    raise SystemExit("refusing to patch an unexpected MyDuck source revision")
if git("status", "--porcelain"):
    raise SystemExit("refusing to patch a dirty MyDuck checkout")
subprocess.run(["git", "-C", str(checkout), "apply", "--check", str(PATCH)], check=True)
subprocess.run(["git", "-C", str(checkout), "apply", "--index", str(PATCH)], check=True)
