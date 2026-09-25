import unittest
import io
import tarfile
import tempfile
from pathlib import Path
from actions_runtime import extend_template, unpack_runtime


class ActionsRuntimeTests(unittest.TestCase):
    def test_runtime_extraction_is_bounded_and_rejects_links_before_writing(self):
        for name, kind in [('runsc', tarfile.REGTYPE), ('../escape', tarfile.REGTYPE),
                           ('gvisor-bin/link', tarfile.SYMTYPE), ('runsc', tarfile.FIFOTYPE)]:
            with self.subTest(name=name, kind=kind), tempfile.TemporaryDirectory() as temp:
                root = Path(temp)
                source = root / 'runtime.tar'
                with tarfile.open(source, 'w') as archive:
                    item = tarfile.TarInfo(name)
                    item.type = kind
                    item.mode = 0o755
                    item.linkname = '/outside'
                    item.size = 2 if kind == tarfile.REGTYPE else 0
                    archive.addfile(item, io.BytesIO(b'ok') if item.size else None)
                if name == 'runsc' and kind == tarfile.REGTYPE:
                    unpack_runtime(source, root / 'out')
                    self.assertEqual((root / 'out/runsc').read_bytes(), b'ok')
                    self.assertEqual((root / 'out/runsc').stat().st_mode & 0o777, 0o755)
                else:
                    with self.assertRaises(ValueError):
                        unpack_runtime(source, root / 'out')
                    self.assertFalse((root / 'out').exists())

    def test_preserves_custom_runtime_and_is_idempotent(self):
        original = '{{ template "base" . }}\n# customer setting\n[custom]\nvalue = true\n'
        root = Path('/opt/hakopod/actions-runtime/release')
        result = extend_template(original, root)
        self.assertTrue(result.startswith(original))
        self.assertEqual(extend_template(result, root), result)
        self.assertIn('runtime_path = "/opt/hakopod/actions-runtime/release/containerd-shim-runsc-v1"', result)

    def test_refuses_unowned_or_edited_runtime(self):
        root = Path('/opt/hakopod/actions-runtime/release')
        for value in ('[runtimes.hakopod-actions]', '# END HAKOPOD MANAGED ACTIONS\n', extend_template('', root).replace('io.containerd.runsc.v1', 'io.containerd.runc.v2')):
            with self.assertRaises(ValueError):
                extend_template(value, root)


if __name__ == '__main__':
    unittest.main()
