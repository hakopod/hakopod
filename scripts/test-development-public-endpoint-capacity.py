"""Mocked safety checks for the shared public-endpoint host capacity module."""

import importlib.util
import json
import os
from pathlib import Path
import tempfile
import subprocess
import unittest


spec = importlib.util.spec_from_file_location(
    'development_public_endpoint_capacity',
    Path(__file__).with_name('development_public_endpoint_capacity.py'))
capacity = importlib.util.module_from_spec(spec)
spec.loader.exec_module(capacity)


GIB = 1024 ** 3
NODES = {'node-a': 'server', 'node-b': 'agent'}


class SharedCapacityTests(unittest.TestCase):
    def cgroup(self, root, path, cpu='max 100000', memory='max', cpuset='0-7'):
        folder = root / path.lstrip('/')
        folder.mkdir(parents=True, exist_ok=True)
        (folder / 'cpu.max').write_text(cpu)
        (folder / 'memory.max').write_text(str(memory))
        (folder / 'cpuset.cpus.effective').write_text(cpuset)

    def process(self, root, pid, cgroup, executable=None):
        folder = root / str(pid)
        folder.mkdir()
        (folder / 'cgroup').write_text('0::' + cgroup + '\n')
        if executable is not None:
            target = root / 'executables' / executable
            target.parent.mkdir(exist_ok=True)
            target.touch(exist_ok=True)
            (folder / 'exe').symlink_to(target)

    def fixture(self):
        temporary = tempfile.TemporaryDirectory()
        base = Path(temporary.name)
        proc = base / 'proc'
        cgroups = base / 'cgroup'
        proc.mkdir()
        cgroups.mkdir()
        online = base / 'online'
        online.write_text('0-7')
        self.cgroup(cgroups, '/', '800000 100000', 16 * GIB)
        self.cgroup(cgroups, '/system.slice', '700000 100000', 14 * GIB)
        self.cgroup(cgroups, '/runner.scope', '100000 100000', 3 * GIB, '0')
        for name, cpuset in (('node-a', '0-1'), ('node-b', '2-3')):
            self.cgroup(cgroups, f'/system.slice/docker-{name}.scope', '200000 100000', 4 * GIB, cpuset)
            self.cgroup(cgroups, f'/system.slice/docker-{name}.scope/init', 'max 100000', 'max', cpuset)
        self.cgroup(cgroups, '/system.slice/docker-control.scope', '50000 100000', 512 * 1024 ** 2, '4')
        self.process(proc, 1, '/init.scope', 'systemd')
        self.process(proc, 2, '/', None)
        self.process(proc, os.getpid(), '/runner.scope', 'python')
        self.process(proc, 101, '/system.slice/docker-node-a.scope/init', 'init')
        self.process(proc, 102, '/system.slice/docker-node-b.scope/init', 'init')
        self.process(proc, 103, '/system.slice/docker-control.scope', 'postgres')
        namespace_files = proc / 'namespace-files'
        namespace_files.mkdir()
        for namespace in ('cgroup', 'mnt'):
            identity = namespace_files / namespace
            identity.touch()
            for pid in ('1', str(os.getpid())):
                folder = proc / pid / 'ns'
                folder.mkdir(exist_ok=True)
                os.link(identity, folder / namespace)
        (proc / str(os.getpid()) / 'mountinfo').write_text(
            f'1 1 0:1 / {cgroups.resolve()} rw - cgroup2 cgroup rw\n')
        budget = {
            'schema_version': 1,
            'cluster': 'k3d-hakopod-dev',
            'reviewed_by': ['root'],
            'root_reserve': {
                'cpu_millis': 1000,
                'memory_bytes': 2 * GIB,
                'cgroups': [
                    {'cgroup_path': '/', 'executables': ['[kernel]']},
                    {'cgroup_path': '/init.scope', 'executables': ['systemd']},
                ],
            },
            'docker_workloads': [
                {'name': 'node-a', 'container_id': 'a' * 64, 'class': 'development-node',
                 'cpu_millis': 2000, 'memory_bytes': 4 * GIB},
                {'name': 'node-b', 'container_id': 'b' * 64, 'class': 'development-node',
                 'cpu_millis': 2000, 'memory_bytes': 4 * GIB},
                {'name': 'control', 'container_id': 'c' * 64, 'class': 'control-postgres',
                 'cpu_millis': 500, 'memory_bytes': 512 * 1024 ** 2},
            ],
            'host_workloads': [
                {'name': 'planner', 'class': 'acceptance-runner', 'cgroup_path': '/runner.scope',
                 'executables': ['python'], 'cpu_millis': 1000, 'memory_bytes': 3 * GIB},
            ],
        }
        inspected = [
            self.container('node-a', 'a', 101, 'server', '0-1', 200000, 4 * GIB),
            self.container('node-b', 'b', 102, 'agent', '2-3', 200000, 4 * GIB),
            self.container('control', 'c', 103, None, '4', 50000, 512 * 1024 ** 2),
        ]
        return temporary, proc, cgroups, online, budget, inspected

    def container(self, name, prefix, pid, role, cpuset, quota, memory):
        labels = {}
        if role is not None:
            labels = {'app': 'k3d', 'k3d.cluster': 'hakopod-dev', 'k3d.role': role}
        return {
            'Id': prefix * 64, 'Name': '/' + name, 'Image': 'sha256:' + 'd' * 64,
            'State': {'Pid': pid, 'Running': True}, 'Config': {'Labels': labels},
            'HostConfig': {'NanoCpus': 0, 'CpuQuota': quota, 'CpuPeriod': 100000,
                           'CpusetCpus': cpuset, 'Memory': memory},
        }

    def command(self, inspected):
        def run(args, **_):
            if args[1] == 'info':
                return json.dumps([8, 16 * GIB]).encode()
            if args[1] == 'ps':
                return ('\n'.join(item['Id'] for item in inspected) + '\n').encode()
            if args[1] == 'inspect':
                return json.dumps(inspected).encode()
            raise AssertionError(args)
        return run

    def test_exact_inventory_and_shared_ancestor_budget_pass(self):
        temporary, proc, cgroups, online, budget, inspected = self.fixture()
        with temporary:
            nodes, evidence = capacity.inspect(
                '/docker', self.command(inspected), budget, NODES, context='k3d-hakopod-dev',
                proc_root=proc, cgroup_root=cgroups, online_path=online)
        self.assertEqual(set(nodes), set(NODES))
        self.assertTrue(capacity.validate_plan_evidence(evidence, nodes))
        self.assertEqual(next(item for item in evidence['ancestor_limits']
                              if item['cgroup_path'] == '/system.slice')['claimed_cpu_millis'], 4500)

    def test_unknown_docker_and_host_workloads_fail_closed(self):
        temporary, proc, cgroups, online, budget, inspected = self.fixture()
        with temporary:
            inspected.append(self.container('unknown', 'e', 104, None, '5', 50000, 256 * 1024 ** 2))
            self.cgroup(cgroups, '/system.slice/docker-unknown.scope', '50000 100000', 256 * 1024 ** 2, '5')
            self.process(proc, 104, '/system.slice/docker-unknown.scope', 'unknown')
            with self.assertRaisesRegex(ValueError, 'unknown or replaced'):
                capacity.inspect('/docker', self.command(inspected), budget, NODES,
                                 context='k3d-hakopod-dev', proc_root=proc,
                                 cgroup_root=cgroups, online_path=online)
        temporary, proc, cgroups, online, budget, inspected = self.fixture()
        with temporary:
            self.cgroup(cgroups, '/customer.slice', '50000 100000', 256 * 1024 ** 2, '6')
            self.process(proc, 104, '/customer.slice', 'customer')
            with self.assertRaisesRegex(ValueError, 'unknown or missing non-Docker'):
                capacity.inspect('/docker', self.command(inspected), budget, NODES,
                                 context='k3d-hakopod-dev', proc_root=proc,
                                 cgroup_root=cgroups, online_path=online)

    def test_actual_limits_and_shared_parent_are_enforced(self):
        temporary, proc, cgroups, online, budget, inspected = self.fixture()
        with temporary:
            budget['docker_workloads'][0]['cpu_millis'] = 100
            with self.assertRaisesRegex(ValueError, 'below its actual effective limits'):
                capacity.inspect('/docker', self.command(inspected), budget, NODES,
                                 context='k3d-hakopod-dev', proc_root=proc,
                                 cgroup_root=cgroups, online_path=online)
        temporary, proc, cgroups, online, budget, inspected = self.fixture()
        with temporary:
            (cgroups / 'system.slice' / 'cpu.max').write_text('400000 100000')
            with self.assertRaisesRegex(ValueError, 'shared ancestor'):
                capacity.inspect('/docker', self.command(inspected), budget, NODES,
                                 context='k3d-hakopod-dev', proc_root=proc,
                                 cgroup_root=cgroups, online_path=online)

    def test_configured_cpuset_must_fit_online_and_ancestors(self):
        temporary, proc, cgroups, online, budget, inspected = self.fixture()
        with temporary:
            inspected[0]['HostConfig']['CpusetCpus'] = '0,8'
            with self.assertRaisesRegex(ValueError, 'outside online'):
                capacity.inspect('/docker', self.command(inspected), budget, NODES,
                                 context='k3d-hakopod-dev', proc_root=proc,
                                 cgroup_root=cgroups, online_path=online)
        temporary, proc, cgroups, online, budget, inspected = self.fixture()
        with temporary:
            (cgroups / 'system.slice' / 'cpuset.cpus.effective').write_text('0-2')
            with self.assertRaisesRegex(ValueError, 'effective ancestor'):
                capacity.inspect('/docker', self.command(inspected), budget, NODES,
                                 context='k3d-hakopod-dev', proc_root=proc,
                                 cgroup_root=cgroups, online_path=online)

    def test_inventory_change_during_inspection_fails(self):
        temporary, proc, cgroups, online, budget, inspected = self.fixture()
        calls = 0

        def changing(args, **kwargs):
            nonlocal calls
            if args[1] == 'ps':
                calls += 1
                ids = [item['Id'] for item in inspected]
                if calls == 2:
                    ids.append('e' * 64)
                return ('\n'.join(ids) + '\n').encode()
            return self.command(inspected)(args, **kwargs)

        with temporary, self.assertRaisesRegex(ValueError, 'changed during inspection'):
            capacity.inspect('/docker', changing, budget, NODES, context='k3d-hakopod-dev',
                             proc_root=proc, cgroup_root=cgroups, online_path=online)

    def test_root_controls_may_be_absent_only_at_host_mount_root(self):
        temporary, proc, cgroups, online, budget, inspected = self.fixture()
        with temporary:
            (cgroups / 'cpu.max').unlink()
            (cgroups / 'memory.max').unlink()
            nodes, evidence = capacity.inspect(
                '/docker', self.command(inspected), budget, NODES,
                context='k3d-hakopod-dev', proc_root=proc,
                cgroup_root=cgroups, online_path=online)
            self.assertEqual(evidence['cpu_millis'], 8000)
            self.assertEqual(evidence['memory_bytes'], 16 * GIB)
            self.assertEqual(set(nodes), set(NODES))
        for control in ('cpu.max', 'memory.max', 'cpuset.cpus.effective'):
            temporary, proc, cgroups, online, budget, inspected = self.fixture()
            with temporary:
                (cgroups / 'system.slice' / control).unlink()
                with self.assertRaisesRegex(ValueError, 'cgroup capacity changed'):
                    capacity.inspect('/docker', self.command(inspected), budget, NODES,
                                     context='k3d-hakopod-dev', proc_root=proc,
                                     cgroup_root=cgroups, online_path=online)

    def test_cgroup_control_change_during_inspection_fails(self):
        temporary, proc, cgroups, online, budget, inspected = self.fixture()
        calls = 0

        def changing(args, **kwargs):
            nonlocal calls
            if args[1] == 'inspect':
                calls += 1
                if calls == 2:
                    (cgroups / 'system.slice' / 'cpu.max').write_text('650000 100000')
            return self.command(inspected)(args, **kwargs)

        with temporary, self.assertRaisesRegex(ValueError, 'cgroup capacity changed'):
            capacity.inspect('/docker', changing, budget, NODES,
                             context='k3d-hakopod-dev', proc_root=proc,
                             cgroup_root=cgroups, online_path=online)

    def test_bounded_command_limits_stdout_stderr_stdin_and_time(self):
        python = os.environ.get('PYTHON', os.sys.executable)
        with self.assertRaisesRegex(ValueError, 'output exceeded'):
            capacity.bounded_command([python, '-c', 'print("x" * 4096)'], maximum=32)
        with self.assertRaisesRegex(ValueError, 'output exceeded'):
            capacity.bounded_command(
                [python, '-c', 'import sys; sys.stderr.write("x" * 4096)'],
                maximum=32, stderr_maximum=32)
        output = capacity.bounded_command(
            [python, '-c', 'import sys; print(len(sys.stdin.buffer.read()))'],
            stdin=b'x' * (256 * 1024), maximum=64)
        self.assertEqual(output.strip(), b'262144')
        with self.assertRaises(subprocess.TimeoutExpired):
            capacity.bounded_command([python, '-c', 'import time; time.sleep(5)'],
                                     maximum=32, timeout=0.05)

    def test_root_reserve_cannot_cover_other_workload_paths(self):
        temporary, _, _, _, budget, _ = self.fixture()
        with temporary:
            budget['root_reserve']['cgroups'][1]['cgroup_path'] = '/system.slice'
            with self.assertRaisesRegex(ValueError, 'only kernel threads and systemd'):
                capacity.validate_budget(budget, 'k3d-hakopod-dev')


if __name__ == '__main__':
    unittest.main()
