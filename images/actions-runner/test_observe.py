import importlib.util
import json
import os
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location('observe', Path(__file__).with_name('observe.py'))
observe = importlib.util.module_from_spec(spec)
spec.loader.exec_module(observe)

class ObserverTest(unittest.TestCase):
    def test_root_pages_rotation_and_symlinks(self):
        with tempfile.TemporaryDirectory() as root:
            pages = Path(root) / '_diag/pages'
            pages.mkdir(parents=True)
            identity = '11111111-1111-1111-1111-111111111111'
            prefix = '22222222-2222-2222-2222-222222222222_' + identity
            page = pages / (prefix + '_1.log')
            page.write_text('2026-09-29T00:00:00Z masked ***\npartial')
            (pages / ('22222222-2222-2222-2222-222222222222_33333333-3333-3333-3333-333333333333_1.log')).write_text('child duplicate\n')
            private = Path(root) / 'private'; private.write_text('do not read\n')
            (pages / (prefix + '_2.log')).symlink_to(private)
            records = []
            with patch.object(observe, 'ROOT', root), patch.object(observe, 'STATE', root + '/state'):
                collector = observe.Collector(records.append); collector.identity = identity
                collector.tick()
                with page.open('a') as stream: stream.write(' tail\n')
                page.unlink()  # Keep the open descriptor after the runner uploads it.
                collector.tick(); collector.tick()
                self.assertEqual([r['text'] for r in records if 'text' in r], ['2026-09-29T00:00:00Z masked ***', 'partial tail'])
                os.close(collector.pages_fd)

    def test_hook_allowlist_and_numeric_fields(self):
        with tempfile.TemporaryDirectory() as root:
            with patch.object(observe, 'STATE', root + '/state'), patch.dict(os.environ, {'GITHUB_TOKEN':'never serialize','GITHUB_REPOSITORY':'org/repo','GITHUB_RUN_ID':'42','GITHUB_RUN_ATTEMPT':'2'}):
                observe.hook()
                data = json.loads((Path(root) / 'state/job.json').read_text())
                self.assertEqual(data['run_id'], 42)
                self.assertEqual(data['attempt'], 2)
                self.assertNotIn('never serialize', json.dumps(data))

    def test_directory_symlink_is_not_followed(self):
        with tempfile.TemporaryDirectory() as root:
            Path(root, '_diag').symlink_to('/tmp')
            with patch.object(observe, 'ROOT', root), patch.object(observe, 'STATE', root + '/state'):
                collector = observe.Collector(lambda _: None)
                collector.identity = '11111111-1111-1111-1111-111111111111'
                with self.assertRaises(OSError): collector.tick()

if __name__ == '__main__': unittest.main()
