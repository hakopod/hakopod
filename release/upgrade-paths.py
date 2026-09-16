#!/usr/bin/env python3
"""Explicit upgrade candidates. Publication requires matching native host evidence."""
import argparse
import json
from pathlib import Path


def sources(version, required=False):
    policy = json.loads(Path(__file__).with_suffix('.json').read_text())
    if required and version not in policy:
        raise ValueError('Declare upgrade source versions in release/upgrade-paths.json before cutting a release')
    return policy.get(version, [])


def matrix(version):
    cases = [('ubuntu-24.04', 'amd64', 'managed'), ('ubuntu-24.04-arm', 'arm64', 'managed'),
             ('ubuntu-24.04', 'amd64', 'local'), ('ubuntu-24.04', 'amd64', 'external')]
    return {'include': [dict(runner=runner, arch=arch, mode=mode, source=source,
                             suffix='fresh' if not source else source)
                        for source in ['', *sources(version)] for runner, arch, mode in cases]}


if __name__ == '__main__':
    parser = argparse.ArgumentParser()
    parser.add_argument('--version', required=True)
    parser.add_argument('--matrix', action='store_true')
    parser.add_argument('--require-policy', action='store_true')
    args = parser.parse_args()
    declared = sources(args.version, required=args.require_policy)
    print(json.dumps(matrix(args.version) if args.matrix else declared))
