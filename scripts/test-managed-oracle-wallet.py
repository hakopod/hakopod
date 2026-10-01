#!/usr/bin/env python3
"""Exercise the wallet process boundary with synthetic native tool prompts."""

import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

HELPER = Path(__file__).resolve().parents[1] / "internal/cluster/database_oracle_wallet.py"
PASSWORD, VALUE = "a" * 64, "b" * 64


class WalletBoundary(unittest.TestCase):
    def run_helper(self, operation, mode="normal"):
        with tempfile.TemporaryDirectory(prefix="oracle-wallet-boundary-") as directory:
            tool = Path(directory) / "orapki"
            tool.write_text("""#!/usr/bin/env python3
import os, sys
password, value = 'a'*64, 'b'*64
tty_in = open('/dev/tty', 'r')
tty_out = open('/dev/tty', 'w')
def prompt(text):
    tty_out.write(text)
    tty_out.flush()
    return tty_in.readline().rstrip('\\r\\n')
if any(password in arg or value in arg for arg in sys.argv):
    sys.exit(9)
if os.environ['TEST_PROMPTS'] == 'unknown':
    prompt('Enter password: ')
elif os.environ['TEST_PROMPTS'] == 'excess':
    print('a'*70000, flush=True)
else:
    if prompt('Enter wallet password: ') != password: sys.exit(8)
    if '-createCredential' in sys.argv or '-createEntry' in sys.argv:
        if prompt('Enter secret/password: ') != value: sys.exit(7)
    else:
        if prompt('Re-enter password: ') != password: sys.exit(6)
# Simulate a native tool accidentally echoing secret input.
print(password, value)
""")
            tool.chmod(0o700)
            (Path(directory) / "mkstore").symlink_to(tool)
            arguments = {"create": [], "import": ["/opt/oracle/test/identity.p12"], "trust": ["/etc/hakopod-oracle-tls/ca.crt"], "credential": ["member-one", "sys"], "entry": ["oracle.dbsecurity.sysPassword"]}[operation]
            request = {"operation": operation, "wallet": "/opt/oracle/test-wallet", "password": PASSWORD, "value": VALUE if operation in ("credential", "entry") else "", "arguments": arguments}
            result = subprocess.run([sys.executable, str(HELPER)], input=json.dumps(request).encode(), env={**os.environ, "PATH": directory + os.pathsep + os.environ["PATH"], "TEST_PROMPTS": mode}, capture_output=True, timeout=10)
            self.assertNotIn(PASSWORD.encode(), result.stdout + result.stderr)
            self.assertNotIn(VALUE.encode(), result.stdout + result.stderr)
            return result

    def test_native_prompts_keep_credentials_out_of_argv_and_output(self):
        for operation in ("create", "import", "trust", "credential", "entry"):
            with self.subTest(operation=operation):
                result = self.run_helper(operation)
                self.assertEqual(result.returncode, 0, result.stderr.decode())

    def test_ambiguous_credential_prompt_refuses_to_guess(self):
        self.assertNotEqual(self.run_helper("credential", "unknown").returncode, 0)

    def test_native_output_is_bounded(self):
        self.assertNotEqual(self.run_helper("create", "excess").returncode, 0)


if __name__ == "__main__":
    unittest.main()
