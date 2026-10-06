#!/usr/bin/env python3
"""Check native Oracle evidence boundaries without starting a cluster."""

import importlib.util
import json
from pathlib import Path
import tempfile
import unittest
from unittest import mock

SPEC = importlib.util.spec_from_file_location("oracle_acceptance", Path(__file__).with_name("run-development-oracle-free-acceptance.py"))
ORACLE = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(ORACLE)


class OracleNativeBoundary(unittest.TestCase):
    def test_foreign_or_duplicate_nodes_stop_before_cluster_access(self):
        for nodes in (["production"], ["k3d-hakopod-dev-server-0"] * 2, []):
            with self.subTest(nodes=nodes), mock.patch.dict(ORACLE.RUNNER, command_output=mock.Mock()) as runner:
                with self.assertRaisesRegex(ValueError, "dedicated development nodes"):
                    ORACLE.environment([], nodes, {}, Path("missing"), "lifecycle", Path("missing"), Path("docker"))
                runner["command_output"].assert_not_called()

    def test_memory_quantities_round_up_and_reject_unbounded_values(self):
        for raw, expected in (("4Gi", 4 * ORACLE.GIB), ("1536Mi", 1536 * 1024 ** 2), ("1.5G", 1500000000), ("1m", 1)):
            self.assertEqual(ORACLE.memory_bytes(raw), expected)
        for raw in ("max", "-1Gi", "NaN", "1e99999999", "8GB"):
            with self.assertRaises(ValueError):
                ORACLE.memory_bytes(raw)

    def test_restartable_init_containers_and_overhead_are_reserved(self):
        container = lambda memory, **values: {"resources": {"requests": {"memory": memory}}, **values}
        pod = {"spec": {"containers": [container("2Gi")], "initContainers": [
            container("1Gi", restartPolicy="Always"), container("3Gi")], "overhead": {"memory": "64Mi"}}}
        self.assertEqual(ORACLE.pod_resource(pod, "memory", "requests"), 4 * ORACLE.GIB + 64 * 1024 ** 2)
        self.assertEqual(ORACLE.pod_resource(pod, "memory", "limits"), 4 * ORACLE.GIB + 64 * 1024 ** 2)

    def test_container_ceiling_and_actual_memory_override_large_node_capacity(self):
        node = {"metadata": {"name": "n"}, "status": {"allocatable": {"cpu": "16", "memory": "128Gi"}}}
        container = {"container_id": "a" * 64, "cpu_milli": 2000, "memory_bytes": 8 * ORACLE.GIB, "memory_current_bytes": 6 * ORACLE.GIB}
        pod = {"spec": {"nodeName": "n", "containers": [{"resources": {"requests": {"cpu": "100m", "memory": "1Gi"}, "limits": {"cpu": "500m", "memory": "2Gi"}}}]}}
        capacity = ORACLE.node_capacity(node, [pod], container)
        self.assertEqual(capacity["available_cpu_milli"], 1500)
        self.assertEqual(capacity["available_memory_bytes"], ORACLE.GIB)
        self.assertEqual(capacity["requested_memory_bytes"], ORACLE.GIB)
        self.assertEqual(capacity["reserved_memory_bytes"], 2 * ORACLE.GIB)

    def test_cluster_totals_cannot_hide_an_unschedulable_database(self):
        fragmented = {name: {"available_cpu_milli": 900, "available_memory_bytes": 6 * ORACLE.GIB} for name in ORACLE.NODES}
        with self.assertRaisesRegex(ValueError, "per-node"):
            ORACLE.feasible_placement(fragmented, "lifecycle")

    def test_recovery_requires_server_capacity_for_bound_service_rollouts(self):
        for case in ("recovery", "http-api"):
            with self.subTest(case=case):
                worker = {"available_cpu_milli": 5000, "available_memory_bytes": 30 * ORACLE.GIB}
                with self.assertRaisesRegex(ValueError, "per-node"):
                    ORACLE.feasible_placement({"k3d-hakopod-database-worker-0": worker}, case)
                capacity = {"k3d-hakopod-dev-server-0": {"available_cpu_milli": 1200, "available_memory_bytes": 2 * ORACLE.GIB}, "k3d-hakopod-database-worker-0": worker}
                ORACLE.feasible_placement(capacity, case)
                capacity["k3d-hakopod-dev-server-0"]["available_memory_bytes"] -= 1
                with self.assertRaisesRegex(ValueError, "per-node"):
                    ORACLE.feasible_placement(capacity, case)

    def test_unbounded_or_swapping_containers_are_rejected_before_cgroup_read(self):
        value = {"id": "a" * 64, "name": "/k3d-hakopod-dev-server-0", "running": True, "pid": 1,
                 "memory": 8 * ORACLE.GIB, "swap": 16 * ORACLE.GIB, "nano_cpus": 2000000000, "quota": 0, "period": 0}
        with mock.patch.dict(ORACLE.RUNNER, command_json=mock.Mock(return_value=value)):
            with self.assertRaisesRegex(ValueError, "no swap"):
                ORACLE.container_budget(Path("docker"), "k3d-hakopod-dev-server-0")

    def test_effective_cgroup_limits_win_over_docker_configuration(self):
        value = {"id": "a" * 64, "name": "/k3d-hakopod-dev-server-0", "running": True, "pid": 123,
                 "memory": 8 * ORACLE.GIB, "swap": 8 * ORACLE.GIB, "nano_cpus": 4000000000, "quota": 0, "period": 0}
        def read(path):
            if path.name == "cgroup":
                return "0::/system.slice/docker-" + "a" * 64 + ".scope/init\n"
            if path.parent.name == "init":
                raise AssertionError("init cgroup does not contain the complete node")
            if path.parent.name == "system.slice":
                return {"memory.max": "max", "memory.current": str(20 * ORACLE.GIB), "cpu.max": "max 100000"}[path.name]
            return {"memory.max": str(4 * ORACLE.GIB), "memory.current": str(ORACLE.GIB),
                    "memory.swap.max": "0", "cpu.max": "200000 100000\n"}[path.name]
        with mock.patch.dict(ORACLE.RUNNER, command_json=mock.Mock(return_value=value)), mock.patch.object(Path, "read_text", read):
            budget = ORACLE.container_budget(Path("docker"), "k3d-hakopod-dev-server-0")
        self.assertEqual(budget["cpu_milli"], 2000)
        self.assertEqual(budget["memory_bytes"], 4 * ORACLE.GIB)
        self.assertEqual(budget["ancestor_memory_headroom_bytes"], 3 * ORACLE.GIB)

    def test_names_are_only_taken_from_the_native_test_package(self):
        with tempfile.TemporaryDirectory() as tmp:
            log = Path(tmp) / "events.jsonl"
            namespace = "hdb-" + "a" * 32
            log.write_text("\n".join(json.dumps({"Package": package, "Output": output}) for package, output in (
                ("unrelated", "Development Oracle namespace hdb-" + "b" * 32),
                ("github.com/hakopod/hakopod/internal/cluster", "test.go:1: Development Oracle namespace " + namespace),
                ("github.com/hakopod/hakopod/internal/cluster", "credentials and unrelated output"))))
            self.assertEqual(ORACLE.fixture_names(log), [namespace])

    def test_cleanup_requires_missing_namespaces_and_volumes(self):
        namespace = "hdb-" + "a" * 32
        with mock.patch.dict(ORACLE.RUNNER, command_output=mock.Mock(return_value=b"{}")):
            with self.assertRaisesRegex(ValueError, "namespace remains"):
                ORACLE.cleanup([], [namespace], "lifecycle")
        with mock.patch.dict(ORACLE.RUNNER, command_output=mock.Mock(return_value=b"")), mock.patch.object(ORACLE, "metadata", return_value={"items": [{"spec": {"claimRef": {"namespace": namespace}}}]}):
            with self.assertRaisesRegex(ValueError, "volume remains"):
                ORACLE.cleanup([], [namespace], "lifecycle")
        with mock.patch.dict(ORACLE.RUNNER, command_output=mock.Mock(return_value=b"")), mock.patch.object(ORACLE, "metadata", return_value={"items": []}):
            self.assertEqual(ORACLE.cleanup([], [namespace], "lifecycle"), {"namespaces": [namespace], "namespaces_absent": True, "persistent_volumes_absent": True})


if __name__ == "__main__":
    unittest.main()
