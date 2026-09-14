import hashlib
import importlib.util
import json
from pathlib import Path
import tempfile
import unittest
import subprocess
from unittest.mock import patch

spec = importlib.util.spec_from_file_location('publication', Path(__file__).with_name('publication.py'))
publication = importlib.util.module_from_spec(spec); spec.loader.exec_module(publication)


class PublicationTest(unittest.TestCase):
    def source_fixture(self, root):
        for name, value in {'.gitignore': '.local/\n', 'go.mod': 'module fixture\n', 'go.sum': '',
                'web/package.json': '{}', 'web/pnpm-lock.yaml': '', 'web/src/page.tsx': 'export const page = 1;\n',
                'cmd/main.go': 'package main\n', 'internal/example.go': 'package example\n',
                'api/openapi.json': '{}', 'installer/example.json': '{}', 'scripts/install.sh': '# fixture\n',
                'packages/ui/package.json': '{}', 'packages/ui/component.tsx': 'export const component = 1;\n',
                'LICENSE': 'Fixture license\n', 'NOTICE': 'Fixture notice\n'}.items():
            path = root / name; path.parent.mkdir(parents=True, exist_ok=True); path.write_text(value)
        ui = publication.tooling_module(Path(publication.__file__).resolve().parents[1] / 'scripts/ui-source.py')
        ui.pack(root / 'packages/ui', root / 'third_party/ui/ui-source.tar.gz')
        templates = root / 'templates'; templates.mkdir()
        (templates / 'catalog.json').write_text('[]\n')
        subprocess.run(['git', 'init', '-q', str(templates)], check=True)
        subprocess.run(['git', '-C', str(templates), 'add', '.'], check=True)
        self.commit_fixture(templates)
        subprocess.run(['git', 'init', '-q', str(root)], check=True)
        subprocess.run(['git', '-C', str(root), 'add', '.'], check=True)
        self.commit_fixture(root)

    def commit_fixture(self, root):
        subprocess.run(['git', '-C', str(root), '-c', 'user.name=Release test', '-c', 'user.email=release@example.com',
                        'commit', '-qm', 'Fixture source'], check=True)

    def source_records(self):
        installer_builder = publication.builder_module('build-installer.py')
        common = dict(version='0.1.0-alpha.1', **installer_builder.git_source_state())
        go = dict(common, source_changed_during_build=False,
                  source_fingerprint_sha256=publication.builder_module('build.py').fingerprint()[0])
        installer = dict(common, source_changed_during_packaging=False,
            source_fingerprint_sha256=installer_builder.source_fingerprint(),
            dashboard_source='snapshot build; installed dependency closure')
        return go, installer

    def write_build_records(self, go, installer):
        for folder, filename, record in [('releases', 'provenance.json', go), ('installer-artifacts', 'installer-provenance.json', installer)]:
            directory = publication.ROOT / '.local' / folder / '0.1.0-alpha.1'; directory.mkdir(parents=True)
            (directory / filename).write_text(json.dumps(record))
            (directory / 'SHA256SUMS').write_text(publication.manifest(directory))

    def test_unchanged_builds_from_same_clean_revision_assemble(self):
        with tempfile.TemporaryDirectory() as temporary, patch.object(publication, 'ROOT', Path(temporary)):
            self.source_fixture(publication.ROOT)
            self.write_build_records(*self.source_records())
            publication.verify(publication.assemble('0.1.0-alpha.1'))

    def test_edit_between_go_and_installer_builds_cannot_claim_clean_tag(self):
        for filename in ('web/src/page.tsx', 'installer/example.json', 'scripts/install.sh'):
            with self.subTest(filename=filename), tempfile.TemporaryDirectory() as temporary, patch.object(publication, 'ROOT', Path(temporary)):
                self.source_fixture(publication.ROOT)
                go, _ = self.source_records()
                (publication.ROOT / filename).write_text('Changed after Go compilation\n')
                _, installer = self.source_records()
                self.assertTrue(installer['source_dirty'])
                # Restoring the checkout cannot make the earlier dirty artifact clean.
                subprocess.run(['git', '-C', temporary, 'restore', filename], check=True)
                self.write_build_records(go, installer)
                with self.assertRaisesRegex(ValueError, 'both builders.*clean'):
                    publication.assemble('0.1.0-alpha.1')
                self.assertFalse((publication.ROOT / '.local/publication/0.1.0-alpha.1').exists())

    def test_assembly_rechecks_current_tree_revision_and_each_fingerprint(self):
        for scenario in ('late-edit', 'new-commit', 'go-fingerprint', 'installer-fingerprint', 'legacy-installer'):
            with self.subTest(scenario=scenario), tempfile.TemporaryDirectory() as temporary, patch.object(publication, 'ROOT', Path(temporary)):
                self.source_fixture(publication.ROOT)
                go, installer = self.source_records()
                if scenario in ('late-edit', 'new-commit'):
                    (publication.ROOT / 'web/src/page.tsx').write_text('Changed after packaging\n')
                    if scenario == 'new-commit':
                        subprocess.run(['git', '-C', temporary, 'add', '.'], check=True)
                        self.commit_fixture(publication.ROOT)
                        _, installer = self.source_records()
                elif scenario == 'go-fingerprint': go['source_fingerprint_sha256'] = '0' * 64
                elif scenario == 'installer-fingerprint': installer['source_fingerprint_sha256'] = '0' * 64
                else: installer.pop('source_revision'); installer.pop('source_dirty')
                self.write_build_records(go, installer)
                with self.assertRaisesRegex(ValueError, 'clean|fingerprints differ'):
                    publication.assemble('0.1.0-alpha.1')

    def test_modified_restored_ui_gitlink_cannot_claim_tagged_origin(self):
        with tempfile.TemporaryDirectory() as temporary, patch.object(publication, 'ROOT', Path(temporary)):
            root = publication.ROOT
            self.source_fixture(root)
            revision = subprocess.check_output(['git', '-C', temporary, 'rev-parse', 'HEAD'], text=True).strip()
            subprocess.run(['git', '-C', temporary, 'rm', '-qr', '--cached', 'packages/ui'], check=True)
            subprocess.run(['git', '-C', temporary, 'update-index', '--add', '--cacheinfo', '160000,' + revision + ',packages/ui'], check=True)
            self.commit_fixture(root)
            go, _ = self.source_records()
            (root / 'packages/ui/component.tsx').write_text('Stable but not tagged UI source\n')
            _, installer = self.source_records()
            self.assertFalse(installer['source_dirty'], 'Restored gitlink reproducer must look clean to Git')
            self.write_build_records(go, installer)
            with self.assertRaisesRegex(ValueError, 'Restored public UI source differs'):
                publication.assemble('0.1.0-alpha.1')

    def test_release_tags_are_explicit_safe_versions(self):
        self.assertEqual(publication.release_version('v0.1.0-alpha.1'), '0.1.0-alpha.1')
        for tag in ('0.1.0', 'vlatest', 'v1.0', 'v1.0.0/../main', 'v1.0.0\n', 'v01.0.0'):
            with self.assertRaises(ValueError): publication.release_version(tag)

    def test_manifest_rejects_missing_extra_changed_and_symlink_assets(self):
        with tempfile.TemporaryDirectory() as temporary:
            directory = Path(temporary)
            asset = directory / 'artifact.tar.gz'; asset.write_bytes(b'fixture')
            (directory / 'SHA256SUMS').write_text(publication.manifest(directory))
            publication.verify(directory)
            asset.write_bytes(b'changed')
            with self.assertRaises(ValueError): publication.verify(directory)
            asset.write_bytes(b'fixture')
            (directory / 'extra').write_bytes(b'extra')
            with self.assertRaises(ValueError): publication.verify(directory)
            (directory / 'extra').unlink()
            asset.unlink(); asset.symlink_to('/dev/null')
            with self.assertRaises(ValueError): publication.verify(directory)

    def test_smoke_reports_must_match_both_architectures_and_exact_bytes(self):
        with tempfile.TemporaryDirectory() as temporary:
            directory = Path(temporary)
            (directory / 'artifact.tar.gz').write_bytes(b'fixture')
            (directory / 'SHA256SUMS').write_text(publication.manifest(directory))
            report = {'artifact_sha256': {'artifact.tar.gz': hashlib.sha256(b'fixture').hexdigest()}}
            for arch in ('amd64', 'arm64'):
                report['checks'] = [{'architecture': arch, 'passed': True, 'execution': 'native'}]
                (directory / ('installer-smoke-' + arch + '.json')).write_text(json.dumps(report))
            publication.finalize(directory)
            publication.verify(directory)
            report['artifact_sha256']['artifact.tar.gz'] = '0' * 64
            (directory / 'installer-smoke-arm64.json').write_text(json.dumps(report))
            # Reflect the report in the manifest so the report-to-artifact binding
            # itself, rather than the ordinary checksum test, must reject it.
            (directory / 'SHA256SUMS').write_text(publication.manifest(directory))
            with self.assertRaisesRegex(ValueError, 'Smoke evidence'):
                publication.finalize(directory)
            report['artifact_sha256']['artifact.tar.gz'] = hashlib.sha256(b'fixture').hexdigest()
            report['checks'][0]['execution'] = 'emulated'
            (directory / 'installer-smoke-arm64.json').write_text(json.dumps(report))
            (directory / 'SHA256SUMS').write_text(publication.manifest(directory))
            with self.assertRaisesRegex(ValueError, 'native Linux/arm64'):
                publication.finalize(directory)


if __name__ == '__main__':
    unittest.main()

    def test_template_source_must_match_pinned_commit_including_ignored_files(self):
        for change in ('edited', 'untracked', 'ignored'):
            with self.subTest(change=change), tempfile.TemporaryDirectory() as temporary, patch.object(publication, 'ROOT', Path(temporary)):
                self.source_fixture(publication.ROOT)
                publication.validate_public_templates()
                source = publication.ROOT / 'templates'
                if change == 'edited':
                    (source / 'catalog.json').write_text('[{"changed":true}]')
                else:
                    if change == 'ignored':
                        (source / '.git/info/exclude').write_text('extra.go\n')
                    (source / 'extra.go').write_text('package templates\n')
                with self.assertRaisesRegex(ValueError, 'template source differs'):
                    publication.validate_public_templates()
