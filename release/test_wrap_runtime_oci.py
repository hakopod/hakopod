import hashlib
import importlib.util
import io
import json
from pathlib import Path
import tarfile
import tempfile
import unittest

SCRIPT = Path(__file__).with_name("wrap-runtime-oci.py")
SPEC = importlib.util.spec_from_file_location("wrap_runtime_oci", SCRIPT)
WRAP = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(WRAP)


def encoded(value):
    return json.dumps(value, sort_keys=True, separators=(",", ":")).encode()


def descriptor(value, media_type):
    return {"mediaType": media_type, "digest": "sha256:" + hashlib.sha256(value).hexdigest(), "size": len(value)}


def fixture(path, corrupt=False, duplicate_layer=False):
    layer = b"layer"
    config = encoded({"architecture": "amd64", "os": "linux", "config": {"User": "app", "Labels": {}}})
    layer_desc = descriptor(layer, "application/vnd.oci.image.layer.v1.tar")
    config_desc = descriptor(config, "application/vnd.oci.image.config.v1+json")
    manifest = encoded({"schemaVersion": 2, "mediaType": "application/vnd.oci.image.manifest.v1+json", "config": config_desc, "layers": [layer_desc, layer_desc] if duplicate_layer else [layer_desc]})
    manifest_desc = descriptor(manifest, "application/vnd.oci.image.manifest.v1+json")
    index = encoded({"schemaVersion": 2, "manifests": [manifest_desc]})
    values = {"oci-layout": encoded({"imageLayoutVersion": "1.0.0"}), "index.json": index,
              "blobs/sha256/" + manifest_desc["digest"][7:]: (manifest[:-1] + b"x" if corrupt else manifest),
              "blobs/sha256/" + config_desc["digest"][7:]: config,
              "blobs/sha256/" + layer_desc["digest"][7:]: layer}
    with tarfile.open(path, "w") as archive:
        for name, value in values.items():
            member = tarfile.TarInfo(name); member.size = len(value)
            archive.addfile(member, io.BytesIO(value))


class Tests(unittest.TestCase):
    def test_deterministic_numeric_wrapper(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory); source = root / "source.tar"; first = root / "first.tar"; second = root / "second.tar"
            fixture(source)
            labels = {"org.opencontainers.image.source": WRAP.SOURCE}
            first_identity = WRAP.wrap(source, first, "1000:1000", labels)
            second_identity = WRAP.wrap(source, second, "1000:1000", labels)
            self.assertEqual(first_identity, second_identity)
            self.assertEqual(first.read_bytes(), second.read_bytes())
            with tarfile.open(first) as archive:
                index = json.load(archive.extractfile("index.json")); manifest_digest = index["manifests"][0]["digest"]
                manifest = json.load(archive.extractfile("blobs/sha256/" + manifest_digest[7:])); config_digest = manifest["config"]["digest"]
                config = json.load(archive.extractfile("blobs/sha256/" + config_digest[7:]))
            self.assertEqual(config["config"]["User"], "1000:1000")
            self.assertEqual(config["config"]["Labels"]["org.opencontainers.image.source"], WRAP.SOURCE)

    def test_preserves_repeated_layer_descriptors_with_one_blob(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory); source = root / "source.tar"; output = root / "output.tar"
            fixture(source, duplicate_layer=True)
            WRAP.wrap(source, output, "1000:1000", {"org.opencontainers.image.source": WRAP.SOURCE})
            with tarfile.open(output) as archive:
                index = json.load(archive.extractfile("index.json")); digest = index["manifests"][0]["digest"]
                manifest = json.load(archive.extractfile("blobs/sha256/" + digest[7:]))
                layer_name = "blobs/sha256/" + manifest["layers"][0]["digest"][7:]
                self.assertEqual(len(manifest["layers"]), 2)
                self.assertEqual([member.name for member in archive.getmembers()].count(layer_name), 1)

    def test_rejects_tampered_manifest_and_preserves_existing_output(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory); source = root / "source.tar"; output = root / "output.tar"
            fixture(source, corrupt=True)
            with self.assertRaisesRegex(ValueError, "checksum mismatch"):
                WRAP.wrap(source, output, "1000:1000", {"org.opencontainers.image.source": WRAP.SOURCE})
            self.assertFalse(output.exists())
            output.write_bytes(b"preserve")
            with self.assertRaisesRegex(ValueError, "fresh"):
                WRAP.wrap(source, output, "1000:1000", {"org.opencontainers.image.source": WRAP.SOURCE})
            self.assertEqual(output.read_bytes(), b"preserve")


if __name__ == "__main__":
    unittest.main()
