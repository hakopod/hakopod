#!/usr/bin/env python3
"""Pure development-workspace contract checks; never starts a process or cluster."""
import copy
import importlib.util
import json
from pathlib import Path
from types import SimpleNamespace
import unittest
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[2]


def module(name, path):
    spec = importlib.util.spec_from_file_location(name, path)
    result = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(result)
    return result


with patch('subprocess.run', side_effect=AssertionError('import launched a process')), \
        patch('subprocess.Popen', side_effect=AssertionError('import launched a process')):
    shared = module('shared_workspace', Path(__file__).with_name('shared-workspace.py'))
    installer = module('stock_actions_runtime', ROOT / 'installer/actions_runtime.py')

UID = '01234567-89ab-cdef-0123-456789abcdef'
SANDBOX = 'b' * 64
CONTAINER = 'c' * 64


def pod(size=4):
    return {'metadata': {'uid': UID, 'name': 'actions-runtime-fixture', 'namespace': 'hp-development',
            'annotations': shared.annotations(size)},
        'spec': {'nodeName': shared.NODE, 'runtimeClassName': 'hakopod-actions',
            'volumes': [{'name': 'runner', 'emptyDir': {'sizeLimit': f'{size}Gi'}}],
            'containers': [{'name': 'runner', 'resources': {'limits': {'ephemeral-storage': f'{size + 1}Gi'}}}]},
        'status': {'containerStatuses': [{'name': 'runner', 'containerID': 'containerd://' + CONTAINER}]}}


def mount(size=4):
    return {'mountinfo': f'42 3 0:1 / /home/runner rw,nosuid - tmpfs none rw,size={(size + 1) * 1024 * 1024}k,mode=770,uid=1001,gid=1001\n',
        'root': {'uid': 1001, 'gid': 1001, 'mode': 0o770},
        'process': {'uid': 1001, 'gid': 1001, 'effective_capabilities': 0}, 'prepare': True, 'ready': True}


def sandbox():
    return {'items': [{'id': SANDBOX, 'state': 'SANDBOX_READY',
        'metadata': {'uid': UID, 'name': 'actions-runtime-fixture', 'namespace': 'hp-development'}}]}


def inspected():
    identity = shared.pod_identity(pod())
    return {'info': {'sandboxID': SANDBOX, 'runtimeSpec': {'mounts': [
        {'destination': '/home/runner', 'source': identity['source'], 'type': 'bind'}]}}}


def stats(used):
    return {'pods': [{'podRef': {'uid': UID, 'namespace': 'hp-development', 'name': 'actions-runtime-fixture'},
        'volume': [{'name': 'runner', 'usedBytes': used}]}]}


class SelectionTests(unittest.TestCase):
    def test_disabled_setup_is_byte_identical_and_enabled_setup_forwards_only_three_keys(self):
        original = installer.runtime_section(Path('/opt/hakopod-actions-fixture'))
        self.assertEqual(shared.runtime_section(original, False), original)
        changed = shared.runtime_section(original, True)
        self.assertEqual(shared.verify_runtime_config(changed)['pod_annotations'], shared.KEYS)
        self.assertEqual(changed.replace('  pod_annotations = ' + json.dumps(shared.KEYS) + '\n', ''), original)
        self.assertFalse(shared.enabled({}))
        profile = installer.runtime_profile(Path('/opt/hakopod-actions-fixture'))
        self.assertEqual(shared.verify_runtime_profile(profile)['overlay2'], 'root:memory,size=512m')
        with self.assertRaises(RuntimeError):
            shared.verify_runtime_profile(profile.replace('root:memory,size=512m', 'all:self'))
        self.assertFalse(shared.enabled({shared.FLAG: '0'}))
        self.assertTrue(shared.enabled({shared.FLAG: '1'}))
        for flag in ('', 'true', 'false', ' 1', '1\n', None):
            with self.subTest(flag=flag), self.assertRaises(RuntimeError):
                shared.enabled({shared.FLAG: flag})
        for name in ('HAKOPOD_ACTIONS_BUILDKIT_CANDIDATE', 'HAKOPOD_ACTIONS_PUBLISH_CANDIDATE'):
            with self.subTest(name=name), self.assertRaises(RuntimeError):
                shared.enabled({shared.FLAG: '1', name: '1'})

    def test_wildcards_extra_keys_and_repeated_configuration_are_rejected(self):
        changed = shared.runtime_section(installer.runtime_section(Path('/fixture')), True)
        for keys in (['dev.gvisor.*'], ['*'], shared.KEYS + ['dev.gvisor.spec.mount.runner.source'], shared.KEYS + shared.KEYS):
            with self.subTest(keys=keys), self.assertRaises(RuntimeError):
                shared.verify_runtime_config(changed.replace(json.dumps(shared.KEYS), json.dumps(keys)))
        with self.assertRaises(RuntimeError):
            shared.runtime_section(changed, True)
        other = '\n[plugins."io.containerd.cri.v1.runtime".containerd.runtimes.runc]\npod_annotations = ["dev.*"]\n'
        with self.assertRaises(RuntimeError):
            shared.verify_runtime_config(changed + other)


class IdentityTests(unittest.TestCase):
    def test_unchanged_disk_limits_and_exact_bind_hints_are_required(self):
        for size in (2, 4, 8):
            identity = shared.pod_identity(pod(size))
            evidence = shared.verify_mount(mount(size), identity)
            self.assertEqual(evidence['limit_bytes'], (size + 1) * shared.GIB)
        mutations = [lambda v: v['metadata'].update(uid='../other'), lambda v: v['spec'].update(nodeName='production'),
            lambda v: v['metadata']['annotations'].update({shared.KEYS[0]: 'tmpfs'}),
            lambda v: v['metadata']['annotations'].update({shared.PREFIX + 'source': '/other'}),
            lambda v: v['spec']['volumes'][0]['emptyDir'].update(medium='Memory'),
            lambda v: v['spec']['volumes'][0]['emptyDir'].update(sizeLimit='16Gi'),
            lambda v: v['spec']['containers'][0]['resources']['limits'].update({'ephemeral-storage': '6Gi'}),
            lambda v: v['status']['containerStatuses'][0].update(containerID='containerd://bad')]
        for mutate in mutations:
            value = pod()
            mutate(value)
            with self.subTest(mutation=mutate), self.assertRaises(RuntimeError):
                shared.pod_identity(value)

    def test_actual_internal_mount_and_unprivileged_process_are_required(self):
        identity = shared.pod_identity(pod())
        mutations = [lambda v: v.update(mountinfo=v['mountinfo'].replace('tmpfs', '9p')),
            lambda v: v.update(mountinfo=v['mountinfo'].replace('size=5242880k', 'size=512m')),
            lambda v: v.update(mountinfo=v['mountinfo'] * 2), lambda v: v['root'].update(uid=0),
            lambda v: v['process'].update(uid=0), lambda v: v['process'].update(effective_capabilities=1),
            lambda v: v.update(prepare=False), lambda v: v.update(ready=False)]
        for mutate in mutations:
            value = mount()
            mutate(value)
            with self.subTest(mutation=mutate), self.assertRaises(RuntimeError):
                shared.verify_mount(value, identity)

    def test_runner_source_and_filestore_sandbox_must_belong_to_the_same_pod(self):
        identity = shared.pod_identity(pod())
        self.assertEqual(shared.verify_container_source(inspected(), identity), SANDBOX)
        shared.verify_sandbox(sandbox(), identity, SANDBOX)
        changed = inspected()
        changed['info']['runtimeSpec']['mounts'][0]['source'] += '/other'
        with self.assertRaises(RuntimeError):
            shared.verify_container_source(changed, identity)
        for mutate in (lambda v: v['items'].append(copy.deepcopy(v['items'][0])),
                       lambda v: v['items'][0]['metadata'].update(uid='other'),
                       lambda v: v['items'][0].update(id='a' * 64)):
            value = sandbox()
            mutate(value)
            with self.subTest(mutation=mutate), self.assertRaises(RuntimeError):
                shared.verify_sandbox(value, identity, SANDBOX)


class AccountingTests(unittest.TestCase):
    def test_physical_blocks_are_measured_instead_of_sparse_logical_size(self):
        value = shared.verify_filestore('ext4\nregular file|1|20|30|67108864|4096\n')
        self.assertEqual(value['allocated_bytes'], 2 * shared.MIB)
        self.assertEqual(value['logical_bytes'], 64 * shared.MIB)
        for text in ('tmpfs\nregular file|1|20|30|67108864|4096', 'ext4\nregular file|2|20|30|67108864|4096',
                     'ext4\nsymbolic link|1|20|30|67108864|4096', 'ext4\nregular file|1|20|30|67108864|0'):
            with self.subTest(text=text), self.assertRaises(RuntimeError):
                shared.verify_filestore(text)

    def test_growth_requires_matching_kubelet_accounting_and_stable_file_identity(self):
        before = {'device': 20, 'inode': 30, 'allocated_bytes': 64 * shared.MIB, 'kubelet_used_bytes': 64 * shared.MIB + 4096}
        after = {**before, 'allocated_bytes': 80 * shared.MIB, 'kubelet_used_bytes': 80 * shared.MIB + 4096}
        self.assertEqual(shared.verify_growth(before, after)['physical_growth_bytes'], shared.PROBE_BYTES)
        for key, value in (('inode', 31), ('allocated_bytes', before['allocated_bytes']), ('kubelet_used_bytes', before['kubelet_used_bytes'])):
            with self.subTest(key=key), self.assertRaises(RuntimeError):
                shared.verify_growth(before, {**after, key: value})
        identity = shared.pod_identity(pod())
        self.assertEqual(shared.volume_accounting(stats(123), identity), 123)
        wrong = stats(123)
        wrong['pods'][0]['podRef']['uid'] = 'other'
        self.assertIsNone(shared.volume_accounting(wrong, identity))
        for value in (-1, '123', True):
            with self.subTest(value=value), self.assertRaises(RuntimeError):
                shared.volume_accounting(stats(value), identity)

    def test_eviction_requires_physical_overage_and_kubelet_storage_eviction(self):
        observer = shared.Observer(None, None, shared.NODE, {})
        observer.identities[UID] = shared.pod_identity(pod(2))
        observer.report['usage'] = {UID: {'physical_bytes': 3 * shared.GIB, 'kubelet_bytes': 3 * shared.GIB}}
        status = {'reason': 'Evicted', 'message': 'Usage of EmptyDir volume "runner" exceeds the limit "2Gi".'}
        observer.verify_eviction(UID, status)
        self.assertTrue(observer.report['eviction_usage']['kubelet_overage_summary_observed'])
        # An eviction can remove the volume before the next stats response;
        # this must be reported as unsampled, never invented as a numeric peak.
        observer.report['usage'][UID]['kubelet_bytes'] = shared.GIB
        observer.verify_eviction(UID, status)
        self.assertFalse(observer.report['eviction_usage']['kubelet_overage_summary_observed'])
        for invalid in ({'reason': 'DeadlineExceeded'}, {'reason': 'Evicted', 'message': 'memory pressure'},
                        {'reason': 'Evicted', 'message': 'Container ephemeral-storage limit exceeded'},
                        {'reason': 'Evicted', 'message': 'Usage of EmptyDir volume "other" exceeds the limit "2Gi".'},
                        {'reason': 'Evicted', 'message': 'Usage of EmptyDir volume "runner" exceeds the limit "3Gi".'}):
            with self.subTest(status=invalid), self.assertRaises(RuntimeError):
                observer.verify_eviction(UID, invalid)
        observer.report['usage'][UID]['physical_bytes'] = shared.GIB
        with self.assertRaises(RuntimeError):
            observer.verify_eviction(UID, status)

    def test_physical_usage_is_sampled_even_without_kubelet_volume_stats(self):
        observer = shared.Observer(None, None, shared.NODE, {})
        observer.identities[UID] = shared.pod_identity(pod(2))
        with patch.object(observer, 'filestore', return_value={'allocated_bytes': 3 * shared.GIB}):
            observer.usage({'pods': []})
        self.assertEqual(observer.report['usage'][UID]['physical_bytes'], 3 * shared.GIB)
        self.assertEqual(observer.report['usage'][UID]['kubelet_bytes'], 0)


class LifecycleTests(unittest.TestCase):
    def test_disk_monitor_is_bounded_joined_and_rejects_malformed_or_replaced_filestores(self):
        class Event:
            stopped = False
            def is_set(self):
                return self.stopped
            def set(self):
                self.stopped = True
            def wait(self, seconds):
                self.assert_interval = seconds
                return True
        class Thread:
            def __init__(self, target, daemon):
                self.target, self.daemon, self.join_timeout = target, daemon, None
            def start(self):
                self.target()
            def join(self, timeout):
                self.join_timeout = timeout
            def is_alive(self):
                return False
        identity = {**shared.pod_identity(pod(2)), 'sandbox_id': SANDBOX}
        before = {'device': 20, 'inode': 30, 'allocated_bytes': 4096}
        for output, invalid in [('ext4\nregular file|1|20|30|3221225472|6291456\n', False),
                                ('malformed', True), ('ext4\nregular file|1|20|31|3221225472|6291456\n', True)]:
            observer = shared.Observer(lambda *args, **kwargs: SimpleNamespace(stdout=output), None, shared.NODE, {})
            observer.identities[UID] = identity
            with self.subTest(output=output), patch.object(shared.threading, 'Event', Event), patch.object(shared.threading, 'Thread', Thread):
                observer.start_disk_monitor(identity, before)
                observer.close()
            stop, thread, evidence = observer.disk_monitor
            self.assertTrue(stop.stopped)
            self.assertEqual(thread.join_timeout, 12)
            self.assertTrue(thread.daemon)
            self.assertEqual(evidence.get('invalid_observation', False), invalid)
            if invalid:
                with self.assertRaises(RuntimeError):
                    observer.verify_eviction(UID, {'reason': 'Evicted', 'message': 'Usage of EmptyDir volume "runner" exceeds the limit "2Gi".'})
            else:
                self.assertEqual(evidence['samples'], 1)
                self.assertEqual(evidence['physical_bytes'], 3 * shared.GIB)
        observer = shared.Observer(None, None, shared.NODE, {})
        observer.disk_monitor = (Event(), SimpleNamespace(join=lambda **kwargs: None, is_alive=lambda: True), {})
        with self.assertRaisesRegex(RuntimeError, 'did not stop'):
            observer.close()

    def test_disk_and_replacement_checks_never_create_containers_or_pull_images(self):
        observer = shared.Observer(None, None, shared.NODE, {})
        with patch.object(observer, 'host', side_effect=[json.dumps(inspected()), json.dumps(sandbox())]), \
                patch.object(observer, 'runner', side_effect=[json.dumps(mount()), '']) as runner, \
                patch.object(observer, 'sample', return_value={'allocated_bytes': 100}), \
                patch('subprocess.run', side_effect=AssertionError('test launched a process')):
            evidence = observer.prepare(pod(), lambda **kwargs: self.fail('minimal probe restarted the daemon'), full=False)
        self.assertEqual(evidence['status'], 'passed')
        self.assertFalse(evidence['full_probe'])
        self.assertEqual(runner.call_count, 2)
        self.assertNotIn('docker', runner.call_args_list[1].args[1])

    def test_full_probe_requires_metadata_and_inode_stability_after_restart(self):
        inodes = {'runner_inode': 10, 'capability_inode': 11}
        before = {'device': 20, 'inode': 30, 'allocated_bytes': 64 * shared.MIB, 'kubelet_used_bytes': 64 * shared.MIB}
        after = {**before, 'allocated_bytes': 80 * shared.MIB, 'kubelet_used_bytes': 80 * shared.MIB}
        for changed in (False, True):
            observer = shared.Observer(None, lambda *args, **kwargs: SimpleNamespace(stdout='10\n11\n'), shared.NODE, {})
            later = {**inodes, 'capability_inode': 12} if changed else inodes
            with self.subTest(changed=changed), \
                    patch.object(observer, 'host', side_effect=[json.dumps(inspected()), json.dumps(sandbox())]), \
                    patch.object(observer, 'runner', side_effect=[json.dumps(mount()), json.dumps(inodes), '', json.dumps(later), '']), \
                    patch.object(observer, 'accounted', side_effect=[before, after]), \
                    patch.object(observer, 'sample', return_value=after):
                if changed:
                    with self.assertRaisesRegex(RuntimeError, 'changed after'):
                        observer.prepare(pod(), lambda **kwargs: None)
                else:
                    evidence = observer.prepare(pod(), lambda **kwargs: None)
                    self.assertEqual(evidence['status'], 'passed')
                    self.assertTrue(evidence['docker_archive_metadata'])
                    self.assertEqual(evidence['shared_inodes'], inodes)

    def test_removal_only_accepts_an_observed_owned_workspace(self):
        observer = shared.Observer(None, None, shared.NODE, {})
        with self.assertRaises(RuntimeError):
            observer.removed(UID)
        identity = {**shared.pod_identity(pod()), 'sandbox_id': SANDBOX}
        observer.identities[UID] = identity
        with patch.object(observer, 'host', return_value='') as host:
            observer.removed(UID)
        self.assertEqual(host.call_args.args[0][-1], identity['source'])
        self.assertEqual(observer.report['removed_workspaces'], [{'uid': UID, 'sandbox_id': SANDBOX, 'source_absent': True}])

    def test_failure_cleanup_retries_only_the_exact_owned_path_and_propagates_failure(self):
        observer = shared.Observer(None, None, shared.NODE, {})
        identity = {**shared.pod_identity(pod()), 'sandbox_id': SANDBOX}
        observer.identities[UID] = identity
        with patch.object(shared.time, 'sleep') as sleep, \
                patch('subprocess.run', side_effect=[SimpleNamespace(returncode=1), SimpleNamespace(returncode=0)]) as command:
            observer.removed(UID, command=command)
        self.assertEqual(command.call_count, 2)
        sleep.assert_called_once_with(2)
        self.assertEqual(command.call_args.args[0][-1], identity['source'])
        observer.removed_uids.clear()
        with patch.object(shared.time, 'sleep'), patch('subprocess.run', return_value=SimpleNamespace(returncode=1)) as command:
            with self.assertRaisesRegex(RuntimeError, 'still has'):
                observer.removed(UID, command=command)
        self.assertEqual(command.call_count, 15)
        self.assertNotIn(UID, observer.removed_uids)
        identity['source'] = '/var/lib/kubelet/pods/another-pod/volumes/kubernetes.io~empty-dir/runner'
        with patch('subprocess.run', side_effect=AssertionError('cleanup touched an unowned path')) as command:
            with self.assertRaisesRegex(RuntimeError, 'path differs'):
                observer.removed(UID, command=command)


if __name__ == '__main__':
    unittest.main()
