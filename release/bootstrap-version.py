#!/usr/bin/env python3
"""Stamp and inspect bootstrap defaults without executing installer code."""
import argparse
import ast
import json
from pathlib import Path
import re
import tarfile

LIMIT = 128 * 1024
START = "<<'HAKOPOD_BOOTSTRAP_PY'\n"
END = '\nHAKOPOD_BOOTSTRAP_PY\n'


def valid_version(version):
    if len(version) > 64 or not re.fullmatch(
            r'(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)(?:-[A-Za-z0-9]+(?:[.-][A-Za-z0-9]+)*)?', version):
        raise ValueError('Invalid bootstrap release version')
    return version


def declaration(script):
    if len(script.encode()) > LIMIT or script.count(START) != 1 or script.count(END) != 1:
        raise ValueError('Invalid bootstrap Python section')
    prefix, body = script.split(START)
    body, suffix = body.split(END)
    tree = ast.parse(body)
    writes = [node for node in ast.walk(tree) if isinstance(node, ast.Name)
              and node.id == 'DEFAULT_VERSION' and isinstance(node.ctx, (ast.Store, ast.Del))]
    # These bindings are strings in the AST rather than Name(Store) nodes.
    for node in ast.walk(tree):
        if isinstance(node, (ast.FunctionDef, ast.AsyncFunctionDef, ast.ClassDef, ast.ExceptHandler,
                             ast.MatchAs, ast.MatchStar)) and node.name == 'DEFAULT_VERSION':
            writes.append(node)
        elif isinstance(node, ast.alias) and (node.asname or node.name.split('.')[0]) == 'DEFAULT_VERSION':
            writes.append(node)
        elif isinstance(node, ast.arg) and node.arg == 'DEFAULT_VERSION':
            writes.append(node)
        elif isinstance(node, ast.MatchMapping) and node.rest == 'DEFAULT_VERSION':
            writes.append(node)
    assignments = [node for node in ast.walk(tree) if isinstance(node, ast.Assign)
                   and any(isinstance(target, ast.Name) and target.id == 'DEFAULT_VERSION' for target in node.targets)]
    if len(assignments) != 1 or len(writes) != 1:
        raise ValueError('Bootstrap must have exactly one DEFAULT_VERSION declaration')
    node = assignments[0]
    if (node not in tree.body or node.col_offset != 0 or len(node.targets) != 1 or node.lineno != node.end_lineno
            or not isinstance(node.value, ast.Constant) or not isinstance(node.value.value, str)):
        raise ValueError('Bootstrap DEFAULT_VERSION must be a top-level string literal')
    remainder = body.splitlines()[node.lineno - 1].encode()[node.end_col_offset:].strip()
    if remainder and not remainder.startswith(b'#'):
        raise ValueError('Bootstrap DEFAULT_VERSION must occupy its own line')
    return prefix, body, suffix, node


def default_version(script):
    return valid_version(declaration(script)[3].value.value)


def render(script, version):
    valid_version(version)
    prefix, body, suffix, node = declaration(script)
    lines = body.splitlines(keepends=True)
    lines[node.lineno - 1] = 'DEFAULT_VERSION = ' + json.dumps(version) + '\n'
    result = prefix + START + ''.join(lines) + END + suffix
    if default_version(result) != version:
        raise ValueError('Bootstrap stamping failed')
    return result


def verify_artifacts(directory, version):
    valid_version(version)
    script = directory / 'installer.sh'
    kit = directory / f'hakopod_{version}_installer.tar.gz'
    if script.is_symlink() or not script.is_file() or script.stat().st_size > LIMIT:
        raise ValueError('Missing or invalid standalone bootstrap')
    standalone = script.read_text()
    if default_version(standalone) != version:
        raise ValueError('Standalone bootstrap default differs from release version')
    if kit.is_symlink() or not kit.is_file():
        raise ValueError('Missing installer kit')
    expected = f'hakopod_{version}_installer/scripts/installer.sh'
    found = []
    with tarfile.open(kit, 'r:gz') as archive:
        for index, member in enumerate(archive):
            if index >= 4096:
                raise ValueError('Installer kit has too many entries')
            if member.name != expected:
                continue
            if not member.isfile() or member.size > LIMIT:
                raise ValueError('Invalid packaged bootstrap')
            found.append(archive.extractfile(member).read(LIMIT + 1).decode())
    if len(found) != 1 or default_version(found[0]) != version or found[0] != standalone:
        raise ValueError('Packaged bootstrap must match the standalone release bootstrap')
    return version


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('directory', type=Path)
    parser.add_argument('version')
    args = parser.parse_args()
    verify_artifacts(args.directory, args.version)
    print(f'Both generated bootstraps default to {args.version}.')
