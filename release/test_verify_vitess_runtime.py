import copy
import hashlib
import importlib.util
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch


spec = importlib.util.spec_from_file_location('vitess_release', Path(__file__).with_name('verify-vitess-runtime.py'))
vitess = importlib.util.module_from_spec(spec)
spec.loader.exec_module(vitess)


ETCD_IMAGE = 'quay.io/coreos/etcd:v3.5.17@sha256:' + '9' * 64


def environment_fixture(case, images):
    filesystem = {'capacity_bytes': 78 * 1024 ** 3, 'available_bytes': 20 * 1024 ** 3}
    canonical = [reference.split('@')[0].rsplit(':', 1)[0] + '@' + reference.split('@')[1]
                 for reference in [*images.values(), ETCD_IMAGE]]
    return {'schema_version': 1, 'case': case, 'minimum_free_bytes': 12 * 1024 ** 3,
            'fixture_budget_bytes': 4 * 1024 ** 3, 'host_filesystem': copy.deepcopy(filesystem),
            'nodes': [{'name': name, 'architecture': 'amd64', 'operating_system': 'linux',
                       'schedulable': True, 'image_gc_high_threshold_percent': 85,
                       'conditions': {'Ready': 'True', 'DiskPressure': 'False', 'MemoryPressure': 'False', 'PIDPressure': 'False'},
                       'filesystems': {'nodefs': copy.deepcopy(filesystem), 'imagefs': copy.deepcopy(filesystem)},
                       'cached_images': sorted(canonical)}
                      for name in ('k3d-hakopod-dev-server-0', 'k3d-hakopod-database-worker-0')]}


class VitessReleaseVerificationTest(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name) / 'source'
        self.directory = Path(self.temporary.name) / 'qualification'
        self.directory.mkdir()
        for name in ('internal/cluster', 'internal/database', 'internal/backup', 'internal/spec',
                     'templates/blueprints/fixture', 'patches', 'scripts', 'release', 'installer'):
            (self.root / name).mkdir(parents=True)
        self.manifest = {'schema_version': 1, 'platform': 'linux/amd64', 'sources': {}, 'images': {}}
        source = []
        self.binary_data = {}
        checksums = []
        for index, (kind, package) in enumerate(vitess.PACKAGES.items()):
            image = package + ':fixture@sha256:' + str(index + 1) * 64
            revision = str(index + 3) * 40
            symbol = 'vitessServer' if kind == 'runtime' else 'vitessOperator'
            source.extend([f'{symbol}Image = "{image}"', f'{symbol}Source = "{revision}"'])
            base = 'example.test/fixture:1@sha256:' + str(index + 5) * 64
            (self.root / ('Dockerfile.vitess-' + kind)).write_text('FROM ' + base + '\n')
            binaries = {}
            for path in sorted(vitess.BINARIES[kind]):
                raw = ('test fixture binary: ' + path).encode()
                digest = hashlib.sha256(raw).hexdigest()
                binaries[path] = digest
                self.binary_data[path] = raw
                folder = 'runtime' if kind == 'runtime' else 'controller'
                checksums.append(digest + '  ' + folder + '/bin/' + Path(path).name)
            self.manifest['images'][kind] = {'reference': image, 'base_image': base, 'binaries': binaries}
            self.manifest['sources'][kind] = {'repository': vitess.SOURCE_REPOSITORIES[kind], 'revision': revision}
        source.append('vitessEtcdImage = "' + ETCD_IMAGE + '"')
        source.append('vitessReleaseQualified = true')
        (self.root / 'internal/cluster/database_vitess.go').write_text('\n'.join(source))
        for name in ('go.mod', 'go.sum', 'scripts/apply-managed-vitess-patches.py', 'scripts/build-managed-vitess.sh',
                     'scripts/run-development-vitess-acceptance.py', 'release/verify-vitess-runtime.py',
                     'release/record-vitess-qualification.py', 'installer/vitess_controller.py'):
            (self.root / name).write_text('test fixture source\n')
        (self.root / 'internal/cluster/live_database_vitess_test.go').write_text('test fixture acceptance source\n')
        for name in ('internal/spec/templates.go', 'internal/cluster/volume_copy.py',
                     'templates/embed.go', 'templates/catalog.json', 'templates/blueprints/fixture/hakopod.toml',
                     'internal/spec/.embedded-fixture'):
            (self.root / name).write_text('test fixture transitive source\n')
        self.manifest['source_files'] = vitess.source_files(self.root)
        self.acceptance = {'schema_version': 1, 'context': 'k3d-hakopod-dev', 'execution': 'native', 'platform': 'linux/amd64',
                           'passed': True, 'failed_tests': [], 'passed_tests': sorted(vitess.REQUIRED_TESTS),
                           'images': {kind: item['reference'] for kind, item in self.manifest['images'].items()},
                           'source_files': self.manifest['source_files']}
        self.acceptance['attempts'] = [{'case': case, 'environment': environment_fixture(case, self.acceptance['images'])}
                                       for case in ('lifecycle', 'recovery', 'reseed', 'revocation')]
        self.acceptance['test_events'] = [
            {'Action': action, 'Package': 'github.com/hakopod/hakopod/internal/cluster', 'Test': name}
            for name in sorted(vitess.REQUIRED_TESTS) for action in ('run', 'pass')]
        (self.directory / 'runtime-upstream.patch').write_text('test fixture runtime patch\n')
        (self.directory / 'operator-upstream.patch').write_text('test fixture operator patch\n')
        (self.directory / 'binary-sha256.txt').write_text('\n'.join(checksums) + '\n')
        self.save()

    def save(self):
        (self.directory / 'native-acceptance.json').write_text(json.dumps(self.acceptance))
        self.manifest['files'] = {name: vitess.file_hash(self.directory / name) for name in (
            'runtime-upstream.patch', 'operator-upstream.patch', 'binary-sha256.txt', 'native-acceptance.json')}
        (self.directory / 'manifest.json').write_text(json.dumps(self.manifest))

    def test_accepts_exact_native_source_and_binaries(self):
        self.assertEqual(vitess.validate_metadata(self.directory, self.root), self.manifest)

    def test_rejects_missing_or_repeated_environment_case(self):
        original = copy.deepcopy(self.acceptance['attempts'])
        for attempts in (None, [], original[:-1], [*original[:-1], original[0]]):
            self.acceptance['attempts'] = attempts
            self.save()
            with self.assertRaises(ValueError):
                vitess.validate_metadata(self.directory, self.root)

    def test_rejects_pressure_missing_images_and_unfunded_fixture_budget(self):
        changes = [
            lambda item: item.update(schema_version=True),
            lambda item: item.update(case='recovery'),
            lambda item: item.update(minimum_free_bytes=10 * 1024 ** 3),
            lambda item: item.update(fixture_budget_bytes=0),
            lambda item: item.update(fixture_budget_bytes=True),
            lambda item: item['host_filesystem'].update(available_bytes=15 * 1024 ** 3),
            lambda item: item['nodes'][0]['conditions'].update(DiskPressure='True'),
            lambda item: item['nodes'][0]['conditions'].pop('DiskPressure'),
            lambda item: item['nodes'][0].update(schedulable=False),
            lambda item: item['nodes'][0].update(name='provider-smoke'),
            lambda item: item['nodes'][0].update(image_gc_high_threshold_percent=70),
            lambda item: item['nodes'][0].update(image_gc_high_threshold_percent=True),
            lambda item: item['nodes'][0]['filesystems']['imagefs'].update(available_bytes=15 * 1024 ** 3),
            lambda item: item['nodes'][0]['filesystems']['nodefs'].update(capacity_bytes=True),
            lambda item: item['nodes'][0]['cached_images'].pop(),
            lambda item: item['nodes'][0]['cached_images'].__setitem__(0, 'example.test/replaced@sha256:' + 'a' * 64),
            lambda item: item['nodes'][0].update(raw_log='must not be retained'),
        ]
        for change in changes:
            with self.subTest(change=change):
                environment = environment_fixture('lifecycle', self.acceptance['images'])
                change(environment)
                with self.assertRaises(ValueError):
                    vitess.validate_native_environment(environment, 'lifecycle', [*self.acceptance['images'].values(), ETCD_IMAGE])

    def test_accepts_exact_budget_boundary_and_preserves_larger_gc_reserve(self):
        environment = environment_fixture('lifecycle', self.acceptance['images'])
        environment['host_filesystem']['available_bytes'] = 16 * 1024 ** 3
        for node in environment['nodes']:
            for filesystem in node['filesystems'].values():
                filesystem['available_bytes'] = 16 * 1024 ** 3
        vitess.validate_native_environment(environment, 'lifecycle', [*self.acceptance['images'].values(), ETCD_IMAGE])
        environment['nodes'][0]['image_gc_high_threshold_percent'] = 80
        with self.assertRaisesRegex(ValueError, 'reserve'):
            vitess.validate_native_environment(environment, 'lifecycle', [*self.acceptance['images'].values(), ETCD_IMAGE])

    def test_rejects_missing_boolean_or_unknown_metadata_schema(self):
        for target in (self.manifest, self.acceptance):
            for value in (None, True, False, 2, '1'):
                with self.subTest(target='manifest' if target is self.manifest else 'acceptance', schema=value):
                    if value is None:
                        target.pop('schema_version', None)
                    else:
                        target['schema_version'] = value
                    self.save()
                    with self.assertRaisesRegex(ValueError, 'schema 1'):
                        vitess.validate_metadata(self.directory, self.root)
            target['schema_version'] = 1

    def test_rejects_source_and_test_changes_after_qualification(self):
        for name in ('internal/cluster/database_vitess.go', 'internal/cluster/live_database_vitess_test.go',
                     'internal/spec/templates.go', 'internal/cluster/volume_copy.py', 'templates/catalog.json',
                     'templates/blueprints/fixture/hakopod.toml', 'internal/spec/.embedded-fixture'):
            path = self.root / name
            original = path.read_text()
            path.write_text(original + '\nchanged\n')
            with self.assertRaisesRegex(ValueError, 'source changed'):
                vitess.validate_metadata(self.directory, self.root)
            path.write_text(original)

    def test_rejects_qualification_for_a_shipping_source_with_admission_closed(self):
        path = self.root / 'internal/cluster/database_vitess.go'
        path.write_text(path.read_text().replace('vitessReleaseQualified = true', 'vitessReleaseQualified = false'))
        self.manifest['source_files'] = vitess.source_files(self.root)
        self.acceptance['source_files'] = self.manifest['source_files']
        self.save()
        with self.assertRaisesRegex(ValueError, 'admission remains closed'):
            vitess.validate_metadata(self.directory, self.root)

    def test_rejects_symbolic_files_and_directories(self):
        for name, target in (('linked.py', self.root / 'internal/cluster/volume_copy.py'),
                             ('linked-directory', self.root / 'templates')):
            path = self.root / 'internal' / name
            path.symlink_to(target)
            with self.assertRaisesRegex(ValueError, 'Symbolic'):
                vitess.source_files(self.root)
            path.unlink()

    def test_bounds_qualification_source_inventory(self):
        for name in ('MAX_SOURCE_FILES', 'MAX_SOURCE_BYTES'):
            with patch.object(vitess, name, 1), self.assertRaisesRegex(ValueError, 'limit'):
                vitess.source_files(self.root)

    def test_rejects_replaced_patch(self):
        (self.directory / 'operator-upstream.patch').write_text('different patch')
        with self.assertRaisesRegex(ValueError, 'artifact checksum'):
            vitess.validate_metadata(self.directory, self.root)

    def test_rejects_unqualified_image_digest(self):
        self.manifest['images']['runtime']['reference'] = vitess.PACKAGES['runtime'] + '@sha256:' + 'f' * 64
        self.save()
        with self.assertRaisesRegex(ValueError, 'qualified digest'):
            vitess.validate_metadata(self.directory, self.root)

    def test_rejects_incomplete_or_foreign_native_acceptance(self):
        original = copy.deepcopy(self.acceptance)
        for change in ({'passed_tests': ['TestManagedVitessLive/standalone']},
                       {'failed_tests': ['TestManagedVitessNativeReseedLive']},
                       {'context': 'operator-cluster'}, {'execution': 'emulated'},
                       {'platform': 'linux/arm64'}, {'passed': False}):
            with self.subTest(change=change):
                self.acceptance = dict(original, **change)
                self.save()
                with self.assertRaisesRegex(ValueError, 'acceptance'):
                    vitess.validate_metadata(self.directory, self.root)

    def test_rejects_symlinked_evidence(self):
        path = self.directory / 'native-acceptance.json'
        raw = path.read_bytes()
        path.unlink()
        target = Path(self.temporary.name) / 'outside-evidence.json'
        target.write_bytes(raw)
        path.symlink_to(target)
        with self.assertRaisesRegex(ValueError, 'symbolic'):
            vitess.validate_metadata(self.directory, self.root)

    def test_rejects_invented_pass_without_run_or_failed_attempt(self):
        events = copy.deepcopy(self.acceptance['test_events'])
        for bad in ([event for event in events if event['Action'] == 'pass'],
                    [dict(events[0], Action='skip'), *events[1:]],
                    [*events, dict(events[0], Action='fail')],
                    [*events, events[0]],
                    [*events, events[0], events[1]],
                    [dict(events[0], Output='raw logs do not belong in public evidence'), *events[1:]]):
            with self.subTest(events=bad):
                self.acceptance['test_events'] = bad
                self.save()
                with self.assertRaisesRegex(ValueError, 'acceptance|events|evidence'):
                    vitess.validate_metadata(self.directory, self.root)

    def image_runner(self, corrupt=False, architecture='amd64', runtime_config=None):
        calls = []
        active = {}
        def run(arguments, config):
            self.assertTrue(config.is_dir())
            self.assertEqual(list(config.iterdir()), [])
            calls.append(arguments)
            if arguments[0] == 'pull':
                return ''
            if arguments[:2] == ['image', 'inspect']:
                kind = next(kind for kind, item in self.manifest['images'].items() if item['reference'] == arguments[-1])
                item = self.manifest['images'][kind]
                label = 'io.hakopod.vitess.upstream' if kind == 'runtime' else 'io.hakopod.vitess.operator-upstream'
                config = copy.deepcopy(vitess.RUNTIME_CONFIG) if kind == 'runtime' else {}
                if kind == 'runtime' and runtime_config:
                    config.update(runtime_config)
                config['Labels'] = {'org.opencontainers.image.source': 'https://github.com/hakopod/hakopod',
                                    label: self.manifest['sources'][kind]['revision']}
                return json.dumps([{'Os': 'linux', 'Architecture': architecture,
                    'RepoDigests': [vitess.PACKAGES[kind] + '@' + item['reference'].rsplit('@', 1)[1]],
                    'Config': config}])
            if arguments[0] == 'create':
                self.assertIn('--network', arguments)
                self.assertIn('none', arguments)
                identity = 'a' * 64
                active[identity] = True
                return identity + '\n'
            if arguments[0] == 'cp':
                identity, path = arguments[1].split(':', 1)
                self.assertIn(identity, active)
                Path(arguments[2]).write_bytes(b'changed' if corrupt else self.binary_data[path])
                return ''
            if arguments[:2] == ['rm', '-v']:
                active.pop(arguments[-1])
                return ''
            self.fail('Unexpected or executable Docker command: ' + repr(arguments))
        return run, calls, active

    def test_verifies_binary_content_without_running_images(self):
        runner, calls, active = self.image_runner()
        vitess.verify_images(self.manifest, runner)
        self.assertFalse(active)
        self.assertEqual(len([call for call in calls if call[0] == 'cp']), 8)
        self.assertFalse(any(call[0] in ('run', 'start', 'exec', 'build') for call in calls))

    def test_rejects_changed_binary_and_cleans_container(self):
        runner, _, active = self.image_runner(corrupt=True)
        with self.assertRaisesRegex(ValueError, 'binary differs'):
            vitess.verify_images(self.manifest, runner)
        self.assertFalse(active)

    def test_rejects_unqualified_architecture_before_creating_container(self):
        runner, calls, active = self.image_runner(architecture='arm64')
        with self.assertRaisesRegex(ValueError, 'platform'):
            vitess.verify_images(self.manifest, runner)
        self.assertFalse(active)
        self.assertFalse(any(call[0] == 'create' for call in calls))

    def test_rejects_changed_runtime_configuration(self):
        for changed in ({'User': 'root'}, {'Env': []}, {'Volumes': {}},
                        {'Cmd': ['sh']}, {'Entrypoint': ['/unexpected']},
                        {'WorkingDir': '/unexpected'}, {'Healthcheck': {'Test': ['CMD', 'true']}},
                        {'ExposedPorts': {'3306/tcp': {}}}, {'StopSignal': 'SIGKILL'},
                        {'OnBuild': ['RUN true']}, {'ArgsEscaped': 'true'}):
            with self.subTest(changed=changed):
                runner, calls, active = self.image_runner(runtime_config=changed)
                with self.assertRaisesRegex(ValueError, 'runtime configuration'):
                    vitess.verify_images(self.manifest, runner)
                self.assertFalse(active)
                self.assertFalse(any(call[0] == 'create' for call in calls))

    def test_accepts_equivalent_linux_runtime_defaults(self):
        runner, _, active = self.image_runner(runtime_config={'WorkingDir': '/', 'ArgsEscaped': True})
        vitess.verify_images(self.manifest, runner)
        self.assertFalse(active)
