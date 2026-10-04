import unittest
from unittest.mock import patch

import modules


class ModulesCLITest(unittest.TestCase):
    def test_vitess_plan_and_apply_reach_managed_database_module(self):
        for option in ('--plan', '--apply-reviewed-plan'):
            with self.subTest(option=option), patch.object(modules, 'main') as main:
                modules.cli(['managed-databases', '--engines', 'vitess', option, '/tmp/review.json'])
                kwargs = {
                    'engines': ['vitess'],
                    'plan_path': '/tmp/review.json' if option == '--plan' else None,
                    'review_path': '/tmp/review.json' if option == '--apply-reviewed-plan' else None,
                }
                main.assert_called_once_with('managed-databases', None, **kwargs)

    def test_unsupported_engine_is_rejected_before_module_handoff(self):
        with patch.object(modules, 'main') as main, self.assertRaises(SystemExit):
            modules.cli(['managed-databases', '--engines', 'unsupported', '--plan', '/tmp/review.json'])
        main.assert_not_called()


if __name__ == '__main__':
    unittest.main()
