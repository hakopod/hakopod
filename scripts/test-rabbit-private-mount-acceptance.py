"""Test fixture ownership and failure cleanup without starting a cluster."""
import importlib.util
import json
from pathlib import Path
import tempfile
import unittest

spec = importlib.util.spec_from_file_location("rabbit_mount", Path(__file__).with_name("rabbit-private-mount-acceptance.py"))
fixture_module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(fixture_module)


class FakeFixture(fixture_module.Fixture):
    def __init__(self, root):
        super().__init__(root, "k3d")
        self.objects = {"container": [], "network": []}
        self.commands = []
        self.fail_container_remove = False
        self.existing_cluster = False

    def docker_objects(self, kind, *, cleanup=False):
        return self.objects[kind][:]

    def run(self, args, **kwargs):
        self.commands.append(args)
        if args[:3] == ["k3d", "cluster", "list"]:
            return json.dumps([{"name": "hakopod-dev"}] if self.existing_cluster else [])
        if args[:3] == ["docker", "container", "rm"]:
            if self.fail_container_remove:
                raise RuntimeError("container removal failed")
            self.objects["container"] = []
            return ""
        if args[:3] == ["docker", "network", "rm"]:
            self.objects["network"] = []
            return ""
        raise AssertionError("Unexpected external operation")

    def owned(self):
        self.objects["container"] = [{"Id": "a" * 64, "Name": "/k3d-hakopod-dev-server-0", "Config": {"Labels": {fixture_module.OWNER: self.owner, "k3d.cluster": "hakopod-dev"}}}]
        self.objects["network"] = [{"Id": "b" * 64, "Name": self.network, "Labels": {fixture_module.OWNER: self.owner}, "Containers": {}}]


class OwnershipTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.fixture = FakeFixture(Path(temporary.name))

    def test_existing_cluster_refused_before_mutation(self):
        self.fixture.existing_cluster = True
        with self.assertRaisesRegex(RuntimeError, "Existing development cluster"):
            self.fixture.start()
        self.assertEqual(self.fixture.commands, [["k3d", "cluster", "list", "-o", "json"]])
        self.assertFalse(self.fixture.cluster_attempted)
        self.assertFalse(self.fixture.network_attempted)

    def test_cleanup_uses_exact_owned_ids(self):
        self.fixture.owned()
        self.fixture.cleanup()
        self.assertTrue(self.fixture.report["cleanup_ok"])
        self.assertEqual(self.fixture.commands, [
            ["docker", "container", "rm", "--force", "--volumes", "a" * 64],
            ["docker", "network", "rm", "b" * 64],
        ])

    def test_changed_owner_is_never_removed(self):
        self.fixture.owned()
        self.fixture.objects["container"][0]["Config"]["Labels"][fixture_module.OWNER] = "foreign"
        self.fixture.cleanup()
        self.assertFalse(self.fixture.report["cleanup_ok"])
        self.assertFalse(any(call[:3] == ["docker", "container", "rm"] for call in self.fixture.commands))
        self.assertEqual(len(self.fixture.objects["container"]), 1)

    def test_network_with_attached_resources_is_preserved(self):
        self.fixture.owned()
        self.fixture.objects["network"][0]["Containers"] = {"c" * 64: {"Name": "unrelated"}}
        self.fixture.cleanup()
        self.assertFalse(self.fixture.report["cleanup_ok"])
        self.assertFalse(any(call[:3] == ["docker", "network", "rm"] for call in self.fixture.commands))

    def test_failure_still_attempts_other_owned_cleanup(self):
        self.fixture.owned()
        self.fixture.fail_container_remove = True
        self.fixture.cleanup()
        self.assertFalse(self.fixture.report["cleanup_ok"])
        self.assertIn(["docker", "network", "rm", "b" * 64], self.fixture.commands)

    def test_absence_is_verified_after_removal(self):
        self.fixture.owned()
        self.fixture.run = lambda _args, **_kwargs: ""
        self.fixture.cleanup()
        self.assertFalse(self.fixture.report["cleanup_ok"])
        self.assertTrue(any("remain" in value for value in self.fixture.report["cleanup_errors"]))

    def test_node_owner_must_match_before_mutation(self):
        self.fixture.owned()
        node = self.fixture.objects["container"][0]
        node["State"] = {"Running": True}
        node["NetworkSettings"] = {"Networks": {self.fixture.network: {"NetworkID": "b" * 64}}}
        self.fixture.report["network_id"] = "b" * 64
        self.fixture.run = lambda _args, **_kwargs: json.dumps([node])
        self.assertEqual(self.fixture.verify_node("a" * 64)["Id"], "a" * 64)
        for field, value in (("Id", "c" * 64), ("Name", "/foreign")):
            original = node[field]
            node[field] = value
            with self.assertRaisesRegex(RuntimeError, "identity or owner changed"):
                self.fixture.verify_node("a" * 64)
            node[field] = original
        node["Config"]["Labels"][fixture_module.OWNER] = "foreign"
        with self.assertRaisesRegex(RuntimeError, "identity or owner changed"):
            self.fixture.verify_node("a" * 64)

    def test_cluster_uid_must_match_before_mutation(self):
        self.fixture.report.update(node_id="a" * 64, cluster_uid="original")
        self.fixture.verify_node = lambda _identifier: None
        self.fixture.kube = lambda _args: json.dumps({"metadata": {"uid": "replaced"}})
        with self.assertRaisesRegex(RuntimeError, "cluster identity changed"):
            self.fixture.verify_cluster()


if __name__ == "__main__":
    unittest.main()
