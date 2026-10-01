#!/usr/bin/env python3
"""Offline tests for the shared public-endpoint fault-proxy launcher."""
import importlib.util
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import Mock, patch


spec = importlib.util.spec_from_file_location('development_public_endpoint_fault_proxy', Path(__file__).with_name(
    'development_public_endpoint_fault_proxy.py'))
launcher = importlib.util.module_from_spec(spec)
spec.loader.exec_module(launcher)


class Process:
    def __init__(self):
        self.pid = 1234

    def poll(self):
        return None

    def wait(self, timeout):
        return 0


class FaultProxyLauncherTests(unittest.TestCase):
    def test_start_uses_immutable_namespaces_and_explicit_exec_filter(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            binary = root / 'fault-proxy'
            binary.write_bytes(b'helper')
            binary.chmod(0o700)
            kubeconfig = root / 'kubeconfig'
            kubeconfig.write_text('owned')
            kubeconfig.chmod(0o600)
            fault = launcher.FaultProxy(binary, launcher.file_hash(binary), '/kubectl', kubeconfig,
                                        'k3d-hakopod-dev', 'proxy', root)
            processes = [Process(), Process()]
            with patch.object(launcher.subprocess, 'Popen', side_effect=processes) as popen, \
                    patch.object(fault, 'wait_ready'), patch.object(fault, 'status', return_value={}):
                result = fault.start(mode='route_reload_after_mutation', database_namespace='hdb-database',
                                     route_namespace='hdb-database', route_resource='dbpe-endpoint')
            self.assertTrue(result.is_file())
            kubectl = popen.call_args_list[0].args[0]
            self.assertEqual(kubectl[kubectl.index('--accept-hosts') + 1], r'^127\.0\.0\.1$')
            self.assertIn('--reject-paths=^/api/.*/pods/.*/attach,^/api/.*/pods/.*/portforward', kubectl)
            self.assertNotIn('^/api/.*/pods/.*/exec', ' '.join(kubectl))
            helper = popen.call_args_list[1].args[0]
            self.assertEqual(helper[0], str(binary))
            self.assertEqual(helper[helper.index('--database-namespace') + 1], 'hdb-database')
            self.assertEqual(helper[helper.index('--route-resource') + 1], 'dbpe-endpoint')
            with self.assertRaises(ValueError):
                fault.start(mode='missing_tcp_crd', database_namespace='hdb-database')

    def test_status_requires_exact_nonnegative_counters(self):
        fault = launcher.FaultProxy('/binary', 'a' * 64, '/kubectl', '/kubeconfig',
                                    'k3d-hakopod-dev', 'proxy', Path('/work'))
        fault.mode = 'route_reload_after_mutation'
        value = {'schema_version': 1, 'mode': fault.mode, 'route_mutation_seen': True,
                 'successful_route_mutations': 1, 'pre_mutation_proxy_upgrade_relays': 2,
                 'pre_mutation_database_upgrade_relays': 1}
        response = Mock()
        response.__enter__ = Mock(return_value=response)
        response.__exit__ = Mock(return_value=False)
        response.read.return_value = json.dumps(value).encode()
        fault.opener.open = Mock(return_value=response)
        self.assertEqual(fault.status(), value)
        value['pre_mutation_database_upgrade_relays'] = -1
        response.read.return_value = json.dumps(value).encode()
        with self.assertRaises(ValueError):
            fault.status()
        value['pre_mutation_database_upgrade_relays'] = True
        response.read.return_value = json.dumps(value).encode()
        with self.assertRaises(ValueError):
            fault.status()

    def test_start_rejects_ambiguous_or_mismatched_namespaces(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            binary = root / 'fault-proxy'
            binary.write_bytes(b'helper')
            binary.chmod(0o700)
            kubeconfig = root / 'kubeconfig'
            kubeconfig.write_text('owned')
            kubeconfig.chmod(0o600)
            fault = launcher.FaultProxy(binary, launcher.file_hash(binary), '/kubectl', kubeconfig,
                                        'k3d-hakopod-dev', 'proxy', root)
            with self.assertRaises(ValueError):
                fault.start(mode='missing_tcp_crd', database_namespace='proxy')
            with self.assertRaises(ValueError):
                fault.start(mode='route_reload_after_mutation', database_namespace='hdb-database',
                            route_namespace='other-database', route_resource='dbpe-endpoint')
            self.assertTrue(launcher.RESOURCE.fullmatch('dbpe-endpoint.example'))


if __name__ == '__main__':
    unittest.main()
