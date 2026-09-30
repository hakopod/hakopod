"""Startup guard regression checks with no Docker or network access."""
import copy
import http.client
import io
import json
import os
import platform
import signal
import tempfile
import unittest
from contextlib import redirect_stderr, redirect_stdout
from pathlib import Path
from unittest.mock import patch


SOURCE = Path(__file__).with_name('actions-workspace-start.py').read_text()
INFO = {'Driver': 'overlay2', 'DockerRootDir': '/home/runner/.docker-data', 'Images': 0, 'Containers': 0,
        'DriverStatus': [['Backing Filesystem', 'tmpfs'], ['Supports d_type', 'true'], ['Native Overlay Diff', 'true']]}
VERSION = {'Version': '29.8.1', 'GitCommit': '464cd50', 'Os': 'linux', 'Arch': 'arm64'}


class WorkspaceStartupTests(unittest.TestCase):
    def run_guard(self, info=None, version=None, marker=b'shared-workspace-v1\n', status=200, transport_error=None, symlink=False, machine='aarch64'):
        responses = {'/info': copy.deepcopy(INFO) if info is None else info,
                     '/version': copy.deepcopy(VERSION) if version is None else version}
        namespace, connections, requests = {}, [], []
        output, errors = io.StringIO(), io.StringIO()
        case = self
        class Connection:
            def __init__(self, host, port, timeout):
                case.assertEqual((host, port, timeout), ('127.0.0.1', 2375, 5))
                connections.append(self)
                self.closed = False
            def request(self, method, path):
                case.assertEqual(method, 'GET')
                case.assertIn(path, responses)
                self.path = path
                requests.append(path)
            def getresponse(self):
                if transport_error:
                    raise transport_error
                connection = self
                class Response:
                    def read(self, maximum):
                        case.assertEqual(maximum, 131073)
                        value = responses[connection.path]
                        return (value if isinstance(value, bytes) else json.dumps(value).encode())[:maximum]
                response = Response()
                response.status = status
                return response
            def close(self):
                self.closed = True
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / 'marker'
            path.write_bytes(marker)
            if symlink:
                target = path.with_suffix('.target')
                path.rename(target)
                path.symlink_to(target)
            original_open = os.open
            def open_marker(name, flags, *args, **kwargs):
                self.assertEqual(name, '/home/runner/.hakopod-shared-prepare')
                self.assertEqual(flags, os.O_RDONLY | os.O_NOFOLLOW)
                return original_open(path, flags, *args, **kwargs)
            with patch.object(os, 'open', side_effect=open_marker), patch.object(http.client, 'HTTPConnection', Connection), \
                    patch.object(platform, 'machine', return_value=machine), patch.object(signal, 'signal', return_value=signal.SIG_DFL) as handlers, \
                    patch.object(signal, 'setitimer') as timer, redirect_stderr(errors), redirect_stdout(output):
                try:
                    exec(compile(SOURCE + '\nregistered = True\n', 'actions-workspace-start.py', 'exec'), namespace)
                except SystemExit as error:
                    self.assertEqual(error.code, 1)
                self.assertEqual(timer.call_args_list[0].args, (signal.ITIMER_REAL, 25))
                self.assertEqual(timer.call_args_list[-1].args, (signal.ITIMER_REAL, 0))
                self.assertEqual(handlers.call_args_list[-1].args, (signal.SIGALRM, signal.SIG_DFL))
        self.assertTrue(all(connection.closed for connection in connections))
        self.assertEqual(output.getvalue(), '')
        return namespace.get('registered', False), errors.getvalue(), requests

    def test_valid_fresh_workspace_reaches_registration_silently(self):
        for machine, architecture in (('aarch64', 'arm64'), ('x86_64', 'amd64')):
            with self.subTest(machine=machine):
                registered, errors, requests = self.run_guard(machine=machine, version={**VERSION, 'Arch': architecture})
                self.assertTrue(registered)
                self.assertEqual(errors, '')
                self.assertEqual(requests, ['/info', '/version'])

    def test_wrong_or_symlinked_marker_never_contacts_docker_or_registers(self):
        for marker, symlink in ((b'wrong', False), (b'shared-workspace-v1\n' + b'x' * 100, False), (b'shared-workspace-v1\n', True)):
            with self.subTest(marker=marker[:24], symlink=symlink):
                registered, errors, requests = self.run_guard(marker=marker, symlink=symlink)
                self.assertFalse(registered)
                self.assertTrue(errors.startswith('Actions workspace startup check failed:'))
                self.assertEqual(requests, [])

    def test_wrong_driver_path_or_nonempty_store_blocks_registration(self):
        for key, value in (('Driver', 'vfs'), ('DockerRootDir', '/var/lib/docker'), ('Images', 1), ('Containers', 1), ('Images', False)):
            with self.subTest(key=key, value=value):
                info = copy.deepcopy(INFO)
                info[key] = value
                registered, _, _ = self.run_guard(info=info)
                self.assertFalse(registered)

    def test_daemon_pin_and_native_architecture_are_required(self):
        for key, value in (('Version', '29.8.2'), ('GitCommit', 'other'), ('Os', 'windows'), ('Arch', 'amd64'), ('Arch', 'unknown')):
            with self.subTest(key=key, value=value):
                version = {**VERSION, key: value}
                registered, _, _ = self.run_guard(version=version)
                self.assertFalse(registered)

    def test_storage_facts_are_exact_and_unambiguous(self):
        values = [None, {}, [], [['Backing Filesystem', 'extfs']], INFO['DriverStatus'] + [['Native Overlay Diff', 'true']],
                  [['Backing Filesystem', 'tmpfs'], ['Supports d_type', 'false'], ['Native Overlay Diff', 'true']],
                  [['Backing Filesystem', 'tmpfs'], ['Supports d_type', 'true'], ['Native Overlay Diff', 'false']]]
        for value in values:
            with self.subTest(value=value):
                registered, _, _ = self.run_guard(info={**INFO, 'DriverStatus': value})
                self.assertFalse(registered)

    def test_bounded_malformed_or_failed_docker_responses_never_register(self):
        for value in (b'x' * 131073, b'not-json', b'[]'):
            with self.subTest(value=value[:20]):
                registered, _, _ = self.run_guard(info=value)
                self.assertFalse(registered)
        for failure in ({'status': 500}, {'transport_error': TimeoutError('development fixture timeout')}):
            with self.subTest(failure=failure):
                registered, errors, _ = self.run_guard(**failure)
                self.assertFalse(registered)
                self.assertNotIn('development fixture timeout', errors)


if __name__ == '__main__':
    unittest.main()
