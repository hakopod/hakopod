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


if __name__ == '__main__':
    unittest.main()
