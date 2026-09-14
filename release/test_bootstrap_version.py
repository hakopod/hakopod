"""A release bootstrap must select its own immutable release by default."""
import importlib.util
import io
import json
from pathlib import Path
import tarfile
import tempfile
import unittest
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[1]


def module(name):
    spec = importlib.util.spec_from_file_location(name, ROOT / 'release' / (name + '.py'))
    value = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(value)
    return value


bootstrap = module('bootstrap-version')
builder = module('build-installer')
TEMPLATE = (ROOT / 'scripts/installer.sh').read_text()
VERSION = '0.1.0-alpha.4'


def script(body):
    return 'python3 - ' + bootstrap.START + body + bootstrap.END + '\n'


def write_kit(directory, copies, version=VERSION):
    with tarfile.open(directory / f'hakopod_{version}_installer.tar.gz', 'w:gz') as archive:
        for content, kind in copies:
            member = tarfile.TarInfo(f'hakopod_{version}_installer/scripts/installer.sh')
            member.type = kind
            member.size = len(content)
            archive.addfile(member, io.BytesIO(content))


class BootstrapVersionTests(unittest.TestCase):
    def test_render_requested_versions_without_editing_template(self):
        before = (ROOT / 'scripts/installer.sh').read_bytes()
        for version in (VERSION, '12.34.56-rc.7', '0.1.0-dev', '1.0.0'):
            with self.subTest(version=version):
                rendered = bootstrap.render(TEMPLATE, version)
                self.assertEqual(bootstrap.default_version(rendered), version)
                self.assertEqual(bootstrap.render(rendered, version), rendered)
        self.assertEqual((ROOT / 'scripts/installer.sh').read_bytes(), before)

    def test_reject_invalid_versions(self):
        for version in ('', 'v1.0.0', '01.0.0', '../1.0.0', '1.0.0\n', '1.0.0-' + 'a' * 64):
            with self.subTest(version=version), self.assertRaises(ValueError):
                bootstrap.render(TEMPLATE, version)

    def test_reject_ambiguous_or_nonliteral_declarations(self):
        for body in (
            'OTHER = "1.0.0"\n',
            'DEFAULT_VERSION = "1.0.0"\nDEFAULT_VERSION = "2.0.0"\n',
            'DEFAULT_VERSION = compute()\n',
            'DEFAULT_VERSION: str = "1.0.0"\n',
            'DEFAULT_VERSION = "1.0.0"\nDEFAULT_VERSION += "-old"\n',
            'DEFAULT_VERSION = "1.0.0"\nDEFAULT_VERSION: str = "2.0.0"\n',
            'DEFAULT_VERSION = "1.0.0"\ndel DEFAULT_VERSION\n',
            'DEFAULT_VERSION = OTHER = "1.0.0"\n',
            'if True:\n    DEFAULT_VERSION = "1.0.0"\n',
            'DEFAULT_VERSION = "1.0.0"; OTHER = 1\n',
            'OTHER = 1; DEFAULT_VERSION = "1.0.0"\n',
            'DEFAULT_VERSION = "1.0.0"\ndef DEFAULT_VERSION(): pass\n',
            'DEFAULT_VERSION = "1.0.0"\nclass DEFAULT_VERSION: pass\n',
            'DEFAULT_VERSION = "1.0.0"\nimport other as DEFAULT_VERSION\n',
            'DEFAULT_VERSION = "1.0.0"\nfrom other import DEFAULT_VERSION\n',
            'DEFAULT_VERSION = "1.0.0"\ntry: pass\nexcept Exception as DEFAULT_VERSION: pass\n',
        ):
            with self.subTest(body=body), self.assertRaises(ValueError):
                bootstrap.render(script(body), VERSION)

    def test_reject_missing_or_duplicate_python_sections(self):
        valid = script('DEFAULT_VERSION = "1.0.0"\n')
        for text in ('DEFAULT_VERSION = "1.0.0"\n', valid + valid,
                     valid.replace(bootstrap.END, ''), valid.replace(bootstrap.START, ''),
                     valid.replace(bootstrap.END, '\nHAKOPOD_BOOTSTRAP_PY_INVALID\n'),
                     valid.replace(bootstrap.END, '\nHAKOPOD_BOOTSTRAP_PY # invalid\n'),
                     valid + '#' * bootstrap.LIMIT):
            with self.subTest(length=len(text)), self.assertRaises(ValueError):
                bootstrap.render(text, VERSION)

    def test_matching_artifacts(self):
        with tempfile.TemporaryDirectory() as temporary:
            directory = Path(temporary)
            content = bootstrap.render(TEMPLATE, VERSION).encode()
            (directory / 'installer.sh').write_bytes(content)
            write_kit(directory, [(content, tarfile.REGTYPE)])
            self.assertEqual(bootstrap.verify_artifacts(directory, VERSION), VERSION)

    def test_reject_old_standalone_even_with_correct_kit(self):
        with tempfile.TemporaryDirectory() as temporary:
            directory = Path(temporary)
            (directory / 'installer.sh').write_text(bootstrap.render(TEMPLATE, '0.1.0-alpha.2'))
            write_kit(directory, [(bootstrap.render(TEMPLATE, VERSION).encode(), tarfile.REGTYPE)])
            with self.assertRaisesRegex(ValueError, 'Standalone bootstrap default'):
                bootstrap.verify_artifacts(directory, VERSION)

    def test_reject_missing_duplicate_symlink_or_mismatched_kit_copy(self):
        content = bootstrap.render(TEMPLATE, VERSION).encode()
        old = bootstrap.render(TEMPLATE, '0.1.0-alpha.2').encode()
        for copies in ([], [(content, tarfile.REGTYPE)] * 2,
                       [(b'', tarfile.SYMTYPE)], [(old, tarfile.REGTYPE)],
                       [(content + b'# different bytes\n', tarfile.REGTYPE)]):
            with self.subTest(copies=len(copies)), tempfile.TemporaryDirectory() as temporary:
                directory = Path(temporary)
                (directory / 'installer.sh').write_bytes(content)
                write_kit(directory, copies)
                with self.assertRaises(ValueError):
                    bootstrap.verify_artifacts(directory, VERSION)

    def test_reject_missing_symlink_or_oversized_standalone(self):
        for kind in ('missing', 'symlink', 'oversized'):
            with self.subTest(kind=kind), tempfile.TemporaryDirectory() as temporary:
                directory = Path(temporary)
                target = directory / 'installer.sh'
                if kind == 'symlink':
                    target.symlink_to(ROOT / 'scripts/installer.sh')
                elif kind == 'oversized':
                    target.write_bytes(b'x' * (bootstrap.LIMIT + 1))
                with self.assertRaisesRegex(ValueError, 'standalone bootstrap'):
                    bootstrap.verify_artifacts(directory, VERSION)

    def test_builder_stamps_both_copies_from_an_old_source_template(self):
        # Run the actual packager against small valid input artifacts. Dashboard
        # compilation and dependency traversal are outside this regression.
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            for folder in ('installer', 'deploy', 'scripts', 'web/dist/server', 'release-input', 'templates'):
                (root / folder).mkdir(parents=True, exist_ok=True)
            old = bootstrap.render(TEMPLATE, '0.1.0-alpha.2')
            (root / 'scripts/installer.sh').write_text(old)
            (root / 'installer/pins.json').write_bytes((ROOT / 'installer/pins.json').read_bytes())
            for name in ('LICENSE', 'NOTICE', 'scripts/install.sh', 'installer/serve.mjs',
                         'web/THIRD_PARTY_NOTICES.md', 'web/dist/server/server.js', 'templates/LICENSE', 'templates/LICENSE-Dokploy', 'templates/NOTICE', 'templates/THIRD_PARTY_NOTICES.md'):
                (root / name).write_text('')
            inputs = root / 'release-input'
            for arch in ('amd64', 'arm64'):
                (inputs / f'hakopod_{VERSION}_linux_{arch}.tar.gz').write_bytes(b'go fixture')
            for name in ('provenance.json', 'hakopod.spdx.json', 'hakopod.cyclonedx.json',
                         'hakopod.syft.json', 'dependency-license-inventory.json'):
                (inputs / name).write_text('{}\n')
            with tarfile.open(inputs / f'hakopod_{VERSION}_dependency-notices.tar.gz', 'w:gz') as archive:
                member = tarfile.TarInfo('dependency-notices/dashboard/LICENSE')
                archive.addfile(member, io.BytesIO())
            (inputs / 'SHA256SUMS').write_text(''.join(
                builder.host.digest(path) + '  ' + path.name + '\n' for path in sorted(inputs.iterdir())))
            state = dict(source_revision='a' * 40, source_dirty=False)
            with patch.object(builder, 'ROOT', root), patch.object(builder, 'git_source_state', return_value=state), \
                    patch.object(builder, 'package_runtime', return_value=0), \
                    patch('sys.argv', ['build-installer.py', '--version', VERSION, '--release-dir', str(inputs), '--use-existing-dist']):
                builder.main()
            output = root / '.local/installer-artifacts' / VERSION
            self.assertEqual(bootstrap.verify_artifacts(output, VERSION), VERSION)
            provenance = json.loads((output / 'installer-provenance.json').read_text())
            self.assertEqual(provenance['bootstrap_default_version'], VERSION)
            self.assertEqual((root / 'scripts/installer.sh').read_text(), old)


if __name__ == '__main__':
    unittest.main()
