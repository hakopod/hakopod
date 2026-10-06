#!/usr/bin/env python3
"""Keep managed runtime qualification fail-closed in the release workflow."""
from pathlib import Path
import re
import unittest

ROOT=Path(__file__).resolve().parent.parent

class ReleaseWorkflowQualificationTest(unittest.TestCase):
    def test_publish_requires_source_driven_managed_runtime_evidence(self):
        workflow=(ROOT/'.github/workflows/release.yml').read_text()
        self.assertIn('needs: [build, smoke, hosts, probe-index, vitess, supabase, neon, oracle-free]',workflow)
        for name in ('vitess','supabase','neon','oracle-free'):
            with self.subTest(name=name):
                match=re.search(rf'(?ms)^  {name}:\n(?P<body>.*?)(?=^  [a-z0-9-]+:\n|\Z)',workflow)
                self.assertIsNotNone(match)
                job=match.group('body')
                checkout=job.index('uses: actions/checkout@')
                templates=job.index('run: git submodule update --init templates')
                verifier=job.index(f'python3 -B release/managed-runtime-availability.py prepare {name} --output .local/managed-{name}-release-evidence')
                upload=job.index('uses: actions/upload-artifact@')
                self.assertLess(checkout,templates)
                self.assertLess(templates,verifier)
                self.assertLess(verifier,upload)
                self.assertGreaterEqual(workflow.count(f'name: managed-{name}-release-evidence'),2)
                self.assertIn(f'path: .local/managed-{name}-release-evidence/*',job)
                self.assertNotIn('if:',job)
                self.assertNotIn('continue-on-error:',job)
        self.assertEqual(workflow.count('release/managed-runtime-availability.py verify '),2)
        publish=workflow.index('gh release create')
        self.assertLess(workflow.rindex('release/managed-runtime-availability.py verify '),publish)

if __name__=='__main__': unittest.main()
