#!/usr/bin/env python3
"""Keep the manual npm recovery run narrow and verified."""
from pathlib import Path
import re
import unittest

ROOT = Path(__file__).resolve().parent.parent


def job(workflow, name):
    match = re.search(rf'(?ms)^  {re.escape(name)}:\n(?P<body>.*?)(?=^  [a-z0-9-]+:\n|\Z)', workflow)
    assert match, name
    return match.group('body')


class NpmCliRecoveryTest(unittest.TestCase):
    def test_recovery_only_republishes_verified_release_archives(self):
        workflow = (ROOT / '.github/workflows/release.yml').read_text()
        self.assertIn("    if: github.event_name == 'push'\n", job(workflow, 'build'))
        recovery = job(workflow, 'npm-cli-recovery')
        self.assertIn("if: github.event_name == 'workflow_dispatch'", recovery)
        self.assertNotIn('needs:', recovery)
        self.assertIn('environment: npm', recovery)
        self.assertIn('id-token: write', recovery)
        self.assertNotIn('contents: write', recovery)
        verify = recovery.index('gh attestation verify')
        self.assertLess(recovery.index('gh release download'), verify)
        self.assertLess(verify, recovery.index('release/publish-npm-cli.sh'))
        self.assertIn('release/publish-npm-cli.sh', job(workflow, 'npm-cli'))
        script = (ROOT / 'release/publish-npm-cli.sh').read_text()
        self.assertIn('sha256sum --check --strict', script)
        self.assertLess(script.index('sha256sum --check'), script.index('npm publish'))
        self.assertIn('already published', script)


if __name__ == '__main__':
    unittest.main()
