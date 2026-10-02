"""Read-only native runner preflight regressions. These do not use Kubernetes."""
import copy
import importlib.util
from pathlib import Path
import subprocess
import runpy
from types import SimpleNamespace
import unittest
from unittest.mock import patch


spec = importlib.util.spec_from_file_location('vitess_acceptance', Path(__file__).with_name('run-development-vitess-acceptance.py'))
runner = importlib.util.module_from_spec(spec)
spec.loader.exec_module(runner)
verifier = runpy.run_path(str(Path(__file__).parents[1] / 'release/verify-vitess-runtime.py'))


class NativePreflightTests(unittest.TestCase):
    def setUp(self):
        self.images = {'runtime': 'ghcr.io/hakopod/runtime:2@sha256:' + '1' * 64,
                       'operator': 'ghcr.io/hakopod/operator:2@sha256:' + '2' * 64,
                       'etcd': 'quay.io/coreos/etcd:3@sha256:' + '3' * 64}
        self.inventory = [{'metadata': {'name': name}, 'status': {
            'nodeInfo': {'architecture': 'amd64', 'operatingSystem': 'linux'},
            'conditions': [{'type': kind, 'status': status} for kind, status in (
                ('Ready', 'True'), ('DiskPressure', 'False'), ('MemoryPressure', 'False'), ('PIDPressure', 'False'))]}}
            for name in runner.NODES]

    def metadata(self, command):
        if command[-1].endswith('/stats/summary'):
            filesystem = {'capacityBytes': 78 * runner.GIB, 'availableBytes': 20 * runner.GIB}
            return {'node': {'fs': filesystem, 'runtime': {'imageFs': filesystem}}}
        if command[-1].endswith('/configz'):
            return {'kubeletconfig': {'imageGCHighThresholdPercent': 85}}
        return {'images': [{'repoDigests': [runner.canonical_image(reference)]} for reference in self.images.values()]}

    def collect(self, metadata=None, inventory=None):
        disk = SimpleNamespace(total=78 * runner.GIB, free=20 * runner.GIB)
        with patch.object(runner.shutil, 'disk_usage', return_value=disk), \
                patch.object(runner, 'command_json', side_effect=metadata or self.metadata) as commands:
            report = runner.native_environment(Path('/fixture'), ['kubectl'],
                self.inventory if inventory is None else inventory, self.images, 'lifecycle', 4)
        return report, commands

    def test_uses_canonical_digests_and_records_real_node_measurements(self):
        report, commands = self.collect()
        self.assertEqual(report['fixture_budget_bytes'], 4 * runner.GIB)
        for node in report['nodes']:
            self.assertEqual(node['conditions']['DiskPressure'], 'False')
            self.assertEqual(node['filesystems']['imagefs']['available_bytes'], 20 * runner.GIB)
            self.assertEqual(node['image_gc_high_threshold_percent'], 85)
            self.assertEqual(node['cached_images'], sorted(runner.canonical_image(image) for image in self.images.values()))
        inspections = [item.args[0] for item in commands.call_args_list if item.args[0][0] == 'docker']
        self.assertEqual(len(inspections), 2)
        self.assertTrue(all(command[2] in runner.NODES and command[3:] == ['crictl', 'images', '-o', 'json'] for command in inspections))

    def test_missing_or_different_digest_is_recorded_as_absent(self):
        def missing(command):
            if command[0] == 'docker':
                return {'images': [{'repoDigests': [runner.canonical_image(self.images['etcd'])]},
                                   {'repoDigests': ['ghcr.io/hakopod/other@sha256:' + '4' * 64]}]}
            return self.metadata(command)
        report, _ = self.collect(missing)
        self.assertTrue(all(node['cached_images'] == [runner.canonical_image(self.images['etcd'])] for node in report['nodes']))

    def test_cri_command_failure_is_not_reported_as_an_empty_cache(self):
        def unavailable(command):
            if command[0] == 'docker':
                raise subprocess.CalledProcessError(1, command)
            return self.metadata(command)
        with self.assertRaises(subprocess.CalledProcessError):
            self.collect(unavailable)

    def test_invalid_or_unbounded_cri_inventory_is_rejected(self):
        for inventory in (None, {}, [None], [{}], [{'repoDigests': None}], [{'repoDigests': [None]}], [{}] * 1025):
            def invalid(command):
                return {'images': inventory} if command[0] == 'docker' else self.metadata(command)
            with self.subTest(inventory=inventory), self.assertRaises(RuntimeError):
                self.collect(invalid)

    def test_foreign_or_duplicated_nodes_cannot_receive_docker_commands(self):
        foreign = copy.deepcopy(self.inventory)
        foreign[0]['metadata']['name'] = 'provider-smoke'
        for inventory in (foreign, self.inventory[:1], [self.inventory[0], self.inventory[0]]):
            with self.subTest(inventory=inventory), patch.object(runner, 'command_json') as command:
                with self.assertRaises(RuntimeError):
                    runner.native_environment(Path('/fixture'), ['kubectl'], inventory, self.images, 'lifecycle', 4)
                command.assert_not_called()

    def test_two_or_three_exact_nodes_are_accepted(self):
        self.assertEqual(runner.fixture_nodes(','.join(runner.NODES)), runner.NODES)
        self.assertEqual(runner.fixture_nodes(','.join(runner.ALLOWED_NODES)), runner.ALLOWED_NODES)

    def test_empty_duplicate_single_or_foreign_node_sets_are_rejected(self):
        for value in ('', runner.NODES[0], runner.NODES[0]+','+runner.NODES[0], runner.NODES[0]+',provider-smoke'):
            with self.subTest(value=value), self.assertRaises(RuntimeError):
                runner.fixture_nodes(value)

    def test_release_verifier_accepts_worker_pair_and_all_three_nodes(self):
        third = copy.deepcopy(self.inventory[0])
        third['metadata']['name'] = 'k3d-hakopod-database-worker-1'
        for inventory, names in (([copy.deepcopy(self.inventory[1]), third], runner.ALLOWED_NODES[1:]),
                                 (self.inventory + [third], runner.ALLOWED_NODES)):
            disk = SimpleNamespace(total=78 * runner.GIB, free=20 * runner.GIB)
            with patch.object(runner.shutil, 'disk_usage', return_value=disk), patch.object(runner, 'command_json', side_effect=self.metadata):
                report = runner.native_environment(Path('/fixture'), ['kubectl'], inventory, self.images, 'lifecycle', 4, names)
            verifier['validate_native_environment'](report, 'lifecycle', list(self.images.values()))

    def test_release_verifier_rejects_duplicate_and_foreign_nodes(self):
        report, _ = self.collect()
        for names in (['k3d-hakopod-dev-server-0'] * 2,
                      ['k3d-hakopod-dev-server-0', 'provider-smoke']):
            bad = copy.deepcopy(report)
            for node, name in zip(bad['nodes'], names):
                node['name'] = name
            with self.assertRaises(ValueError):
                verifier['validate_native_environment'](bad, 'lifecycle', list(self.images.values()))

    def test_registry_port_survives_tag_removal(self):
        digest = '@sha256:' + 'a' * 64
        self.assertEqual(runner.canonical_image('registry.test:5000/repo:2' + digest), 'registry.test:5000/repo' + digest)
        self.assertEqual(runner.canonical_image('registry.test:5000/repo' + digest), 'registry.test:5000/repo' + digest)

    def test_go_overlays_workspaces_and_target_overrides_are_removed(self):
        ambient = {'PATH': '/usr/bin', 'GOFLAGS': '-overlay=/unreviewed.json -tags=unreviewed',
                   'GOWORK': '/other/go.work', 'GOENV': '/other/env', 'GOOS': 'darwin',
                   'GOARCH': 'arm64', 'GOTOOLCHAIN': 'auto', 'GOEXPERIMENT': 'unreviewed',
                   'CGO_CFLAGS': '-include /other/input.h', 'CC': 'other-compiler',
                   'HAKOPOD_OTHER': 'other-context', 'AWS_PROFILE': 'other-profile'}
        with patch.dict(runner.os.environ, ambient, clear=True):
            env = runner.native_go_environment(Path('/fixture'), Path('/fixture/kube'), Path('/fixture/storage'))
        for name in ('GOEXPERIMENT', 'CGO_CFLAGS', 'HAKOPOD_OTHER', 'AWS_PROFILE'):
            self.assertNotIn(name, env)
        for name, value in {'GOFLAGS': '-mod=readonly', 'GOENV': 'off', 'GOWORK': 'off',
                            'GOTOOLCHAIN': 'local', 'GOOS': 'linux', 'GOARCH': 'amd64',
                            'CGO_ENABLED': '1', 'CC': '/usr/bin/gcc'}.items():
            self.assertEqual(env[name], value)


if __name__ == '__main__':
    unittest.main()
