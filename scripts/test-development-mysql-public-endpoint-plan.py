"""Non-network safety checks for the public-endpoint read-only planner."""
from datetime import datetime, timedelta, timezone
import importlib.util
import json
from pathlib import Path
import sys
import tempfile
import unittest
from unittest.mock import patch


spec = importlib.util.spec_from_file_location(
    'mysql_public_endpoint_plan',
    Path(__file__).with_name('plan-development-mysql-public-endpoint-acceptance.py'))
planner = importlib.util.module_from_spec(spec)
spec.loader.exec_module(planner)


class EndpointPlanSafetyTests(unittest.TestCase):
    def protected_json(self, folder, name, value):
        path = Path(folder) / name
        path.write_text(json.dumps(value))
        path.chmod(0o600)
        return path

    def test_lane_grant_requires_both_current_owners(self):
        now = datetime(2026, 10, 1, tzinfo=timezone.utc)
        grant = {'schema_version': 1, 'cluster': planner.CONTEXT,
                 'purpose': 'mysql-public-endpoint', 'approvals': ['root', 'vitess_backend'],
                 'expires_at': (now + timedelta(hours=1)).isoformat(), 'nonce': 'a' * 32}
        with tempfile.TemporaryDirectory() as folder:
            path = self.protected_json(folder, 'grant.json', grant)
            planner.validate_lane_grant(path, now)
            for approvals in (['root'], ['vitess_backend'], ['root', 'other']):
                grant['approvals'] = approvals
                path.write_text(json.dumps(grant))
                with self.assertRaises(ValueError):
                    planner.validate_lane_grant(path, now)
            grant['approvals'] = ['root', 'vitess_backend']
            grant['expires_at'] = (now + timedelta(hours=9)).isoformat()
            path.write_text(json.dumps(grant))
            with self.assertRaises(ValueError):
                planner.validate_lane_grant(path, now)

    def test_protected_inputs_reject_group_access_and_links(self):
        with tempfile.TemporaryDirectory() as folder:
            path = Path(folder) / 'config'
            path.write_bytes(b'{}')
            path.chmod(0o640)
            with self.assertRaises(ValueError):
                planner.protected(path)
            path.chmod(0o600)
            self.assertEqual(planner.protected(path), b'{}')
            link = Path(folder) / 'link'
            link.symlink_to(path)
            with self.assertRaises(OSError):
                planner.protected(link)

    def test_shared_capacity_module_uses_declared_docker_limits(self):
        limits = planner.capacity.docker_declared_limits(
            {'NanoCpus': 4_000_000_000, 'CpuQuota': 200000, 'CpuPeriod': 100000,
             'CpusetCpus': '0-7', 'Memory': 4 * 1024 ** 3})
        self.assertEqual(limits, {'cpu_millis': 2000, 'memory_bytes': 4 * 1024 ** 3,
                                  'cpuset': set(range(8))})
        with self.assertRaises(ValueError):
            planner.capacity.docker_declared_limits(
                {'NanoCpus': 0, 'CpuQuota': 0, 'CpuPeriod': 0, 'CpusetCpus': '', 'Memory': 1})

    def test_current_misreported_node_capacity_is_rejected(self):
        docker = {'cpu_millis': 1500, 'memory_bytes': 4 * 1024 ** 3}
        advertised = {'cpu_millis': 8000, 'memory_bytes': 4 * 1024 ** 3}
        occupied = {'cpu_millis': 100, 'memory_bytes': 128 * 1024 ** 2}
        with self.assertRaisesRegex(ValueError, 'allocatable capacity exceeds'):
            planner.available_node_capacity(docker, advertised, occupied)

    def test_existing_requests_conservatively_sum_init_and_can_exhaust_capacity(self):
        pods = [{'status': {'phase': 'Running'}, 'spec': {
            'containers': [{'resources': {'requests': {'cpu': '700m', 'memory': '700Mi'}}}],
            'initContainers': [{'resources': {'requests': {'cpu': '900m', 'memory': '800Mi'}}}],
            'overhead': {'cpu': '50m', 'memory': '32Mi'},
        }}]
        occupied = planner.pod_requests(pods)
        self.assertEqual(occupied, {'cpu_millis': 1650, 'memory_bytes': 1532 * 1024 ** 2})
        with self.assertRaisesRegex(ValueError, 'exceed the node capacity'):
            planner.available_node_capacity({'cpu_millis': 1500, 'memory_bytes': 1024 ** 3},
                                            {'cpu_millis': 1200, 'memory_bytes': 1024 ** 3}, occupied)

    def test_command_bounds_stdout_and_stderr_while_collecting(self):
        for stream in ('stdout', 'stderr'):
            code = f'import sys; sys.{stream}.write("x" * 200000); sys.{stream}.flush()'
            with self.subTest(stream=stream), self.assertRaisesRegex(ValueError, 'exceeded its limit'):
                planner.command([sys.executable, '-c', code], maximum=1024)

    def test_command_does_not_block_when_child_ignores_stdin(self):
        code = 'import sys; sys.stdout.write("x" * 200000); sys.stdout.flush()'
        with self.assertRaisesRegex(ValueError, 'exceeded its limit'):
            planner.command([sys.executable, '-c', code], stdin=b'x' * (1024 * 1024), maximum=1024)

    def render(self, extra_host_port=''):
        return f'''---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: proxy
spec:
  template:
    spec:
      nodeSelector:
        kubernetes.io/hostname: k3d-hakopod-dev-server-0
      containers:
        - name: controller
          image: {planner.HAPROXY_IMAGE}
          ports:
            - containerPort: 15432
              hostPort: 15432
{extra_host_port}---
apiVersion: v1
kind: Service
metadata:
  name: proxy
spec:
  type: ClusterIP
  ports:
    - name: postgres
      port: 15432
'''.encode()

    def test_render_allows_exactly_one_host_port(self):
        self.assertRegex(planner.validate_render(self.render()), r'^[a-f0-9]{64}$')
        with self.assertRaisesRegex(ValueError, 'sole host port'):
            planner.validate_render(self.render('            - containerPort: 80\n              hostPort: 80\n'))

    def test_render_values_disable_web_ports_and_pin_the_server(self):
        values = planner.render_values({'ingress_class': 'haproxy'})['controller']
        self.assertEqual(values['deployment']['hostPorts'], {'http': 0, 'https': 0, 'stat': 0})
        self.assertEqual(values['service']['tcpPorts'],
                         [{'name': 'mysql-15432', 'port': 15432, 'targetPort': 15432}])
        self.assertFalse(any(values['service']['enablePorts'].values()))
        self.assertEqual(values['nodeSelector']['kubernetes.io/hostname'], list(planner.NODES)[0])

    def test_installed_haproxy_requires_owned_pinned_single_port_resources(self):
        labels = {'app.kubernetes.io/managed-by': 'Helm', 'app.kubernetes.io/instance': 'endpoint'}
        deployment = {'metadata': {'uid': 'deployment-uid', 'labels': labels}, 'spec': {'replicas': 1,
                      'template': {'spec': {'nodeSelector': {'kubernetes.io/hostname': list(planner.NODES)[0]},
                      'containers': [{'image': planner.HAPROXY_IMAGE,
                                      'ports': [{'containerPort': 15432, 'hostPort': 15432}]}]}}}}
        config = {'metadata': {'uid': 'config-uid', 'labels': labels}}
        crd = {'metadata': {'uid': 'crd-uid'}, 'spec': {'group': 'ingress.v3.haproxy.org',
               'names': {'plural': 'tcps'}, 'versions': [{'name': 'v3', 'served': True}]}}
        self.assertEqual(planner.validate_installed_haproxy(deployment, config, crd, 'endpoint'),
                         {'deployment_uid': 'deployment-uid', 'configmap_uid': 'config-uid',
                          'tcp_crd_uid': 'crd-uid'})
        deployment['spec']['template']['spec']['containers'][0]['ports'].append(
            {'containerPort': 80, 'hostPort': 80})
        with self.assertRaisesRegex(ValueError, 'host ports'):
            planner.validate_installed_haproxy(deployment, config, crd, 'endpoint')

    def test_helm_operation_is_template_only(self):
        rendered = self.render()
        calls = []
        def capture(args, **kwargs):
            calls.append((args, kwargs))
            return rendered
        config = {'helm': '/helm', 'haproxy_chart': '/chart.tgz',
                  'proxy_namespace': 'haproxy-controller', 'ingress_class': 'haproxy'}
        with patch.object(planner, 'command', side_effect=capture):
            planner.dry_render(config)
        self.assertEqual(calls[0][0][1], 'template')
        self.assertNotIn('install', calls[0][0])
        self.assertNotIn('upgrade', calls[0][0])

    def test_attempt_paths_include_plan_and_must_be_new(self):
        with tempfile.TemporaryDirectory() as folder:
            paths = planner.attempt_paths(Path(folder), 7)
            self.assertEqual(set(paths), {'work', 'events', 'evidence', 'plan'})
            self.assertTrue(all('mysql-public-endpoint-v7' in value for value in paths.values()))


if __name__ == '__main__':
    unittest.main()
