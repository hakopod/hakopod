import importlib.util
import os
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch
from types import SimpleNamespace

module = importlib.util.spec_from_file_location('volume_copy', Path(__file__).with_name('volume_copy.py'))
copy = importlib.util.module_from_spec(module)
module.loader.exec_module(copy)


class VolumeCopyTests(unittest.TestCase):
    def test_verified_copy_preserves_data_links_and_original(self):
        with tempfile.TemporaryDirectory() as folder:
            source, target = Path(folder) / 'source', Path(folder) / 'target'
            source.mkdir(); target.mkdir()
            (source / 'database').mkdir(mode=0o700)
            data = source / 'database' / 'data.bin'
            data.write_bytes(bytes(range(256)) * 300)
            data.chmod(0o600)
            os.link(data, source / 'hardlink')
            (source / 'symlink').symlink_to('database/data.bin')
            report = copy.migrate(source, target, 1 << 30)
            copied = target / 'data'
            self.assertTrue(report['verified'])
            self.assertEqual(data.read_bytes(), (copied / 'database/data.bin').read_bytes())
            self.assertEqual((copied / 'hardlink').stat().st_ino, (copied / 'database/data.bin').stat().st_ino)
            self.assertEqual(os.readlink(copied / 'symlink'), 'database/data.bin')
            self.assertEqual(data.stat().st_mode & 0o777, (copied / 'database/data.bin').stat().st_mode & 0o777)
            # A failed/partial destination is safely replaced on an explicit retry.
            (copied / 'extra').write_text('partial attempt')
            self.assertTrue(copy.migrate(source, target, 1 << 30)['verified'])
            self.assertFalse((target / 'data/extra').exists())

    def test_provider_owned_mount_root_uses_service_group(self):
        with tempfile.TemporaryDirectory() as folder:
            source, target = Path(folder) / 'source', Path(folder) / 'target'
            source.mkdir(); target.mkdir()
            (source / 'data').write_text('database bytes')
            original_stat = Path.stat
            unavailable_group = max(os.getgid(), *os.getgroups(), 0) + 10000
            def provider_stat(path, *args, **kwargs):
                info = original_stat(path, *args, **kwargs)
                if path == source and kwargs.get('follow_symlinks', True):
                    return SimpleNamespace(st_uid=0, st_gid=unavailable_group, st_mode=info.st_mode)
                return info
            with patch.object(Path, 'stat', provider_stat):
                self.assertTrue(copy.migrate(source, target, 1 << 30)['verified'])
            self.assertEqual((target / 'data').stat().st_gid, os.getgid())
            self.assertEqual((target / 'data/data').read_text(), 'database bytes')

    def test_too_small_preserves_both_filesystems(self):
        with tempfile.TemporaryDirectory() as folder:
            source, target = Path(folder) / 'source', Path(folder) / 'target'
            source.mkdir(); target.mkdir()
            (source / 'data').write_text('keep original')
            (target / 'sentinel').write_text('not touched before fit check')
            with self.assertRaisesRegex(ValueError, 'headroom'):
                copy.migrate(source, target, copy.HEADROOM)
            self.assertEqual((source / 'data').read_text(), 'keep original')
            self.assertTrue((target / 'sentinel').exists())

    def test_special_files_rejected_before_copy(self):
        with tempfile.TemporaryDirectory() as folder:
            source, target = Path(folder) / 'source', Path(folder) / 'target'
            source.mkdir(); target.mkdir(); os.mkfifo(source / 'fifo')
            with self.assertRaisesRegex(ValueError, 'special files'):
                copy.migrate(source, target, 1 << 30)
            self.assertTrue((source / 'fifo').exists())

    def test_app_owned_lost_found_is_preserved(self):
        if os.getuid() == 0:
            self.skipTest('application runs unprivileged')
        with tempfile.TemporaryDirectory() as folder:
            source, target = Path(folder) / 'source', Path(folder) / 'target'
            source.mkdir(); target.mkdir(); (source / 'lost+found').mkdir()
            (source / 'lost+found/user-data').write_text('preserve')
            copy.migrate(source, target, 1 << 30)
            self.assertEqual((target / 'data/lost+found/user-data').read_text(), 'preserve')


if __name__ == '__main__':
    unittest.main()
