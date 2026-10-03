import copy
import hashlib
import importlib.util
import io
import json
from pathlib import Path
import tarfile
import tempfile
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location("runtime_publisher", Path(__file__).with_name("publish-managed-runtime.py"))
publisher = importlib.util.module_from_spec(spec)
spec.loader.exec_module(publisher)


class PublicationTests(unittest.TestCase):
    def fixture(self, root, *, mutate_config=None, mutate_entries=None):
        config = {"os": "linux", "architecture": "amd64", "config": {"User": "10001:10001", "Labels": {"org.opencontainers.image.source": publisher.SOURCE}}}
        if mutate_config:
            mutate_config(config)
        blobs = {}
        def blob(data, media_type):
            digest = hashlib.sha256(data).hexdigest()
            blobs["blobs/sha256/" + digest] = data
            return {"digest": "sha256:" + digest, "size": len(data), "mediaType": media_type}
        manifest = {"schemaVersion": 2, "config": blob(json.dumps(config).encode(), "application/vnd.oci.image.config.v1+json"), "layers": [blob(b"test layer", "application/vnd.oci.image.layer.v1.tar+gzip")], "mediaType": "application/vnd.oci.image.manifest.v1+json"}
        descriptor = blob(json.dumps(manifest).encode(), manifest["mediaType"])
        entries = [("oci-layout", json.dumps({"imageLayoutVersion": "1.0.0"}).encode()), ("index.json", json.dumps({"schemaVersion": 2, "manifests": [descriptor]}).encode()), *blobs.items()]
        if mutate_entries:
            mutate_entries(entries)
        archive = root / "vitess-runtime.oci.tar"
        with tarfile.open(archive, "w") as output:
            for name, body in entries:
                member = tarfile.TarInfo(name)
                if body is None:
                    member.type = tarfile.SYMTYPE
                    member.linkname = "/tmp/foreign"
                    output.addfile(member)
                else:
                    member.size = len(body)
                    output.addfile(member, io.BytesIO(body))
        image = {"archive": archive.name, "archive_sha256": hashlib.sha256(archive.read_bytes()).hexdigest(), "image_digest": descriptor["digest"], "repository": "ghcr.io/hakopod/managed-vitess", "platform": "linux/amd64", "config_user": "10001:10001", "labels": {"org.opencontainers.image.source": publisher.SOURCE}}
        return archive, image

    def test_valid_archive_and_plan(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            archive, image = self.fixture(root)
            self.assertGreater(publisher.inspect_archive(archive, image, root / "layout"), 0)
            plan = root / "plan.json"
            plan.write_text(json.dumps({"schema_version": 1, "images": [image]}))
            self.assertEqual(publisher.plan(plan, hashlib.sha256(plan.read_bytes()).hexdigest()), [image])

    def test_plan_rejects_unknown_duplicate_or_untrusted_fields(self):
        mutations = [
            lambda p: p.update(schema_version=True),
            lambda p: p.update(extra=True),
            lambda p: p["images"].append(copy.deepcopy(p["images"][0])),
            lambda p: p["images"][0].update(archive="../escape.oci.tar"),
            lambda p: p["images"][0].update(repository="ghcr.io/other/neon-storage"),
            lambda p: p["images"][0].update(platform="linux/arm64"),
            lambda p: p["images"][0].update(labels={"org.opencontainers.image.source": "https://example.test"}),
        ]
        for mutate in mutations:
            with self.subTest(mutate=mutate), tempfile.TemporaryDirectory() as directory:
                root = Path(directory)
                _, image = self.fixture(root)
                data = {"schema_version": 1, "images": [image]}
                mutate(data)
                path = root / "plan.json"
                path.write_text(json.dumps(data))
                with self.assertRaises(ValueError):
                    publisher.plan(path, hashlib.sha256(path.read_bytes()).hexdigest())

    def test_supporting_platform_images_use_only_reviewed_repositories(self):
        for name in ("managed-neon-compute-tls", "managed-neon-controller-database", "managed-supabase-pooler", "managed-supabase-realtime"):
            with self.subTest(name=name), tempfile.TemporaryDirectory() as directory:
                root = Path(directory)
                _, image = self.fixture(root)
                image["repository"] = "ghcr.io/hakopod/" + name
                path = root / "plan.json"
                path.write_text(json.dumps({"schema_version": 1, "images": [image]}))
                self.assertEqual(publisher.plan(path, hashlib.sha256(path.read_bytes()).hexdigest()), [image])
        for repository in ("ghcr.io/other/managed-supabase-pooler", "ghcr.io/hakopod/managed-supabase", "ghcr.io/hakopod/managed-neon-anything", "ghcr.io/hakopod/managed-supabase-pooler/extra", "ghcr.io/hakopod/managed-supabase-pooler:latest"):
            with self.subTest(repository=repository):
                self.assertIsNone(publisher.REPOSITORY.fullmatch(repository))

    def test_rejects_archive_checksum_and_image_digest_changes(self):
        for field, value in [("archive_sha256", "a" * 64), ("image_digest", "sha256:" + "b" * 64)]:
            with self.subTest(field=field), tempfile.TemporaryDirectory() as directory:
                root = Path(directory)
                archive, image = self.fixture(root)
                image[field] = value
                with self.assertRaises(ValueError):
                    publisher.inspect_archive(archive, image, root / "layout")

    def test_refuses_unsafe_or_incomplete_archives(self):
        mutations = [
            lambda e: e.append(("../escape", b"bad")),
            lambda e: e.append(("link", None)),
            lambda e: e.append(e[0]),
            lambda e: e.pop(),
            lambda e: e.append(("blobs/sha256/" + "a" * 64, b"wrong hash")),
        ]
        for mutate in mutations:
            with self.subTest(mutate=mutate), tempfile.TemporaryDirectory() as directory:
                root = Path(directory)
                archive, image = self.fixture(root, mutate_entries=mutate)
                with self.assertRaises(ValueError):
                    publisher.inspect_archive(archive, image, root / "layout")
                self.assertFalse((root.parent / "escape").exists())

    def test_refuses_platform_user_and_source_mismatch(self):
        mutations = [lambda c: c.update(os="windows"), lambda c: c.update(architecture="arm64"), lambda c: c["config"].update(User="0"), lambda c: c["config"].update(Labels={}), lambda c: c.update(config=[]), lambda c: c["config"].update(Labels=[])]
        for mutate in mutations:
            with self.subTest(mutate=mutate), tempfile.TemporaryDirectory() as directory:
                root = Path(directory)
                archive, image = self.fixture(root, mutate_config=mutate)
                with self.assertRaises(ValueError):
                    publisher.inspect_archive(archive, image, root / "layout")

    def test_publication_does_not_replace_existing_or_unverifiable_tag(self):
        with tempfile.TemporaryDirectory() as directory:
            _, image = self.fixture(Path(directory))
            for result in [(0, "sha256:" + "a" * 64, ""), (1, "", "DENIED")]:
                with self.subTest(result=result), patch.object(publisher, "checked", side_effect=["", image["image_digest"], "sha256:" + "a" * 64]) as checked, patch.object(publisher, "run", return_value=result):
                    with self.assertRaises(ValueError):
                        publisher.publish("crane", image, Path(directory))
                    self.assertFalse(any("tag" in call.args[0] for call in checked.call_args_list))

    def test_publication_tags_only_the_confirmed_missing_manifest(self):
        with tempfile.TemporaryDirectory() as directory:
            _, image = self.fixture(Path(directory))
            tag = 'sha256-' + image['image_digest'][7:]
            reference = image['repository'] + ':' + tag
            url = 'https://ghcr.io/v2/hakopod/managed-vitess/manifests/' + tag
            error = 'Error: fetching manifest ' + reference + ': GET ' + url + ': MANIFEST_UNKNOWN: manifest unknown\n'
            with patch.object(publisher, 'run', return_value=(1, '', error)) as lookup, patch.object(publisher, 'checked', side_effect=['', image['image_digest'], '', image['image_digest']]) as checked:
                self.assertEqual(publisher.publish('crane', image, Path(directory)), image['repository'] + '@' + image['image_digest'])
                self.assertEqual(lookup.call_args.args[0], ['crane', 'manifest', reference])
                self.assertEqual([call.args[0][1] for call in checked.call_args_list], ['push', 'digest', 'tag', 'digest'])

    def test_command_output_is_bounded(self):
        with self.assertRaisesRegex(ValueError, "output exceeded"):
            publisher.run(["python3", "-c", "print('x' * 300000)"], timeout=10)

    def test_command_duration_is_bounded(self):
        with self.assertRaisesRegex(ValueError, "timed out"):
            publisher.run(["python3", "-c", "import time; time.sleep(5)"], timeout=0.1)


if __name__ == "__main__":
    unittest.main()
