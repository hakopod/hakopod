#!/usr/bin/env python3
"""Mocked source-level safety tests. They do not qualify an Oracle endpoint."""
from datetime import datetime, timedelta, timezone
import importlib.util
import json
from pathlib import Path
from types import SimpleNamespace
import tempfile
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location('oracle_plan', Path(__file__).with_name(
    'plan-development-oracle-public-endpoint-acceptance.py'))
planner = importlib.util.module_from_spec(spec)
spec.loader.exec_module(planner)


class OraclePlanSafetyTests(unittest.TestCase):
    def protected_json(self, root, name, value):
        path = root / name
        path.write_text(json.dumps(value))
        path.chmod(0o600)
        return path

    def test_lane_grant_requires_exact_context_purpose_reviewers_and_freshness(self):
        with tempfile.TemporaryDirectory() as folder:
            root = Path(folder)
            now = datetime.now(timezone.utc)
            grant = {'schema_version': 1, 'cluster': planner.CONTEXT,
                     'purpose': 'oracle-free-public-tcps',
                     'approvals': ['root', 'oracle_transition_review'],
                     'expires_at': (now + timedelta(hours=1)).isoformat(), 'nonce': 'a' * 32}
            path = self.protected_json(root, 'grant.json', grant)
            self.assertEqual(planner.validate_lane_grant(path, now)[0], grant)
            for key, value in (('cluster', 'other'), ('purpose', 'oracle'),
                               ('approvals', ['root']), ('nonce', 'bad')):
                changed = dict(grant, **{key: value})
                path.unlink()
                path = self.protected_json(root, 'grant.json', changed)
                with self.assertRaises(ValueError):
                    planner.validate_lane_grant(path, now)
            path.unlink()
            path = self.protected_json(root, 'grant.json', dict(grant,
                expires_at=(now - timedelta(seconds=1)).isoformat()))
            with self.assertRaises(ValueError):
                planner.validate_lane_grant(path, now)

    def test_binary_approvals_bind_separate_gate_states_binary_and_source(self):
        with tempfile.TemporaryDirectory() as folder:
            root = Path(folder)
            qualification = root / 'qualification'
            withdrawal = root / 'withdrawal'
            qualification.write_bytes(b'gate-open')
            withdrawal.write_bytes(b'gate-closed')
            for path in (qualification, withdrawal):
                path.chmod(0o700)
            now = datetime.now(timezone.utc)
            config = {'qualification_binary': str(qualification), 'withdrawal_binary': str(withdrawal)}
            source_hash = 'b' * 64
            for name, gate, purpose in (
                    ('qualification', True, 'oracle-free-public-tcps-qualification'),
                    ('withdrawal', False, 'oracle-free-public-tcps-withdrawal')):
                approval = {'schema_version': 1, 'purpose': purpose,
                            'binary_sha256': planner.file_hash(Path(config[name + '_binary'])),
                            'source_manifest_sha256': ('e' * 64 if name == 'qualification' else source_hash),
                            'gate': gate,
                            'reviewed_by': ['root', 'oracle_transition_review'],
                            'approved_at': now.isoformat(),
                            'expires_at': (now + timedelta(hours=1)).isoformat(), 'nonce': 'c' * 32}
                config[name + '_approval_file'] = str(self.protected_json(root, name + '.json', approval))
            result = planner.validate_binary_approvals(config, source_hash, now)
            self.assertTrue(result['qualification']['gate'])
            self.assertFalse(result['withdrawal']['gate'])
            changed = json.loads(Path(config['withdrawal_approval_file']).read_text())
            changed['gate'] = True
            Path(config['withdrawal_approval_file']).write_text(json.dumps(changed))
            with self.assertRaisesRegex(ValueError, 'separate current root approvals'):
                planner.validate_binary_approvals(config, source_hash, now)

    def test_oracle_peak_reserves_singleton_replacement_and_probes(self):
        available = {'cpu_millis': 10000, 'memory_bytes': 16 * 1024 ** 3}
        nodes = {name: {'remaining_before_plan': dict(available)} for name in planner.NODES}
        peak = planner.plan_oracle_peak(nodes, 'k3d-hakopod-dev-server-0')
        self.assertEqual(peak['k3d-hakopod-dev-server-0']['oracle_members'], 1)
        self.assertEqual(peak['k3d-hakopod-dev-server-0']['replacement_members'], 1)
        self.assertEqual(peak['k3d-hakopod-database-worker-0']['probes'], 1)
        nodes['k3d-hakopod-dev-server-0']['remaining_before_plan']['memory_bytes'] = 4 * 1024 ** 3
        with self.assertRaisesRegex(ValueError, 'no capacity'):
            planner.plan_oracle_peak(nodes, 'k3d-hakopod-dev-server-0')

    def test_storage_reinspection_rejects_lost_free_capacity(self):
        before = {name: {'container_id': name, 'container_path': '/data', 'host_path': '/host/' + name,
                         'device': 1, 'total_bytes': 100, 'free_bytes_observed': 80,
                         'required_free_bytes': 70} for name in planner.NODES}
        after = json.loads(json.dumps(before))
        self.assertTrue(planner.same_storage(before, after))
        after['k3d-hakopod-dev-server-0']['free_bytes_observed'] = 69
        self.assertFalse(planner.same_storage(before, after))

    def test_external_provider_must_be_fresh_and_outside_cluster(self):
        now = datetime.now(timezone.utc)
        value = {'schema_version': 1, 'provider': 'approved_runner', 'source_cidr': '8.8.8.8/32',
                 'outside_development_cluster': True, 'inventory_observed_at': now.isoformat(),
                 'expires_at': (now + timedelta(hours=1)).isoformat(), 'nonce': 'd' * 32}
        config = {'external_probe_command': '/provider', 'external_probe_config_file': '/provider.json',
                  'public_address': '10.0.0.2'}
        with patch.object(planner, 'command', return_value=json.dumps(value).encode()):
            self.assertEqual(planner.inspect_external_provider(config, now)[0], value)
        for change in ({'outside_development_cluster': False},
                       {'inventory_observed_at': (now - timedelta(minutes=6)).isoformat()},
                       {'source_cidr': '10.0.0.2/32'}):
            candidate = dict(value, **change)
            with patch.object(planner, 'command', return_value=json.dumps(candidate).encode()):
                with self.assertRaises(ValueError):
                    planner.inspect_external_provider(config, now)

    def test_kubernetes_inspection_rejects_active_database_workload(self):
        node = {'metadata': {'name': 'k3d-hakopod-dev-server-0', 'labels': {
                    'kubernetes.io/hostname': 'k3d-hakopod-dev-server-0'}}, 'spec': {}, 'status': {
                    'conditions': [{'type': 'Ready', 'status': 'True'},
                                   {'type': 'DiskPressure', 'status': 'False'},
                                   {'type': 'MemoryPressure', 'status': 'False'},
                                   {'type': 'PIDPressure', 'status': 'False'}],
                    'nodeInfo': {'architecture': 'amd64', 'operatingSystem': 'linux', 'kubeletVersion': 'v1'},
                    'addresses': [{'type': 'InternalIP', 'address': '10.0.0.2'}],
                    'allocatable': {'cpu': '8', 'memory': '16Gi'}}}
        other = json.loads(json.dumps(node))
        other['metadata']['name'] = 'k3d-hakopod-database-worker-0'
        other['metadata']['labels']['kubernetes.io/hostname'] = other['metadata']['name']
        other['status']['addresses'][0]['address'] = '10.0.0.3'
        pod = {'metadata': {'namespace': 'hdb-owned', 'labels': {'hakopod.io/database-id': 'db'}},
               'status': {'phase': 'Running'}, 'spec': {'containers': []}}
        responses = [{'items': [node, other]}, {'items': [pod]}]
        config = {'kubectl': '/kubectl', 'kubeconfig': '/kubeconfig', 'public_address': '10.0.0.2',
                  'allowed_node': node['metadata']['name'], 'proxy_namespace': 'proxy',
                  'proxy_configmap': 'proxy', 'proxy_release': 'release'}
        docker = {name: {'cpu_millis': 8000, 'memory_bytes': 16 * 1024 ** 3} for name in planner.NODES}
        with patch.object(planner, 'command', return_value=planner.CONTEXT.encode()), \
                patch.object(planner, 'kube_json', side_effect=responses):
            with self.assertRaisesRegex(ValueError, 'active database'):
                planner.inspect_kubernetes(config, docker)

    def test_haproxy_requires_exact_image_port_node_and_crd(self):
        deployment = {'metadata': {'labels': {'app.kubernetes.io/managed-by': 'Helm',
                       'app.kubernetes.io/instance': 'release'}, 'uid': 'deployment'}, 'spec': {'replicas': 1,
                       'template': {'spec': {'nodeSelector': {'kubernetes.io/hostname': planner.NODES.keys().__iter__().__next__()},
                       'containers': [{'image': planner.HAPROXY_IMAGE, 'ports': [{'hostPort': 15432}]}]}}}}
        configmap = {'metadata': {'uid': 'config', 'labels': {'app.kubernetes.io/managed-by': 'Helm',
                     'app.kubernetes.io/instance': 'release'}}}
        crd = {'metadata': {'uid': 'crd'}, 'spec': {'group': 'ingress.v3.haproxy.org',
               'names': {'plural': 'tcps'}, 'versions': [{'name': 'v3', 'served': True}]}}
        self.assertEqual(planner.validate_installed_haproxy(deployment, configmap, crd, 'release')['tcp_crd_uid'], 'crd')
        deployment['spec']['template']['spec']['containers'][0]['image'] = 'unreviewed'
        with self.assertRaises(ValueError):
            planner.validate_installed_haproxy(deployment, configmap, crd, 'release')

    def test_protected_inputs_reject_permissions_and_symlinks(self):
        with tempfile.TemporaryDirectory() as folder:
            root = Path(folder)
            value = root / 'value'
            value.write_text('x')
            value.chmod(0o644)
            with self.assertRaises(ValueError):
                planner.protected(value)
            value.chmod(0o600)
            link = root / 'link'
            link.symlink_to(value)
            with self.assertRaises(OSError):
                planner.protected(link)


if __name__ == '__main__':
    unittest.main()
