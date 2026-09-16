import json
from types import SimpleNamespace
import unittest
from unittest.mock import patch

import credentials
import host


class CredentialNamespaceTests(unittest.TestCase):
    def run_repair(self, labels):
        value = {'metadata': {'uid': 'original-uid', 'resourceVersion': '42', 'labels': labels}}
        with patch.object(credentials.subprocess, 'run', return_value=SimpleNamespace(stdout=json.dumps(value))) as run:
            changed = credentials.repair_namespace('a'*32)
            return changed, run.call_args_list

    def test_missing_label_is_repaired_with_concurrency_guards(self):
        changed, calls = self.run_repair({credentials.INSTALLATION: 'a'*32, 'operator-label': 'preserved'})
        self.assertTrue(changed)
        self.assertEqual(len(calls), 2)
        patch_value = json.loads(calls[1].kwargs['input'])
        self.assertEqual(patch_value[:2], [
            {'op': 'test', 'path': '/metadata/uid', 'value': 'original-uid'},
            {'op': 'test', 'path': '/metadata/resourceVersion', 'value': '42'},
        ])
        self.assertEqual([p['path'] for p in patch_value if p['op'] != 'test'],
                         ['/metadata/labels/app.kubernetes.io~1managed-by'])

    def test_healthy_engine_and_installer_namespaces_are_unchanged(self):
        for labels in ({credentials.MANAGED_BY: 'hakopod'}, host.namespace_manifest('hakopod-system', 'a'*32)['metadata']['labels']):
            changed, calls = self.run_repair(labels)
            self.assertFalse(changed)
            self.assertEqual(len(calls), 1)

    def test_unknown_or_foreign_ownership_is_never_adopted(self):
        for labels in ({}, {credentials.INSTALLATION: 'b'*32},
                       {credentials.INSTALLATION: 'a'*32, credentials.MANAGED_BY: 'other'}):
            with self.subTest(labels=labels), self.assertRaises(ValueError):
                self.run_repair(labels)

    def test_bad_identity_rejected_before_kubernetes(self):
        with patch.object(credentials.subprocess, 'run') as run:
            with self.assertRaises(ValueError): credentials.repair_namespace('')
            run.assert_not_called()

    def test_missing_namespace_is_left_for_engine_creation(self):
        with patch.object(credentials.subprocess, 'run', return_value=SimpleNamespace(stdout='')) as run:
            self.assertFalse(credentials.repair_namespace('a'*32))
            self.assertEqual(run.call_count, 1)

    def test_installer_namespaces_include_both_ownership_labels(self):
        for name in ('hakopod-system', 'haproxy-controller', 'cert-manager'):
            labels = host.namespace_manifest(name, 'a'*32)['metadata']['labels']
            self.assertEqual(labels, {credentials.INSTALLATION: 'a'*32, credentials.MANAGED_BY: 'hakopod'})

    def test_namespace_cli_rejects_invalid_identity_or_name(self):
        for name, identity in (('bad/name', 'a'*32), ('valid', 'bad')):
            with self.assertRaises(ValueError): host.namespace_manifest(name, identity)
