#!/usr/bin/env python3
"""Mocked source-level safety tests. They do not qualify an Oracle endpoint."""
import copy
import importlib.util
import json
from pathlib import Path
from types import SimpleNamespace
import tempfile
import unittest
from unittest.mock import Mock, patch

spec = importlib.util.spec_from_file_location('oracle_acceptance', Path(__file__).with_name(
    'run-development-oracle-public-endpoint-acceptance.py'))
runner = importlib.util.module_from_spec(spec)
spec.loader.exec_module(runner)


class OracleRunnerSafetyTests(unittest.TestCase):
    def test_singleton_transition_preserves_statefulset_and_two_volume_identities(self):
        volumes = [
            {'claim_name': 'backup-database-0', 'claim_uid': 'c1', 'volume_name': 'pv1',
             'volume_uid': 'v1', 'backing_fingerprint': 'b1'},
            {'claim_name': 'data-database-0', 'claim_uid': 'c2', 'volume_name': 'pv2',
             'volume_uid': 'v2', 'backing_fingerprint': 'b2'},
        ]
        before = {'statefulset_uid': 'set', 'pod_uid': 'old', 'volumes': volumes,
                  'credentials_secret_sha256': 'credentials', 'ca_fingerprint': 'ca'}
        after = dict(before, pod_uid='new')
        runner.require_singleton_transition(before, after)
        for mutate in (lambda value: value.update(statefulset_uid='other'),
                       lambda value: value.update(pod_uid='old'),
                       lambda value: value.update(volumes=volumes[:1]),
                       lambda value: value['volumes'][0].update(backing_fingerprint='changed'),
                       lambda value: value.update(credentials_secret_sha256='changed'),
                       lambda value: value.update(ca_fingerprint='changed')):
            candidate = copy.deepcopy(after)
            mutate(candidate)
            with self.assertRaises(ValueError):
                runner.require_singleton_transition(before, candidate)
        candidate = dict(after, ca_fingerprint='renewed')
        runner.require_singleton_transition(before, candidate, ca_may_change=True)

    def test_oracle_leaf_requires_exact_ordered_private_and_public_names(self):
        private = runner.oracle_private_dns_names('db')
        runner.require_oracle_dns_names({'dns_names': private}, 'db')
        runner.require_oracle_dns_names({'dns_names': private + ['database-15432.example.test']}, 'db',
                                        'database-15432.example.test')
        with self.assertRaises(ValueError):
            runner.require_oracle_dns_names({'dns_names': list(reversed(private))}, 'db')
        with self.assertRaises(ValueError):
            runner.require_oracle_dns_names({'dns_names': private + ['extra.example.test']}, 'db')

    def test_owned_volume_inventory_binds_claimref_reclaim_and_backing_identity(self):
        claims = {'items': [
            {'metadata': {'name': 'data-database-0', 'uid': 'claim-data', 'namespace': 'hdb-db',
                          'resourceVersion': '1', 'labels': {'hakopod.io/database-id': 'db',
                          'app.kubernetes.io/managed-by': 'hakopod'}},
             'spec': {'volumeName': 'pv-data'}, 'status': {'phase': 'Bound'}},
            {'metadata': {'name': 'backup-database-0', 'uid': 'claim-backup', 'namespace': 'hdb-db',
                          'resourceVersion': '1', 'labels': {'hakopod.io/database-id': 'db',
                          'app.kubernetes.io/managed-by': 'hakopod'}},
             'spec': {'volumeName': 'pv-backup'}, 'status': {'phase': 'Bound'}},
        ]}
        volumes = [
            {'metadata': {'name': 'pv-data', 'uid': 'volume-data'}, 'spec': {
                'claimRef': {'name': 'data-database-0', 'uid': 'claim-data', 'namespace': 'hdb-db'},
                'persistentVolumeReclaimPolicy': 'Delete', 'local': {'path': '/data/a'}}},
            {'metadata': {'name': 'pv-backup', 'uid': 'volume-backup'}, 'spec': {
                'claimRef': {'name': 'backup-database-0', 'uid': 'claim-backup', 'namespace': 'hdb-db'},
                'persistentVolumeReclaimPolicy': 'Delete', 'local': {'path': '/data/b'}}},
        ]
        with patch.object(runner, 'kube_json', side_effect=[claims, *volumes]):
            result = runner.owned_volume_inventory([], 'db')
        self.assertEqual(len(result), 2)
        self.assertTrue(all(item['backing_fingerprint'] for item in result))
        volumes[0]['spec']['claimRef']['uid'] = 'changed'
        with patch.object(runner, 'kube_json', side_effect=[claims, *volumes]):
            with self.assertRaisesRegex(ValueError, 'claimRef'):
                runner.owned_volume_inventory([], 'db')

    def test_policy_opens_only_owned_haproxy_peer_on_tcps(self):
        base = {'from': [{'podSelector': {}}, {'namespaceSelector': {'matchLabels': {
            'hakopod.io/database-access-db': 'true'}}}],
                'ports': [{'port': 2484, 'protocol': 'TCP'}]}
        public = {'from': [{
            'namespaceSelector': {'matchLabels': {'kubernetes.io/metadata.name': 'proxy'}},
            'podSelector': {'matchLabels': {'app.kubernetes.io/name': 'kubernetes-ingress',
                                            'app.kubernetes.io/instance': 'release'}}}],
                  'ports': [{'port': 2484, 'protocol': 'TCP'}]}
        policy = {'metadata': {'uid': 'uid', 'resourceVersion': '1', 'namespace': 'hdb-db',
                  'labels': {'hakopod.io/database-id': 'db', 'app.kubernetes.io/managed-by': 'hakopod'}},
                  'spec': {'ingress': [base, public]}}
        config = {'proxy_namespace': 'proxy', 'proxy_release': 'release'}
        with patch.object(runner, 'kube_json', return_value=policy):
            runner.inspect_public_network_policy([], 'db', config, True)
            policy['spec']['ingress'][1]['from'].append({'ipBlock': {'cidr': '0.0.0.0/0'}})
            with self.assertRaises(ValueError):
                runner.inspect_public_network_policy([], 'db', config, True)
        policy['spec']['ingress'] = [base]
        with patch.object(runner, 'kube_json', return_value=policy):
            runner.inspect_public_network_policy([], 'db', config, False)

    def test_durable_checkpoints_cover_oracle_transition_without_writing_phase(self):
        expected = {'identity_issuing', 'identity_rolling', 'identity_converging', 'cancelling_identity'}
        self.assertTrue(expected.issubset(set(runner.DurableCheckpoints.PHASES)))
        gate = runner.DurableCheckpoints('/psql', {'PGDATABASE': 'owned'})
        with patch.object(gate, 'sql', return_value='1') as sql:
            gate.install('identity_rolling')
            self.assertIn('NEW.next_attempt_at=', sql.call_args.args[0])
            gate.resume('operation', 'database', 'endpoint', 'identity_rolling')
            statement = sql.call_args.args[0]
            self.assertIn("status='queued' AND phase='identity_rolling'", statement)
            self.assertNotIn('managed_database_public_endpoint_operations SET phase=', statement)
        with patch.object(gate, 'sql', return_value='1') as sql:
            gate.resume('operation', 'database', 'endpoint', 'identity_issuing',
                        next_phase='identity_converging')
            statement = sql.call_args.args[0]
            self.assertIn("UPDATE acceptance_checkpoint SET phase='identity_converging'", statement)
            self.assertNotIn('managed_database_public_endpoint_operations SET phase=', statement)
        with patch.object(gate, 'sql') as sql:
            with self.assertRaises(ValueError):
                gate.resume('operation', 'database', 'endpoint', 'identity_issuing', next_phase='unknown')
            sql.assert_not_called()
        with patch.object(gate, 'sql') as sql:
            with self.assertRaises(ValueError):
                gate.resume("x';drop", 'database', 'endpoint', 'identity_rolling')
            sql.assert_not_called()
        with patch.object(gate, 'sql', return_value='7:123.45'):
            self.assertEqual(gate.marker('operation', 'cancelling_identity'), '7:123.45')

    def test_fixture_sql_fences_stale_review_and_maintenance_identity(self):
        fixture = runner.FixtureSQL('/psql', {'PGDATABASE': 'owned'})
        with patch.object(fixture, 'sql', side_effect=['review', 'database', 'database', 'endpoint', 'endpoint']) as sql:
            fixture.expire_review('review')
            fixture.hold_maintenance('database')
            fixture.release_maintenance('database')
            fixture.mask_endpoint_purpose('endpoint', 'read_only')
            fixture.mask_endpoint_purpose('endpoint', 'read_write')
        statements = [item.args[0] for item in sql.call_args_list]
        self.assertIn("consumed_at IS NULL", statements[0])
        self.assertIn("maintenance_lease='oracle-acceptance'", statements[1])
        self.assertIn("maintenance_lease='oracle-acceptance'", statements[2])
        self.assertIn("'\"read_only\"'::jsonb", statements[3])
        self.assertIn("'\"read_write\"'::jsonb", statements[4])

    def test_route_reload_retention_waits_for_unacknowledged_owned_route(self):
        fault = Mock()
        claim = {'metadata': {'uid': 'claim'}}
        with patch.object(runner, 'inspect_route') as route, patch.object(
                runner, 'inspect_claim', return_value=claim) as inspect_claim:
            fault.status.return_value = {'route_mutation_seen': False}
            self.assertIsNone(runner.inspect_route_reload_retention(
                fault, [], 'proxy', 15432, 'database', 'endpoint'))
            route.assert_not_called()
            fault.status.return_value = {'route_mutation_seen': True, 'successful_route_mutations': 1,
                                         'pre_mutation_proxy_upgrade_relays': 1,
                                         'pre_mutation_database_upgrade_relays': 0}
            with self.assertRaises(ValueError):
                runner.inspect_route_reload_retention(
                    fault, [], 'proxy', 15432, 'database', 'endpoint')
            fault.status.return_value = {'route_mutation_seen': True, 'successful_route_mutations': 1,
                                         'pre_mutation_proxy_upgrade_relays': 1,
                                         'pre_mutation_database_upgrade_relays': 1}
            route.return_value = None
            self.assertIsNone(runner.inspect_route_reload_retention(
                fault, [], 'proxy', 15432, 'database', 'endpoint'))
            route.return_value = {'metadata': {'annotations': {'hakopod.io/tcp-acknowledged': ''}}}
            self.assertIsNone(runner.inspect_route_reload_retention(
                fault, [], 'proxy', 15432, 'database', 'endpoint'))
            route.return_value = {'metadata': {'annotations': {}}}
            self.assertEqual(runner.inspect_route_reload_retention(
                fault, [], 'proxy', 15432, 'database', 'endpoint'), claim)
            inspect_claim.assert_called_once_with([], 'proxy', 15432, 'database', 'endpoint')

    def test_operator_allocation_conflict_requires_exact_problem(self):
        message = 'revision or idempotency conflict: every operator endpoint allocation is reserved'
        self.assertEqual(runner.require_operator_allocation_conflict(
            {'error': {'code': 'conflict', 'message': message}}), message)
        for problem in ({'error': {'code': 'database_public_endpoint_dns', 'message': message}},
                        {'error': {'code': 'conflict', 'message': 'another conflict'}}, None):
            with self.assertRaises(ValueError):
                runner.require_operator_allocation_conflict(problem)

    def test_route_is_direct_tcps_to_exact_oracle_service(self):
        endpoint = {'spec': {'purpose': 'read_write', 'max_connections': 8},
                    'allocation': {'port': 15432}}
        route = {'spec': [{'service': {'name': 'database', 'port': 2484}, 'frontend': {
            'maxconn': 8, 'binds': {'v4': {'address': '0.0.0.0', 'port': 15432}},
            'acl_list': [{'acl_name': 'allowed_source', 'criterion': 'src',
                          'value': '10.0.0.2/32 8.8.8.8/32'}],
            'tcp_request_rule_list': [{'type': 'connection', 'action': 'reject',
                                      'cond': 'unless', 'cond_test': 'allowed_source'}]}}]}
        runner.validate_route(route, endpoint, ['10.0.0.2/32', '8.8.8.8/32'])
        route['spec'][0]['service']['port'] = 1521
        with self.assertRaises(ValueError):
            runner.validate_route(route, endpoint, ['10.0.0.2/32', '8.8.8.8/32'])

    def test_external_probe_requires_every_negative_and_no_secret_echo(self):
        checks = ['verified', 'wrong_ca_rejected', 'wrong_hostname_rejected', 'wrong_password_rejected',
                  'plaintext_rejected', 'port_1521_refused', 'port_5500_refused',
                  'valid_reconnect_after_each_negative']
        attestation = {'provider': 'outside', 'nonce': 'f' * 32, 'source_cidr': '198.51.100.2/32'}
        result = {'schema_version': 1, 'provider': 'outside', 'nonce': 'f' * 32,
                  'outside_development_cluster': True,
                  'source_cidr': '198.51.100.2/32', 'leaf_fingerprint': 'a' * 64,
                  'identity': 'APP|FREEPDB1', 'checks': {name: True for name in checks}}
        config = {'external_probe_command': '/provider', 'external_probe_config_file': '/config'}
        endpoint = {'host': 'database-15432.example.test', 'address': '10.0.0.2', 'port': 15432}
        with patch.object(runner, 'command', return_value=json.dumps(result).encode()):
            runner.external_probe(config, endpoint, 'secret-password', 'public-ca', 'a' * 64,
                                  attestation)
        result['checks']['plaintext_rejected'] = False
        with patch.object(runner, 'command', return_value=json.dumps(result).encode()):
            with self.assertRaises(ValueError):
                runner.external_probe(config, endpoint, 'secret-password', 'public-ca', 'a' * 64,
                                      attestation)
        result['checks']['plaintext_rejected'] = True
        result['nonce'] = 'e' * 32
        with patch.object(runner, 'command', return_value=json.dumps(result).encode()):
            with self.assertRaises(ValueError):
                runner.external_probe(config, endpoint, 'secret-password', 'public-ca', 'a' * 64,
                                      attestation)
        with patch.object(runner, 'command', return_value=b'secret-password'):
            with self.assertRaisesRegex(ValueError, 'exposed'):
                runner.external_probe(config, endpoint, 'secret-password', 'public-ca', 'a' * 64,
                                      attestation)

    def test_preflight_binds_distinct_binary_and_external_provider_hashes(self):
        fields = set(runner.CONFIG_FIELDS)
        self.assertIn('qualification_binary', fields)
        self.assertIn('withdrawal_binary', fields)
        self.assertIn('fault_proxy_binary', fields)
        self.assertIn('qualification_approval_file', fields)
        self.assertIn('withdrawal_approval_file', fields)
        self.assertIn('external_probe_command', fields)
        source = Path(__file__).with_name('run-development-oracle-public-endpoint-acceptance.py').read_text()
        self.assertNotIn('apply_patch', source)
        self.assertIn('gate_closed_binary_started', source)
        self.assertIn("fault_mode='master_socket_unavailable'", source)
        self.assertIn("accept_publication('publishing')", source)
        self.assertNotIn('recovery-runtime.json', source)

    def test_lock_rejects_parallel_mutation(self):
        with tempfile.TemporaryDirectory() as folder:
            first = runner.acquire_acceptance_lock(Path(folder))
            try:
                with self.assertRaisesRegex(ValueError, 'another database'):
                    runner.acquire_acceptance_lock(Path(folder))
            finally:
                first.close()


if __name__ == '__main__':
    unittest.main()
