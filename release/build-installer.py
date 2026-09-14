#!/usr/bin/env python3
"""Build a local installer kit and actual dashboard runtime; never upload anything."""
import argparse
import gzip
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import tarfile

ROOT = Path(__file__).resolve().parents[1]
spec = importlib.util.spec_from_file_location('installer_host', ROOT / 'installer/host.py')
host = importlib.util.module_from_spec(spec); spec.loader.exec_module(host)
spec = importlib.util.spec_from_file_location('release_bootstrap', Path(__file__).with_name('bootstrap-version.py'))
bootstrap = importlib.util.module_from_spec(spec); spec.loader.exec_module(bootstrap)

def archive(source, destination):
    epoch = int(os.environ.get('SOURCE_DATE_EPOCH', '0'))
    with destination.open('wb') as stream, gzip.GzipFile(fileobj=stream, mode='wb', filename='', mtime=epoch) as zipped:
        with tarfile.open(fileobj=zipped, mode='w') as tar:
            for path in sorted(source.rglob('*')):
                if path.is_dir() and not path.is_symlink(): continue
                info = tar.gettarinfo(str(path), arcname=str(Path(source.name) / path.relative_to(source)))
                info.uid = info.gid = 0; info.uname = info.gname = ''; info.mtime = epoch
                info.mode = 0o755 if path.suffix == '.sh' else 0o644
                if path.is_symlink(): tar.addfile(info)
                else:
                    with path.open('rb') as file: tar.addfile(info, file)

def owned_directory(path):
    if path.exists():
        if path.is_symlink() or not (path / '.hakopod-generated').is_file():
            raise ValueError('Refusing to replace an unowned directory: ' + str(path))
        shutil.rmtree(path)
    path.mkdir(parents=True)
    (path / '.hakopod-generated').write_text('Hakopod installer artifact builder\n')

def source_fingerprint():
    h = hashlib.sha256()
    for name in ('LICENSE', 'NOTICE'):
        h.update((name + '\0' + host.digest(ROOT / name)).encode())
    for folder in ('web', 'packages/ui', 'templates', 'installer', 'scripts', 'deploy', 'release/notices'):
        base = ROOT / folder
        if not base.is_dir(): continue
        for directory, folders, files in os.walk(base):
            folders[:] = sorted(x for x in folders if x not in ('node_modules', '.git', 'dist', '.tanstack', '__pycache__'))
            for name in sorted(files):
                path = Path(directory) / name
                if path.suffix == '.pyc' or path.name == '.git' or path.name.startswith('.env'): continue
                h.update((str(path.relative_to(ROOT)) + '\0' + host.digest(path)).encode())
    return h.hexdigest()

def git_source_state():
    revision = subprocess.check_output(['git', 'rev-parse', '--verify', 'HEAD'], cwd=ROOT, text=True).strip()
    dirty = bool(subprocess.check_output(['git', 'status', '--porcelain', '--ignore-submodules=all'], cwd=ROOT, text=True).strip())
    return dict(source_revision=revision, source_dirty=dirty)

def validate_static_stylesheets(dist):
    # Client and SSR compilation can disagree if a CSS generator scans build
    # outputs. Catch missing hashed stylesheets before packaging the runtime.
    references = set()
    pattern = re.compile(r'''["'](/assets/[^"'?#\s]+\.css)(?:[?#][^"']*)?["']''')
    for path in (dist / 'server').rglob('*.js'):
        references.update(pattern.findall(path.read_text()))
    for reference in sorted(references):
        if not (dist / 'client' / reference.lstrip('/')).is_file():
            raise ValueError('SSR stylesheet is missing from client assets: ' + reference)

def package_runtime(dist, output):
    """Copy only actual SSR external packages and their installed runtime closure.

    Keep distinct pnpm peer contexts through internal relative symlinks. No npm
    install, lifecycle scripts, dev dependency tree or platform-native addon is
    put on the target machine.
    """
    modules = output / 'node_modules'; modules.mkdir()
    root_modules = ROOT / 'web/node_modules'
    copied = {}; inventory = []
    def resolve(name, source=None):
        options = []
        if source:
            options += [ancestor / 'node_modules' / name for ancestor in (source, *source.parents)]
        options.append(root_modules / name)
        for candidate in options:
            if (candidate / 'package.json').is_file(): return candidate.resolve()
        raise ValueError('Unresolved production dependency: ' + name)
    def link(path, target):
        path.parent.mkdir(parents=True, exist_ok=True)
        path.symlink_to(os.path.relpath(target, path.parent))
    def copy_package(source):
        source = source.resolve()
        if source in copied: return copied[source]
        package = json.loads((source / 'package.json').read_text())
        name = package['name']
        if not re.fullmatch(r'(?:@[A-Za-z0-9_.-]+/)?[A-Za-z0-9_.-]+', name): raise ValueError('Unsafe package name')
        identity = name + '@' + package['version']
        if '.pnpm' in source.parts: identity += ':' + source.parts[source.parts.index('.pnpm') + 1]
        key = hashlib.sha256(identity.encode()).hexdigest()[:20]
        parent = modules / '.store' / key / 'node_modules'
        destination = parent / name
        copied[source] = destination
        shutil.copytree(source, destination, symlinks=False, ignore=shutil.ignore_patterns('node_modules', '.git'))
        for path in destination.rglob('*'):
            if not path.is_file(): continue
            if path.suffix.lower() in ('.node', '.dll', '.so', '.dylib', '.exe'):
                raise ValueError('Native runtime addon needs a platform-specific bundle: ' + name)
            with path.open('rb') as file: magic = file.read(4)
            if magic in (b'\x7fELF', b'\xcf\xfa\xed\xfe', b'\xfe\xed\xfa\xcf'):
                raise ValueError('Native package binary cannot enter an architecture-neutral dashboard: ' + name)
        licenses = [str(path.relative_to(destination)) for path in destination.rglob('*')
                    if path.is_file() and re.search(r'(?i)(license|licence|copying|notice)', path.name)]
        if not licenses and (name, package['version']) == ('react-remove-scroll-bar', '2.3.8'):
            shutil.copyfile(ROOT / 'release/notices/react-remove-scroll-bar.LICENSE', destination / 'LICENSE.upstream')
            shutil.copyfile(ROOT / 'release/notices/react-remove-scroll-bar.provenance.json', destination / 'LICENSE.upstream.provenance.json')
            licenses = ['LICENSE.upstream', 'LICENSE.upstream.provenance.json']
        if not licenses: raise ValueError('Recover and preserve upstream runtime license text before packaging: ' + name)
        inventory.append(dict(name=name, version=package['version'], declared_license=package.get('license'),
                              license_files=licenses, artifact_path=str(destination.relative_to(output))))
        deps = set(package.get('dependencies', {})) | set(package.get('peerDependencies', {})) | set(package.get('optionalDependencies', {}))
        for dependency in sorted(deps):
            if dependency.startswith('@types/'): continue
            try: dependency_source = resolve(dependency, source)
            except ValueError:
                if dependency in package.get('optionalDependencies', {}) or package.get('peerDependenciesMeta', {}).get(dependency, {}).get('optional'): continue
                raise
            link(parent / dependency, copy_package(dependency_source))
        return destination
    bare = set()
    # Vite emits static external imports at line starts. Restrict the grammar so
    # prose such as `from "..."` inside an SSR string is never treated as code.
    imports = re.compile(r'''^\s*(?:import\s+(?:[^;"'\n]+\s+from\s+)?|export\s+[^;"'\n]+\s+from\s+)["']([^"']+)["']|\b(?:import|require)\(\s*["']([@A-Za-z0-9_./:-]+)["']\s*\)''', re.M)
    for path in [*dist.rglob('*.js'), output / 'serve.mjs']:
        for static, dynamic in imports.findall(path.read_text()):
            value = static or dynamic
            if value.startswith(('.', '/', 'node:')): continue
            name = '/'.join(value.split('/')[:2]) if value.startswith('@') else value.split('/')[0]
            bare.add(name)
    for name in sorted(bare): link(modules / name, copy_package(resolve(name)))
    (output / 'runtime-inventory.json').write_text(json.dumps(sorted(inventory, key=lambda x: (x['name'], x['version'])), indent=2) + '\n')
    return len(inventory)

def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--version', default='0.1.0-dev')
    parser.add_argument('--upgrade-from', action='append', default=[], help='Explicitly tested source release; repeat for each supported upgrade')
    parser.add_argument('--release-dir', type=Path)
    parser.add_argument('--use-existing-dist', action='store_true', help='Development smoke only: package existing dist with explicitly unknown source freshness')
    args = parser.parse_args()
    bootstrap.valid_version(args.version)
    for source_version in args.upgrade_from: bootstrap.valid_version(source_version)
    if len(set(args.upgrade_from)) != len(args.upgrade_from) or args.version in args.upgrade_from:
        raise ValueError("Upgrade sources must be distinct older releases")
    release_dir = args.release_dir or ROOT / '.local/releases' / args.version
    # Verify Go archive bytes before replacing any previously generated installer output.
    for arch in ('amd64', 'arm64'):
        name = f'hakopod_{args.version}_linux_{arch}.tar.gz'
        checksum = host.digest(release_dir / name)
        if f'{checksum}  {name}' not in (release_dir / 'SHA256SUMS').read_text().splitlines():
            raise ValueError('Go release checksum does not match: ' + name)
    stage = ROOT / '.local/installer-stage' / args.version
    destination = ROOT / '.local/installer-artifacts' / args.version
    owned_directory(stage); owned_directory(destination)
    source_state = git_source_state()
    before = source_fingerprint()
    if not args.use_existing_dist:
        # The snapshot prevents source edits halfway through Vite compilation.
        source = stage / 'source'
        for folder in ('web', 'packages/ui', 'templates'):
            if (ROOT / folder).exists():
                shutil.copytree(ROOT / folder, source / folder,
                    ignore=shutil.ignore_patterns('node_modules', 'dist', '.git', '.env*', '.tanstack', '__pycache__'))
        (source / 'web/node_modules').symlink_to(ROOT / 'web/node_modules', target_is_directory=True)
        env = dict(os.environ, NODE_OPTIONS='--max-old-space-size=512', GOMAXPROCS='2', GOMEMLIMIT='256MiB')
        subprocess.run(['pnpm', '--dir', str(source / 'web'), 'build'], cwd=ROOT, env=env, check=True)
        dist = source / 'web/dist'
    else: dist = ROOT / 'web/dist'
    if not (dist / 'server/server.js').is_file(): raise ValueError('Built SSR dashboard is missing')
    validate_static_stylesheets(dist)
    dashboard = stage / f'hakopod_{args.version}_dashboard'; dashboard.mkdir()
    shutil.copytree(dist, dashboard / 'dist')
    shutil.copyfile(ROOT / 'installer/serve.mjs', dashboard / 'serve.mjs')
    (dashboard / 'package.json').write_text(json.dumps(dict(name='@hakopod/installed-dashboard', private=True, type='module', version=args.version)) + '\n')
    for name in ('LICENSE', 'NOTICE'): shutil.copyfile(ROOT / name, dashboard / name)
    # Include collected notices for bundled client/SSR dependencies as well as
    # the external runtime closure. They do not affect service memory usage.
    notices_name = f'hakopod_{args.version}_dependency-notices.tar.gz'
    notices_archive = release_dir / notices_name
    if f'{host.digest(notices_archive)}  {notices_name}' not in (release_dir / 'SHA256SUMS').read_text().splitlines():
        raise ValueError('Dependency notice archive checksum does not match')
    host.unpack(notices_archive, stage / 'notices', 'dependency-notices')
    shutil.copytree(stage / 'notices/dependency-notices/dashboard', dashboard / 'third-party-licenses/declared-dashboard-dependencies')
    shutil.copyfile(ROOT / 'web/THIRD_PARTY_NOTICES.md', dashboard / 'DASHBOARD_NOTICES.md')
    for name in ('LICENSE', 'THIRD_PARTY_NOTICES.md', 'assets/OFL.txt'):
        source = ROOT / 'packages/ui' / name
        if source.is_file():
            target = dashboard / 'third-party-licenses/hakopod-ui' / name
            target.parent.mkdir(parents=True, exist_ok=True); shutil.copyfile(source, target)
    for name in ('LICENSE', 'LICENSE-Dokploy', 'NOTICE', 'THIRD_PARTY_NOTICES.md'):
        target = dashboard / 'third-party-licenses/hakopod-templates' / name
        target.parent.mkdir(parents=True, exist_ok=True)
        shutil.copyfile(ROOT / 'templates' / name, target)
    count = package_runtime(dashboard / 'dist/server', dashboard)
    kit = stage / f'hakopod_{args.version}_installer'; kit.mkdir()
    for folder in ('installer', 'deploy'):
        shutil.copytree(ROOT / folder, kit / folder, ignore=shutil.ignore_patterns('__pycache__', '*.pyc'))
    (kit / 'scripts').mkdir()
    shutil.copyfile(ROOT / 'scripts/install.sh', kit / 'scripts/install.sh')
    rendered_bootstrap = bootstrap.render((ROOT / 'scripts/installer.sh').read_text(), args.version)
    (kit / 'scripts/installer.sh').write_text(rendered_bootstrap)
    for name in ('LICENSE', 'NOTICE'): shutil.copyfile(ROOT / name, kit / name)
    after = source_fingerprint()
    if before != after and not args.use_existing_dist: raise ValueError('Source changed while packaging; retry after edits finish')
    for source in (dashboard, kit): archive(source, destination / (source.name + '.tar.gz'))
    (destination / 'installer.sh').write_text(rendered_bootstrap)
    bootstrap.verify_artifacts(destination, args.version)
    for arch in ('amd64', 'arm64'):
        name = f'hakopod_{args.version}_linux_{arch}.tar.gz'; shutil.copyfile(release_dir / name, destination / name)
    for name in ('provenance.json', 'hakopod.spdx.json', 'hakopod.cyclonedx.json', 'hakopod.syft.json', 'dependency-license-inventory.json', notices_name):
        source = release_dir / name
        if f'{host.digest(source)}  {name}' not in (release_dir / 'SHA256SUMS').read_text().splitlines():
            raise ValueError('Original release metadata checksum does not match: ' + name)
        shutil.copyfile(source, destination / ('go-and-lock-provenance.json' if name == 'provenance.json' else name))
    final_state = git_source_state()
    provenance = dict(version=args.version, source_revision=source_state['source_revision'],
        source_dirty=source_state['source_dirty'] or final_state['source_dirty'],
        source_fingerprint_sha256=before,
        source_changed_during_packaging=before != after or source_state['source_revision'] != final_state['source_revision'],
        dashboard_source='existing dist; source freshness not established' if args.use_existing_dist else 'snapshot build; installed dependency closure',
        native_runtime_addons=False, dashboard_runtime_packages=count, published=False,
        bootstrap_default_version=bootstrap.default_version(rendered_bootstrap),
        scope='Dashboard actual runtime files/dependency inventory, installer inputs, and separately verified Go archives. OS and image packages are outside this inventory.')
    (destination / 'installer-provenance.json').write_text(json.dumps(provenance, indent=2) + '\n')
    (destination / 'upgrade.json').write_text(json.dumps(dict(schema_version=1, version=args.version,
        from_versions=args.upgrade_from, runtime_pins_sha256=host.digest(ROOT / 'installer/pins.json'))) + '\n')
    (destination / 'SHA256SUMS').write_text(''.join(host.digest(path) + '  ' + path.name + '\n'
        for path in sorted(destination.iterdir()) if path.is_file() and not path.name.startswith('.') and path.name != 'SHA256SUMS'))
    print(f'Packaged {count} actual SSR runtime packages; local installer artifacts: {destination}')

if __name__ == '__main__': main()
