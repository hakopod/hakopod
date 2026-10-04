#!/usr/bin/env python3
"""Require qualified managed Vitess in release artifacts."""
from pathlib import Path
import unittest


ROOT = Path(__file__).resolve().parent.parent


class ReleaseScopeTest(unittest.TestCase):
    def test_qualified_vitess_is_a_release_dependency(self):
        workflow = (ROOT / '.github/workflows/release.yml').read_text()
        self.assertIn('\n  vitess:\n', workflow)
        self.assertIn('managed-vitess-qualification', workflow)
        self.assertIn('probe-index, vitess, supabase, neon', workflow)

    def test_installer_requires_qualified_vitess_by_default(self):
        builder = (ROOT / 'release/build-installer.py').read_text()
        call = "subprocess.run(['python3', str(ROOT / 'installer/build_database_controllers.py')"
        self.assertIn(call, builder)
        self.assertNotIn("'--without-vitess', '--output'", builder)
        controller_builder = (ROOT / 'installer/build_database_controllers.py').read_text()
        self.assertIn('def build(destination, redis_image, include_vitess=True)', controller_builder)
        self.assertIn("parser.add_argument('--without-vitess',action='store_false'", controller_builder)
        self.assertIn('if include_vitess:qualify_vitess(HERE.parent)', controller_builder)


if __name__ == '__main__':
    unittest.main()
