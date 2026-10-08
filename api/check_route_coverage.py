#!/usr/bin/env python3
"""Check literal Go API route declarations against the public contract.

This is source coverage, not runtime verification. Dynamic route expressions
and transports that dispatch several methods need separate review.
"""
import json
from pathlib import Path
import re
import sys

# Read Go string tokens. Ignore comments so examples do not become routes.
TOKENS = re.compile(r'//[^\n]*|/\*.*?\*/|"(?:\\.|[^"\\])*"|`[^`]*`', re.S)
ROUTE = re.compile(r'(GET|POST|PUT|PATCH|DELETE|HEAD|OPTIONS) (/api/v1(?:/[^\s]*)?)')
METHODS = {'get', 'post', 'put', 'patch', 'delete', 'head', 'options'}


def literal_routes(source):
    result = set()
    for token in TOKENS.finditer(source):
        raw = token.group()
        if raw.startswith('//') or raw.startswith('/*'):
            continue
        value = raw[1:-1]
        match = ROUTE.fullmatch(value)
        if match:
            result.add(match.groups())
    return result


def check_coverage(declarations, contract, exclusions):
    errors = []
    for route in sorted(declarations - contract - exclusions.keys()):
        errors.append(f'Add this route to OpenAPI or record its exclusion: {route[0]} {route[1]}')
    for route, reason in sorted(exclusions.items()):
        if not isinstance(reason, str) or not reason.strip():
            errors.append(f'Explain this route exclusion: {route[0]} {route[1]}')
        if route not in declarations:
            errors.append(f'Remove this obsolete route exclusion: {route[0]} {route[1]}')
        if route in contract:
            errors.append(f'Remove this exclusion for a documented route: {route[0]} {route[1]}')
    return errors


def main():
    root = Path(__file__).resolve().parent.parent
    document = json.loads((root / 'api/openapi.json').read_text())
    contract = {(method.upper(), '/api/v1' + path)
                for path, methods in document['paths'].items()
                for method in methods if method in METHODS}
    declarations = set()
    for path in (root / 'internal/api').glob('*.go'):
        if not path.name.endswith('_test.go'):
            declarations.update(literal_routes(path.read_text()))
    entries = json.loads((root / 'api/route_coverage_exclusions.json').read_text())
    exclusions = {tuple(key.split(' ', 1)): reason for key, reason in entries.items()}
    errors = check_coverage(declarations, contract, exclusions)
    if errors:
        print('\n'.join(errors), file=sys.stderr)
        return 1
    print(f'{len(declarations)} literal API route declarations: '
          f'{len(declarations & contract)} in OpenAPI, {len(exclusions)} explicit exclusions.')
    print('Dynamic route expressions and alternate transports require separate review.')
    return 0


if __name__ == '__main__':
    raise SystemExit(main())
