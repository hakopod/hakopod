#!/usr/bin/env python3
"""Keep unqualified managed Vitess outside release artifacts."""
from pathlib import Path
import unittest


ROOT = Path(__file__).resolve().parent.parent


class ReleaseScopeTest(unittest.TestCase):
    def test_unqualified_vitess_is_not_a_release_dependency(self):
        workflow = (ROOT / '.github/workflows/release.yml').read_text()
        self.assertNotIn('\n  vitess:\n', workflow)
        self.assertNotIn('managed-vitess-qualification', workflow)
        self.assertNotIn('probe-index, vitess', workflow)

    def test_installer_does_not_request_unqualified_vitess(self):
        builder = (ROOT / 'release/build-installer.py').read_text()
        call = "subprocess.run(['python3', str(ROOT / 'installer/build_database_controllers.py')"
        self.assertIn(call, builder)
        self.assertNotIn("'--include-vitess', '--output'", builder)
        controller_builder = (ROOT / 'installer/build_database_controllers.py').read_text()
        self.assertIn("parser.add_argument('--include-vitess',action='store_true'", controller_builder)
        self.assertIn('if include_vitess:qualify_vitess(HERE.parent)', controller_builder)


if __name__ == '__main__':
    unittest.main()
