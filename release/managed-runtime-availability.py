#!/usr/bin/env python3
"""Read compiled release gates and retain qualified or held runtime evidence."""
import argparse
import gzip
import hashlib
import json
import os
from pathlib import Path
import re
import runpy
import subprocess
import sys
import tarfile
import tempfile

ROOT = Path(__file__).resolve().parents[1]
GATES = {
    'oracle-free': ('internal/cluster/database_oracle_free_qualification.go', 'oracleFreeReleaseQualified', 'const'),
    'vitess': ('internal/cluster/database_vitess.go', 'vitessReleaseQualified', 'const'),
    'supabase': ('internal/managedplatform/supabase_qualification.go', 'SupabaseReleaseQualified', 'func'),
    'neon': ('internal/managedplatform/neon_qualification.go', 'NeonReleaseQualified', 'func'),
}
MAX_SOURCE_BYTES = 1024 * 1024
MAX_ARCHIVE_BYTES = 128 * 1024 * 1024
MAX_ARCHIVE_FILES = 256
TOKEN = re.compile(r'//[^\n]*|/\*[\s\S]*?\*/|`[^`]*`|"(?:\\[^\n]|[^"\\\n])*"|\'(?:\\[^\n]|[^\'\\\n])*\'|\n|[^\S\n]+|[A-Za-z_][A-Za-z_0-9]*|[0-9][A-Za-z_0-9.]*|[^\s]')


def _tokens(source):
    """Lex comments and strings before examining declarations, with Go semicolons."""
    tokens = []
    for match in TOKEN.finditer(source):
        value = match.group()
        if value in ('"', "'", '`') or (value == '/' and source[match.start():].startswith('/*')):
            raise ValueError('Malformed Go comment or string in release gate source')
        if value.startswith(('//', '/*')):
            value = '\n' if '\n' in value else ''
        elif value.startswith(('"', "'", '`')):
            value = '<literal>'
        if value == '\n':
            if tokens and (re.fullmatch(r'[A-Za-z_0-9][A-Za-z_0-9.]*', tokens[-1]) or tokens[-1] in ('<literal>', ')', ']', '}')):
                tokens.append(';')
        elif value and not value.isspace():
            tokens.append(value)
    return tokens


def _gate(path, name, kind):
    if path.is_symlink() or not path.is_file() or path.stat().st_size > MAX_SOURCE_BYTES:
        raise ValueError('Missing, symbolic or oversized release gate: ' + str(path))
    source = path.read_text()
    if re.search(r'^\s*//\s*(?:go:build|\+build)\b', source, re.M):
        raise ValueError('Release gate cannot have a build constraint: ' + name)
    tokens = _tokens(source)
    top_level, depth = set(), 0
    for index, token in enumerate(tokens):
        if depth == 0:
            top_level.add(index)
        depth += (token == '{') - (token == '}')
        if depth < 0:
            raise ValueError('Unbalanced release gate source: ' + name)
    if depth:
        raise ValueError('Unbalanced release gate source: ' + name)
    definitions = []
    if kind == 'func':
        for index in range(len(tokens) - 1):
            if tokens[index:index + 2] != ['func', name]:
                continue
            if index not in top_level:
                raise ValueError('Release gate must be a top-level declaration: ' + name)
            tail = tokens[index + 2:index + 11]
            if tail[:5] != ['(', ')', 'bool', '{', 'return'] or len(tail) < 7 or tail[5] not in ('true', 'false'):
                raise ValueError('Release gate must return one literal boolean: ' + name)
            end = 7 if tail[6] == ';' else 6
            if len(tail) <= end or tail[end] != '}':
                raise ValueError('Conditional or malformed release gate: ' + name)
            definitions.append(tail[5] == 'true')
    else:
        for index, token in enumerate(tokens):
            if token != 'const':
                continue
            cursor = index + 1
            grouped = cursor < len(tokens) and tokens[cursor] == '('
            if grouped:
                cursor += 1
            while cursor < len(tokens):
                start, depth = cursor, 0
                while cursor < len(tokens):
                    token = tokens[cursor]
                    if depth == 0 and (token == ';' or (grouped and token == ')')):
                        break
                    if token in ('(', '[', '{'):
                        depth += 1
                    elif token in (')', ']', '}'):
                        depth -= 1
                    cursor += 1
                declaration = tokens[start:cursor]
                if name in declaration:
                    if index not in top_level:
                        raise ValueError('Release gate must be a top-level declaration: ' + name)
                    if declaration[:2] == [name, 'bool']:
                        declaration = [name, *declaration[2:]]
                    if len(declaration) != 3 or declaration[:2] != [name, '='] or declaration[2] not in ('true', 'false'):
                        raise ValueError('Release gate must be one literal boolean constant: ' + name)
                    definitions.append(declaration[2] == 'true')
                if not grouped or cursor == len(tokens) or tokens[cursor] == ')':
                    break
                cursor += 1
    if len(definitions) != 1:
        raise ValueError('Release gate is missing or ambiguous: ' + name)
    return definitions[0]


def release_availability(root=ROOT):
    root = Path(root)
    return {runtime: _gate(root / path, name, kind) for runtime, (path, name, kind) in GATES.items()}


def source_revision(root=ROOT):
    top_level = subprocess.check_output(['git', 'rev-parse', '--show-toplevel'], cwd=root, text=True, timeout=15).strip()
    if Path(top_level).resolve() != Path(root).resolve():
        raise ValueError('Release source must be the root of its own Git checkout')
    revision = subprocess.check_output(['git', 'rev-parse', '--verify', 'HEAD'], cwd=root, text=True, timeout=15).strip()
    if not re.fullmatch(r'[0-9a-f]{40}', revision):
        raise ValueError('Release source revision is invalid')
    return revision


def source_gate_hashes(root=ROOT):
    return {path: hashlib.sha256((Path(root) / path).read_bytes()).hexdigest() for path, _, _ in GATES.values()}


def _digest(path):
    if path.is_symlink() or not path.is_file() or path.stat().st_size > MAX_ARCHIVE_BYTES:
        raise ValueError('Release evidence is missing, symbolic or oversized: ' + path.name)
    digest = hashlib.sha256()
    with path.open('rb') as stream:
        for chunk in iter(lambda: stream.read(1024 * 1024), b''):
            digest.update(chunk)
    return digest.hexdigest()


def _archive(source, destination, epoch):
    paths, size = [], 0
    for path in source.rglob('*'):
        if path.is_symlink() or not (path.is_file() or path.is_dir()):
            raise ValueError('Verified runtime evidence contains an unsupported file')
        size += path.stat().st_size if path.is_file() else 0
        if len(paths) >= MAX_ARCHIVE_FILES - 1 or size > MAX_ARCHIVE_BYTES:
            raise ValueError('Verified runtime evidence exceeds archive bounds')
        paths.append(path)
    with destination.open('xb') as stream, gzip.GzipFile(fileobj=stream, mode='wb', filename='', mtime=epoch) as zipped:
        with tarfile.open(fileobj=zipped, mode='w') as archive:
            for path in [source, *sorted(paths)]:
                if path.is_symlink() or not (path.is_file() or path.is_dir()):
                    raise ValueError('Verified runtime evidence contains an unsupported file')
                info = archive.gettarinfo(str(path), arcname=str(Path(source.name) / path.relative_to(source)))
                info.uid = info.gid = 0
                info.uname = info.gname = ''
                info.mtime = epoch
                info.mode = 0o755 if path.is_dir() else 0o644
                if path.is_file():
                    with path.open('rb') as file:
                        archive.addfile(info, file)
                else:
                    archive.addfile(info)


def prepare_evidence(runtime, output, root=ROOT):
    root, output = Path(root), Path(output)
    available = release_availability(root)
    if runtime not in available:
        raise ValueError('Unknown managed runtime')
    if output.exists() or output.is_symlink():
        raise ValueError('Use a fresh managed runtime evidence directory')
    revision, hashes = source_revision(root), source_gate_hashes(root)
    output.parent.mkdir(parents=True, exist_ok=True)
    status = {'schema_version': 1, 'runtime': runtime, 'source_revision': revision,
              'source_gate_sha256': hashes, 'release_runtime_qualified': available[runtime]}
    with tempfile.TemporaryDirectory(prefix='managed-runtime-', dir=output.parent) as temporary:
        stage = Path(temporary)
        payload = stage / 'payload'
        payload.mkdir()
        if available[runtime]:
            qualified = stage / (runtime + '-qualified')
            # Each existing verifier retains its exact source, image and native-evidence checks.
            subprocess.run([sys.executable, '-B', str(root / f'release/verify-{runtime}-runtime.py'), '--output', str(qualified)],
                           cwd=root, env=dict(os.environ, PYTHONDONTWRITEBYTECODE='1'), check=True, timeout=3600)
            archive = payload / f'managed-{runtime}-qualification.tar.gz'
            epoch = int(subprocess.check_output(['git', 'log', '-1', '--format=%ct'], cwd=root, text=True, timeout=15).strip())
            _archive(qualified, archive, epoch)
            status.update(status='qualified', archive=archive.name, archive_sha256=_digest(archive))
        else:
            status.update(status='held', reason='The compiled release gate is closed.')
        if release_availability(root) != available or source_gate_hashes(root) != hashes or source_revision(root) != revision:
            raise ValueError('Release source changed while preparing runtime evidence')
        (payload / f'managed-{runtime}-{status["status"]}.json').write_text(json.dumps(status, indent=2, sort_keys=True) + '\n')
        payload.rename(output)
    return status


def _unique_object(pairs):
    result = {}
    for key, value in pairs:
        if key in result:
            raise ValueError('Duplicate key in runtime release status')
        result[key] = value
    return result


def _verify_archive(runtime, path, root):
    prefix = runtime + '-qualified'
    with tempfile.TemporaryDirectory(prefix='managed-runtime-verify-') as temporary:
        target = Path(temporary) / prefix
        target.mkdir()
        names, total = set(), 0
        with tarfile.open(path, 'r:gz') as archive:
            for member in archive:
                total += member.size
                if member.name in names or len(names) >= MAX_ARCHIVE_FILES or total > MAX_ARCHIVE_BYTES:
                    raise ValueError('Duplicate or oversized runtime qualification archive')
                names.add(member.name)
                if member.name == prefix and member.isdir():
                    continue
                if not member.isfile() or not re.fullmatch(re.escape(prefix) + r'/[A-Za-z0-9][A-Za-z0-9._-]*', member.name):
                    raise ValueError('Unsafe runtime qualification archive member')
                with (target / Path(member.name).name).open('xb') as output:
                    source = archive.extractfile(member)
                    while chunk := source.read(1024 * 1024):
                        output.write(chunk)
        verifier = runpy.run_path(str(root / f'release/verify-{runtime}-runtime.py'))
        manifest = verifier['validate_metadata'](target, root)
        expected_files = {'manifest.json', 'release-verification.json', *manifest['files']}
        if {file.name for file in target.iterdir()} != expected_files:
            raise ValueError('Runtime qualification archive file inventory differs')
        report = json.loads((target / 'release-verification.json').read_text(), object_pairs_hook=_unique_object)
        expected = {'schema_version': 2, 'platform': 'linux/amd64', 'anonymous_pull_verified': True,
                    'native_acceptance_reused': True, 'release_runtime_qualified': True,
                    'deployment_qualified': False, 'images': manifest['images']}
        if runtime == 'supabase':
            expected['image_config_identities_verified'] = True
        elif runtime in ('vitess', 'oracle-free'):
            expected['schema_version'] = 1
            expected['image_binary_hashes_verified'] = True
            expected['images'] = {kind: item['reference'] for kind, item in manifest['images'].items()}
            del expected['release_runtime_qualified'], expected['deployment_qualified']
        if (not isinstance(report, dict) or type(report.get('schema_version')) is not int or report != expected or
                any(type(report.get(key)) is not bool for key, value in expected.items() if type(value) is bool)):
            raise ValueError('Runtime qualification archive lacks exact release verification')


def verify_evidence(directory, root=ROOT):
    directory, root = Path(directory), Path(root)
    available = release_availability(root)
    revision, hashes = source_revision(root), source_gate_hashes(root)
    allowed = set()
    for runtime, enabled in available.items():
        state = 'qualified' if enabled else 'held'
        status_name = f'managed-{runtime}-{state}.json'
        allowed.add(status_name)
        status_path = directory / status_name
        if status_path.is_symlink() or not status_path.is_file() or status_path.stat().st_size > 16384:
            raise ValueError('Missing or invalid managed runtime status: ' + runtime)
        status = json.loads(status_path.read_text(), object_pairs_hook=_unique_object)
        expected = {'schema_version': 1, 'runtime': runtime, 'source_revision': revision,
                    'source_gate_sha256': hashes, 'release_runtime_qualified': enabled, 'status': state}
        if enabled:
            name = f'managed-{runtime}-qualification.tar.gz'
            allowed.add(name)
            expected.update(archive=name, archive_sha256=_digest(directory / name))
        else:
            expected['reason'] = 'The compiled release gate is closed.'
        if not isinstance(status, dict) or type(status.get('schema_version')) is not int or type(status.get('release_runtime_qualified')) is not bool or status != expected:
            raise ValueError('Managed runtime status differs from compiled source: ' + runtime)
        if enabled:
            _verify_archive(runtime, directory / expected['archive'], root)
    managed = set()
    for path in directory.glob('managed-*'):
        if path.name not in allowed:
            raise ValueError('Unexpected managed runtime evidence or a held-runtime qualification archive')
        managed.add(path.name)
    if managed != allowed:
        raise ValueError('Unexpected managed runtime evidence or a held-runtime qualification archive')
    return available


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--root', type=Path, default=ROOT)
    commands = parser.add_subparsers(dest='command')
    prepare = commands.add_parser('prepare')
    prepare.add_argument('runtime', choices=GATES)
    prepare.add_argument('--output', type=Path, required=True)
    verify = commands.add_parser('verify')
    verify.add_argument('directory', type=Path)
    args = parser.parse_args()
    if args.command == 'prepare':
        value = prepare_evidence(args.runtime, args.output, args.root)
    elif args.command == 'verify':
        value = verify_evidence(args.directory, args.root)
    else:
        value = release_availability(args.root)
    print(json.dumps(value, sort_keys=True))


if __name__ == '__main__':
    main()
