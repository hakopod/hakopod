#!/usr/bin/env python3
"""Keep failed or changed Oracle builds out of image packaging."""

import hashlib
import importlib.util
import json
from pathlib import Path
import tempfile
import unittest
from unittest import mock

SPEC = importlib.util.spec_from_file_location("oracle_free_packager", Path(__file__).with_name("package-managed-oracle-free.py"))
PACKAGER = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(PACKAGER)


def digest(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


class PackagingBoundary(unittest.TestCase):
    def setUp(self):
        self.scratch = tempfile.TemporaryDirectory(prefix="oracle-free-package-test-")
        self.addCleanup(self.scratch.cleanup)
        self.base = Path(self.scratch.name)
        self.source, self.evidence, self.build = [self.base / name for name in ("source", "evidence", "upstream-build")]
        self.evidence.mkdir()
        required = [
            "Dockerfile.oracle-free-operator", "scripts/apply-managed-oracle-patches.py",
            "scripts/build-managed-oracle-free.sh", "scripts/export-oracle-free-crd.py",
            "installer/oracle_free_controller.py", "patches/oracle-operator/hakopod_free_policy.go.txt",
            "patches/oracle-operator/hakopod_free_resource.go.txt", "patches/oracle-operator/hakopod_free_health.go.txt",
        ]
        self.hashes = {}
        for name in required:
            path = self.source / name
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_text("reviewed fixture: " + name + "\n")
            self.hashes[name] = digest(path)
        self.manifest = self.evidence / "formatted-source-sha256.json"
        self.write_manifest()
        (self.evidence / "exit-code.txt").write_text("0\n")
        self.artifacts = []
        for name in ("bin/manager", "upstream.patch", "crds/singleinstancedatabases.yaml"):
            path = self.build / "image" / name
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_text("built fixture: " + name + "\n")
            self.artifacts.append(path)
        self.checksums = self.build / "artifact-sha256.txt"
        self.checksums.write_text("".join(digest(path) + "  " + str(path) + "\n" for path in self.artifacts))
        self.artifact_sha = digest(self.checksums)
        (self.build / "image/Dockerfile").write_bytes((self.source / "Dockerfile.oracle-free-operator").read_bytes())
        (self.build / "source").mkdir()
        for name in ("source/LICENSE.txt", "image/LICENSE.txt"):
            (self.build / name).write_text("fixture license\n")

    def write_manifest(self):
        self.manifest.write_text(json.dumps(self.hashes))
        self.source_sha = digest(self.manifest)

    def verify(self):
        return PACKAGER.verified_build(self.base, self.source_sha, self.artifact_sha)

    def assert_refused_before_builder(self):
        output = self.base / "package"
        with mock.patch.object(PACKAGER.PUBLISHER, "checked") as builder:
            with self.assertRaises(ValueError):
                PACKAGER.package(self.base, output, self.source_sha, self.artifact_sha)
            builder.assert_not_called()
        self.assertFalse(output.exists())

    def test_complete_matching_build_is_accepted(self):
        self.assertEqual(self.verify(), self.build / "image")

    def test_failed_build_is_not_packaged(self):
        (self.evidence / "exit-code.txt").write_text("1\n")
        self.assert_refused_before_builder()

    def test_changed_source_manifest_is_not_packaged(self):
        self.manifest.write_text(self.manifest.read_text() + "\n")
        self.assert_refused_before_builder()

    def test_changed_reviewed_source_is_not_packaged(self):
        (self.source / "patches/oracle-operator/hakopod_free_health.go.txt").write_text("changed\n")
        self.assert_refused_before_builder()

    def test_incomplete_source_receipt_is_not_packaged(self):
        del self.hashes["patches/oracle-operator/hakopod_free_health.go.txt"]
        self.write_manifest()
        self.assert_refused_before_builder()

    def test_source_path_cannot_escape_inventory(self):
        self.hashes["../outside"] = "a" * 64
        self.write_manifest()
        self.assert_refused_before_builder()

    def test_changed_artifact_is_not_packaged(self):
        self.artifacts[0].write_text("changed binary\n")
        self.assert_refused_before_builder()

    def test_changed_artifact_receipt_is_not_packaged(self):
        self.checksums.write_text(self.checksums.read_text() + "\n")
        self.assert_refused_before_builder()

    def test_unexpected_artifact_is_not_packaged(self):
        unknown = self.build / "image/unexpected"
        unknown.write_text("extra\n")
        self.checksums.write_text(self.checksums.read_text() + digest(unknown) + "  " + str(unknown) + "\n")
        self.artifact_sha = digest(self.checksums)
        self.assert_refused_before_builder()

    def test_missing_artifact_inventory_is_not_packaged(self):
        self.checksums.write_text(self.checksums.read_text().splitlines()[0] + "\n")
        self.artifact_sha = digest(self.checksums)
        self.assert_refused_before_builder()

    def test_changed_dockerfile_is_not_packaged(self):
        (self.build / "image/Dockerfile").write_text("FROM unreviewed\n")
        self.assert_refused_before_builder()

    def test_changed_license_is_not_packaged(self):
        (self.build / "image/LICENSE.txt").write_text("changed license\n")
        self.assert_refused_before_builder()

    def test_artifact_symlink_is_not_packaged(self):
        target = self.base / "substitute"
        target.write_bytes(self.artifacts[0].read_bytes())
        self.artifacts[0].unlink()
        self.artifacts[0].symlink_to(target)
        self.assert_refused_before_builder()


if __name__ == "__main__":
    unittest.main()
