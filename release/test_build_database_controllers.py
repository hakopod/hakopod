"""Build fixtures exercise release admission without downloads or Kubernetes."""
import json
from pathlib import Path
import runpy
import sys
import tempfile
import unittest
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[1]
with patch.object(sys, 'path', [str(ROOT / 'installer'), *sys.path]):
    import build_database_controllers as builder

AVAILABILITY = runpy.run_path(str(ROOT / 'release/managed-runtime-availability.py'))


class BuildDatabaseControllersTest(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)
        self.here = self.root / 'installer'
        self.here.mkdir()
        (self.here / 'database-controller-sources.json').write_text(json.dumps(dict.fromkeys(builder.RELEASE_ENGINES, {})))
        (self.here / 'pins.json').write_text('{"helm": {}}')
        self.set_gates(False)
        self.patchers = [
            patch.object(builder, 'HERE', self.here),
            patch.object(builder.runpy, 'run_path', return_value={**AVAILABILITY, 'source_revision': lambda root: 'a' * 40}),
            patch.object(builder, 'helm_binary', return_value=Path('/fixture/helm')),
            patch.object(builder, 'render', side_effect=lambda engine, *args: [{'apiVersion': 'v1', 'kind': 'Namespace', 'metadata': {'name': engine}}]),
            patch.object(builder, 'qualify_vitess'),
            patch.object(builder, 'render_vitess', return_value=([], {'fixture': True})),
        ]
        self.mocks = [item.start() for item in self.patchers]
        for item in self.patchers:
            self.addCleanup(item.stop)

    def set_gates(self,enabled):
        for relative,name,kind in AVAILABILITY['GATES'].values():
            path=self.root/relative
            path.parent.mkdir(parents=True,exist_ok=True)
            value='true' if enabled else 'false'
            source=f'const {name} = {value}\n' if kind=='const' else f'func {name}() bool {{ return {value} }}\n'
            path.write_text('package fixture\n'+source)

    def test_default_bundle_omits_held_vitess_without_qualification_access(self):
        output=self.root/'held'
        builder.build(output,builder.REDIS_CONTROLLER_IMAGE)
        manifest=json.loads((output/'manifest.json').read_text())
        self.assertEqual(manifest['schema_version'],2)
        self.assertEqual(manifest['managed_runtimes'],dict.fromkeys(AVAILABILITY['GATES'],False))
        self.assertEqual(manifest['source_revision'],'a'*40)
        self.assertEqual(set(manifest['files']),{'postgresql.json','redis.json','mongodb.json'})
        self.assertFalse((output/'vitess.json').exists())
        self.mocks[4].assert_not_called()
        self.mocks[5].assert_not_called()

    def test_caller_and_environment_cannot_enable_a_held_runtime(self):
        with patch.dict(builder.os.environ,{'HAKOPOD_INCLUDE_VITESS':'true','HAKOPOD_VITESS_RELEASE_QUALIFIED':'true'}):
            with self.assertRaisesRegex(ValueError,'compiled release gate'):
                builder.build(self.root/'override',builder.REDIS_CONTROLLER_IMAGE,True)
        self.mocks[2].assert_not_called()
        self.mocks[4].assert_not_called()

    def test_enabled_runtime_requires_qualification_and_cannot_be_omitted(self):
        self.set_gates(True)
        with self.assertRaisesRegex(ValueError,'compiled release gate'):
            builder.build(self.root/'omitted',builder.REDIS_CONTROLLER_IMAGE,False)
        output=self.root/'qualified'
        builder.build(output,builder.REDIS_CONTROLLER_IMAGE)
        self.mocks[4].assert_called_once_with(self.root)
        self.mocks[5].assert_called_once()
        self.assertTrue((output/'vitess.json').is_file())
        self.assertTrue(json.loads((output/'manifest.json').read_text())['managed_runtimes']['vitess'])

    def test_enabled_qualification_failure_cannot_create_a_bundle(self):
        self.set_gates(True)
        self.mocks[4].side_effect=ValueError('native evidence is absent')
        output=self.root/'unqualified'
        with self.assertRaisesRegex(ValueError,'native evidence is absent'):
            builder.build(output,builder.REDIS_CONTROLLER_IMAGE)
        self.assertFalse(output.exists())
        self.mocks[2].assert_not_called()

    def test_missing_source_gate_stops_before_downloads(self):
        (self.root/AVAILABILITY['GATES']['neon'][0]).unlink()
        with self.assertRaisesRegex(ValueError,'Missing'):
            builder.build(self.root/'missing',builder.REDIS_CONTROLLER_IMAGE)
        self.mocks[2].assert_not_called()


if __name__=='__main__':
    unittest.main()
