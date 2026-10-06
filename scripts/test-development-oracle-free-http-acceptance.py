#!/usr/bin/env python3
"""Exercise HTTP runner resource and interruption boundaries without a cluster."""
import importlib.util
import json
import os
from pathlib import Path
import signal
import sys
import tempfile
from types import SimpleNamespace
import unittest
from unittest import mock

SPEC = importlib.util.spec_from_file_location("oracle_http", Path(__file__).with_name("run-development-oracle-free-http-acceptance.py"))
HTTP = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(HTTP)


class HTTPRunnerTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory(dir=os.environ["TMPDIR"])
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name)

    def cgroup(self, **overrides):
        data = {"cgroup": "0::/test.scope\n", "memory.max": str(6 * HTTP.GIB),
                "memory.swap.max": "0", "pids.max": "512", "cpu.max": "200000 100000"}
        data.update(overrides)
        return mock.patch.object(Path, "read_text", lambda path: data[path.name])

    def test_runner_requires_finite_cpu_memory_task_and_no_swap_limits(self):
        with self.cgroup():
            self.assertEqual(HTTP.runner_limits(), {"memory_bytes": 6 * HTTP.GIB, "cpu_milli": 2000,
                                                   "tasks": 512, "swap_bytes": 0})
        for limits in ({"memory.max": "max"}, {"memory.swap.max": "4096"}, {"pids.max": "1024"},
                       {"cpu.max": "max 100000"}, {"cpu.max": "400000 100000"}):
            with self.subTest(limits=limits), self.cgroup(**limits), self.assertRaisesRegex(ValueError, "requires at most"):
                HTTP.runner_limits()

    def host(self, *, memory_gib=64, cpus=16, container=None):
        pods = {"items": []}
        nodes = {"items": [{"metadata": {"name": "k3d-hakopod-dev-server-0"}}]}
        identifier = "a" * 64
        def output(command):
            if command[1] == "ps":
                return (identifier + "\n").encode() if container else b""
            if command[1] == "inspect":
                return json.dumps(dict(id=identifier, **container)).encode()
            raise AssertionError("unexpected runtime command")
        stack = mock.patch.multiple(HTTP, metadata=mock.Mock(side_effect=lambda kube, args: pods if args[1] == "pods" else nodes))
        return stack, mock.patch.object(Path, "read_text", lambda path: f"MemAvailable: {memory_gib * 1024**2} kB\nMemTotal: {memory_gib * 1024**2} kB\n"), mock.patch.object(os, "sched_getaffinity", return_value=set(range(cpus))), mock.patch.dict(HTTP.RUNNER, command_output=output)

    def test_host_counts_pending_oracle_and_host_fixture_capacity(self):
        limits = {"memory_bytes": 6 * HTTP.GIB, "cpu_milli": 2000}
        a, b, c, d = self.host(cpus=9)
        with a, b, c, d:
            result = HTTP.host_capacity([], limits, Path("docker"))
        self.assertEqual(result["required_cpu_milli"], 8400)
        self.assertGreater(result["required_memory_bytes"], 19 * HTTP.GIB)
        for options, message in (({"cpus": 8}, "CPU headroom"), ({"memory_gib": 16}, "memory headroom")):
            a, b, c, d = self.host(**options)
            with a, b, c, d, self.assertRaisesRegex(ValueError, message):
                HTTP.host_capacity([], limits, Path("docker"))

    def test_unbounded_foreign_container_is_not_ignored(self):
        container = dict(name="/another-workload", running=True, memory=HTTP.GIB, nano_cpus=0, quota=0, period=0)
        a, b, c, d = self.host(container=container)
        with a, b, c, d, self.assertRaisesRegex(ValueError, "bounded non-cluster"):
            HTTP.host_capacity([], {"memory_bytes": HTTP.GIB, "cpu_milli": 1000}, Path("docker"))

    def test_normal_process_output_and_exit_are_retained(self):
        log = self.root / "normal.log"
        self.assertEqual(HTTP.run_test([sys.executable, "-c", "print('bounded fixture')"], os.environ.copy(), log), 0)
        self.assertEqual(log.read_text(), "bounded fixture\n")

    def test_process_group_drains_after_its_leader_exits(self):
        log = self.root / "orphan.log"
        code = "import os,signal,time; p=os.fork();\nif p: print(p,flush=True); os._exit(0)\nsignal.signal(signal.SIGTERM,signal.SIG_IGN); os.close(1); os.close(2); time.sleep(30)"
        with mock.patch.object(HTTP, "GROUP_TERM_SECONDS", 0.2), mock.patch.object(HTTP, "GROUP_KILL_SECONDS", 3):
            self.assertEqual(HTTP.run_test([sys.executable, "-c", code], os.environ.copy(), log), 0)
        child = int(log.read_text().strip())
        with self.assertRaises(ProcessLookupError):
            os.kill(child, 0)

    def test_output_limit_stops_the_child_and_keeps_bounded_log(self):
        log = self.root / "large.log"
        with mock.patch.dict(HTTP.RUNNER, MAX_LOG_BYTES=1024), self.assertRaisesRegex(RuntimeError, "output bound"):
            HTTP.run_test([sys.executable, "-c", "import os; os.write(1,b'x'*8192)"], os.environ.copy(), log)
        self.assertLessEqual(log.stat().st_size, 1024)

    def test_deadline_stops_the_child_before_returning(self):
        log = self.root / "deadline.log"
        with mock.patch.object(HTTP, "MAX_TEST_SECONDS", 0.1), self.assertRaisesRegex(RuntimeError, "time bound"):
            HTTP.run_test([sys.executable, "-c", "import os,time; print(os.getpid(),flush=True); time.sleep(30)"], os.environ.copy(), log)
        with self.assertRaises(ProcessLookupError):
            os.kill(int(log.read_text().strip()), 0)

    def test_interrupt_during_fixture_prepare_still_cleans_host_resources(self):
        actions = []
        class Fixtures:
            def __init__(self, *args):
                pass
            def prepare(self):
                actions.append("prepare")
                os.kill(os.getpid(), signal.SIGTERM)
                raise AssertionError("signal was ignored")
            def cleanup(self):
                actions.append("cleanup")
                return {"postgres_container_absent": True, "s3_container_absent": True, "credential_files_absent": True}
        args = SimpleNamespace(output=self.root / "output", cache=self.root / "cache", kubeconfig=Path("config"),
            kubectl=Path("kubectl"), go=Path("go"), docker=Path("docker"), sidb_crd=Path("sidb"), nodes="node")
        with mock.patch.object(HTTP, "runner_limits", return_value={}), mock.patch.object(HTTP, "host_capacity", return_value={}), \
                mock.patch.dict(HTTP.NATIVE, environment=mock.Mock(return_value=({}, {}))), \
                mock.patch.dict(HTTP.VERIFIER, source_files=lambda root: {}, source_images=lambda root: {}), \
                mock.patch.object(HTTP.runpy, "run_path", return_value={"Fixtures": Fixtures}), \
                mock.patch.object(HTTP, "run_test", side_effect=AssertionError("native run attempted")), \
                self.assertRaisesRegex(RuntimeError, "inspect retained"):
            HTTP.run(args)
        self.assertEqual(actions, ["prepare", "cleanup"])
        failure = json.loads((args.output / "failed-attempt.json").read_text())
        self.assertEqual(failure["failure_type"], "InterruptedError")
        self.assertTrue(failure["host_cleanup_receipt_written"])

    def test_output_and_cache_reject_parent_directory_escape(self):
        for field in ("output", "cache"):
            args = SimpleNamespace(output=self.root / "output", cache=self.root / "cache")
            setattr(args, field, Path("/srv/hakopod-backup-scratch/../outside"))
            with self.subTest(field=field), mock.patch.object(HTTP, "runner_limits", return_value={}), \
                    self.assertRaisesRegex(ValueError, "managed scratch paths"):
                HTTP.run(args)
            self.assertFalse((self.root / "output").exists())


if __name__ == "__main__":
    unittest.main()
