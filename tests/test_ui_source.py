import hashlib
import importlib.util
import io
from pathlib import Path
import tarfile
import tempfile
import unittest

spec = importlib.util.spec_from_file_location("ui_source", Path(__file__).resolve().parents[1] / "scripts/ui-source.py")
ui = importlib.util.module_from_spec(spec)
spec.loader.exec_module(ui)


class UISourceTests(unittest.TestCase):
    def test_deterministic_round_trip_and_preserves_working_copy(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            source = root / "source"
            source.mkdir()
            (source / "package.json").write_text('{"name":"@hakopod/hatch-ui"}\n')
            (source / "button.tsx").write_text("export const button = 'hello'\n")
            (source / ".git").mkdir()
            (source / ".git" / "secret-local-config").write_text("excluded")
            archive = root / "ui.tar.gz"
            ui.pack(source, archive)
            before = archive.read_bytes()
            ui.pack(source, archive)
            self.assertEqual(before, archive.read_bytes())
            destination = root / "restored"
            self.assertTrue(ui.restore(archive, destination))
            self.assertEqual((destination / "button.tsx").read_bytes(), (source / "button.tsx").read_bytes())
            self.assertFalse((destination / ".git").exists())
            (destination / "button.tsx").write_text("local edit")
            self.assertFalse(ui.restore(archive, destination))
            self.assertEqual((destination / "button.tsx").read_text(), "local edit")

    def test_hatch_snapshot_only_ships_the_consumer_package(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            source = root / "source"
            package = source / "packages/ui"
            package.mkdir(parents=True)
            (source / "package.json").write_text('{"name":"hatch"}')
            (package / "package.json").write_text('{"name":"@hakopod/hatch-ui"}')
            (package / "button.tsx").write_text("export const button = 'hatch'")
            for name in ["apps/docs", "examples/dashboard", "node_modules"]:
                folder = source / name
                folder.mkdir(parents=True)
                (folder / "excluded.txt").write_text("not part of the consumer")
            archive = root / "ui.tar.gz"
            ui.pack(source, archive)
            destination = root / "restored"
            self.assertTrue(ui.restore(archive, destination))
            self.assertTrue((destination / "packages/ui/button.tsx").is_file())
            self.assertFalse((destination / "apps").exists())
            self.assertFalse((destination / "examples").exists())
            self.assertFalse((destination / "node_modules").exists())

    def test_refuses_traversal_even_with_matching_checksum(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            archive = root / "ui.tar.gz"
            with tarfile.open(archive, "w:gz") as bundle:
                item = tarfile.TarInfo("ui/../../outside")
                item.size = 1
                bundle.addfile(item, io.BytesIO(b"x"))
            archive.with_suffix(".gz.sha256").write_text(hashlib.sha256(archive.read_bytes()).hexdigest())
            with self.assertRaises(ValueError):
                ui.restore(archive, root / "restored")
            self.assertFalse((root / "outside").exists())


if __name__ == "__main__":
    unittest.main()
