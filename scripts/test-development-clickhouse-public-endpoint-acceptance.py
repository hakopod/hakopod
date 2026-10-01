"""Mocked safety checks. These do not qualify a native endpoint."""
import copy
from datetime import datetime, timedelta, timezone
import importlib.util
import json
from pathlib import Path
from types import SimpleNamespace
import tempfile
import unittest
from unittest.mock import Mock, patch

spec = importlib.util.spec_from_file_location('clickhouse_acceptance', Path(__file__).with_name(
    'run-development-clickhouse-public-endpoint-acceptance.py'))
runner = importlib.util.module_from_spec(spec)
spec.loader.exec_module(runner)


class RunnerSafetyTests(unittest.TestCase):
    def test_native_preflight_matches_exact_shape_source_and_storage(self):
        with tempfile.TemporaryDirectory() as folder:
            root = Path(folder)
            source = root / 'clickhouse-public-endpoint-source'
            source.mkdir()
            binary = source / 'hakopod-server'
            config_path, grant_path, budget_path = [root / name for name in ('config.json', 'grant.json', 'budget.json')]
            config = dict.fromkeys(runner.CONFIG_FIELDS, '')
            config.update(fault_proxy_binary=str(source / 'fault-proxy'), fixture_shape='cluster', host_budget_file=str(budget_path), probe_image='probe@sha256:' + 'a' * 64)
            for path, value in ((config_path, config), (grant_path, {}), (budget_path, {})):
                path.write_text(json.dumps(value))
                path.chmod(0o600)
            now = datetime.now(timezone.utc)
            shape = runner.fixture.fixture('cluster')
            peak = runner.fixture.node_peak('cluster')
            nodes = {}
            for name, role in zip(runner.NODES, ('server', 'agent')):
                available = {'cpu_millis': 8000, 'memory_bytes': 16 * 1024 ** 3}
                nodes[name] = {'role': role, 'cpus': 8, 'cpu_millis': 8000, 'memory_bytes': 16 * 1024 ** 3,
                               'cpuset': list(range(8)), 'image_id': 'sha256:' + 'd' * 64, 'internal_ip': '10.0.0.2',
                               'allocatable': available, 'occupied_requests': {'cpu_millis': 0, 'memory_bytes': 0},
                               'remaining_before_plan': available.copy(), 'planned_peak': peak.copy(),
                               'remaining_after_peak': {key: available[key] - peak[key] for key in available}}
            plan_path = root / 'clickhouse-public-endpoint-v1.plan.json'
            plan = {'schema_version': 1, 'status': 'ready', 'mutation_mode': 'read_only_preflight',
                    'generated_at': now.isoformat(), 'grant_expires_at': (now + timedelta(hours=1)).isoformat(),
                    'context': runner.CONTEXT, 'attempt': 1, 'source': str(source),
                    'source_manifest_sha256': 'h', 'config_sha256': 'h', 'host_budget_sha256': 'h',
                    'lane_grant_sha256': 'h', 'server_binary_sha256': 'h', 'fault_proxy_binary_sha256': 'h',
                    'images': {'haproxy': runner.HAPROXY_IMAGE, 'clickhouse_server': runner.CLICKHOUSE_SERVER_IMAGE,
                               'clickhouse_operator': runner.CLICKHOUSE_OPERATOR_IMAGE, 'clickhouse_keeper': runner.CLICKHOUSE_KEEPER_IMAGE,
                               'probe': config['probe_image']}, 'nodes': nodes, 'host_budget': {}, 'fixture': shape,
                    'storage': {name: {'free_bytes_observed': 100 * 1024 ** 3,
                                      'required_free_bytes': shape['scratch_free_bytes_required']} for name in runner.NODES},
                    'haproxy': {'host_ports': [15432], 'node': runner.NODES[0], 'chart_sha256': runner.HAPROXY_CHART_SHA256,
                                'render_sha256': 'a' * 64, 'installed': {'deployment_uid': 'd', 'configmap_uid': 'c', 'tcp_crd_uid': 't'}},
                    'attempt_paths': {key: str(root / ('clickhouse-public-endpoint-v1.' + suffix)) for key, suffix in
                                      (('work', 'work'), ('events', 'jsonl'), ('evidence', 'evidence.json'), ('plan', 'plan.json'))},
                    'cleanup_contract': ['one', 'two', 'three', 'four', 'five']}
            def validate(candidate):
                plan_path.write_text(json.dumps(candidate))
                plan_path.chmod(0o600)
                return runner.validate_preflight_plan(plan_path, root, source, config_path, grant_path, binary, 1)
            with patch.object(runner, 'file_hash', return_value='h'), \
                    patch.object(runner, 'source_manifest_hash', return_value='h'), \
                    patch.object(runner.capacity, 'validate_budget'), \
                    patch.object(runner.capacity, 'validate_plan_evidence', return_value=True):
                self.assertEqual(validate(plan), plan)
                for mutate in (lambda p: p['fixture'].update(members=1),
                               lambda p: p['nodes'][runner.NODES[0]]['planned_peak'].update(surge_keepers=0),
                               lambda p: p['images'].update(clickhouse_keeper='unreviewed'),
                               lambda p: p['storage'][runner.NODES[1]].update(free_bytes_observed=0),
                               lambda p: p.update(fault_proxy_binary_sha256='changed'),
                               lambda p: p.update(source_manifest_sha256='changed')):
                    candidate = copy.deepcopy(plan)
                    mutate(candidate)
                    with self.assertRaises(ValueError):
                        validate(candidate)

    def test_capacity_and_storage_are_reacquired_before_mutation(self):
        config = {'fault_proxy_binary': '/fault-proxy', 'host_budget_file': '/budget', 'public_address': '10.0.0.2', 'fixture_shape': 'cluster'}
        docker = {name: {'cpu_millis': 8000} for name in runner.NODES}
        kube = {name: {'allocatable': {'cpu_millis': 7000}} for name in runner.NODES}
        installed = {'deployment_uid': 'd', 'configmap_uid': 'c', 'tcp_crd_uid': 't'}
        plan = {'attempt_paths': {'plan': '/plan'}, 'nodes': {name: {**docker[name], **kube[name]} for name in runner.NODES},
                'host_budget': {'ok': True}, 'host_budget_sha256': 'h', 'storage': {'reviewed': True},
                'haproxy': {'installed': installed, 'render_sha256': 'render'}, 'config_sha256': 'h',
                'lane_grant_sha256': 'h', 'server_binary_sha256': 'h', 'fault_proxy_binary_sha256': 'h', 'source_manifest_sha256': 's'}
        planner = SimpleNamespace(NODES=runner.NODES, PUBLIC_PORT=15432,
            validate_lane_grant=Mock(), validate_config=Mock(return_value=config),
            inspect_docker=Mock(return_value=(docker, {'ok': True}, 'h')),
            inspect_kubernetes=Mock(return_value=(kube, installed)), inspect_storage=Mock(return_value={'reviewed': True}),
            same_storage=lambda a, b: a == b, check_port_available=Mock(), dry_render=Mock(return_value='render'))
        with patch.object(runner, 'validate_preflight_plan', return_value=plan), \
                patch.object(runner, 'file_hash', return_value='h'), patch.object(runner, 'source_manifest_hash', return_value='s'), \
                patch.object(runner.shutil, 'disk_usage', return_value=SimpleNamespace(free=200 * 1024 ** 3)):
            args = (plan, Path('/root'), Path('/source'), Path('/config'), Path('/grant'), Path('/binary'), 1, planner)
            self.assertEqual(runner.reinspect_before_mutation(*args), config)
            planner.inspect_storage.return_value = {'reviewed': False}
            with self.assertRaisesRegex(ValueError, 'state changed'):
                runner.reinspect_before_mutation(*args)
            planner.inspect_storage.return_value = {'reviewed': True}
            planner.inspect_docker.return_value = (docker, {'ok': False}, 'h')
            with self.assertRaisesRegex(ValueError, 'state changed'):
                runner.reinspect_before_mutation(*args)

    def test_namespace_not_found_does_not_swallow_arbitrary_kubectl_failure(self):
        with patch.object(runner, 'command', return_value=b'') as command:
            self.assertIsNone(runner.kube_json(['/kubectl'], ['get', 'namespace', 'owned'], missing=True))
        self.assertIn('--ignore-not-found', command.call_args.args[0])
        self.assertNotIn('allowed', command.call_args.kwargs)
        with patch.object(runner, 'command', side_effect=ValueError('forbidden')):
            with self.assertRaisesRegex(ValueError, 'forbidden'):
                runner.kube_json(['/kubectl'], ['get', 'namespace', 'owned'], missing=True)

    def test_probe_passes_transport_shape_and_exact_issued_leaf(self):
        shape = runner.fixture.fixture('cluster')
        endpoint = {'host': 'database-15432.example.test', 'port': 15432, 'address': '10.0.0.2'}
        with patch.object(runner, 'command', return_value=b'PASS verified\n') as command:
            runner.probe(['/kubectl'], 'probe-namespace', 'allowed', '/probe', endpoint,
                         'native', 'verified', shape=shape, fingerprint='a' * 64)
        argv = command.call_args.args[0]
        self.assertEqual(argv[argv.index('--purpose') + 1], 'native')
        self.assertEqual(argv[argv.index('--shards') + 1], '2')
        self.assertEqual(argv[argv.index('--replicas') + 1], '2')
        self.assertEqual(argv[argv.index('--expect-fingerprint') + 1], 'a' * 64)
        self.assertNotIn('CLICKHOUSE_PASSWORD', ' '.join(argv))
        with patch.object(runner, 'command', return_value=b'PASS wrong-ca-rejected\n'):
            with self.assertRaises(ValueError):
                runner.probe(['/kubectl'], 'probe', 'allowed', '/probe', endpoint, 'https', 'verified')

    def test_checkpoint_changes_schedule_not_operation_state(self):
        gate = runner.DurableCheckpoints('/psql', {'PGDATABASE': 'owned-control'})
        with patch.object(gate, 'sql', return_value='1') as sql:
            gate.install()
            installed = sql.call_args.args[0]
            self.assertIn('NEW.next_attempt_at=', installed)
            self.assertNotIn('NEW.phase=', installed.split('THEN', 1)[1])
            gate.resume('operation-1', 'database-1', 'endpoint-1', 'accepted', 'identity')
            resumed = sql.call_args.args[0]
            self.assertIn("AND status='queued' AND phase='accepted' AND lease='' AND lease_until IS NULL", resumed)
            self.assertIn("database_id='database-1' AND endpoint_id='endpoint-1'", resumed)
            self.assertNotIn('SET phase=', resumed.split('WITH resumed AS', 1)[1])
        with patch.object(gate, 'sql', return_value='0'):
            with self.assertRaisesRegex(ValueError, 'real stored operation'):
                gate.resume('operation-1', 'database-1', 'endpoint-1', 'accepted', 'identity')
        with patch.object(gate, 'sql') as sql:
            with self.assertRaises(ValueError):
                gate.resume("x'; DROP TABLE users", 'database-1', 'endpoint-1', 'accepted', 'identity')
            sql.assert_not_called()

    def test_checkpoint_requires_actual_queued_phase(self):
        api = Mock()
        api.call.side_effect = [{'status': 'running', 'phase': 'identity'},
                                {'status': 'queued', 'phase': 'identity'}]
        with patch.object(runner.time, 'sleep'):
            result = runner.wait_checkpoint(api, 'operation', 'identity')
        self.assertEqual(result, {'status': 'queued', 'phase': 'identity'})
        api.call.side_effect = [{'status': 'succeeded', 'phase': 'configured'}]
        with self.assertRaisesRegex(ValueError, 'before the required'):
            runner.wait_checkpoint(api, 'operation', 'identity')

    def test_public_route_maps_both_transports_and_refuses_plaintext_ports(self):
        for purpose, port in (('native', 9440), ('https', 8443)):
            endpoint = {'spec': {'purpose': purpose, 'max_connections': 8}, 'allocation': {'port': 15432}}
            route = {'spec': [{'service': {'name': 'database', 'port': port}, 'frontend': {
                'maxconn': 8, 'binds': {'v4': {'address': '0.0.0.0', 'port': 15432}},
                'acl_list': [{'acl_name': 'allowed_source', 'criterion': 'src', 'value': '10.0.0.1/32'}],
                'tcp_request_rule_list': [{'type': 'connection', 'action': 'reject',
                                          'cond': 'unless', 'cond_test': 'allowed_source'}]}}]}
            runner.validate_route(route, endpoint, '10.0.0.1')
            route['spec'][0]['service']['port'] = 9000
            with self.assertRaises(ValueError):
                runner.validate_route(route, endpoint, '10.0.0.1')

    def test_public_policy_never_opens_keeper_or_arbitrary_proxy_pods(self):
        config = {'proxy_namespace': 'owned-proxy', 'proxy_release': 'owned-release'}
        policy = {'metadata': {'labels': {'hakopod.io/database-id': 'db', 'app.kubernetes.io/managed-by': 'hakopod'}},
                  'spec': {'ingress': [{'from': [{
                      'namespaceSelector': {'matchLabels': {'kubernetes.io/metadata.name': 'owned-proxy'}},
                      'podSelector': {'matchLabels': {'app.kubernetes.io/name': 'kubernetes-ingress',
                                                    'app.kubernetes.io/instance': 'owned-release'}}}],
                      'ports': [{'port': 9440, 'protocol': 'TCP'}, {'port': 8443, 'protocol': 'TCP'}]}]}}
        with patch.object(runner, 'kube_json', return_value=policy):
            runner.inspect_public_network_policy([], 'db', config, True)
            with self.assertRaises(ValueError):
                runner.inspect_public_network_policy([], 'db', config, False)
            policy['spec']['ingress'][0]['ports'].append({'port': 9281, 'protocol': 'TCP'})
            with self.assertRaises(ValueError):
                runner.inspect_public_network_policy([], 'db', config, True)

    def test_san_updates_cannot_hide_keeper_rollout(self):
        identity = {'database-ca': 'ca', 'database-tls': 'internal', 'keeper_template': 'template',
                    'keeper_uids': ['a', 'b', 'c'], 'database-client-tls': 'leaf-1'}
        updated = dict(identity, **{'database-client-tls': 'leaf-2'})
        runner.require_internal_identity_unchanged(identity, updated)
        updated['keeper_uids'] = ['a', 'b', 'd']
        with self.assertRaises(ValueError):
            runner.require_internal_identity_unchanged(identity, updated)

    def test_client_certificate_requires_exact_ordered_private_and_public_names(self):
        shape = runner.fixture.fixture('standalone')
        expected = [
            'database', 'database.hdb-db', 'database.hdb-db.svc',
            'database.hdb-db.svc.cluster.local', 'chi-database-managed-0-0',
            'chi-database-managed-0-0.hdb-db.svc',
            'chi-database-managed-0-0.hdb-db.svc.cluster.local',
            'chi-database-managed-0-0-0.chi-database-managed-0-0.hdb-db.svc.cluster.local',
            'a.example.test', 'z.example.test',
        ]
        self.assertEqual(runner.expected_client_dns_names(
            'db', shape, ('z.example.test', 'a.example.test', 'z.example.test')), expected)
        cluster = runner.fixture.fixture('cluster')
        clustered = runner.expected_client_dns_names('db', cluster, ('public.example.test',))
        self.assertEqual(clustered, expected[:4] + [
            'chi-database-managed-0-0', 'chi-database-managed-0-0.hdb-db.svc',
            'chi-database-managed-0-0.hdb-db.svc.cluster.local',
            'chi-database-managed-0-0-0.chi-database-managed-0-0.hdb-db.svc.cluster.local',
            'chi-database-managed-0-1', 'chi-database-managed-0-1.hdb-db.svc',
            'chi-database-managed-0-1.hdb-db.svc.cluster.local',
            'chi-database-managed-0-1-0.chi-database-managed-0-1.hdb-db.svc.cluster.local',
            'chi-database-managed-1-0', 'chi-database-managed-1-0.hdb-db.svc',
            'chi-database-managed-1-0.hdb-db.svc.cluster.local',
            'chi-database-managed-1-0-0.chi-database-managed-1-0.hdb-db.svc.cluster.local',
            'chi-database-managed-1-1', 'chi-database-managed-1-1.hdb-db.svc',
            'chi-database-managed-1-1.hdb-db.svc.cluster.local',
            'chi-database-managed-1-1-0.chi-database-managed-1-1.hdb-db.svc.cluster.local',
            'database-keeper-0.hdb-db.svc.cluster.local',
            'database-keeper-1.hdb-db.svc.cluster.local',
            'database-keeper-2.hdb-db.svc.cluster.local', 'public.example.test'])
        facts = runner.require_exact_client_dns_names({'client_names': expected}, 'db', shape,
                                                      ('z.example.test', 'a.example.test'))
        self.assertEqual(facts['dns_name_count'], len(expected))
        self.assertRegex(facts['dns_names_sha256'], r'^[a-f0-9]{64}$')
        for actual in (expected[:-1], expected + ['extra.example.test'], expected + [expected[-1]],
                       expected[:1] + list(reversed(expected[1:3])) + expected[3:]):
            with self.assertRaisesRegex(ValueError, 'exact ordered identity'):
                runner.require_exact_client_dns_names({'client_names': actual}, 'db', shape,
                                                      ('z.example.test', 'a.example.test'))

    def test_partial_volume_inventory_and_late_bindings_are_checked(self):
        claim = {'metadata': {'name': 'data', 'uid': 'claim-uid', 'namespace': 'hdb-db'},
                 'spec': {'volumeName': 'owned-pv'}, 'status': {'phase': 'Bound'}}
        volume = {'metadata': {'name': 'owned-pv', 'uid': 'pv-uid'}, 'spec': {
            'claimRef': {'name': 'data', 'uid': 'claim-uid', 'namespace': 'hdb-db'},
            'persistentVolumeReclaimPolicy': 'Delete'}}
        with patch.object(runner, 'kube_json', side_effect=[{'items': [claim]}, volume]):
            result = runner.owned_volume_inventory([], 'db', runner.fixture.fixture('cluster'), complete=False)
        self.assertEqual(result, [{'name': 'owned-pv', 'uid': 'pv-uid', 'claim_uid': 'claim-uid'}])
        # Even without an earlier inventory, a late bound PV blocks cleanup.
        with patch.object(runner, 'kube_json', return_value={'items': [volume]}), \
                patch.object(runner.time, 'monotonic', side_effect=[0, 0, 2]), patch.object(runner.time, 'sleep'):
            with self.assertRaisesRegex(ValueError, 'not reclaimed'):
                runner.wait_volume_cleanup([], [], 'hdb-db', timeout=1)

    def test_lock_rejects_parallel_native_mutation(self):
        with tempfile.TemporaryDirectory() as folder:
            first = runner.acquire_acceptance_lock(Path(folder))
            try:
                with self.assertRaisesRegex(ValueError, 'another database'):
                    runner.acquire_acceptance_lock(Path(folder))
            finally:
                first.close()


if __name__ == '__main__':
    unittest.main()
