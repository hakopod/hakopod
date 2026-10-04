#!/usr/bin/env python3
"""Keep controller packaging tied to compiled managed-runtime release gates."""
from pathlib import Path
import unittest


ROOT = Path(__file__).resolve().parent.parent


class ReleaseScopeTest(unittest.TestCase):
    def test_vitess_release_evidence_remains_a_publication_dependency(self):
        workflow = (ROOT / '.github/workflows/release.yml').read_text()
        self.assertIn('\n  vitess:\n', workflow)
        self.assertIn('managed-vitess-release-evidence', workflow)
        self.assertIn('probe-index, vitess, supabase, neon', workflow)

    def test_installer_derives_vitess_from_the_shared_source_gate(self):
        builder = (ROOT / 'release/build-installer.py').read_text()
        call = "subprocess.run(['python3', '-B', str(ROOT / 'installer/build_database_controllers.py')"
        self.assertIn(call, builder)
        self.assertNotIn("'--without-vitess', '--output'", builder)
        controller_builder = (ROOT / 'installer/build_database_controllers.py').read_text()
        self.assertIn('def build(destination, redis_image, include_vitess=None)', controller_builder)
        self.assertIn("parser.add_argument('--without-vitess',action='store_false'", controller_builder)
        self.assertIn('if include_vitess:qualify_vitess(HERE.parent)', controller_builder)
        self.assertIn("include_vitess=managed_runtimes['vitess']", controller_builder)
        self.assertIn('release/managed-runtime-availability.py', controller_builder)


if __name__ == '__main__':
    unittest.main()
