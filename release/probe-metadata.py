#!/usr/bin/env python3
"""Bind native probe smoke reports to the anonymously readable release index."""
import json
from pathlib import Path
import re
import sys


def assemble(directory, tag, revision):
    if not re.fullmatch(r'v[0-9]+\.[0-9]+\.[0-9]+(?:-[A-Za-z0-9.-]+)?', tag):
        raise ValueError('Invalid probe release tag')
    if not re.fullmatch(r'[0-9a-f]{40}', revision):
        raise ValueError('Invalid source revision')
    reference = (directory / 'probe-image.txt').read_text().strip()
    match = re.fullmatch(r'(ghcr.io/[a-z0-9_.-]+/[a-z0-9_.-]+)@(sha256:[0-9a-f]{64})', reference)
    if not match:
        raise ValueError('Invalid digest-pinned probe reference')
    descriptors = json.loads((directory / 'probe-index.json').read_text())['manifests']
    if len(descriptors) != 2:
        raise ValueError('Probe index must contain exactly two native images')
    platforms = {}
    for descriptor in descriptors:
        platform = descriptor['platform']
        arch = platform['architecture']
        if platform['os'] != 'linux' or arch not in ('amd64', 'arm64') or arch in platforms:
            raise ValueError('Unexpected or duplicate probe platform')
        expected = f"{match[1]}@{descriptor['digest']}"
        if (directory / f'probe-{arch}.txt').read_text().strip() != expected:
            raise ValueError('Probe index differs from the tested image digest')
        report = json.loads((directory / f'probe-smoke-{arch}.json').read_text())
        if report['architecture'] != arch or report['execution'] != 'native' or report['passed'] is not True:
            raise ValueError('Missing successful native probe smoke')
        platforms[arch] = {'image': expected, 'smoke': report}
    result = {'schema_version': 1, 'tag': tag, 'source_revision': revision,
              'image': reference, 'platforms': platforms, 'anonymous_manifest_access': True}
    (directory / 'probe-image.json').write_text(json.dumps(result, indent=2) + '\n')
    return result


if __name__ == '__main__':
    assemble(Path(sys.argv[1]), sys.argv[2], sys.argv[3])
