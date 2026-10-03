#!/usr/bin/env python3
"""Check that native SQL probes use each platform's own public issuer."""
from datetime import datetime, timedelta, timezone
import copy
import hashlib
import importlib.util
import os
from pathlib import Path
import ssl
import subprocess
import tempfile
import unittest
from unittest import mock

ROOT = Path(__file__).resolve().parent.parent
SPEC = importlib.util.spec_from_file_location("neon_driver", ROOT / "examples/neon-native-acceptance/driver.py")
DRIVER = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(DRIVER)


class TrustTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.certificates = tempfile.TemporaryDirectory()
        cls.trusts = {}
        for pid in ("a" * 32, "b" * 32):
            root = Path(cls.certificates.name)
            certificate, key = root / (pid + ".crt"), root / (pid + ".key")
            subprocess.run([
                "openssl", "req", "-x509", "-newkey", "ec", "-pkeyopt", "ec_paramgen_curve:prime256v1",
                "-nodes", "-keyout", str(key), "-out", str(certificate), "-days", "1",
                "-subj", "/CN=Hakopod platform " + pid + "/OU=managed-platform-" + pid,
                "-addext", "basicConstraints=critical,CA:TRUE",
            ], check=True, timeout=15, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
            key.chmod(0o600)
            pem = certificate.read_text()
            now = datetime.now(timezone.utc)
            cls.trusts[pid] = {
                "certificate_pem": pem,
                "fingerprint": hashlib.sha256(ssl.PEM_cert_to_DER_cert(pem)).hexdigest(),
                "issuer": "CN=Hakopod platform " + pid + ",OU=managed-platform-" + pid,
                "not_before": (now - timedelta(minutes=1)).isoformat(),
                "expires_at": (now + timedelta(hours=1)).isoformat(),
            }

    @classmethod
    def tearDownClass(cls):
        cls.certificates.cleanup()

    def fixture(self, directory):
        driver = DRIVER.Driver.__new__(DRIVER.Driver)
        driver.root = Path(directory)
        driver.bound = {pid: ("namespace-" + pid, "operation-" + pid) for pid in self.trusts}
        driver.a = mock.Mock(local_port=15433)
        driver.api = mock.Mock(side_effect=lambda method, path: copy.deepcopy(self.trusts[path.split("/")[-2]]))
        return driver

    def test_separate_platforms_fetch_separate_issuers_and_cache_only_their_own_file(self):
        with tempfile.TemporaryDirectory() as directory:
            driver = self.fixture(directory)
            source, target = "a" * 32, "b" * 32
            first, second = driver.platform_ca(source), driver.platform_ca(target)
            self.assertNotEqual(first, second)
            self.assertNotEqual(first.read_bytes(), second.read_bytes())
            self.assertEqual(first.stat().st_mode & 0o777, 0o600)
            self.assertEqual(driver.platform_ca(source), first)
            self.assertEqual(driver.api.call_args_list, [
                mock.call("GET", "/api/v1/managed-platforms/" + source + "/trust"),
                mock.call("GET", "/api/v1/managed-platforms/" + target + "/trust"),
            ])
            with mock.patch.dict(os.environ, {"PGSSLROOTCERT": str(first), "PGSSLMODE": "disable", "PGSERVICE": "untrusted", "PSQLRC": "/tmp/untrusted"}):
                environment = driver.proxy_environment(target, "protected-fixture-password")
            self.assertEqual(environment["PGSSLROOTCERT"], str(second))
            self.assertEqual(environment["PGSSLMODE"], "verify-full")
            self.assertEqual(environment["PGHOST"], "neon-proxy")
            self.assertEqual(environment["PGOPTIONS"], "endpoint=" + target)
            self.assertNotIn("PGSERVICE", environment)
            self.assertNotIn("PSQLRC", environment)

    def test_unbound_or_malformed_platform_never_reaches_trust_api(self):
        with tempfile.TemporaryDirectory() as directory:
            driver = self.fixture(directory)
            for pid in ("c" * 32, "../other", None):
                with self.subTest(pid=pid), self.assertRaisesRegex(RuntimeError, "not bound"):
                    driver.platform_ca(pid)
            driver.api.assert_not_called()

    def test_rejects_changed_issuer_digest_extra_material_and_invalid_validity(self):
        source = "a" * 32
        mutations = (
            lambda value: value.update(fingerprint="0" * 64),
            lambda value: value.update(issuer="CN=Hakopod platform " + "b" * 32),
            lambda value: value.update(certificate_pem=value["certificate_pem"] * 2),
            lambda value: value.update(certificate_pem="invalid"),
            lambda value: value.update(private_key_pem="must-not-be-returned"),
            lambda value: value.update(expires_at="2000-01-01T00:00:00Z"),
            lambda value: value.update(not_before="2999-01-01T00:00:00Z"),
            lambda value: value.update(expires_at="2999-01-01T00:00:00"),
            lambda value: value.update(not_before=None),
        )
        for mutate in mutations:
            with tempfile.TemporaryDirectory() as directory:
                driver = self.fixture(directory)
                value = copy.deepcopy(self.trusts[source])
                mutate(value)
                driver.api = mock.Mock(return_value=value)
                with self.subTest(mutate=mutate), self.assertRaises(RuntimeError):
                    driver.platform_ca(source)
                self.assertEqual(list(Path(directory).iterdir()), [])

    def test_modified_or_symbolic_cached_trust_is_refused(self):
        for symbolic in (False, True):
            with tempfile.TemporaryDirectory() as directory:
                driver = self.fixture(directory)
                path = driver.platform_ca("a" * 32)
                if symbolic:
                    path.unlink()
                    path.symlink_to(Path(self.certificates.name) / ("b" * 32 + ".crt"))
                else:
                    path.write_text(self.trusts["b" * 32]["certificate_pem"])
                with self.subTest(symbolic=symbolic), self.assertRaises(RuntimeError):
                    driver.platform_ca("a" * 32)
                self.assertEqual(driver.api.call_count, 1)


if __name__ == "__main__":
    unittest.main()
