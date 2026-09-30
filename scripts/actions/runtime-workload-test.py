#!/usr/bin/env python3
"""Pure runtime selection and evidence checks; no Docker or cluster execution."""
import copy
import importlib.util
import io
import json
from pathlib import Path
import unittest
from unittest.mock import patch

with patch('subprocess.Popen', side_effect=AssertionError('import launched a process')), \
        patch.object(Path, 'mkdir', side_effect=AssertionError('import created a workspace')):
    spec = importlib.util.spec_from_file_location('runtime_workload', Path(__file__).with_name('runtime-workload.py'))
    workload = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(workload)

CANDIDATE = 'ghcr.io/hakopod/buildkit:v0.32.2-hakopod-' + 'a' * 40 + '@sha256:' + 'b' * 64


class RuntimeSelectionTests(unittest.TestCase):
    def test_shared_workspace_is_explicit_and_cannot_build_or_publish_images(self):
        for flag in ('0', '1'):
            self.assertEqual(workload.runtime_selection_from_environment({'HAKOPOD_ACTIONS_SHARED_WORKSPACE': flag}), workload.runtime_buildkit_selection())
        for flag in ('true', '', 'yes', ' 1'):
            with self.subTest(flag=flag), self.assertRaisesRegex(RuntimeError, 'Shared workspace flag'):
                workload.runtime_selection_from_environment({'HAKOPOD_ACTIONS_SHARED_WORKSPACE': flag})
        for key in ('HAKOPOD_ACTIONS_BUILDKIT_CANDIDATE', 'HAKOPOD_ACTIONS_PUBLISH_CANDIDATE'):
            with self.subTest(key=key), self.assertRaisesRegex(RuntimeError, 'Shared workspace experiments'):
                workload.runtime_selection_from_environment({'HAKOPOD_ACTIONS_SHARED_WORKSPACE': '1', key: '1'})

    def test_default_remains_stock_033_native_and_candidate_is_explicit_overlay(self):
        stock = workload.runtime_buildkit_selection()
        self.assertEqual(stock['reference'], workload.BUILDKIT)
        self.assertEqual(stock['version'], 'v0.33.0')
        self.assertEqual(stock['snapshotter'], 'native')
        self.assertFalse(stock['force_overlay_diff'])
        self.assertEqual(workload.runtime_selection_from_environment({}), stock)
        for force in (False, True):
            selected = workload.runtime_buildkit_selection(CANDIDATE, force)
            self.assertEqual(selected['snapshotter'], 'overlayfs')
            command = workload.builder_command(selected, Path('/fixture.toml'))
            self.assertIn('image=' + CANDIDATE, command)
            self.assertEqual('env.BUILDKIT_DEBUG_FORCE_OVERLAY_DIFF=true' in command, force)
        for value in (None, workload.BUILDKIT, workload.benchmark_helpers()['BUILDKIT'], CANDIDATE.split('@')[0],
                      CANDIDATE + '\n', ' ' + CANDIDATE, CANDIDATE + ',network=host',
                      CANDIDATE.replace('/hakopod/', '/other/'), CANDIDATE.replace('v0.32.2', 'v0.32.3'),
                      CANDIDATE.replace('b' * 64, 'B' * 64)):
            with self.subTest(value=value), self.assertRaises(RuntimeError):
                workload.runtime_buildkit_selection(value)
        with self.assertRaisesRegex(RuntimeError, 'explicit candidate'):
            workload.runtime_buildkit_selection('', True)

    def test_runtime_candidate_rejects_every_export_or_candidate_build_overlap(self):
        candidate = {'HAKOPOD_ACTIONS_RUNTIME_BUILDKIT_IMAGE': CANDIDATE}
        self.assertEqual(workload.runtime_selection_from_environment(candidate)['reference'], CANDIDATE)
        for key in ('HAKOPOD_ACTIONS_BUILDKIT_CANDIDATE', 'HAKOPOD_ACTIONS_PUBLISH_CANDIDATE',
                    'HAKOPOD_ACTIONS_EXPORT_BENCHMARK', 'HAKOPOD_ACTIONS_BUILDKIT_QUALIFICATION',
                    'HAKOPOD_ACTIONS_EXPORT_BUILDKIT_IMAGE', 'HAKOPOD_ACTIONS_EXPORT_FORCE_OVERLAY_DIFF',
                    'HAKOPOD_ACTIONS_EXPORT_INTEGRATION_TESTS', 'HAKOPOD_ACTIONS_EXPORT_TEST_RUN'):
            with self.subTest(key=key), self.assertRaisesRegex(RuntimeError, 'separately'):
                workload.runtime_selection_from_environment({**candidate, key: '1'})
        for force in ('true', '', 'yes'):
            with self.subTest(force=force), self.assertRaises(RuntimeError):
                workload.runtime_selection_from_environment({**candidate, 'HAKOPOD_ACTIONS_RUNTIME_FORCE_OVERLAY_DIFF': force})
        with self.assertRaisesRegex(RuntimeError, 'explicit candidate'):
            workload.runtime_selection_from_environment({'HAKOPOD_ACTIONS_RUNTIME_FORCE_OVERLAY_DIFF': '1'})

    def test_actual_builder_requires_selected_native_image_source_snapshotter_and_env(self):
        for reference, force in (('', False), (CANDIDATE, False), (CANDIDATE, True)):
            selected = workload.runtime_buildkit_selection(reference, force)
            version = 'buildkitd github.com/moby/buildkit ' + selected['version'] + ' ' + selected['upstream_revision']
            workers = 'org.mobyproject.buildkit.worker.snapshotter: ' + selected['snapshotter'] + '\n'
            settings = ['HAKOPOD_BUILDKIT_USERXATTR=true'] if reference else []
            if force:
                settings.append('BUILDKIT_DEBUG_FORCE_OVERLAY_DIFF=true')
            container = {'Config': {'Image': selected['reference'], 'Env': settings}, 'Image': 'sha256:' + 'c' * 64}
            image = {'Id': container['Image'], 'Os': 'linux', 'Architecture': 'amd64'}
            identity = workload.runtime_builder_identity(selected, container, image, version, 'amd64', workers)
            self.assertEqual(identity['snapshotter'], selected['snapshotter'])
            self.assertEqual(identity['force_overlay_diff'], force)
            failures = [
                ({**container, 'Image': 'sha256:' + 'd' * 64}, image, version, workers),
                ({**container, 'Config': {**container['Config'], 'Image': CANDIDATE + 'wrong'}}, image, version, workers),
                ({**container, 'Config': {**container['Config'], 'Env': settings + ['HAKOPOD_BUILDKIT_USERXATTR=true']}}, image, version, workers),
                ({**container, 'Config': {**container['Config'], 'Env': settings + ['BUILDKIT_DEBUG_FORCE_OVERLAY_DIFF=false']}}, image, version, workers),
                (container, {**image, 'Architecture': 'arm64'}, version, workers),
                (container, {**image, 'Os': 'windows'}, version, workers),
                (container, image, version.replace(selected['upstream_revision'], '0' * 40), workers),
                (container, image, version.replace(selected['version'], 'v0.0.0'), workers),
                (container, image, version, workers.replace(selected['snapshotter'], 'other')),
                (container, image, version, workers + workers),
            ]
            for args in failures:
                with self.subTest(reference=reference, force=force, args=args), self.assertRaises(RuntimeError):
                    workload.runtime_builder_identity(selected, *args[:3], 'amd64', args[3])


class CompilerEvidenceTests(unittest.TestCase):
    def test_current_and_wrapped_buildx_progress_prove_cold_and_warm_compilers(self):
        records = []
        for architecture in ('amd64', 'arm64'):
            records += [{'id': architecture, 'name': '[linux/' + architecture + ' compile 4/4] RUN go build -p=1 main.go',
                         'started': '2026-09-30T00:00:00Z'},
                        {'id': architecture, 'completed': '2026-09-30T00:00:01Z'}]
        for wrapped in (False, True):
            for host in ('amd64', 'arm64'):
                values = [{'vertexes': [{('digest' if key == 'id' else key): value for key, value in item.items()}]}
                          if wrapped else item for item in records]
                metrics = workload.compiler_metrics(io.StringIO('\n'.join(map(json.dumps, values))), host)
                workload.verify_compiler_metrics(metrics, host, cold=True)
                self.assertEqual(sum(item['emulated'] for item in metrics), 1)
                warm = [{**item, 'cached': True} for item in metrics]
                workload.verify_compiler_metrics(warm, host, cold=False)
                with self.assertRaisesRegex(RuntimeError, 'Cold compiler'):
                    workload.verify_compiler_metrics(warm, host, cold=True)
                with self.assertRaisesRegex(RuntimeError, 'Warm build'):
                    workload.verify_compiler_metrics(metrics, host, cold=False)

    def test_missing_cached_incomplete_or_mislabeled_compilers_fail(self):
        vertices = [{'architecture': arch, 'cached': False, 'emulated': arch == 'arm64', 'duration_seconds': 1.0}
                    for arch in ('amd64', 'arm64')]
        invalid = [vertices[:1], vertices + vertices, [{**vertices[0]}, {**vertices[0]}]]
        for key, value in [('cached', True), ('emulated', False), ('duration_seconds', 0),
                           ('duration_seconds', float('nan')), ('duration_seconds', float('inf'))]:
            changed = copy.deepcopy(vertices)
            changed[1][key] = value
            invalid.append(changed)
        changed = copy.deepcopy(vertices)
        changed[1].pop('duration_seconds')
        invalid.append(changed)
        for values in invalid:
            with self.subTest(values=values), self.assertRaises(RuntimeError):
                workload.verify_compiler_metrics(values, 'amd64', cold=True)


if __name__ == '__main__':
    unittest.main()
