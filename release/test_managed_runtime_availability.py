"""Source gates and release evidence must fail closed before publication."""
import io
import json
from pathlib import Path
import runpy
import shutil
import subprocess
import tarfile
import tempfile
import unittest
from unittest.mock import patch

MODULE = runpy.run_path(str(Path(__file__).with_name('managed-runtime-availability.py')))
# Functions share their execution namespace, which is distinct from run_path's result.
STATE = MODULE['release_availability'].__globals__


class ManagedRuntimeAvailabilityTest(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name) / 'source'
        self.root.mkdir()
        self.set_gates(False)
        self.revision = patch.dict(STATE, source_revision=lambda root: 'a' * 40)
        self.revision.start()
        self.addCleanup(self.revision.stop)

    def set_gates(self, enabled):
        for runtime, (relative, name, kind) in MODULE['GATES'].items():
            path = self.root / relative
            path.parent.mkdir(parents=True, exist_ok=True)
            value = 'true' if enabled else 'false'
            declaration = f'const (\n{name} = {value}\n)\n' if kind == 'const' else f'func {name}() bool {{ return {value} }}\n'
            path.write_text('package fixture\n' + declaration)

    def prepare_all(self):
        output = Path(self.temporary.name) / 'publication'
        output.mkdir()
        for runtime in MODULE['GATES']:
            stage = Path(self.temporary.name) / runtime
            MODULE['prepare_evidence'](runtime, stage, self.root)
            for path in stage.iterdir():
                shutil.copyfile(path, output / path.name)
        return output

    def test_all_literal_gate_combinations(self):
        for enabled in (False, True):
            self.set_gates(enabled)
            self.assertEqual(MODULE['release_availability'](self.root), dict.fromkeys(MODULE['GATES'], enabled))

    def test_source_revision_rejects_an_export_inside_a_parent_checkout(self):
        with patch.object(subprocess, 'check_output', return_value=str(self.root.parent)):
            with self.assertRaisesRegex(ValueError, 'own Git checkout'):
                MODULE['source_revision'](self.root)
        with patch.object(subprocess, 'check_output', side_effect=[str(self.root), 'a' * 40]):
            self.assertEqual(MODULE['source_revision'](self.root), 'a' * 40)

    def test_commented_and_quoted_declarations_are_not_gates(self):
        for runtime, (relative, name, kind) in MODULE['GATES'].items():
            path = self.root / relative
            original = path.read_text()
            for source in ('// ' + original.replace('\n', '\n// '), '/*' + original + '*/', 'package fixture\nvar text = `' + original + '`\n'):
                with self.subTest(runtime=runtime, source=source):
                    path.write_text(source)
                    with self.assertRaisesRegex(ValueError, 'missing or ambiguous'):
                        MODULE['release_availability'](self.root)
            path.write_text(original)

    def test_missing_conditional_duplicate_or_malformed_gate_is_rejected(self):
        for runtime, (relative, name, kind) in MODULE['GATES'].items():
            path = self.root / relative
            original = path.read_text()
            malformed = [original.replace('false', 'environmentEnabled()'), original.replace('false', 'false || true'),
                         original + original, '//go:build acceptance\n' + original, '// +build acceptance\n' + original,
                         original + '\n/* never closed', original + '\nvar broken = "never closed',
                         original.replace(name, name + 'Other')]
            if kind == 'func':
                malformed += [original.replace('return false', 'if ready { return true }; return false'),
                              original.replace('return false', 'return false; enable()')]
            else:
                malformed += ['package fixture\nfunc inner() { const ' + name + ' = false }\n']
            for source in malformed:
                with self.subTest(runtime=runtime, source=source):
                    path.write_text(source)
                    with self.assertRaises(ValueError):
                        MODULE['release_availability'](self.root)
            path.write_text(original)
            path.unlink()
            with self.assertRaises(ValueError):
                MODULE['release_availability'](self.root)
            path.write_text(original)

    def test_comments_and_whitespace_do_not_change_a_literal_gate(self):
        for relative, name, kind in MODULE['GATES'].values():
            path = self.root / relative
            source = f'const (\n{name} bool = /* exact gate */ false\n)\n' if kind == 'const' else f'func {name} ( ) bool {{\nreturn /* exact gate */ false\n}}\n'
            path.write_text('package fixture\n// fake declaration is harmless\n' + source)
        self.assertEqual(MODULE['release_availability'](self.root), dict.fromkeys(MODULE['GATES'], False))

    def test_held_runtimes_have_status_only_and_never_invoke_verifiers(self):
        with patch.object(subprocess, 'run') as run:
            output = self.prepare_all()
        run.assert_not_called()
        self.assertEqual({path.name for path in output.iterdir()}, {f'managed-{name}-held.json' for name in MODULE['GATES']})
        self.assertEqual(MODULE['verify_evidence'](output, self.root), dict.fromkeys(MODULE['GATES'], False))

    def test_unexpected_archive_and_stale_or_forged_status_are_rejected(self):
        output = self.prepare_all()
        path = output / 'managed-neon-held.json'
        original = path.read_text()
        for change in ({'release_runtime_qualified': True}, {'release_runtime_qualified': 0}, {'schema_version': True},
                       {'source_revision': 'b' * 40}, {'source_gate_sha256': {}}, {'archive': 'managed-neon-qualification.tar.gz'}):
            value = json.loads(original)
            value.update(change)
            path.write_text(json.dumps(value))
            with self.subTest(change=change), self.assertRaises(ValueError):
                MODULE['verify_evidence'](output, self.root)
        path.write_text(original.replace('"status": "held"', '"status": "held", "status": "held"'))
        with self.assertRaisesRegex(ValueError, 'Duplicate'):
            MODULE['verify_evidence'](output, self.root)
        path.write_text(original)
        injected = output / 'managed-neon-qualification.tar.gz'
        injected.write_bytes(b'unqualified')
        with self.assertRaisesRegex(ValueError, 'Unexpected'):
            MODULE['verify_evidence'](output, self.root)
        injected.unlink()
        path.unlink()
        with self.assertRaisesRegex(ValueError, 'Missing'):
            MODULE['verify_evidence'](output, self.root)

    def test_source_change_invalidates_held_status(self):
        output = self.prepare_all()
        relative = MODULE['GATES']['neon'][0]
        path = self.root / relative
        path.write_text(path.read_text() + '// source changed\n')
        with self.assertRaisesRegex(ValueError, 'differs from compiled source'):
            MODULE['verify_evidence'](output, self.root)

    def verifier_output(self, command, **kwargs):
        runtime = Path(command[2]).name.removeprefix('verify-').removesuffix('-runtime.py')
        self.assertEqual(command[:2], [MODULE['sys'].executable, '-B'])
        self.assertEqual(command[2], str(self.root / f'release/verify-{runtime}-runtime.py'))
        self.assertEqual(command[3], '--output')
        self.assertTrue(kwargs['check'])
        self.assertEqual(kwargs['env']['PYTHONDONTWRITEBYTECODE'], '1')
        output = Path(command[4])
        output.mkdir()
        (output / 'manifest.json').write_text('{"fixture": "verifier output"}')

    def test_every_enabled_runtime_invokes_its_existing_verifier_and_binds_archive_bytes(self):
        self.set_gates(True)
        with patch.object(subprocess, 'run', side_effect=self.verifier_output) as run, patch.object(subprocess, 'check_output', return_value='1000\n'):
            output = self.prepare_all()
        self.assertEqual(run.call_count, len(MODULE['GATES']))
        with patch.dict(STATE, _verify_archive=lambda runtime, path, root: None):
            self.assertEqual(MODULE['verify_evidence'](output, self.root), dict.fromkeys(MODULE['GATES'], True))
            archive = output / 'managed-neon-qualification.tar.gz'
            archive.write_bytes(archive.read_bytes() + b'tampered')
            with self.assertRaisesRegex(ValueError, 'differs from compiled source'):
                MODULE['verify_evidence'](output, self.root)

    def test_enabled_verifier_failure_cannot_be_converted_into_a_hold(self):
        self.set_gates(True)
        output = Path(self.temporary.name) / 'failed'
        with patch.object(subprocess, 'run', side_effect=subprocess.CalledProcessError(1, ['verifier'])):
            with self.assertRaises(subprocess.CalledProcessError):
                MODULE['prepare_evidence']('neon', output, self.root)
        self.assertFalse(output.exists())

    def test_gate_change_during_verification_cannot_publish(self):
        self.set_gates(True)
        output = Path(self.temporary.name) / 'changed'
        def change(command, **kwargs):
            self.verifier_output(command, **kwargs)
            self.set_gates(False)
        with patch.object(subprocess, 'run', side_effect=change), patch.object(subprocess, 'check_output', return_value='1000\n'):
            with self.assertRaisesRegex(ValueError, 'source changed'):
                MODULE['prepare_evidence']('neon', output, self.root)
        self.assertFalse(output.exists())

    def test_qualified_archive_revalidates_exact_metadata_and_release_receipt(self):
        for runtime in MODULE['GATES']:
            with self.subTest(runtime=runtime):
                directory = Path(self.temporary.name) / (runtime + '-qualified')
                directory.mkdir()
                manifest = {'images': {}, 'files': {}}
                report = {'schema_version': 2, 'platform': 'linux/amd64', 'anonymous_pull_verified': True,
                          'native_acceptance_reused': True, 'release_runtime_qualified': True,
                          'deployment_qualified': False, 'images': {}}
                if runtime == 'supabase':
                    report['image_config_identities_verified'] = True
                elif runtime in ('vitess', 'oracle-free', 'myduck'):
                    report.update(schema_version=1, image_binary_hashes_verified=True)
                    del report['release_runtime_qualified'], report['deployment_qualified']
                (directory / 'manifest.json').write_text(json.dumps(manifest))
                (directory / 'release-verification.json').write_text(json.dumps(report))
                archive = Path(self.temporary.name) / (runtime + '.tar.gz')
                MODULE['_archive'](directory, archive, 1000)
                with patch.object(runpy, 'run_path', return_value={'validate_metadata': lambda target, root: manifest}) as verifier:
                    MODULE['_verify_archive'](runtime, archive, self.root)
                    verifier.assert_called_once_with(str(self.root / f'release/verify-{runtime}-runtime.py'))
                with patch.object(runpy, 'run_path', return_value={'validate_metadata': lambda target, root: (_ for _ in ()).throw(ValueError('native evidence changed'))}):
                    with self.assertRaisesRegex(ValueError, 'native evidence changed'):
                        MODULE['_verify_archive'](runtime, archive, self.root)
                archive.unlink()
                report['anonymous_pull_verified'] = 1
                (directory / 'release-verification.json').write_text(json.dumps(report))
                MODULE['_archive'](directory, archive, 1000)
                with patch.object(runpy, 'run_path', return_value={'validate_metadata': lambda target, root: manifest}):
                    with self.assertRaisesRegex(ValueError, 'exact release verification'):
                        MODULE['_verify_archive'](runtime, archive, self.root)

    def test_unsafe_qualified_archive_never_reaches_metadata_verifier(self):
        for name, kind in (('../escape', tarfile.REGTYPE), ('neon-qualified/link', tarfile.SYMTYPE)):
            archive = Path(self.temporary.name) / 'unsafe.tar.gz'
            with tarfile.open(archive, 'w:gz') as tar:
                member = tarfile.TarInfo(name)
                member.type = kind
                member.size = 0
                tar.addfile(member, io.BytesIO())
            with patch.object(runpy, 'run_path') as verifier, self.assertRaisesRegex(ValueError, 'Unsafe'):
                MODULE['_verify_archive']('neon', archive, self.root)
            verifier.assert_not_called()


if __name__ == '__main__':
    unittest.main()
