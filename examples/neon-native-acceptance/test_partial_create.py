#!/usr/bin/env python3
"""Development fixtures for partial-create evidence and cleanup ordering."""
import copy
import hashlib
import importlib.util
import json
from pathlib import Path
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import Mock, patch

spec = importlib.util.spec_from_file_location("partial_create_test", Path(__file__).with_name("partial-create.py"))
M = importlib.util.module_from_spec(spec)
spec.loader.exec_module(M)
SOURCE, TARGET, TENANT, ORIGINAL = (c * 32 for c in "1234")
TIMELINE, ANCESTOR, TOKEN, TARGET_TENANT, TARGET_TIMELINE = (c * 32 for c in "56789")
RUN = "a" * 32
ERROR = b'{"msg":"Cannot branch off the timeline that is not present in pageserver"}'


def atomic(path, value):
    path.write_text(json.dumps(value, sort_keys=True) + "\n")
    path.chmod(0o600)


class PartialCreateTests(unittest.TestCase):
    def setUp(self):
        self.__dict__.pop("body", None)
        self.__dict__.pop("owner", None)
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)
        self.calls, self.recorded = [], []
        self.bad = None
        self.target = {"revision": 3, "namespace_uid": "target-namespace", "tenant_id": TARGET_TENANT,
                       "timeline_id": TARGET_TIMELINE, "tenant_generation": 2, "timeline_generation": 1,
                       "pod_uids": ["target-pod"], "data_sha256": M.digest(("backup-" + RUN).encode())}
        self.driver = SimpleNamespace(evidence=self.root, run_id=RUN, deleted_platforms=set(),
            bound={SOURCE: ("source-namespace", "b" * 32), TARGET: ("target-namespace", "c" * 32)},
            pending_lifecycle={"run_id": RUN, "platform_id": SOURCE, "namespace_uid": "source-namespace",
                               "tenant_id": TENANT, "timeline_id": ORIGINAL, "created": True, "deleted": False})
        self.current = {"id": SOURCE, "revision": 1, "spec": {"name": "source"}}
        self.driver.api = lambda method, path, body=None: self.current if method == "GET" else {
            "platform_id": SOURCE, "platform_revision": 1, "namespace_uid": "source-namespace", "tenant_id": TENANT, "timeline_id": ORIGINAL}
        self.driver.k = lambda *args: ""
        self.driver.delete = self.delete
        self.driver.record = lambda case, observation: self.recorded.append((case, observation))
        atomic(self.root / "backup-recovery.json", {"platform_id": SOURCE})
        test = self

        class Controller:
            def __init__(self, driver, platform):
                self.platform, self.bearer = platform, "development-bearer-" + platform

            def __enter__(self):
                return self

            def __exit__(self, *args):
                pass

            def request(self, method, path, body=None, owner=None, deletion=None, bearer=None):
                test.calls.append((method, path))
                if self.platform == TARGET:
                    return (200 if test.bad == "foreign" else 401 if method == "GET" else 409), b'{}'
                if method == "POST":
                    test.body, test.owner = body, owner
                    return (201, b'{}') if test.bad == "successful-create" else (500, ERROR)
                if path == "/control/v1/tenant/" + TENANT:
                    return 200, json.dumps({"ownership_token": "d" * 32}).encode()
                return 404, b'{}'

        class Objects:
            def __init__(self, *args):
                pass

            def empty(self, path):
                if test.bad == "remote-data" and hasattr(test, "body"):
                    raise RuntimeError("partial timeline unexpectedly has remote data")
                return 0

            def marker(self, path, optional=False):
                if optional:
                    return None
                if "tenant-deletions" in path:
                    return {"schema_version": 1, "delete_token": "e" * 32, "authorization_digest": "f" * 64}, "e" * 64
                unused, unused_digest, request_hash = M.branch_request(TENANT, TIMELINE, ANCESTOR)
                value = {"schema_version": 1, "token": TOKEN, "request_hash": request_hash}
                if test.bad == "reservation" and "reservations" in path:
                    value["token"] = "f" * 32
                return value, "d" * 64

        self.controllers, self.objects = Controller, Objects

    def delete(self, platform, name, seconds):
        self.calls.append(("API_DELETE", platform))
        return {"id": "f" * 32, "platform_id": platform, "kind": "delete", "status": "succeeded", "revision": 2}

    def ledger(self, driver, platform, tenant, timeline):
        if not hasattr(self, "body"):
            return []
        unused, unused_digest, request_hash = M.branch_request(tenant, timeline, ANCESTOR)
        result = {"resource_kind": "timeline", "external_key": tenant + "/" + timeline, "state": "applying", "result": None,
                  "request_hash": request_hash, "ownership_token_sha256": M.digest(TOKEN.encode()), "timeline_rows": 0}
        if self.bad == "ledger":
            result["request_hash"] = "0" * 64
        if self.bad == "completed-ledger":
            result["state"], result["result"], result["timeline_rows"] = "completed", "present", 1
        return [result]

    def snapshot(self, driver, platform):
        value = copy.deepcopy(self.target)
        if self.bad == "target-changed" and self.driver.deleted_platforms:
            value["revision"] += 1
        return value

    def run_case(self):
        volumes = lambda driver: [] if driver.deleted_platforms else [{"metadata": {"name": "pvc-source", "uid": "source-pv"}, "spec": {"claimRef": {"namespace": "managed-platform-" + SOURCE}}}]
        with patch.object(M, "Controller", self.controllers), patch.object(M, "Objects", self.objects), \
             patch.object(M, "ledger", self.ledger), patch.object(M, "target_snapshot", self.snapshot), \
             patch.object(M, "volume_inventory", volumes), patch.object(M.secrets, "token_hex", side_effect=[TIMELINE, ANCESTOR, TOKEN]):
            return M.run(self.driver, {"id": SOURCE}, {"id": TARGET}, atomic)

    def resources(self):
        return {"source": {"platform_id": SOURCE, "namespace_uid": "source-namespace"},
                "recovery_target": {"platform_id": TARGET, "namespace_uid": "target-namespace"}}

    def test_actual_observation_chain_is_required_before_delete(self):
        observation = self.run_case()
        self.assertEqual(self.recorded, [("partial-create-cleanup", observation)])
        self.assertEqual(sum(method == "API_DELETE" for method, path in self.calls), 1)
        M.validate_observation(observation, self.resources())
        M.validate_supporting_files(self.root / "partial-create-cleanup.json", observation)
        self.assertNotIn(TOKEN, json.dumps(observation))

    def test_failed_branch_and_matching_reservation_are_required(self):
        for failure, message in (("successful-create", "did not fail for its missing ancestor"),
                                 ("ledger", "exact unfinished provider authority"),
                                 ("completed-ledger", "exact unfinished provider authority"),
                                 ("reservation", "reservation differs"), ("remote-data", "unexpectedly has remote data")):
            with self.subTest(failure=failure):
                self.setUp()
                self.bad = failure
                with self.assertRaisesRegex(RuntimeError, message):
                    self.run_case()
                self.assertFalse(any(method == "API_DELETE" for method, path in self.calls))
                self.assertFalse(self.recorded)

    def test_foreign_acceptance_and_target_changes_cannot_record_success(self):
        for failure, message in (("foreign", "deleted source bearer reached"), ("target-changed", "another admitted platform changed")):
            with self.subTest(failure=failure):
                self.setUp()
                self.bad = failure
                with self.assertRaisesRegex(RuntimeError, message):
                    self.run_case()
                self.assertEqual(self.driver.deleted_platforms, {SOURCE})
                self.assertFalse(self.recorded)

    def test_forged_structural_outcomes_and_changed_artifacts_fail(self):
        valid = self.run_case()
        for field, change in (("create_http_status", 201), ("source_namespace_absent", False), ("data_prefix_objects_after", 1),
                              ("reservation_before_sha256", None), ("source_revision", 2), ("target_after", {})):
            with self.subTest(field=field), self.assertRaises(RuntimeError):
                M.validate_observation(dict(valid, **{field: change}), self.resources())
        (self.root / "partial-create-error.body").write_bytes(b'{}')
        with self.assertRaisesRegex(RuntimeError, "real failure witness"):
            M.validate_supporting_files(self.root / "partial-create-cleanup.json", valid)

    def test_unrelated_http_errors_are_not_the_expected_provider_failure(self):
        for status, error in ((401, ERROR), (500, b'object store unavailable'), (500, b'ancestor not found')):
            self.assertFalse(M.missing_ancestor_error(status, error))

    def test_request_matches_typed_provider_canonicalization(self):
        body, normalized, request_hash = M.branch_request(TENANT, TIMELINE, ANCESTOR)
        # Pinned owned_json_request serializes the typed untagged Branch model;
        # ownership::parse sorts object keys before hashing the compact JSON.
        raw = ('{"ancestor_start_lsn":null,"ancestor_timeline_id":"' + ANCESTOR +
               '","new_timeline_id":"' + TIMELINE + '","pg_version":null}').encode()
        self.assertEqual(M.encoded(body), raw)
        self.assertEqual(normalized, hashlib.sha256(raw).hexdigest())
        self.assertEqual(request_hash, hashlib.sha256(b"timeline\0" + (TENANT + "/" + TIMELINE).encode() + b"\0" + raw).hexdigest())
        for identities in ((TENANT, TIMELINE, TIMELINE), (TENANT, "../invalid", ANCESTOR)):
            with self.assertRaises(RuntimeError):
                M.branch_request(*identities)

    def test_every_supporting_file_is_immutable_and_protected(self):
        valid = self.run_case()
        names = ("partial-create-preparation.json", "partial-create-request.json", "partial-create-error.body",
                 "source-lifecycle-before-partial-create.json", "backup-recovery.json")
        for name in names:
            path = self.root / name
            original = path.read_bytes()
            with self.subTest(name=name, change="bytes"), self.assertRaises((RuntimeError, KeyError)):
                path.write_bytes(b'{}')
                M.validate_supporting_files(self.root / "partial-create-cleanup.json", valid)
            path.write_bytes(original)
            with self.subTest(name=name, change="permissions"), self.assertRaisesRegex(RuntimeError, "protected"):
                path.chmod(0o644)
                M.validate_supporting_files(self.root / "partial-create-cleanup.json", valid)
            path.chmod(0o600)
            saved = path.with_suffix(path.suffix + ".saved")
            path.rename(saved)
            path.symlink_to(saved)
            with self.subTest(name=name, change="symlink"), self.assertRaisesRegex(RuntimeError, "protected"):
                M.validate_supporting_files(self.root / "partial-create-cleanup.json", valid)
            path.unlink()
            saved.rename(path)


class ControllerTransportTests(unittest.TestCase):
    def setUp(self):
        self.controller = M.Controller.__new__(M.Controller)
        self.controller.driver = SimpleNamespace(a=SimpleNamespace(local_port=18080))
        self.controller.target, self.controller.context = Mock(), object()
        self.controller.bearer = "development-controller-bearer"

    def test_tls_name_port_headers_body_and_bounds_are_exact(self):
        with patch.object(M, "LocalTLS") as tls:
            connection = tls.return_value
            connection.getresponse.return_value.status = 500
            connection.getresponse.return_value.read.return_value = ERROR
            body, unused, unused_hash = M.branch_request(TENANT, TIMELINE, ANCESTOR)
            result = self.controller.request("POST", "/v1/tenant/" + TENANT + "/timeline", body, owner=TOKEN)
            self.assertEqual(result, (500, ERROR))
            tls.assert_called_once_with("neon-storage-controller", 18082, timeout=45, context=self.controller.context)
            self.controller.target.assert_called_once_with()
            args, kwargs = connection.request.call_args
            self.assertEqual(args, ("POST", "/v1/tenant/" + TENANT + "/timeline"))
            self.assertEqual(kwargs["body"], M.encoded(body))
            self.assertEqual(kwargs["headers"], {"Authorization": "Bearer development-controller-bearer", "Accept": "application/json",
                                               "Content-Type": "application/json", "Hakopod-Ownership-Token": TOKEN})
            connection.getresponse.return_value.read.assert_called_once_with(M.LIMIT + 1)
            connection.close.assert_called_once_with()

    def test_request_closes_after_oversized_or_failed_response(self):
        for failure in ("oversized", "timeout"):
            with self.subTest(failure=failure), patch.object(M, "LocalTLS") as tls:
                response = tls.return_value.getresponse.return_value
                if failure == "oversized":
                    response.read.return_value = b"x" * (M.LIMIT + 1)
                else:
                    response.read.side_effect = TimeoutError("development timeout")
                with self.assertRaises((RuntimeError, TimeoutError)):
                    self.controller.request("GET", "/control/v1/tenant/" + TENANT)
                tls.return_value.close.assert_called_once_with()

    def test_unowned_target_and_invalid_routes_cannot_open_transport(self):
        with patch.object(M, "LocalTLS") as tls:
            for method, path, owner in (("PUT", "/v1/tenant/" + TENANT, None), ("GET", "/metrics", None),
                                        ("DELETE", "/v1/tenant/" + TENANT, "invalid")):
                with self.assertRaises(RuntimeError):
                    self.controller.request(method, path, owner=owner)
            self.controller.target.side_effect = RuntimeError("owned target changed")
            with self.assertRaisesRegex(RuntimeError, "owned target changed"):
                self.controller.request("GET", "/control/v1/tenant/" + TENANT)
            tls.assert_not_called()

    def test_local_socket_preserves_verified_tls_server_name(self):
        context, raw_socket = Mock(), Mock()
        connection = M.LocalTLS.__new__(M.LocalTLS)
        connection._context, connection.host, connection.port, connection.timeout = context, "neon-storage-controller", 18082, 45
        with patch.object(M.socket, "create_connection", return_value=raw_socket) as connect:
            connection.connect()
        connect.assert_called_once_with(("127.0.0.1", 18082), 45)
        context.wrap_socket.assert_called_once_with(raw_socket, server_hostname="neon-storage-controller")
        self.assertIs(connection.sock, context.wrap_socket.return_value)


class ControllerLedgerTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        images = Path(self.temporary.name) / "images.json"
        image = "example.invalid/controller-database@sha256:" + "c" * 64
        atomic(images, {"controller-database": image})
        namespace = "managed-platform-" + SOURCE
        container = {"name": "controller-database", "image": image}
        self.workload = {"metadata": {"uid": "database-workload", "ownerReferences": [{"apiVersion": "v1", "kind": "Namespace", "name": namespace, "uid": "source-namespace"}]},
                         "spec": {"template": {"spec": {"containers": [copy.deepcopy(container)]}}}}
        self.pod = {"metadata": {"name": "neon-controller-database-0", "labels": {"hakopod.io/neon-role": "controller-database"},
                                 "ownerReferences": [{"kind": "StatefulSet", "uid": "database-workload"}]},
                    "spec": {"containers": [copy.deepcopy(container)]}}
        self.driver = SimpleNamespace(a=SimpleNamespace(images=str(images), kubeconfig="/private/development-kubeconfig"),
            bound={SOURCE: ("source-namespace", "operation")}, pods=lambda platform: (namespace, [self.pod]),
            claim_namespace=Mock(return_value=True),
            k=lambda *args: json.dumps(self.workload), command=Mock(return_value="[]"))

    def test_ledger_query_is_read_only_scoped_and_bounded(self):
        self.assertEqual(M.ledger(self.driver, SOURCE, TENANT, TIMELINE), [])
        self.driver.claim_namespace.assert_called_once_with(SOURCE, "operation")
        args, kwargs = self.driver.command.call_args
        argv, timeout = args
        self.assertEqual(timeout, 20)
        self.assertEqual(argv[:9], ["kubectl", "--kubeconfig", "/private/development-kubeconfig", "--context", "k3d-hakopod-dev", "--request-timeout=15s", "-n", "managed-platform-" + SOURCE, "exec"])
        self.assertIn("PGOPTIONS=-c default_transaction_read_only=on -c statement_timeout=5000", argv)
        self.assertEqual(argv[-3:], ["ON_ERROR_STOP=1", "-f", "-"])
        self.assertNotIn("-h", argv)
        query = kwargs["data"].decode()
        self.assertTrue(query.startswith("SELECT "))
        self.assertIn("WHERE external_key='" + TENANT + "/" + TIMELINE + "' LIMIT 2", query)
        self.assertIn("FROM timelines WHERE tenant_id='" + TENANT + "' AND timeline_id='" + TIMELINE + "'", query)
        self.assertIn("ownership_token_sha256", query)

    def test_ledger_refuses_mismatched_workload_and_image_before_exec(self):
        original = copy.deepcopy(self.pod)
        for change in ("owner", "image", "scope"):
            self.pod = copy.deepcopy(original)
            if change == "owner":
                self.pod["metadata"]["ownerReferences"][0]["uid"] = "foreign-workload"
            elif change == "image":
                self.pod["spec"]["containers"][0]["image"] = "example.invalid/controller-database:latest"
            with self.subTest(change=change), self.assertRaises(RuntimeError):
                M.ledger(self.driver, SOURCE, TENANT if change != "scope" else "bad' OR true", TIMELINE)
        self.driver.command.assert_not_called()


if __name__ == "__main__":
    unittest.main()
