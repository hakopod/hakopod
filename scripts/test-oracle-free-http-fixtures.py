#!/usr/bin/env python3
import importlib.util
from pathlib import Path
import tempfile
import os
import subprocess
import unittest

P = Path(__file__).with_name("oracle-free-http-fixtures.py")
spec = importlib.util.spec_from_file_location("fixtures", P)
module = importlib.util.module_from_spec(spec); spec.loader.exec_module(module)

class FixtureTests(unittest.TestCase):
    def test_postgres_digest_normalizes_only_docker_hub_aliases(self):
        digest = "a"*64
        expected = "postgres@sha256:" + digest
        for value in ("postgres@sha256:"+digest, "library/postgres@sha256:"+digest,
                      "docker.io/library/postgres:17-alpine@sha256:"+digest):
            self.assertEqual(module.normalized_postgres_digest(value), expected)
        self.assertIsNone(module.normalized_postgres_digest("example.com/postgres@sha256:"+digest))
        self.assertIsNone(module.normalized_postgres_digest("postgres:latest"))

    def test_import_has_no_side_effect_and_shim_is_strict(self):
        with tempfile.TemporaryDirectory() as root:
            fixture = module.Fixtures(Path(root)/"out", Path(root)/"cache", Path("/bin/false"))
            self.assertFalse(fixture.state_path.exists())
            source = module._shim_source("/docker", "a"*32, "/cid", "/state", root)
            self.assertIn(module.S3_IMAGE, source)
            self.assertIn("--memory-swap=384m", source)
            self.assertIn("--security-opt=no-new-privileges", source)

    def test_cleanup_is_repeatable_without_started_resources(self):
        with tempfile.TemporaryDirectory() as root:
            fake = Path(root)/"docker"
            fake.write_text("#!/bin/sh\n[ \"$1\" = ps ] && exit 0\nexit 1\n"); os.chmod(fake, 0o700)
            fixture = module.Fixtures(Path(root)/"out", Path(root)/"cache", fake)
            fixture.output.mkdir()
            first = fixture.cleanup(); second = fixture.cleanup()
            self.assertEqual({item["component"] for item in first["removed"]}, {"postgres", "s3"})
            self.assertEqual({item["component"] for item in second["removed"]}, {"postgres", "s3"})
            self.assertTrue(second["credential_files_absent"])
            self.assertTrue(second["postgres_container_absent"])
            self.assertTrue(second["s3_container_absent"])

    def test_cleanup_accepts_only_recorded_absent_ids(self):
        with tempfile.TemporaryDirectory() as root:
            fake = Path(root)/"docker"
            fake.write_text("#!/bin/sh\n[ \"$1\" = ps ] && exit 0\nexit 1\n"); os.chmod(fake, 0o700)
            fixture = module.Fixtures(Path(root)/"out", Path(root)/"cache", fake)
            fixture.output.mkdir(); fixture.pg_cidfile.write_text("a"*64+"\n"); fixture.s3_cidfile.write_text("b"*64+"\n")
            os.chmod(fixture.pg_cidfile, 0o600); os.chmod(fixture.s3_cidfile, 0o600)
            receipt = fixture.cleanup()
            self.assertTrue(receipt["postgres_container_absent"])
            self.assertTrue(receipt["s3_container_absent"])
            self.assertEqual({item["container_id"] for item in receipt["removed"]}, {"a"*64, "b"*64})

    def test_shim_rejects_non_fixture_run(self):
        with tempfile.TemporaryDirectory() as root:
            shim = Path(root)/"docker"; shim.write_text(module._shim_source("/bin/false", "a"*32, str(Path(root)/"cid"), str(Path(root)/"state"), root)); os.chmod(shim, 0o700)
            result = subprocess.run([str(shim), "run", "alpine:latest"], capture_output=True)
            self.assertNotEqual(result.returncode, 0)
            duplicate = subprocess.run([str(shim), "run", "--name", "fixture", "--memory=384m", "--memory=2g", "--cpus=1", "--pids-limit=128", "-p", "127.0.0.1::9000", module.S3_IMAGE], capture_output=True)
            self.assertNotEqual(duplicate.returncode, 0)

    def test_cleanup_fails_closed_when_docker_is_unavailable(self):
        with tempfile.TemporaryDirectory() as root:
            fixture = module.Fixtures(Path(root)/"out", Path(root)/"cache", Path("/bin/false"))
            fixture.output.mkdir()
            with self.assertRaises(ValueError): fixture.cleanup()

    def test_lost_cidfile_discovers_only_nonce_owned_container(self):
        class Fake(module.Fixtures):
            def _run(self, args, **kwargs):
                if args[:3] == ["ps", "-aq", "--no-trunc"] and "--filter" in args:
                    return "a"*64 if "component=s3" in args[-1] else ""
                if args[:3] == ["ps", "-aq", "--no-trunc"]: return ""
                if args[0] == "rm": return ""
                raise AssertionError(args)
            def _inspect(self, cid, name, image, component):
                self.assert_identity = (cid, name, image, component)
                return {"Id": cid}
        with tempfile.TemporaryDirectory() as root:
            fixture = Fake(Path(root)/"out", Path(root)/"cache", Path("/docker")); fixture.output.mkdir()
            fixture.s3_state.write_text('{"nonce":"%s","image":"%s","name":"owned-s3"}' % (fixture.nonce, module.S3_IMAGE)); os.chmod(fixture.s3_state, 0o600)
            receipt = fixture.cleanup()
            self.assertTrue(receipt["s3_container_absent"])
            self.assertEqual(fixture.assert_identity[:2], ("a"*64, "owned-s3"))

    def test_malformed_cid_and_state_are_rejected(self):
        with tempfile.TemporaryDirectory() as root:
            fixture = module.Fixtures(Path(root)/"out", Path(root)/"cache", Path("/bin/false")); fixture.output.mkdir()
            fixture.pg_cidfile.write_text("short\n"); os.chmod(fixture.pg_cidfile, 0o600)
            with self.assertRaises(ValueError): fixture.cleanup()
            fixture.pg_cidfile.unlink(); fixture.s3_state.write_text('{"nonce":"wrong","image":"wrong","name":"x"}')
            with self.assertRaises(ValueError): fixture.cleanup()

    def test_source_contract_bounds_readiness_and_hides_password(self):
        source = module.Path(module.__file__).read_text()
        self.assertIn("POSTGRES_PASSWORD_FILE=/run/secrets/postgres_password", source)
        self.assertNotIn('"POSTGRES_PASSWORD=" + password', source)
        self.assertIn('timeout=5', source)
        shim = module._shim_source("/docker", "a"*32, "/cid", "/state", "/safe/root")
        self.assertIn("os.path.commonpath", shim)
        self.assertIn("refusing Docker access outside owned S3 fixture", shim)

    def test_shim_accepts_exact_run_and_rejects_lexical_escape(self):
        with tempfile.TemporaryDirectory() as root:
            root = Path(root); safe = root/"safe"; safe.mkdir(mode=0o700)
            config = safe/"s3.json"; config.write_text("{}"); os.chmod(config, 0o600)
            data = safe/"data"; data.mkdir(mode=0o700)
            marker = root/"called"
            real = root/"real-docker"; real.write_text("#!/bin/sh\ntouch %s\nexit 0\n" % marker); os.chmod(real, 0o700)
            shim = root/"docker"; state = root/"state"; cid = root/"cid"
            shim.write_text(module._shim_source(str(real), "a"*32, str(cid), str(state), str(safe))); os.chmod(shim, 0o700)
            rid = "b"*32
            args = ["run","-d","--name","hakopod-backup-smoke-"+rid[:10],"--label","com.hakopod.test=backups","--label","com.hakopod.test-run="+rid,"--user",str(os.getuid())+":"+str(os.getgid()),"--entrypoint","/usr/bin/weed","--memory=384m","--cpus=1","--pids-limit=128","-p","127.0.0.1::9000","-e","GODEBUG=fips140=on","-e","GOMEMLIMIT=256MiB","--mount","type=bind,source="+str(config)+",target=/etc/seaweedfs/s3.json,readonly","--mount","type=bind,source="+str(data)+",target=/data",module.S3_IMAGE,"-logtostderr=true","server","-s3","-s3.port=9000","-s3.config=/etc/seaweedfs/s3.json","-dir=/data","-master.volumePreallocate","-master.volumeSizeLimitMB=8","-volume.max=16","-ip=127.0.0.1","-ip.bind=0.0.0.0"]
            self.assertEqual(subprocess.run([str(shim), *args]).returncode, 0)
            self.assertTrue(marker.exists()); marker.unlink(); state.unlink()
            outside = root/"outside"; outside.mkdir(mode=0o700)
            args[22] = "type=bind,source="+str(outside)+",target=/etc/seaweedfs/s3.json,readonly"
            self.assertNotEqual(subprocess.run([str(shim), *args], capture_output=True).returncode, 0)
            self.assertFalse(marker.exists())

if __name__ == "__main__": unittest.main()
