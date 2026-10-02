#!/usr/bin/env python3
"""Focused contract tests for held Supabase create-review validation."""
from contextlib import redirect_stderr, redirect_stdout
from datetime import datetime, timedelta, timezone
import importlib.util
import io
import json
import os
from pathlib import Path
import stat
import tempfile
import unittest
from unittest import mock
from types import SimpleNamespace


ROOT = Path(__file__).resolve().parents[1]
REVIEW_PATH = ROOT / "examples" / "supabase-native-acceptance" / "review.py"
MODULE_SPEC = importlib.util.spec_from_file_location("supabase_held_review", REVIEW_PATH)
review_module = importlib.util.module_from_spec(MODULE_SPEC)
assert MODULE_SPEC.loader is not None
MODULE_SPEC.loader.exec_module(review_module)


NOW = datetime(2026, 10, 2, 8, 0, tzinfo=timezone.utc)
PLATFORM_ID = "1" * 32
REVIEW_ID = "2" * 32


def specification():
    return {
        "version": 1,
        "type": "supabase",
        "database": {"postgres_version": 17},
    }


def valid_document():
    return {
        "blocked": False,
        "platform": {
            "id": PLATFORM_ID,
            "project": "project-a",
            "environment": "production",
            "spec": specification(),
        },
        "review": {
            "id": REVIEW_ID,
            "kind": "create",
            "expected_revision": 0,
            "request_hash": "3" * 64,
            "authority_fingerprint": "4" * 64,
            "expires_at": (NOW + timedelta(minutes=10)).isoformat(),
        },
        "plan": {"namespace": "managed-platform-" + PLATFORM_ID},
    }


class ValidateReviewTests(unittest.TestCase):
    def validate(self, document=None, spec=None):
        return review_module.validate_review(
            valid_document() if document is None else document,
            specification() if spec is None else spec,
            "project-a",
            "production",
            now=NOW,
        )

    def assert_rejected(self, mutate):
        document = valid_document()
        mutate(document)
        with self.assertRaises((ValueError, TypeError, KeyError)):
            self.validate(document)

    def test_accepts_exact_server_create_review(self):
        document = valid_document()
        self.assertIs(self.validate(document), document)

    def test_requires_available_complete_review(self):
        for mutate in (
            lambda value: value.update(blocked=True),
            lambda value: value.pop("platform"),
            lambda value: value.update(review=[]),
            lambda value: value.update(plan=None),
        ):
            with self.subTest(mutate=mutate):
                self.assert_rejected(mutate)

    def test_rejects_unexpected_top_level_and_authority_fields(self):
        for mutate in (
            lambda value: value.update(debug={"secret": "must-not-pass"}),
            lambda value: value["review"].update(signature="unexpected"),
            lambda value: value["review"].update(authority={"token": "unexpected"}),
        ):
            with self.subTest(mutate=mutate):
                self.assert_rejected(mutate)

    def test_binds_exact_project_environment_and_specification(self):
        mutations = (
            lambda value: value["platform"].update(project="project-b"),
            lambda value: value["platform"].update(environment="staging"),
            lambda value: value["platform"].update(spec={**specification(), "version": 2}),
            lambda value: value["platform"].update(spec={**specification(), "extra": True}),
        )
        for mutate in mutations:
            with self.subTest(mutate=mutate):
                self.assert_rejected(mutate)

    def test_requires_server_identity_and_matching_namespace(self):
        for mutate in (
            lambda value: value["platform"].update(id="not-an-id"),
            lambda value: value["platform"].update(id="A" * 32),
            lambda value: value["plan"].update(namespace="managed-platform-" + "5" * 32),
        ):
            with self.subTest(mutate=mutate):
                self.assert_rejected(mutate)

    def test_requires_create_at_integer_revision_zero(self):
        for mutate in (
            lambda value: value["review"].update(kind="update"),
            lambda value: value["review"].update(expected_revision=1),
            lambda value: value["review"].update(expected_revision=False),
            lambda value: value["review"].pop("expected_revision"),
        ):
            with self.subTest(mutate=mutate):
                self.assert_rejected(mutate)

    def test_requires_review_identity_and_authority_hashes(self):
        for mutate in (
            lambda value: value["review"].update(id="6" * 31),
            lambda value: value["review"].update(request_hash="g" * 64),
            lambda value: value["review"].update(authority_fingerprint="7" * 63),
            lambda value: value["review"].update(authority_fingerprint=None),
        ):
            with self.subTest(mutate=mutate):
                self.assert_rejected(mutate)

    def test_requires_aware_future_expiry_within_server_window(self):
        expiries = (
            NOW.isoformat(),
            (NOW - timedelta(seconds=1)).isoformat(),
            (NOW + timedelta(seconds=611)).isoformat(),
            (NOW + timedelta(minutes=5)).replace(tzinfo=None).isoformat(),
            "invalid",
            None,
            123,
        )
        for expiry in expiries:
            with self.subTest(expiry=expiry):
                self.assert_rejected(lambda value, expiry=expiry: value["review"].update(expires_at=expiry))


class ProtectedJSONTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.directory = Path(self.temporary.name)

    def tearDown(self):
        self.temporary.cleanup()

    def write(self, name="review.json", body=None, mode=0o600):
        path = self.directory / name
        path.write_text(json.dumps(valid_document()) if body is None else body, encoding="utf-8")
        path.chmod(mode)
        return path

    def test_reads_owned_regular_mode_0600_file(self):
        self.assertEqual(review_module.protected_json(self.write()), valid_document())

    def test_rejects_permissive_mode_directory_symlink_and_oversize(self):
        permissive = self.write("permissive.json", mode=0o640)
        symlink = self.directory / "link.json"
        symlink.symlink_to(self.write("target.json"))
        oversized = self.write("large.json", body=" " * ((1 << 20) + 1))
        for path in (permissive, self.directory, symlink, oversized):
            with self.subTest(path=path.name), self.assertRaises(ValueError):
                review_module.protected_json(path)

    def test_rejects_wrong_owner_from_lstat(self):
        path = self.write()
        actual = path.lstat()
        fake = os.stat_result((actual.st_mode, actual.st_ino, actual.st_dev, actual.st_nlink,
                               actual.st_uid + 1, actual.st_gid, actual.st_size,
                               actual.st_atime, actual.st_mtime, actual.st_ctime))
        with mock.patch.object(Path, "lstat", return_value=fake), self.assertRaises(ValueError):
            review_module.protected_json(path)

    def test_opens_with_nofollow_and_rejects_inode_swap(self):
        path = self.write()
        real_open = os.open
        opened_flags = []

        def record_open(name, flags):
            opened_flags.append(flags)
            return real_open(name, flags)

        with mock.patch.object(review_module.os, "open", side_effect=record_open):
            review_module.protected_json(path)
        self.assertTrue(opened_flags[0] & os.O_NOFOLLOW)

        actual = path.lstat()
        changed = os.stat_result((actual.st_mode, actual.st_ino + 1, actual.st_dev, actual.st_nlink,
                                  actual.st_uid, actual.st_gid, actual.st_size,
                                  actual.st_atime, actual.st_mtime, actual.st_ctime))
        with mock.patch.object(review_module.os, "fstat", return_value=changed), self.assertRaises(ValueError):
            review_module.protected_json(path)

    @staticmethod
    def changed_stat(value, **changes):
        fields = ("st_dev", "st_ino", "st_mode", "st_uid", "st_gid", "st_size",
                  "st_mtime_ns", "st_ctime_ns")
        values = {field: getattr(value, field) for field in fields}
        values.update(changes)
        return SimpleNamespace(**values)

    def test_rechecks_open_descriptor_metadata_before_read(self):
        path = self.write()
        before = path.lstat()
        cases = (
            {"st_mode": stat.S_IFREG | 0o640},
            {"st_uid": before.st_uid + 1},
            {"st_gid": before.st_gid + 1},
            {"st_size": before.st_size + 1},
            {"st_mtime_ns": before.st_mtime_ns + 1},
            {"st_ctime_ns": before.st_ctime_ns + 1},
        )
        for changes in cases:
            with self.subTest(changes=changes):
                opened = self.changed_stat(before, **changes)
                with mock.patch.object(review_module.os, "fstat", return_value=opened), self.assertRaises(ValueError):
                    review_module.protected_json(path)

    def test_rechecks_descriptor_and_path_identity_after_read(self):
        path = self.write()
        stable = path.lstat()
        changed = self.changed_stat(stable, st_mtime_ns=stable.st_mtime_ns + 1)
        with mock.patch.object(review_module.os, "fstat", side_effect=(stable, changed)), self.assertRaises(ValueError):
            review_module.protected_json(path)
        with mock.patch.object(Path, "lstat", side_effect=(stable, changed)), self.assertRaises(ValueError):
            review_module.protected_json(path)

    def test_rejects_duplicate_keys_at_any_depth(self):
        for body in ('{"blocked":false,"blocked":false}',
                     '{"review":{"id":"a","id":"b"}}'):
            with self.subTest(body=body), self.assertRaises(ValueError):
                review_module.protected_json(self.write(body=body))


class CLITests(unittest.TestCase):
    def invoke(self, review, spec):
        with tempfile.TemporaryDirectory() as directory:
            review_path = Path(directory) / "review.json"
            spec_path = Path(directory) / "spec.json"
            review_path.write_text(json.dumps(review), encoding="utf-8")
            spec_path.write_text(json.dumps(spec), encoding="utf-8")
            review_path.chmod(0o600)
            spec_path.chmod(0o600)
            stdout = io.StringIO()
            stderr = io.StringIO()
            argv = ["review.py", "--review", str(review_path), "--spec", str(spec_path),
                    "--project", "project-a", "--environment", "production"]
            with mock.patch("sys.argv", argv), redirect_stdout(stdout), redirect_stderr(stderr):
                try:
                    review_module.main()
                    code = 0
                except SystemExit as raised:
                    code = raised.code
            return code, stdout.getvalue(), stderr.getvalue()

    def test_success_projects_only_consumed_platform_and_review_fields(self):
        document = valid_document()
        document["review"]["expires_at"] = (datetime.now(timezone.utc) + timedelta(minutes=5)).isoformat()
        document["platform"]["server_internal"] = "platform-secret"
        document["plan"]["server_internal"] = "plan-secret"
        code, output, error = self.invoke(document, specification())
        self.assertEqual((code, error), (0, ""))
        projection = json.loads(output)
        self.assertEqual(set(projection), {"blocked", "platform", "review"})
        self.assertEqual(set(projection["platform"]), {"id", "project", "environment", "spec"})
        self.assertNotIn("platform-secret", output)
        self.assertNotIn("plan-secret", output)

    def test_invalid_input_emits_only_generic_error(self):
        secret = "super-secret-review-material"
        with tempfile.TemporaryDirectory() as directory:
            review_path = Path(directory) / "review.json"
            spec_path = Path(directory) / "spec.json"
            review_path.write_text('{"secret":"' + secret + '","blocked":true}', encoding="utf-8")
            spec_path.write_text(json.dumps(specification()), encoding="utf-8")
            review_path.chmod(0o600)
            spec_path.chmod(0o600)
            stdout = io.StringIO()
            stderr = io.StringIO()
            argv = ["review.py", "--review", str(review_path), "--spec", str(spec_path),
                    "--project", "project-a", "--environment", "production"]
            with mock.patch("sys.argv", argv), redirect_stdout(stdout), redirect_stderr(stderr):
                with self.assertRaises(SystemExit) as raised:
                    review_module.main()
            self.assertEqual(raised.exception.code, 2)
            self.assertEqual(stdout.getvalue(), "")
            self.assertEqual(stderr.getvalue(), "held create review is invalid; obtain a new server review\n")
            self.assertNotIn(secret, stderr.getvalue())

    def test_non_string_expiry_is_generic_rejection_without_traceback(self):
        document = valid_document()
        document["review"]["expires_at"] = None
        code, output, error = self.invoke(document, specification())
        self.assertEqual(code, 2)
        self.assertEqual(output, "")
        self.assertEqual(error, "held create review is invalid; obtain a new server review\n")


if __name__ == "__main__":
    suite = unittest.defaultTestLoader.loadTestsFromModule(__import__(__name__))
    result = unittest.TextTestRunner(stream=io.StringIO(), verbosity=0).run(suite)
    if not result.wasSuccessful():
        raise SystemExit("supabase held review tests failed")
    print(f"supabase held review tests passed ({result.testsRun})")
