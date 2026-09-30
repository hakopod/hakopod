#!/usr/bin/env python3
"""Bounded capture regressions using tiny Python writers, never a real cluster."""
import copy
import importlib.util
import json
import os
from pathlib import Path
import signal
import subprocess
import sys
import tempfile
import time
from types import SimpleNamespace
import unittest
from unittest.mock import patch

with patch('subprocess.Popen', side_effect=AssertionError('import launched a process')):
    spec = importlib.util.spec_from_file_location('docker_acceptance', Path(__file__).with_name('docker-acceptance.py'))
    acceptance = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(acceptance)
POPEN = subprocess.Popen
RUNNING = {'status': {'containerStatuses': [{'name': 'runner', 'state': {'running': {'startedAt': '2026-09-30T00:00:00Z'}}}]}}


def writer(code):
    def spawn(_args, **kwargs):
        return POPEN([sys.executable, '-u', '-c', code], **kwargs)
    return spawn


class CaptureTests(unittest.TestCase):
    def test_success_requires_complete_clean_capture(self):
        valid = {'started': True, 'stdout_bytes': 100, 'exit_code': 0}
        acceptance.verify_log_capture(valid)
        for key, value in [('started', False), ('stdout_bytes', 0), ('exit_code', 1), ('exit_code', None),
                ('truncated', True), ('deadline_reached', True), ('drain_deadline_reached', True),
                ('error_type', 'CollectorDidNotStop'), ('capture_error_type', 'ValueError'),
                ('malformed_events', 1), ('malformed_reports', 1)]:
            with self.subTest(key=key, value=value), self.assertRaises(AssertionError):
                acceptance.verify_log_capture({**valid, key: value})

    def test_eviction_keeps_earlier_phase_and_command_without_terminal_refetch(self):
        event = {'phase': 'docker-command', 'status': 'started', 'arguments': ['pull', 'selected-candidate']}
        raw = 'HAKOPOD_EXPORT_BENCHMARK ' + json.dumps(event) + '\n'
        code = 'import sys; sys.stdout.write(' + repr(raw) + '); sys.stdout.flush(); sys.stderr.write("container removed\\n"); sys.exit(1)'
        pod = {'metadata': {'name': 'actions-runtime-fixture'}, 'spec': {'nodeName': acceptance.NODE, 'initContainers': [], 'containers': []},
               'status': {'phase': 'Failed', 'reason': 'Evicted', 'message': '4Gi workspace exceeded'}}
        def evicted(*args, **kwargs):
            kwargs['capture'].observe(RUNNING)
            return pod
        with tempfile.TemporaryDirectory() as temp, patch.object(acceptance, 'OUTPUT', Path(temp)), \
                patch.object(acceptance, 'REPORT', {}), patch.object(acceptance, 'DEADLINE', time.monotonic() + 10), \
                patch.object(acceptance.subprocess, 'Popen', side_effect=writer(code)), \
                patch.object(acceptance, 'export_fixture', return_value='fixture'), patch.object(acceptance, 'apply'), \
                patch.object(acceptance, 'wait_for', side_effect=evicted), \
                patch.object(acceptance, 'command', return_value=SimpleNamespace(stdout='[0,0,0,0,""]')), \
                patch.object(acceptance, 'logs', side_effect=AssertionError('terminal refetch discarded evidence')):
            with self.assertRaisesRegex(AssertionError, 'exactly one JSON report'):
                acceptance.run_export_benchmark({})
            self.assertEqual((Path(temp) / 'export-benchmark.log').read_text(), raw)
            self.assertEqual(acceptance.REPORT['export_benchmark_events'], [event])
            self.assertEqual(acceptance.REPORT['export_benchmark_pod']['reason'], 'Evicted')
            self.assertEqual(acceptance.REPORT['export_log_capture']['exit_code'], 1)

    def test_complete_failure_report_survives_a_wait_error(self):
        result = {'status': 'failed', 'error': 'storage failure'}
        raw = 'HAKOPOD_EXPORT_REPORT ' + json.dumps(result) + '\n'
        def failed_wait(*args, **kwargs):
            kwargs['capture'].observe(RUNNING)
            raise RuntimeError('API disconnected after source failure')
        with tempfile.TemporaryDirectory() as temp, patch.object(acceptance, 'OUTPUT', Path(temp)), \
                patch.object(acceptance, 'REPORT', {}), patch.object(acceptance, 'DEADLINE', time.monotonic() + 10), \
                patch.object(acceptance.subprocess, 'Popen', side_effect=writer('print(' + repr(raw[:-1]) + ')')), \
                patch.object(acceptance, 'export_fixture', return_value='fixture'), patch.object(acceptance, 'apply'), \
                patch.object(acceptance, 'wait_for', side_effect=failed_wait):
            with self.assertRaisesRegex(RuntimeError, 'API disconnected'):
                acceptance.run_export_benchmark({})
            self.assertEqual(json.loads((Path(temp) / 'export-benchmark-report.json').read_text()), result)

    def test_stdout_and_stderr_are_bounded_while_the_process_is_running(self):
        for stream, limit, suffix in [('stdout', 128, '.log'), ('stderr', 65536, '-capture.log')]:
            code = 'import sys,time; sys.' + stream + '.write("x"*200000); sys.' + stream + '.flush(); time.sleep(10)'
            with self.subTest(stream=stream), tempfile.TemporaryDirectory() as temp, \
                    patch.object(acceptance, 'OUTPUT', Path(temp)), patch.object(acceptance, 'LOG_LIMIT', 128), \
                    patch.object(acceptance, 'DEADLINE', time.monotonic() + 10), \
                    patch.object(acceptance.subprocess, 'Popen', side_effect=writer(code)) as follower:
                capture = acceptance.BoundedLogCapture('fixture', 'export-benchmark')
                capture.observe(RUNNING)
                result = capture.close()
                self.assertTrue(result['truncated'])
                self.assertEqual((Path(temp) / ('export-benchmark' + suffix)).stat().st_size, limit)
                self.assertFalse(capture.thread.is_alive())
                self.assertIsNotNone(capture.process.poll())
                arguments = follower.call_args.args[0]
                self.assertIn('--tail=-1', arguments)
                self.assertFalse(any(value.startswith('--limit-bytes') for value in arguments))

    def test_pipe_holding_descendant_is_terminated_after_parent_exits(self):
        child = 'import signal,time; signal.signal(signal.SIGTERM, signal.SIG_IGN); print("child ready",flush=True); time.sleep(20)'
        code = 'import subprocess,sys; subprocess.Popen([sys.executable,"-u","-c",' + repr(child) + '])'
        with tempfile.TemporaryDirectory() as temp, patch.object(acceptance, 'OUTPUT', Path(temp)), \
                patch.object(acceptance, 'DEADLINE', time.monotonic() + 10), \
                patch.object(acceptance.subprocess, 'Popen', side_effect=writer(code)), \
                patch.object(acceptance.os, 'killpg', wraps=os.killpg) as groups:
            capture = acceptance.BoundedLogCapture('fixture', 'export-benchmark')
            capture.observe(RUNNING)
            started = time.monotonic()
            result = capture.close()
            self.assertLess(time.monotonic() - started, 7)
            self.assertFalse(capture.thread.is_alive())
            self.assertNotIn('error_type', result)
            groups.assert_any_call(capture.process.pid, signal.SIGTERM)
            groups.assert_any_call(capture.process.pid, signal.SIGKILL)

    def test_capture_parse_failure_does_not_replace_the_wait_error(self):
        with tempfile.TemporaryDirectory() as temp, patch.object(acceptance, 'OUTPUT', Path(temp)), \
                patch.object(acceptance, 'REPORT', {}), patch.object(acceptance, 'export_fixture', return_value='fixture'), \
                patch.object(acceptance, 'apply'), patch.object(acceptance, 'wait_for', side_effect=RuntimeError('original wait error')), \
                patch.object(acceptance, 'captured_records', side_effect=ValueError('capture parse error')):
            with self.assertRaisesRegex(RuntimeError, 'original wait error'):
                acceptance.run_export_benchmark({})
            self.assertEqual(acceptance.REPORT['export_log_capture']['capture_error_type'], 'ValueError')

    def test_partial_last_record_never_hides_previous_complete_evidence(self):
        with tempfile.TemporaryDirectory() as temp, patch.object(acceptance, 'OUTPUT', Path(temp)):
            (Path(temp) / 'export-benchmark.log').write_bytes(b'PREFIX {"phase":"prefetch"}\nPREFIX {"phase":')
            records, malformed = acceptance.captured_records('export-benchmark', 'PREFIX ')
            self.assertEqual(records, [{'phase': 'prefetch'}])
            self.assertEqual(malformed, 1)


class QualificationEnvelopeTests(unittest.TestCase):
    def test_qualification_requires_verified_native_builders_and_cleanup(self):
        selected = acceptance.export_buildkit_selection('ghcr.io/hakopod/buildkit:v0.32.2-hakopod-' + 'a' * 40 + '@sha256:' + 'b' * 64)
        builder = {'image': selected['reference'], 'architecture': 'amd64', 'version': selected['version'],
                   'image_id': 'sha256:' + 'c' * 64, 'snapshotter': 'overlayfs', 'managed_userxattr': True, 'force_overlay_diff': True}
        value = {'schema_version': 1, 'scenario': 'buildkit-metadata-and-registry-cache', 'status': 'passed',
                 'metadata_cache_checks_passed': True, 'context': 'k3d-hakopod-dev', 'architecture': 'amd64',
                 'snapshotter': 'overlayfs', 'force_overlay_diff': True, 'buildkit_selection': selected,
                 'images': {'buildkit': selected['reference']}, 'cleanup': {'status': 'passed'},
                 'cold_builder': {**builder, 'name': 'hako-export-012345abcd-metadata-cold'},
                 'restored_builder': {**builder, 'name': 'hako-export-012345abcd-metadata-restored'}}
        acceptance.verify_qualification_report(value, selected, 'amd64', True)
        mutations = [lambda v: v.update(metadata_cache_checks_passed=False), lambda v: v.update(scenario='export'),
                     lambda v: v['cleanup'].update(status='failed'), lambda v: v['restored_builder'].update(image_id='bad'),
                     lambda v: v['restored_builder'].update(architecture='arm64'),
                     lambda v: v['restored_builder'].update(name=v['cold_builder']['name']),
                     lambda v: v['cold_builder'].update(name='hako-export-112345abcd-metadata-restored'),
                     lambda v: v['restored_builder'].update(managed_userxattr=False)]
        for mutate in mutations:
            changed = copy.deepcopy(value)
            mutate(changed)
            with self.subTest(mutation=mutate), self.assertRaises(AssertionError):
                acceptance.verify_qualification_report(changed, selected, 'amd64', True)
        with self.assertRaisesRegex(AssertionError, 'managed candidate'):
            acceptance.verify_qualification_report(value, acceptance.export_buildkit_selection(), 'amd64', True)


class ServicePullEnvelopeTests(unittest.TestCase):
    def evidence(self, architecture='amd64', driver='vfs'):
        spec = importlib.util.spec_from_file_location('service_pull_test_data', Path(__file__).with_name('service-pull-benchmark.py'))
        benchmark = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(benchmark)
        services = []
        for service in benchmark.SERVICES:
            expected = service['platforms'][architecture]
            services.append({'service': service['name'], 'target_image_absent_before': True,
                             'target_config_absent_before': True, 'version': 'fixture version',
                             'image': {'config_digest': expected['config_digest'], 'platform': 'linux/' + architecture,
                                       'repository_digest': service['reference'].rsplit('@', 1)[1]},
                             'cleanup': {'status': 'passed', 'removed_config_digest': expected['config_digest']},
                             'pull': {'total_seconds': 5, 'event_count': len(expected['layer_prefixes']) * 2, 'stream_bytes': 100,
                                      'layers': {layer: {'Download complete': 1, 'Pull complete': 4,
                                                         'extraction_registration_queue_seconds': 3}
                                                 for layer in expected['layer_prefixes']}}})
        return {'schema_version': 1, 'scenario': 'cold-service-image-pulls', 'status': 'passed',
                'context': 'k3d-hakopod-dev', 'architecture': architecture, 'storage_driver': driver,
                'limits': {'images': 2, 'seconds_per_image': 600, 'stream_bytes_per_image': 4 * 1024 * 1024,
                           'events_per_image': 4096}, 'services': services}

    def test_requires_exact_cold_images_bounded_layer_timings_and_cleanup(self):
        for architecture in ('amd64', 'arm64'):
            for driver in ('vfs', 'overlay2'):
                acceptance.verify_service_pull_report(self.evidence(architecture, driver), architecture, driver)
        original = self.evidence()
        changes = [lambda v: v.update(status='failed'), lambda v: v.update(context='operator'),
                   lambda v: v.update(architecture='arm64'), lambda v: v.update(storage_driver='overlay2'),
                   lambda v: v['limits'].update(seconds_per_image=601), lambda v: v['services'].pop(),
                   lambda v: v['services'].reverse(), lambda v: v['services'][0].update(target_image_absent_before=False),
                   lambda v: v['services'][0].update(target_config_absent_before=False),
                   lambda v: v['services'][0]['image'].update(config_digest='sha256:' + '0' * 64),
                   lambda v: v['services'][0]['cleanup'].update(status='failed'),
                   lambda v: v['services'][0].update(version=''),
                   lambda v: v['services'][0]['pull'].update(total_seconds=float('nan')),
                   lambda v: v['services'][0]['pull'].update(total_seconds=True),
                   lambda v: v['services'][0]['pull'].update(event_count=1),
                   lambda v: v['services'][0]['pull'].update(stream_bytes=0),
                   lambda v: v['services'][0]['pull']['layers'].popitem()]
        for change in changes:
            value = copy.deepcopy(original)
            change(value)
            with self.subTest(change=change), self.assertRaises(AssertionError):
                acceptance.verify_service_pull_report(value, 'amd64', 'vfs')
        for key, changed in [('Download complete', 6), ('Pull complete', 6),
                             ('extraction_registration_queue_seconds', 99), ('Pull complete', float('inf'))]:
            value = copy.deepcopy(original)
            next(iter(value['services'][0]['pull']['layers'].values()))[key] = changed
            with self.subTest(key=key, changed=changed), self.assertRaises(AssertionError):
                acceptance.verify_service_pull_report(value, 'amd64', 'vfs')

    def test_service_progress_and_failure_survive_a_wait_error(self):
        event = {'service': 'postgres:16', 'measured': True}
        failure = {'schema_version': 1, 'status': 'failed', 'error': 'second pull failed'}
        raw = 'HAKOPOD_SERVICE_PULL_EVENT ' + json.dumps(event) + '\nHAKOPOD_SERVICE_PULL ' + json.dumps(failure) + '\n'
        def failed_wait(*args, **kwargs):
            kwargs['capture'].observe(RUNNING)
            raise RuntimeError('API disconnected')
        with tempfile.TemporaryDirectory() as temp, patch.object(acceptance, 'OUTPUT', Path(temp)), \
                patch.object(acceptance, 'REPORT', {'limits': {}}), patch.object(acceptance, 'DEADLINE', time.monotonic() + 10), \
                patch.object(acceptance.subprocess, 'Popen', side_effect=writer('print(' + repr(raw[:-1]) + ')')), \
                patch.object(acceptance, 'diagnostic_fixture', return_value='fixture'), patch.object(acceptance, 'apply'), \
                patch.object(acceptance, 'wait_for', side_effect=failed_wait):
            with self.assertRaisesRegex(RuntimeError, 'API disconnected'):
                acceptance.run_service_pull_benchmark({})
            self.assertEqual(json.loads((Path(temp) / 'service-pull-benchmark-report.json').read_text()), failure)
            self.assertEqual(acceptance.REPORT['service_pull_events'], [event])


class RuntimeEnvelopeTests(unittest.TestCase):
    def evidence(self, image='', force=False, architecture='amd64'):
        selected = acceptance.runtime_helpers().runtime_buildkit_selection(image, force)
        builder = {'image': selected['reference'], 'image_id': 'sha256:' + 'c' * 64,
                   'version': 'buildkitd github.com/moby/buildkit ' + selected['version'] + ' ' + selected['upstream_revision'],
                   'architecture': architecture, 'managed_userxattr': selected['kind'] == 'candidate',
                   'snapshotter': selected['snapshotter'], 'force_overlay_diff': force,
                   'upstream_revision': selected['upstream_revision']}
        cold = [{'architecture': arch, 'cached': False, 'emulated': arch != architecture, 'duration_seconds': 1.0}
                for arch in ('amd64', 'arm64')]
        result = {'buildkit_selection': selected, 'builder': builder, 'platforms': ['amd64', 'arm64'],
                  'pullback_execution_platforms': ['amd64', 'arm64'], 'native_cross_compile_platforms': ['amd64', 'arm64'],
                  'manifest_digest': 'sha256:' + 'd' * 64, 'native_and_emulated_compiler': True,
                  'failed_build_recovered': True, 'post_failure_pullback_verified': True, 'buildkit_max_parallelism': 2,
                  'parallel_build_requests': [{'request': 1}, {'request': 2}],
                  'timings': {'cold_compiler_vertices': cold, 'warm_compiler_vertices': [{**item, 'cached': True} for item in cold]}}
        def event(phase, details=None):
            return {'phase': phase, 'status': 'passed', 'details': copy.deepcopy(details or {})}
        events = [event('sandbox-boundary'), event('containers-and-services'),
                  event('runtime-builder-verified', {'selection': selected, 'builder': builder}),
                  event('build-results-verified', result)]
        result['timings']['ephemeral_builder_stop_seconds'] = 1.0
        events += [event('compiled-multiarch-registry-concurrency', result), event('complete')]
        return selected, events

    def test_normal_candidate_and_forced_paths_require_full_runtime_evidence(self):
        candidate = 'ghcr.io/hakopod/buildkit:v0.32.2-hakopod-' + 'a' * 40 + '@sha256:' + 'b' * 64
        for image, force in (('', False), (candidate, False), (candidate, True)):
            for architecture in ('amd64', 'arm64'):
                selected, events = self.evidence(image, force, architecture)
                identity = acceptance.verify_runtime_report(events, selected, architecture)
                self.assertEqual(identity['image'], selected['reference'])
                self.assertEqual(identity['force_overlay_diff'], force)

    def test_missing_changed_or_cached_compiler_evidence_cannot_pass(self):
        selected, events = self.evidence()
        mutations = [lambda values: values.pop(), lambda values: values.append(copy.deepcopy(values[-1])),
                     lambda values: values[2]['details']['builder'].update(image_id='bad'),
                     lambda values: values[2]['details']['builder'].update(architecture='arm64'),
                     lambda values: values[2]['details']['builder'].update(snapshotter='overlayfs'),
                     lambda values: values[2]['details']['builder'].update(force_overlay_diff=True),
                     lambda values: values[2]['details']['selection'].update(reference='mutable'),
                     lambda values: values[4]['details'].update(pullback_execution_platforms=['amd64']),
                     lambda values: values[4]['details'].update(post_failure_pullback_verified=False)]
        for mutate in mutations:
            changed = copy.deepcopy(events)
            mutate(changed)
            with self.subTest(mutation=mutate), self.assertRaises((AssertionError, RuntimeError)):
                acceptance.verify_runtime_report(changed, selected, 'amd64')
        for cached, cold in ((True, True), (False, False)):
            changed = copy.deepcopy(events)
            key = 'cold_compiler_vertices' if cold else 'warm_compiler_vertices'
            for position in (3, 4):
                changed[position]['details']['timings'][key][1]['cached'] = cached
            with self.subTest(cold=cold), self.assertRaises(RuntimeError):
                acceptance.verify_runtime_report(changed, selected, 'amd64')


if __name__ == '__main__':
    unittest.main()
