import unittest
from pathlib import Path
from actions_runtime import extend_template


class ActionsRuntimeTests(unittest.TestCase):
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
