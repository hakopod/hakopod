"""Non-network safety checks for the MySQL public endpoint runner."""
from datetime import datetime, timedelta, timezone
import importlib.util
import json
import os
from pathlib import Path
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import patch


spec = importlib.util.spec_from_file_location(
    'mysql_public_endpoint_acceptance',
    Path(__file__).with_name('run-development-mysql-public-endpoint-acceptance.py'))
runner = importlib.util.module_from_spec(spec)
spec.loader.exec_module(runner)


class AcceptanceSafetyTests(unittest.TestCase):
    def protected_json(self, folder, name, value):
        path = Path(folder) / name
        path.write_text(json.dumps(value))
        path.chmod(0o600)
        return path

    def plan_host_budget(self, nodes, cpu_millis, memory_bytes):
        docker = []
        claims = []
        for index, (name, node) in enumerate(nodes.items()):
            path = f'/system.slice/docker-{index}.scope/init'
            docker.append({'name': name, 'container_id': chr(ord('a') + index) * 64,
                           'class': 'development-node', 'cgroup_path': path,
                           'cpu_millis': node['cpu_millis'], 'memory_bytes': node['memory_bytes'],
                           'reserved_cpu_millis': node['cpu_millis'],
                           'reserved_memory_bytes': node['memory_bytes'], 'cpuset': node['cpuset'],
                           'image_id': node['image_id']})
            claims.append((path, node['cpu_millis'], node['memory_bytes']))
        docker.append({'name': 'control', 'container_id': 'f' * 64, 'class': 'control-postgres',
                       'cgroup_path': '/system.slice/docker-control.scope', 'cpu_millis': 500,
                       'memory_bytes': 512 * 1024 ** 2, 'reserved_cpu_millis': 500,
                       'reserved_memory_bytes': 512 * 1024 ** 2, 'cpuset': [8],
                       'image_id': 'sha256:' + 'f' * 64})
        host = [{'name': 'runner', 'class': 'acceptance-runner', 'cgroup_path': '/runner.scope',
                 'executables': ['python3'], 'cpu_millis': 1000, 'memory_bytes': 3 * 1024 ** 3,
                 'reserved_cpu_millis': 1000, 'reserved_memory_bytes': 3 * 1024 ** 3,
                 'cpuset': [9]}]
        claims.extend([('/system.slice/docker-control.scope', 500, 512 * 1024 ** 2),
                       ('/runner.scope', 1000, 3 * 1024 ** 3)])
        ancestors = []
        for path in sorted({ancestor for claim, _, _ in claims
                            for ancestor in runner.capacity._ancestors(claim)}):
            cpu_claim = sum(cpu for claim, cpu, _ in claims if runner.capacity._descendant(claim, path))
            memory_claim = sum(memory for claim, _, memory in claims if runner.capacity._descendant(claim, path))
            if path == '/':
                cpu_claim += 1000
                memory_claim += 2 * 1024 ** 3
            ancestors.append({'cgroup_path': path,
                              'cpu_millis': cpu_millis if path == '/' else cpu_claim,
                              'memory_bytes': memory_bytes if path == '/' else memory_claim,
                              'claimed_cpu_millis': cpu_claim, 'claimed_memory_bytes': memory_claim})
        return {'schema_version': 1, 'cpu_millis': cpu_millis, 'memory_bytes': memory_bytes,
                'online_cpus': list(range(12)), 'reviewed_by': ['root'],
                'reserved': {'root_cpu_millis': 1000, 'root_memory_bytes': 2 * 1024 ** 3,
                             'runner_cpu_millis': 1000, 'runner_memory_bytes': 3 * 1024 ** 3,
                             'control_postgres_cpu_millis': 500,
                             'control_postgres_memory_bytes': 512 * 1024 ** 2},
                'docker_workloads': docker, 'host_workloads': host, 'ancestor_limits': ancestors}

    def test_protected_inputs_refuse_group_access_and_symlinks(self):
        with tempfile.TemporaryDirectory() as folder:
            path = Path(folder) / 'input'
            path.write_bytes(b'protected')
            path.chmod(0o640)
            with self.assertRaises(ValueError):
                runner.protected(path)
            path.chmod(0o600)
            self.assertEqual(runner.protected(path), b'protected')
            link = Path(folder) / 'link'
            link.symlink_to(path)
            with self.assertRaises(OSError):
                runner.protected(link)

    def test_lane_grant_requires_both_current_coordinators(self):
        with tempfile.TemporaryDirectory() as folder:
            grant = {'schema_version': 1, 'cluster': runner.CONTEXT, 'purpose': 'mysql-public-endpoint',
                     'approvals': ['root', 'vitess_backend'],
                     'expires_at': (datetime.now(timezone.utc) + timedelta(hours=1)).isoformat(),
                     'nonce': 'a' * 32}
            path = self.protected_json(folder, 'grant.json', grant)
            runner.validate_lane_grant(path)
            for approvals in (['root'], ['vitess_backend'], ['root', 'other']):
                grant['approvals'] = approvals
                path.write_text(json.dumps(grant))
                with self.assertRaises(ValueError):
                    runner.validate_lane_grant(path)
            grant['approvals'] = ['root', 'vitess_backend']
            grant['expires_at'] = (datetime.now(timezone.utc) + timedelta(hours=9)).isoformat()
            path.write_text(json.dumps(grant))
            with self.assertRaises(ValueError):
                runner.validate_lane_grant(path)

    def test_native_runner_requires_a_fresh_matching_read_only_plan(self):
        with tempfile.TemporaryDirectory() as folder:
            root = Path(folder)
            source = root / 'public-endpoint-source-v1'
            source.mkdir()
            binary = source / 'hakopod-server'
            binary.write_bytes(b'binary')
            fault_binary = source / 'fault-proxy'
            fault_binary.write_bytes(b'helper')
            config = {name: '' for name in runner.CONFIG_FIELDS}
            config['fault_proxy_binary'] = str(fault_binary)
            config['probe_image'] = 'registry.test/probe@sha256:' + 'a' * 64
            host_budget = {
                'schema_version': 1, 'cluster': runner.CONTEXT, 'reviewed_by': ['root'],
                'root_reserve': {'cpu_millis': 1000, 'memory_bytes': 2 * 1024 ** 3,
                                 'cgroups': [{'cgroup_path': '/', 'executables': ['[kernel]']},
                                             {'cgroup_path': '/init.scope', 'executables': ['systemd']}]},
                'docker_workloads': [
                    {'name': runner.NODES[0], 'container_id': 'a' * 64, 'class': 'development-node',
                     'cpu_millis': 4000, 'memory_bytes': 8 * 1024 ** 3},
                    {'name': runner.NODES[1], 'container_id': 'b' * 64, 'class': 'development-node',
                     'cpu_millis': 4000, 'memory_bytes': 8 * 1024 ** 3},
                    {'name': 'control', 'container_id': 'c' * 64, 'class': 'control-postgres',
                     'cpu_millis': 500, 'memory_bytes': 512 * 1024 ** 2}],
                'host_workloads': [
                    {'name': 'runner', 'class': 'acceptance-runner', 'cgroup_path': '/runner.scope',
                     'executables': ['python3'], 'cpu_millis': 1000, 'memory_bytes': 3 * 1024 ** 3}],
            }
            host_budget_path = self.protected_json(folder, 'host-budget.json', host_budget)
            config['host_budget_file'] = str(host_budget_path)
            config_path = self.protected_json(folder, 'config.json', config)
            grant_path = Path(folder) / 'grant.json'
            grant_path.write_bytes(b'grant')
            grant_path.chmod(0o600)
            attempt = 3
            stem = f'mysql-public-endpoint-v{attempt}'
            plan_path = root / (stem + '.plan.json')
            now = datetime.now(timezone.utc)
            plan = {
                'schema_version': 1, 'status': 'ready', 'mutation_mode': 'read_only_preflight',
                'generated_at': now.isoformat(), 'grant_expires_at': (now + timedelta(hours=1)).isoformat(),
                'context': runner.CONTEXT, 'attempt': attempt, 'source': str(source),
                'source_manifest_sha256': 'b' * 64, 'config_sha256': runner.file_hash(config_path),
                'host_budget_sha256': runner.file_hash(host_budget_path),
                'lane_grant_sha256': runner.file_hash(grant_path),
                'server_binary_sha256': runner.file_hash(binary),
                'fault_proxy_binary_sha256': runner.file_hash(fault_binary),
                'images': {'haproxy': runner.HAPROXY_IMAGE, 'mysql_server': runner.MYSQL_SERVER_IMAGE,
                           'mysql_operator': runner.MYSQL_OPERATOR_IMAGE,
                           'mysql_router': runner.MYSQL_ROUTER_IMAGE, 'probe': config['probe_image']},
                'nodes': {
                    runner.NODES[0]: {'role': 'server', 'cpus': 4, 'cpu_millis': 4000,
                                      'memory_bytes': 8 * 1024 ** 3, 'cpuset': [0, 1, 2, 3],
                                      'image_id': 'sha256:' + 'c' * 64, 'internal_ip': '172.20.0.2',
                                      'kubelet_version': 'v1.35.0',
                                      'allocatable': {'cpu_millis': 3500, 'memory_bytes': 7 * 1024 ** 3},
                                      'occupied_requests': {'cpu_millis': 500, 'memory_bytes': 512 * 1024 ** 2},
                                      'remaining_before_plan': {'cpu_millis': 3000,
                                                                'memory_bytes': 6656 * 1024 ** 2},
                                      'planned_peak': {'cpu_millis': 1400, 'memory_bytes': 2784 * 1024 ** 2,
                                                       'members': 1, 'routers': 1,
                                                       'surge_members': 1, 'surge_routers': 0},
                                      'remaining_after_peak': {'cpu_millis': 1600,
                                                               'memory_bytes': 3872 * 1024 ** 2}},
                    runner.NODES[1]: {'role': 'agent', 'cpus': 4, 'cpu_millis': 4000,
                                      'memory_bytes': 8 * 1024 ** 3, 'cpuset': [4, 5, 6, 7],
                                      'image_id': 'sha256:' + 'd' * 64, 'internal_ip': '172.20.0.4',
                                      'kubelet_version': 'v1.35.0',
                                      'allocatable': {'cpu_millis': 3500, 'memory_bytes': 7 * 1024 ** 3},
                                      'occupied_requests': {'cpu_millis': 500, 'memory_bytes': 512 * 1024 ** 2},
                                      'remaining_before_plan': {'cpu_millis': 3000,
                                                                'memory_bytes': 6656 * 1024 ** 2},
                                      'planned_peak': {'cpu_millis': 1500, 'memory_bytes': 2912 * 1024 ** 2,
                                                       'members': 2, 'routers': 1,
                                                       'surge_members': 0, 'surge_routers': 1},
                                      'remaining_after_peak': {'cpu_millis': 1500,
                                                               'memory_bytes': 3744 * 1024 ** 2}},
                },
                'host_budget': None,
                'haproxy': {'chart_sha256': runner.HAPROXY_CHART_SHA256,
                            'render_sha256': 'e' * 64, 'node': runner.NODES[0], 'host_ports': [15432],
                            'installed': {'deployment_uid': 'deployment-uid', 'configmap_uid': 'config-uid',
                                          'tcp_crd_uid': 'crd-uid'}},
                'attempt_paths': {'work': str(root / (stem + '.work')),
                                  'events': str(root / (stem + '.jsonl')),
                                  'evidence': str(root / (stem + '.evidence.json')),
                                  'plan': str(plan_path)},
                'cleanup_contract': ['one', 'two', 'three', 'four', 'five'],
            }
            plan['host_budget'] = self.plan_host_budget(plan['nodes'], 12000, 32 * 1024 ** 3)
            plan_path.write_text(json.dumps(plan))
            plan_path.chmod(0o600)
            with patch.object(runner, 'source_manifest_hash', return_value='b' * 64):
                runner.validate_preflight_plan(plan_path, root, source, config_path, grant_path,
                                               binary, attempt)
                fault_binary.write_bytes(b'changed helper')
                with self.assertRaises(ValueError):
                    runner.validate_preflight_plan(plan_path, root, source, config_path, grant_path,
                                                   binary, attempt)
                fault_binary.write_bytes(b'helper')
                plan['nodes'][runner.NODES[1]]['allocatable']['cpu_millis'] = 2100
                plan_path.write_text(json.dumps(plan))
                with self.assertRaises(ValueError):
                    runner.validate_preflight_plan(plan_path, root, source, config_path, grant_path,
                                                   binary, attempt)

    def test_acceptance_lock_excludes_other_database_runners(self):
        with tempfile.TemporaryDirectory() as folder:
            root = Path(folder)
            first = runner.acquire_acceptance_lock(root)
            try:
                with self.assertRaisesRegex(ValueError, 'owns the mutation lock'):
                    runner.acquire_acceptance_lock(root)
            finally:
                first.close()
            second = runner.acquire_acceptance_lock(root)
            second.close()

    def test_mutation_boundary_requires_an_exact_fresh_reinspection(self):
        with tempfile.TemporaryDirectory() as folder:
            root = Path(folder)
            source = root / 'mysql-public-endpoint-source-v4'
            source.mkdir()
            config_path = root / 'config.json'
            grant_path = root / 'grant.json'
            binary = source / 'hakopod-server'
            host_budget_file = root / 'host-budget.json'
            for path in (config_path, grant_path, binary, host_budget_file):
                path.write_bytes(b'x')
            docker_nodes = {name: {'role': role, 'cpu_millis': 4000}
                            for name, role in zip(runner.NODES, ('server', 'agent'))}
            kubernetes_nodes = {name: {'allocatable': {'cpu_millis': 3500}}
                                for name in runner.NODES}
            nodes = {name: {**docker_nodes[name], **kubernetes_nodes[name]} for name in runner.NODES}
            host_budget = {'reviewed': True}
            installed = {'deployment_uid': 'd', 'configmap_uid': 'c', 'tcp_crd_uid': 't'}
            plan_path = root / 'mysql-public-endpoint-v4.plan.json'
            plan = {
                'attempt_paths': {'plan': str(plan_path)}, 'nodes': nodes,
                'host_budget': host_budget, 'host_budget_sha256': 'h',
                'haproxy': {'installed': installed, 'render_sha256': 'r'},
                'config_sha256': 'h', 'lane_grant_sha256': 'h',
                'server_binary_sha256': 'h', 'fault_proxy_binary_sha256': 'h', 'source_manifest_sha256': 's',
            }
            planner = SimpleNamespace(
                NODES=runner.NODES, PUBLIC_PORT=15432,
                validate_lane_grant=lambda _path: None,
                validate_config=lambda *_args: {'host_budget_file': str(host_budget_file),
                                                'public_address': '127.0.0.1', 'fault_proxy_binary': str(binary)},
                inspect_docker=lambda _config: (docker_nodes, host_budget, 'h'),
                inspect_kubernetes=lambda _config, _docker: (kubernetes_nodes, installed),
                check_port_available=lambda *_args: None,
                dry_render=lambda _config: 'r',
            )
            with (patch.object(runner, 'validate_preflight_plan', return_value=plan),
                  patch.object(runner, 'file_hash', return_value='h'),
                  patch.object(runner, 'source_manifest_hash', return_value='s')):
                self.assertEqual(
                    runner.reinspect_before_mutation(
                        plan, root, source, config_path, grant_path, binary, 4, planner),
                    {'host_budget_file': str(host_budget_file), 'public_address': '127.0.0.1', 'fault_proxy_binary': str(binary)})
                for label, docker_result, host_result, kube_result in (
                        ('Docker', {**docker_nodes, runner.NODES[0]: {'role': 'server', 'cpu_millis': 3000}},
                         host_budget, kubernetes_nodes),
                        ('cgroup', docker_nodes, {'reviewed': False}, kubernetes_nodes),
                        ('Kubernetes', docker_nodes, host_budget,
                         {**kubernetes_nodes, runner.NODES[1]: {'allocatable': {'cpu_millis': 3400}}})):
                    with self.subTest(label=label):
                        planner.inspect_docker = lambda _config, d=docker_result, h=host_result: (d, h, 'h')
                        planner.inspect_kubernetes = lambda _config, _docker, k=kube_result: (k, installed)
                        with self.assertRaisesRegex(ValueError, 'state changed'):
                            runner.reinspect_before_mutation(
                                plan, root, source, config_path, grant_path, binary, 4, planner)
                planner.inspect_docker = lambda _config: (docker_nodes, host_budget, 'h')
                planner.inspect_kubernetes = lambda *_args: (_ for _ in ()).throw(
                    ValueError('public endpoint port already has an allocation claim'))
                with self.assertRaisesRegex(ValueError, 'allocation claim'):
                    runner.reinspect_before_mutation(
                        plan, root, source, config_path, grant_path, binary, 4, planner)

    def test_api_refuses_non_loopback_origins_and_redirects(self):
        for origin in ('https://127.0.0.1:8080', 'http://example.test', 'http://user@localhost',
                       'http://localhost/api', 'http://localhost?next=other'):
            with self.assertRaises(ValueError):
                runner.API(origin, 'unused')
        with self.assertRaises(ValueError):
            runner.NoRedirect().redirect_request(None, None, 302, '', {}, 'http://elsewhere.test')

    def test_operator_domains_require_canonical_dns_labels(self):
        self.assertTrue(runner.valid_domain('database.development.example'))
        for value in ('single-label', 'Upper.example', 'bad..example', '-bad.example', 'bad-.example'):
            self.assertFalse(runner.valid_domain(value))

    def test_runner_requires_bounded_memory_and_one_cpu(self):
        runner.validate_cgroup_limits(str(3 * 1024 ** 3), '100000 100000')
        runner.validate_cgroup_limits(str(256 * 1024 ** 2), '50000 100000')
        for memory, cpu in (('max', '100000 100000'), (str(4 * 1024 ** 3), '100000 100000'),
                            (str(3 * 1024 ** 3), 'max 100000'), (str(3 * 1024 ** 3), '200000 100000')):
            with self.assertRaises(ValueError):
                runner.validate_cgroup_limits(memory, cpu)

    def test_postgres_admin_url_is_bounded_to_supported_options(self):
        env = runner.postgres_environment('postgresql://fixture:secret@127.0.0.1:5432/control?sslmode=require', 'owned')
        self.assertEqual(env['PGDATABASE'], 'owned')
        self.assertEqual(env['PGPASSWORD'], 'secret')
        self.assertEqual(env['PGSSLMODE'], 'require')
        for value in ('postgresql://fixture@127.0.0.1/control',
                      'postgresql://fixture:secret@127.0.0.1/control?target_session_attrs=read-write',
                      'https://fixture:secret@127.0.0.1/control'):
            with self.assertRaises(ValueError):
                runner.postgres_environment(value)

    def test_probe_fixtures_are_owned_pinned_and_isolated(self):
        calls = []
        original_command, original_wait = runner.command, runner.wait_pod
        try:
            runner.command = lambda args, **kwargs: calls.append((args, kwargs.get('stdin'))) or b''
            addresses = {'allowed': '172.18.0.2', 'denied': '172.18.0.3'}
            runner.wait_pod = lambda kube, namespace, name, timeout=120: {
                'status': {'phase': 'Running', 'podIP': addresses[name], 'hostIP': addresses[name]}}
            result = runner.create_probe_fixtures(
                ['kubectl'], 'hp-pe-fixture', 'pe-' + '1' * 16,
                'registry.test/probe@sha256:' + 'a' * 64, '/probe',
                {'allowed': runner.NODES[0], 'denied': runner.NODES[1]}, 'super-secret-fixture', 'certificate')
        finally:
            runner.command, runner.wait_pod = original_command, original_wait
        self.assertEqual(result, addresses)
        manifests = [json.loads(payload) for _, payload in calls if payload]
        self.assertEqual([item['kind'] for item in manifests], ['Namespace', 'Secret', 'Pod', 'Pod'])
        for pod in manifests[2:]:
            self.assertTrue(pod['spec']['hostNetwork'])
            self.assertFalse(pod['spec']['automountServiceAccountToken'])
            container = pod['spec']['containers'][0]
            self.assertIn('@sha256:', container['image'])
            self.assertTrue(container['securityContext']['readOnlyRootFilesystem'])
            self.assertEqual(container['securityContext']['capabilities']['drop'], ['ALL'])
            self.assertNotIn('super-secret-fixture', json.dumps(pod))

    def test_claim_inspection_refuses_wrong_owner(self):
        original = runner.kube_json
        good = {'metadata': {'labels': {'hakopod.io/database-public-endpoint-claim': 'true',
                                        'hakopod.io/database-id': 'database',
                                        'hakopod.io/database-public-endpoint-id': 'endpoint'}},
                'data': {'database_id': 'database', 'endpoint_id': 'endpoint'}}
        try:
            runner.kube_json = lambda *_args, **_kwargs: good
            self.assertIs(runner.inspect_claim([], 'proxy', 15432, 'database', 'endpoint'), good)
            good['data']['endpoint_id'] = 'other'
            with self.assertRaises(ValueError):
                runner.inspect_claim([], 'proxy', 15432, 'database', 'endpoint')
        finally:
            runner.kube_json = original

    def test_mysql_leaf_requires_exact_ordered_private_and_public_names(self):
        expected = runner.expected_mysql_dns_names('db', [
            'z.example.test', 'a.example.test', 'z.example.test'])
        self.assertEqual(expected[-2:], ['a.example.test', 'z.example.test'])
        snapshot = {'dns_names': expected, 'leaf_fingerprint': 'a' * 64}
        facts = runner.require_exact_mysql_dns_names(
            snapshot, 'db', ['z.example.test', 'a.example.test'], 'a' * 64)
        self.assertEqual(facts['dns_name_count'], len(expected))
        self.assertRegex(facts['dns_names_sha256'], r'^[a-f0-9]{64}$')
        for names in (expected[:-1], expected + ['extra.example.test'], expected + [expected[-1]],
                      list(reversed(expected))):
            with self.assertRaises(ValueError):
                runner.require_exact_mysql_dns_names(
                    {'dns_names': names, 'leaf_fingerprint': 'a' * 64}, 'db',
                    ['z.example.test', 'a.example.test'], 'a' * 64)
        for fingerprint in ('b' * 64, None, '', 'not-a-fingerprint'):
            with self.assertRaisesRegex(ValueError, 'API observation'):
                runner.require_exact_mysql_dns_names(
                    snapshot, 'db', ['z.example.test', 'a.example.test'], fingerprint)

    def test_route_validation_binds_source_cap_port_and_role(self):
        endpoint = {'spec': {'purpose': 'read_only', 'max_connections': 8},
                    'allocation': {'port': 15432}}
        route = {'spec': [{'service': {'name': 'database', 'port': 6447},
                           'frontend': {'maxconn': 8, 'binds': {'v4': {'address': '0.0.0.0', 'port': 15432}},
                                        'acl_list': [{'acl_name': 'allowed_source', 'criterion': 'src', 'value': '172.18.0.2/32'}],
                                        'tcp_request_rule_list': [{'type': 'connection', 'action': 'reject',
                                                                   'cond': 'unless', 'cond_test': 'allowed_source'}]}}]}
        runner.validate_route(route, endpoint, '172.18.0.2')
        route['spec'][0]['frontend']['maxconn'] = 9
        with self.assertRaises(ValueError):
            runner.validate_route(route, endpoint, '172.18.0.2')


if __name__ == '__main__':
    unittest.main()
