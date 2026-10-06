#!/usr/bin/env python3
"""Export only Oracle's v4 SIDB schema, without cluster-wide webhook services."""

import argparse
from pathlib import Path
import sys
import yaml

sys.path.insert(0, str(Path(__file__).resolve().parents[1] / "installer"))
from oracle_free_controller import render_bytes


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("source", type=Path)
    parser.add_argument("output", type=Path)
    args = parser.parse_args()
    if args.output.exists():
        raise SystemExit("Refusing to replace an existing CRD artifact")
    if args.source.is_symlink() or args.source.stat().st_size > 8 * 1024 * 1024:
        raise SystemExit("Oracle CRD source must be a bounded regular file")
    document = render_bytes(args.source.read_bytes())[0]
    args.output.write_text(yaml.safe_dump(document, sort_keys=False))


if __name__ == "__main__":
    main()
