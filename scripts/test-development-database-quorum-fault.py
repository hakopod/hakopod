#!/usr/bin/env python3
"""Check development fault ownership and rescue-unit behavior without a cluster."""
import copy
import importlib.util
import json
import os
from pathlib import Path
import unittest
from unittest.mock import patch


SPEC = importlib.util.spec_from_file_location('quorum_fault', Path(__file__).with_name('development-database-quorum-fault.py'))
HELPER = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(HELPER)


class DevelopmentFaultTests(unittest.TestCase):
    def setUp(self):
        self.database = 'a' * 32
        self.uid = '01234567-89ab-cdef-0123-456789abcdef'
        self.container = 'b' * 64
        self.lease = '566710d7-b493-4a69-a59e-d0a5772669c5'
        self.pod_name = 'database-0'
        self.state = 'RUNNING'
        self.calls = []
        self.fail_watchdog = False
        labels = {'hakopod.io/database-id': self.database, 'app.kubernetes.io/managed-by': 'hakopod'}
        self.namespace = {'metadata': {'uid': 'namespace', 'labels': labels}}
        self.object = {'metadata': {'uid': 'database', 'labels': labels}}
        self.pod = {
            'metadata': {'uid': self.uid, 'labels': labels, 'ownerReferences': [{'kind': 'StatefulSet', 'uid': 'set'}]},
            'spec': {'nodeName': 'k3d-hakopod-database-worker-0', 'containers': [
                {'name': 'mongod', 'image': 'quay.io/mongodb/mongodb-community-server:8.0.32-ubi8@sha256:7c905b7efb6d7713ded906b88483d68776fbd37722a47fdaf7444b9ccdb0a86d'}]},
            'status': {'containerStatuses': [{'name': 'mongod', 'containerID': 'containerd://' + self.container}]},
        }
        self.statefulset = {'metadata': {'uid': 'set', 'ownerReferences': [{'kind': 'MongoDBCommunity', 'uid': 'database'}]}}
        self.runtime = {'Labels': {'io.kubernetes.pod.uid': self.uid, 'io.kubernetes.container.name': 'mongod'}}
        self.environment = {'HAKOPOD_DATABASE_MONGODB_TEST': '1', 'HAKOPOD_TEST_KUBECONFIG': '/fixture/development-kubeconfig', 'PATH': '/fixture/bin:/usr/bin', 'AWS_SECRET_ACCESS_KEY': 'not-forwarded'}

    def command(self, args):
        self.calls.append(args)
        if args[:2] == ['sudo', 'systemd-run']:
            if self.fail_watchdog:
                raise RuntimeError('fixture rescue unit failed')
            return b''
        if args[0] == 'kubectl':
            kind = args[args.index('get') + 1]
            value = {'namespace': self.namespace, 'mongodbcommunity': self.object,
                     'clickhouseinstallation': self.object, 'pod': self.pod,
                     'statefulset': self.statefulset}[kind]
            return json.dumps(value).encode()
        if args[:3] == ['sudo', 'docker', 'inspect']:
            if self.environment.get('HAKOPOD_CLICKHOUSE_TEST') == '1':
                return json.dumps({'k3d.cluster': 'hakopod-dev', 'k3d.role': 'agent',
                                   'com.hakopod.lease-id': self.lease}).encode()
            return b'{"k3d.cluster":"hakopod-dev"}'
        if args[-3:-1] == ['containers', 'info']:
            return json.dumps(self.runtime).encode()
        if args[-2:] == ['tasks', 'list']:
            return ('TASK PID STATUS\n' + self.container + ' 123 ' + self.state + '\n').encode()
        if args[-3:-1] == ['tasks', 'pause']:
            self.state = 'PAUSED'
            return b''
        if args[-3:-1] == ['tasks', 'resume']:
            self.state = 'RUNNING'
            return b''
        raise AssertionError('unexpected fixture command')

    def invoke(self, action):
        with patch.dict(os.environ, self.environment, clear=True), patch.object(HELPER, 'run', self.command), \
             patch.object(HELPER.sys, 'argv', ['helper', action, self.database, self.pod_name, self.uid]), \
             patch.object(HELPER.subprocess, 'run') as stop, patch.object(HELPER.time, 'sleep') as sleep:
            HELPER.main()
            return stop, sleep

    def clickhouse(self):
        self.environment = {
            'HAKOPOD_CLICKHOUSE_TEST': '1',
            'HAKOPOD_CLICKHOUSE_FAULT_NODES': 'k3d-hakopod-clickhouse-worker-0',
            'HAKOPOD_CLICKHOUSE_LEASE_ID': self.lease,
            'HAKOPOD_TEST_KUBECONFIG': '/fixture/development-kubeconfig',
            'PATH': '/fixture/bin:/usr/bin',
        }
        self.pod['spec']['nodeName'] = 'k3d-hakopod-clickhouse-worker-0'
        self.pod['spec']['containers'] = [{'name': 'keeper', 'image': 'docker.io/clickhouse/clickhouse-keeper:26.3.33.24@sha256:3fd59d9efb8c9e9136c3c924ceaa004f65c0b14699f34e4eae4eb63e2f860803'}]
        self.pod['status']['containerStatuses'][0]['name'] = 'keeper'
        self.pod_name = 'database-keeper-0'
        self.statefulset['metadata']['ownerReferences'] = [{'kind': 'Namespace', 'uid': 'namespace'}]
        self.runtime['Labels']['io.kubernetes.container.name'] = 'keeper'

    def test_pause_schedules_independent_bounded_rescue_before_mutation(self):
        self.invoke('pause')
        rescue = next(call for call in self.calls if call[:2] == ['sudo', 'systemd-run'])
        pause = next(call for call in self.calls if call[-3:-1] == ['tasks', 'pause'])
        self.assertLess(self.calls.index(rescue), self.calls.index(pause))
        self.assertEqual(self.state, 'PAUSED')
        for option in ['--collect', '--property=RuntimeMaxSec=300s', '--property=MemoryMax=256M', '--property=CPUQuota=25%']:
            self.assertIn(option, rescue)
        forwarded = {value.split('=', 2)[1] for value in rescue if value.startswith('--setenv=')}
        self.assertEqual(forwarded, {'PATH', 'HAKOPOD_TEST_KUBECONFIG', 'HAKOPOD_DATABASE_MONGODB_TEST'})
        self.assertEqual(rescue[-4:], ['resume-after', self.database, self.pod_name, self.uid])

    def test_rescue_failure_prevents_pause(self):
        self.fail_watchdog = True
        with self.assertRaises(RuntimeError):
            self.invoke('pause')
        self.assertEqual(self.state, 'RUNNING')
        self.assertFalse(any(call[-3:-1] == ['tasks', 'pause'] for call in self.calls))

    def test_verify_paused_is_read_only_and_rejects_ended_fault(self):
        with self.assertRaises(ValueError):
            self.invoke('verify-paused')
        self.state = 'PAUSED'
        self.calls.clear()
        stop, _ = self.invoke('verify-paused')
        self.assertEqual(self.state, 'PAUSED')
        self.assertFalse(any(call[-3:-1] in (['tasks', 'pause'], ['tasks', 'resume']) for call in self.calls))
        stop.assert_not_called()

    def test_explicit_resume_cancels_only_its_rescue_unit(self):
        self.state = 'PAUSED'
        stop, _ = self.invoke('resume')
        self.assertEqual(self.state, 'RUNNING')
        self.assertEqual(stop.call_args.args[0], ['sudo', 'systemctl', 'stop', HELPER.watchdog_unit(self.database, self.uid)])

    def test_watchdog_resumes_without_stopping_itself(self):
        self.state = 'PAUSED'
        stop, sleep = self.invoke('resume-after')
        self.assertEqual(self.state, 'RUNNING')
        sleep.assert_called_once_with(90)
        stop.assert_not_called()

    def test_changed_pod_or_runtime_or_wrong_node_cannot_mutate(self):
        original = copy.deepcopy(self.pod)
        for defect in ('uid', 'node', 'runtime'):
            with self.subTest(defect=defect):
                self.pod = copy.deepcopy(original)
                self.runtime['Labels']['io.kubernetes.pod.uid'] = self.uid
                if defect == 'uid':
                    self.pod['metadata']['uid'] = 'replaced'
                elif defect == 'node':
                    self.pod['spec']['nodeName'] = 'k3d-hakopod-provider-smoke-20260929'
                else:
                    self.runtime['Labels']['io.kubernetes.pod.uid'] = 'replaced'
                self.calls.clear()
                with self.assertRaises(ValueError):
                    self.invoke('pause')
                self.assertFalse(any(call[:2] == ['sudo', 'systemd-run'] or call[-3:-1] == ['tasks', 'pause'] for call in self.calls))

    def test_clickhouse_requires_explicit_dedicated_nodes_and_forwards_them(self):
        self.clickhouse()
        self.invoke('pause')
        rescue = next(call for call in self.calls if call[:2] == ['sudo', 'systemd-run'])
        self.assertIn('--setenv=HAKOPOD_CLICKHOUSE_FAULT_NODES=k3d-hakopod-clickhouse-worker-0', rescue)
        self.assertIn('--setenv=HAKOPOD_CLICKHOUSE_LEASE_ID=' + self.lease, rescue)

        self.environment.pop('HAKOPOD_CLICKHOUSE_FAULT_NODES')
        self.calls.clear()
        with self.assertRaises(ValueError):
            self.invoke('pause')
        self.assertFalse(any(call[:2] == ['sudo', 'systemd-run'] or call[-3:-1] == ['tasks', 'pause'] for call in self.calls))

    def test_clickhouse_requires_exact_node_lease(self):
        self.clickhouse()
        for lease in (None, 'fedcba98-7654-3210-fedc-ba9876543210'):
            with self.subTest(lease=lease):
                if lease is None:
                    self.environment.pop('HAKOPOD_CLICKHOUSE_LEASE_ID')
                else:
                    self.environment['HAKOPOD_CLICKHOUSE_LEASE_ID'] = lease
                self.calls.clear()
                with self.assertRaises(ValueError):
                    self.invoke('pause')
                self.assertFalse(any(call[:2] == ['sudo', 'systemd-run'] or call[-3:-1] == ['tasks', 'pause'] for call in self.calls))

    def test_clickhouse_rejects_legacy_and_unselected_nodes(self):
        self.clickhouse()
        for node in ('k3d-hakopod-database-worker-0', 'k3d-hakopod-clickhouse-worker-1'):
            with self.subTest(node=node):
                self.pod['spec']['nodeName'] = node
                self.calls.clear()
                with self.assertRaises(ValueError):
                    self.invoke('pause')
                self.assertFalse(any(call[:2] == ['sudo', 'systemd-run'] or call[-3:-1] == ['tasks', 'pause'] for call in self.calls))


if __name__ == '__main__':
    unittest.main()
