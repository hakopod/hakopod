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

    def test_runtime_class_only_reaches_managed_actions_module(self):
        with patch.object(modules, 'main') as main:
            modules.cli(['managed-actions', '--runtime-class-only'])
        main.assert_called_once_with('managed-actions', None, engines=None, plan_path=None, review_path=None,
                                     runtime_class_only=True)

    def test_adopt_node_reaches_managed_actions_module(self):
        with patch.object(modules, 'main') as main:
            modules.cli(['managed-actions', '--adopt-node', 'spot-1'])
        main.assert_called_once_with('managed-actions', None, engines=None, plan_path=None, review_path=None,
                                     adopt_node='spot-1')

    def test_runtime_class_only_rejects_other_modules_and_profiles(self):
        for module, profile, options in (('storage', None, {'runtime_class_only': True}),
                                         ('managed-actions', 'vfs', {'runtime_class_only': True}),
                                         ('storage', None, {'adopt_node': 'spot-1'}),
                                         ('managed-actions', None, {'runtime_class_only': True, 'adopt_node': 'spot-1'})):
            with self.subTest(module=module, options=options), patch.object(modules.os, 'geteuid', return_value=0), \
                 patch.object(modules.os, 'umask'), self.assertRaises(ValueError):
                modules.apply_module(module, profile, **options)

    def test_unsupported_engine_is_rejected_before_module_handoff(self):
        with patch.object(modules, 'main') as main, self.assertRaises(SystemExit):
            modules.cli(['managed-databases', '--engines', 'unsupported', '--plan', '/tmp/review.json'])
        main.assert_not_called()


if __name__ == '__main__':
    unittest.main()
