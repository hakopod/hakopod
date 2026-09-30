#!/usr/bin/env python3
"""Pure tests for the development-only service pull benchmark."""
import importlib.util
import json
from pathlib import Path
import subprocess
import unittest
from unittest.mock import patch


SPEC = importlib.util.spec_from_file_location('service_pull_benchmark', Path(__file__).with_name('service-pull-benchmark.py'))
benchmark = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(benchmark)


class Response:
    def __init__(self, status, body=b'', lines=()):
        self.status = status
        self.body = body
        self.lines = iter(lines)

    def read(self, size):
        return self.body

    def readline(self, size):
        return next(self.lines, b'')


class Connection:
    def __init__(self, responses, calls, *args, **kwargs):
        self.responses, self.calls = responses, calls
        self.sock = self

    def settimeout(self, value):
        self.timeout = value

    def request(self, method, path):
        self.calls.append((method, path))

    def getresponse(self):
        return self.responses.pop(0)

    def close(self):
        pass


def api_with(responses, clock=lambda: 1.0):
    calls = []
    return benchmark.DockerAPI(lambda *args, **kwargs: Connection(responses, calls), clock), calls


class PullTests(unittest.TestCase):
    def test_stream_records_both_completion_times_for_exact_layers(self):
        events = [
            {'id': 'aaa', 'status': 'Downloading'},
            {'id': 'aaa', 'status': 'Download complete'},
            {'id': 'bbb', 'status': 'Download complete'},
            {'id': 'aaa', 'status': 'Pull complete'},
            {'id': 'bbb', 'status': 'Pull complete'},
            {'status': 'Digest: sha256:' + 'c' * 64},
        ]
        lines = [json.dumps(event).encode() + b'\n' for event in events]
        times = iter(range(20))
        api, calls = api_with([Response(200, lines=lines)], lambda: next(times))
        result = api.pull('docker.io/library/example@sha256:' + 'c' * 64, ('aaa', 'bbb'))
        self.assertEqual(result['layers'], {
            'aaa': {'Download complete': 3, 'Pull complete': 7, 'extraction_registration_queue_seconds': 4},
            'bbb': {'Download complete': 5, 'Pull complete': 9, 'extraction_registration_queue_seconds': 4},
        })
        self.assertEqual(calls[0][0], 'POST')
        self.assertIn('fromImage=docker.io%2Flibrary%2Fexample', calls[0][1])

    def test_stream_rejects_missing_layer_and_engine_error(self):
        api, _ = api_with([Response(200, lines=(b'{"id":"aaa","status":"Pull complete"}\n',))])
        with self.assertRaisesRegex(RuntimeError, 'complete timings'):
            api.pull('docker.io/library/example@sha256:' + 'c' * 64, ('aaa',))
        api, _ = api_with([Response(200, lines=(b'{"error":"pull failed"}\n',))])
        with self.assertRaisesRegex(RuntimeError, 'engine error'):
            api.pull('docker.io/library/example@sha256:' + 'c' * 64, ('aaa',))

    def test_stream_enforces_absolute_deadline(self):
        times = iter((0, 601))
        api, _ = api_with([Response(200)], lambda: next(times))
        with self.assertRaisesRegex(RuntimeError, 'exceeded 600 seconds'):
            api.pull('docker.io/library/example@sha256:' + 'c' * 64, ('aaa',))

    def test_signal_deadline_restores_handler_and_timer_after_interrupt(self):
        previous = object()
        with patch.object(benchmark.signal, 'getsignal', return_value=previous), \
             patch.object(benchmark.signal, 'getitimer', return_value=(7.0, 2.0)), \
             patch.object(benchmark.signal, 'signal') as install, \
             patch.object(benchmark.signal, 'setitimer') as timer:
            with self.assertRaisesRegex(RuntimeError, 'absolute deadline'):
                with benchmark.absolute_deadline(10):
                    installed = install.call_args_list[0].args[1]
                    installed(None, None)
        self.assertEqual(install.call_args_list[-1].args, (benchmark.signal.SIGALRM, previous))
        self.assertEqual(timer.call_args_list[0].args, (benchmark.signal.ITIMER_REAL, 10))
        self.assertEqual(timer.call_args_list[1].args, (benchmark.signal.ITIMER_REAL, 0))
        self.assertEqual(timer.call_args_list[-1].args[2], 2.0)


class VerificationTests(unittest.TestCase):
    def test_environment_requires_loopback_context_native_driver_and_root(self):
        class API:
            def request(self, method, path):
                if path == '/version':
                    return 200, {'Arch': 'amd64', 'Os': 'linux', 'Version': benchmark.DAEMON_VERSION,
                                 'GitCommit': benchmark.DAEMON_COMMIT, 'KernelVersion': '6.1.0-gvisor'}
                return 200, {'Architecture': 'x86_64', 'OSType': 'linux', 'Driver': 'vfs',
                             'DockerRootDir': '/home/runner/.docker-data'}
        environment = {'DOCKER_HOST': benchmark.DOCKER_HOST,
                       'HAKOPOD_SERVICE_PULL_BENCHMARK_DEV_CONTEXT': benchmark.DEV_CONTEXT,
                       'HAKOPOD_ACTIONS_DOCKER_STORAGE_DRIVER': 'vfs'}
        self.assertEqual(benchmark.verify_environment(API(), environment, 'x86_64'),
                         {'architecture': 'amd64', 'storage_driver': 'vfs'})
        for key, value in (('DOCKER_HOST', 'tcp://remote:2375'),
                           ('HAKOPOD_SERVICE_PULL_BENCHMARK_DEV_CONTEXT', 'other'),
                           ('HAKOPOD_ACTIONS_DOCKER_STORAGE_DRIVER', 'overlay2')):
            changed = dict(environment, **{key: value})
            with self.subTest(key=key), self.assertRaises(RuntimeError):
                benchmark.verify_environment(API(), changed, 'x86_64')

    def test_environment_normalizes_native_info_arch_and_pins_gvisor_daemon(self):
        class API:
            def __init__(self):
                self.version = {'Arch': 'arm64', 'Os': 'linux', 'Version': benchmark.DAEMON_VERSION,
                                'GitCommit': benchmark.DAEMON_COMMIT, 'KernelVersion': 'gVisor'}

            def request(self, method, path):
                if path == '/version':
                    return 200, dict(self.version)
                return 200, {'Architecture': 'aarch64', 'OSType': 'linux', 'Driver': 'overlay2',
                             'DockerRootDir': '/home/runner/.docker-data'}

        environment = {'DOCKER_HOST': benchmark.DOCKER_HOST,
                       'HAKOPOD_SERVICE_PULL_BENCHMARK_DEV_CONTEXT': benchmark.DEV_CONTEXT,
                       'HAKOPOD_ACTIONS_DOCKER_STORAGE_DRIVER': 'overlay2'}
        self.assertEqual(benchmark.verify_environment(API(), environment, 'aarch64')['architecture'], 'arm64')
        for key, value in (('Version', '29.8.0'), ('GitCommit', 'other'), ('KernelVersion', 'Linux')):
            api = API()
            api.version[key] = value
            with self.subTest(key=key), self.assertRaises(RuntimeError):
                benchmark.verify_environment(api, environment, 'aarch64')

    def test_image_identity_is_exact(self):
        service = benchmark.SERVICES[0]
        expected = service['platforms']['amd64']['config_digest']
        image = {'Id': expected, 'Os': 'linux', 'Architecture': 'amd64', 'RepoDigests': [service['reference']]}
        self.assertEqual(benchmark.verify_image(image, service, 'amd64')['config_digest'], expected)
        for alias in benchmark.repository_digest_aliases(service['reference']):
            self.assertEqual(benchmark.verify_image({**image, 'RepoDigests': [alias]}, service, 'amd64')['config_digest'], expected)
        for mutation in (lambda value: value.update(Id='sha256:' + '0' * 64),
                         lambda value: value.update(Architecture='arm64'),
                         lambda value: value.update(RepoDigests=[]),
                         lambda value: value.update(RepoDigests=['mirror/library/postgres@' + service['reference'].rsplit('@', 1)[1]])):
            changed = dict(image)
            mutation(changed)
            with self.assertRaises(RuntimeError):
                benchmark.verify_image(changed, service, 'amd64')

    def test_cleanup_uses_only_verified_exact_config_id(self):
        service = benchmark.SERVICES[1]
        expected = service['platforms']['amd64']['config_digest']

        class API:
            def __init__(self):
                self.inspections = 0
                self.removed = []

            def inspect(self, reference, missing_ok=False):
                self.inspections += 1
                if self.inspections <= 2:
                    return None
                return {'Id': expected, 'Os': 'linux', 'Architecture': 'amd64', 'RepoDigests': [service['reference']]}

            def pull(self, reference, prefixes):
                return {'total_seconds': 1, 'layers': {}}

            def remove_exact(self, image_id):
                self.removed.append(image_id)

        api = API()
        with patch.object(benchmark, 'version_check', return_value='version'):
            result = benchmark.benchmark_service(api, service, 'amd64')
        self.assertEqual(result['version'], 'version')
        self.assertEqual(api.removed, [expected])

    def test_unverified_config_is_never_removed(self):
        service = benchmark.SERVICES[1]

        class API:
            def __init__(self):
                self.inspections = 0
                self.removed = []

            def inspect(self, reference, missing_ok=False):
                self.inspections += 1
                if self.inspections <= 2:
                    return None
                return {'Id': 'sha256:' + '0' * 64, 'Os': 'linux', 'Architecture': 'amd64',
                        'RepoDigests': [service['reference']]}

            def pull(self, reference, prefixes):
                return {'total_seconds': 1, 'layers': {}}

            def remove_exact(self, image_id):
                self.removed.append(image_id)

        api = API()
        with self.assertRaisesRegex(RuntimeError, 'config digest') as raised:
            benchmark.benchmark_service(api, service, 'amd64')
        self.assertRegex(str(raised.exception.__cause__), 'cleanup.*identities')
        self.assertEqual(api.removed, [])

    def test_pull_failure_removes_only_twice_verified_new_config(self):
        service = benchmark.SERVICES[0]
        expected = service['platforms']['amd64']['config_digest']
        image = {'Id': expected, 'Os': 'linux', 'Architecture': 'amd64',
                 'RepoDigests': ['postgres@' + service['reference'].rsplit('@', 1)[1]]}

        class API:
            def __init__(self):
                self.inspections = 0
                self.removed = []

            def inspect(self, reference, missing_ok=False):
                self.inspections += 1
                return None if self.inspections <= 2 else dict(image)

            def pull(self, reference, prefixes):
                raise RuntimeError('bounded pull failure')

            def remove_exact(self, image_id):
                self.removed.append(image_id)

        api = API()
        with self.assertRaisesRegex(RuntimeError, 'bounded pull failure'):
            benchmark.benchmark_service(api, service, 'amd64')
        self.assertEqual(api.removed, [expected])

    def test_version_timeout_cleans_only_inspected_owned_container(self):
        service = benchmark.SERVICES[0]
        expected = service['platforms']['amd64']['config_digest']

        class API:
            def __init__(self):
                self.removed = []

            def inspect_container(self, name, missing_ok=False):
                return {'Id': 'a' * 64, 'Name': '/' + name, 'Image': expected,
                        'Config': {'Labels': {benchmark.OWNER_LABEL: name}}}

            def remove_container_exact(self, container_id):
                self.removed.append(container_id)

        api = API()
        with patch.object(benchmark.subprocess, 'run', side_effect=subprocess.TimeoutExpired(['docker'], 30)) as run:
            with self.assertRaises(subprocess.TimeoutExpired):
                benchmark.version_check(api, service, expected)
        command = run.call_args.args[0]
        self.assertIn('--network', command)
        self.assertIn('--read-only', command)
        self.assertIn('--pids-limit', command)
        self.assertEqual(api.removed, ['a' * 64])


if __name__ == '__main__':
    unittest.main()
