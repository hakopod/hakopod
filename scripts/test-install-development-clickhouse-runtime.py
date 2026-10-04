#!/usr/bin/env python3
"""Check dedicated ClickHouse runtime target validation without a cluster."""
import importlib.util
from pathlib import Path
import unittest


SPEC = importlib.util.spec_from_file_location(
    "clickhouse_runtime", Path(__file__).with_name("install-development-clickhouse-runtime.py"))
RUNTIME = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(RUNTIME)


class DevelopmentClickHouseRuntimeTests(unittest.TestCase):
    def test_requires_explicit_unique_dedicated_targets(self):
        valid = "k3d-hakopod-clickhouse-worker-0"
        self.assertEqual(RUNTIME.target_nodes([valid]), (valid,))
        for values in ([], [valid, valid], ["k3d-hakopod-database-worker-0"],
                       [f"k3d-hakopod-clickhouse-worker-{i}" for i in range(5)]):
            with self.subTest(values=values), self.assertRaises(ValueError):
                RUNTIME.target_nodes(values)

    def test_worker_ownership_requires_cluster_role_and_exact_lease(self):
        lease = "01234567-89ab-cdef-0123-456789abcdef"
        labels = {"k3d.cluster": "hakopod-dev", "k3d.role": "agent", "com.hakopod.lease-id": lease}
        self.assertTrue(RUNTIME.owned_worker(labels, lease))
        for key, value in (("k3d.cluster", "other"), ("k3d.role", "server"),
                           ("com.hakopod.lease-id", "fedcba98-7654-3210-fedc-ba9876543210")):
            changed = dict(labels, **{key: value})
            with self.subTest(key=key):
                self.assertFalse(RUNTIME.owned_worker(changed, lease))

    def test_target_must_be_free_of_non_system_pods_and_direct_tasks(self):
        kube = {"metadata": {"namespace": "kube-system", "uid": "pod-uid"}}
        owned = {"Labels": {"io.kubernetes.pod.uid": "pod-uid"}}
        self.assertTrue(RUNTIME.target_is_idle([kube], [owned]))
        self.assertFalse(RUNTIME.target_is_idle([{"metadata": {"namespace": "hdb-fixture"}}], [owned]))
        self.assertFalse(RUNTIME.target_is_idle([kube], [{"Labels": {}}]))
        self.assertFalse(RUNTIME.target_is_idle([kube], [{"Labels": {"io.kubernetes.pod.uid": "orphan"}}]))

    def test_worker_capacity_must_be_explicit_positive_and_bounded(self):
        valid = {"NanoCpus": 8_000_000_000, "Memory": 32 * 1024 * 1024 * 1024}
        self.assertTrue(RUNTIME.bounded_worker(valid))
        for config in ({}, {**valid, "NanoCpus": 0}, {**valid, "Memory": 0},
                       {**valid, "NanoCpus": True}, {**valid, "Memory": True},
                       {**valid, "NanoCpus": 8_000_000_001},
                       {**valid, "Memory": 32 * 1024 * 1024 * 1024 + 1}):
            with self.subTest(config=config):
                self.assertFalse(RUNTIME.bounded_worker(config))


if __name__ == "__main__":
    unittest.main()
