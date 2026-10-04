#!/usr/bin/env python3
"""Keep managed runtime qualification fail-closed in the release workflow."""
from pathlib import Path
import re
import unittest

ROOT=Path(__file__).resolve().parent.parent

class ReleaseWorkflowQualificationTest(unittest.TestCase):
    def test_publish_requires_exact_managed_runtime_verifiers(self):
        workflow=(ROOT/'.github/workflows/release.yml').read_text()
        self.assertIn('needs: [build, smoke, hosts, probe-index, vitess, supabase, neon]',workflow)
        for name in ('vitess','supabase','neon'):
            with self.subTest(name=name):
                match=re.search(rf'(?ms)^  {name}:\n(?P<body>.*?)(?=^  [a-z0-9-]+:\n|\Z)',workflow)
                self.assertIsNotNone(match)
                job=match.group('body')
                checkout=job.index('uses: actions/checkout@')
                templates=job.index('run: git submodule update --init templates')
                verifier=job.index(f'python3 release/verify-{name}-runtime.py --output .local/{name}-qualified')
                archive=job.index(f'-C .local {name}-qualified')
                upload=job.index('uses: actions/upload-artifact@')
                self.assertLess(checkout,templates)
                self.assertLess(templates,verifier)
                self.assertLess(verifier,archive)
                self.assertLess(archive,upload)
                self.assertGreaterEqual(workflow.count(f'name: managed-{name}-qualification'),2)
                self.assertIn(f'path: .local/managed-{name}-qualification.tar.gz',job)

if __name__=='__main__': unittest.main()
