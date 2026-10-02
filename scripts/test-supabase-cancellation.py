#!/usr/bin/env python3
"""Behavioral tests for the Supabase recovery-cancellation helper."""
import os
from pathlib import Path
import subprocess
import tempfile
import unittest

ROOT = Path(__file__).resolve().parent.parent
HELPER = ROOT / "examples/supabase-native-acceptance/cancel-recovery.sh"
PINNED_JQ = Path("/srv/hakopod-backup-scratch/supavisor-admin-secret-source-v1/native-prep-v1/tools/jq-1.8.2-linux-amd64")
PLATFORM = "a" * 32
OPERATION = "b" * 32

KUBECTL = r'''#!/bin/sh
case " $* " in
  *" config current-context "*) [ "$MOCK_SCENARIO" = wrong-context ] && echo foreign || echo k3d-hakopod-dev ;;
  *" get namespace "*)
    uid=uid-source; owner=owner; [ "$MOCK_SCENARIO" = wrong-uid ] && uid=foreign
    [ "$MOCK_SCENARIO" = wrong-owner ] && owner=foreign
    printf '{"metadata":{"uid":"%s","deletionTimestamp":null,"labels":{"app.kubernetes.io/managed-by":"hakopod","hakopod.io/managed-platform-id":"%s","hakopod.io/owner-operation-id":"%s","hakopod.io/resource-intent-id":"intent"}}}\n' "$uid" "$HAKOPOD_ACCEPTANCE_PLATFORM_ID" "$owner" ;;
  *) exit 91 ;;
esac
'''

CURL = r'''#!/bin/sh
url=
for argument do case "$argument" in http://*) url=$argument;; esac; done
[ "$MOCK_SCENARIO" = api-timeout ] && { sleep 1; exit 28; }
case "$url" in
  */api/v1/managed-platforms/*)
    printf '{"id":"%s","project":"project","environment":"environment","revision":3,"spec":{"kind":"supabase"},"status":"ready","observation":{"ready":true},"reserved_cpu_milli":1,"reserved_memory_bytes":2,"reserved_storage_gib":3}\n' "$HAKOPOD_ACCEPTANCE_PLATFORM_ID" ;;
  */api/v1/managed-platform-recovery/reviews) echo '{"id":"review-id"}' ;;
  */api/v1/managed-platform-recovery/operations)
    printf '{"id":"%s","kind":"backup","source_platform_id":"%s","status":"queued"}\n' "''' + OPERATION + r'''" "$HAKOPOD_ACCEPTANCE_PLATFORM_ID" ;;
  */cancel) echo '{"cancel_requested":true}' ;;
  */api/v1/managed-platform-recovery-operations/*)
    status=cancelled; artifact=''
    [ "$MOCK_SCENARIO" = succeeded ] && status=succeeded
    [ "$MOCK_SCENARIO" = artifact ] && artifact=',"result_artifact_id":"cccccccccccccccccccccccccccccccc"'
    printf '{"id":"%s","kind":"backup","source_platform_id":"%s","status":"%s","cancel_requested":true%s}\n' "''' + OPERATION + r'''" "$HAKOPOD_ACCEPTANCE_PLATFORM_ID" "$status" "$artifact" ;;
  *) exit 92 ;;
esac
'''


class CancellationAcceptanceTest(unittest.TestCase):
    def setUp(self):
        if not PINNED_JQ.is_file():
            self.skipTest("pinned jq is unavailable")
        self.temp = tempfile.TemporaryDirectory()
        self.directory = Path(self.temp.name)
        bindir = self.directory / "bin"; bindir.mkdir()
        for name, source in (("kubectl", KUBECTL), ("curl", CURL)):
            path = bindir / name; path.write_text(source); path.chmod(0o755)
        (bindir / "jq").symlink_to(PINNED_JQ)
        self.headers = self.directory / "headers"
        self.headers.write_text("authorization: Bearer fixture-secret\n"); self.headers.chmod(0o600)
        self.kubeconfig = self.directory / "kubeconfig"; self.kubeconfig.write_text("fixture\n")
        self.environment = dict(os.environ, PATH=str(bindir) + ":/usr/bin:/bin", MOCK_SCENARIO="cancelled",
            KUBECONFIG=str(self.kubeconfig), HAKOPOD_ACCEPTANCE_API_URL="http://mock",
            HAKOPOD_ACCEPTANCE_API_HEADERS=str(self.headers), HAKOPOD_ACCEPTANCE_PROJECT="project",
            HAKOPOD_ACCEPTANCE_ENVIRONMENT="environment", HAKOPOD_ACCEPTANCE_PLATFORM_ID=PLATFORM,
            HAKOPOD_ACCEPTANCE_NAMESPACE_UID="uid-source", HAKOPOD_ACCEPTANCE_RECOVERY_DESTINATION_ID="d" * 32,
            HAKOPOD_ACCEPTANCE_OWNER_OPERATION_ID="owner", HAKOPOD_ACCEPTANCE_RESOURCE_INTENT_ID="intent",
            HAKOPOD_ACCEPTANCE_RECOVERY_DESTINATION_REVISION="1")

    def tearDown(self):
        self.temp.cleanup()

    def run_helper(self, scenario):
        environment = dict(self.environment, MOCK_SCENARIO=scenario)
        return subprocess.run([str(HELPER)], env=environment, cwd=self.directory, capture_output=True,
                              text=True, timeout=15)

    def test_cancelled_operation_passes_without_credentials(self):
        result = self.run_helper("cancelled")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn('"artifact_absent":true', result.stdout)
        self.assertNotIn("fixture-secret", result.stdout + result.stderr)

    def test_race_to_succeeded_fails_honestly(self):
        result = self.run_helper("succeeded")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("completed before cancellation", result.stderr)

    def test_namespace_identity_fences_fail_closed(self):
        for scenario in ("wrong-context", "wrong-uid", "wrong-owner"):
            with self.subTest(scenario=scenario):
                self.assertNotEqual(self.run_helper(scenario).returncode, 0)

    def test_terminal_artifact_is_rejected(self):
        result = self.run_helper("artifact")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("published or retained", result.stderr)

    def test_api_failure_is_bounded_and_silent(self):
        result = self.run_helper("api-timeout")
        self.assertNotEqual(result.returncode, 0)
        self.assertNotIn("fixture-secret", result.stdout + result.stderr)


if __name__ == "__main__":
    unittest.main()
