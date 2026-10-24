import unittest
import io
import json
import os
import stat
import tarfile
import tempfile
from contextlib import contextmanager
from pathlib import Path
from types import SimpleNamespace
from unittest.mock import patch

import actions_runtime
import modules
from actions_runtime import extend_template, unpack_runtime


class ActionsRuntimeTests(unittest.TestCase):
    def test_runtime_extraction_is_bounded_and_rejects_links_before_writing(self):
        for name, kind in [('runsc', tarfile.REGTYPE), ('../escape', tarfile.REGTYPE),
                           ('gvisor-bin/link', tarfile.SYMTYPE), ('runsc', tarfile.FIFOTYPE)]:
            with self.subTest(name=name, kind=kind), tempfile.TemporaryDirectory() as temp:
                root = Path(temp)
                source = root / 'runtime.tar'
                with tarfile.open(source, 'w') as archive:
                    item = tarfile.TarInfo(name)
                    item.type = kind
                    item.mode = 0o755
                    item.linkname = '/outside'
                    item.size = 2 if kind == tarfile.REGTYPE else 0
                    archive.addfile(item, io.BytesIO(b'ok') if item.size else None)
                if name == 'runsc' and kind == tarfile.REGTYPE:
                    unpack_runtime(source, root / 'out')
                    self.assertEqual((root / 'out/runsc').read_bytes(), b'ok')
                    self.assertEqual((root / 'out/runsc').stat().st_mode & 0o777, 0o755)
                else:
                    with self.assertRaises(ValueError):
                        unpack_runtime(source, root / 'out')
                    self.assertFalse((root / 'out').exists())

    def test_preserves_custom_runtime_and_is_idempotent(self):
        original = '{{ template "base" . }}\n# customer setting\n[custom]\nvalue = true\n'
        root = Path('/opt/hakopod/actions-runtime/release')
        result = extend_template(original, root)
        self.assertTrue(result.startswith(original))
        self.assertEqual(extend_template(result, root), result)
        self.assertIn('runtime_path = "/opt/hakopod/actions-runtime/release/containerd-shim-runsc-v1"', result)

    def test_refuses_unowned_or_edited_runtime(self):
        root = Path('/opt/hakopod/actions-runtime/release')
        for value in ('[runtimes.hakopod-actions]', '# END HAKOPOD MANAGED ACTIONS\n', extend_template('', root).replace('io.containerd.runsc.v1', 'io.containerd.runc.v2')):
            with self.assertRaises(ValueError):
                extend_template(value, root)

    def test_owned_template_transitions_preserve_surrounding_configuration(self):
        root = Path('/opt/hakopod/actions-runtime/release')
        custom = '{{ template "base" . }}\n# keep this exactly\n[custom]\nvalue = true\n'
        vfs = extend_template(custom, root, 'vfs')
        shared = extend_template(vfs, root, actions_runtime.WORKSPACE_PROFILE)
        self.assertEqual(shared.replace(actions_runtime.runtime_section(root, actions_runtime.WORKSPACE_PROFILE), ''),
                         custom.rstrip() + '\n\n')
        self.assertEqual(extend_template(shared, root, 'vfs'), vfs)
        self.assertEqual(extend_template(shared, root, actions_runtime.WORKSPACE_PROFILE), shared)

    def test_generated_runtime_requires_exact_options_and_scoped_forwarders(self):
        root = Path('/opt/hakopod/actions-runtime/release')
        # The template contains Go directives, but K3s' generated config is
        # ordinary TOML. Exercise the generated-config verifier with the owned
        # rendered stanza rather than the unrendered template preamble.
        shared = actions_runtime.runtime_section(root, actions_runtime.WORKSPACE_PROFILE)
        actions_runtime.verify_generated_config(shared, root, actions_runtime.WORKSPACE_PROFILE)
        actions_runtime.verify_generated_config(actions_runtime.runtime_section(root, 'vfs'), root, 'vfs')
        for changed in (
                shared.replace('io.containerd.runsc.v1.options', 'io.containerd.runc.v2.options'),
                shared.replace(str(root / 'runsc-actions.toml'), '/tmp/unowned.toml'),
                shared.replace('  ConfigPath =', '  Unexpected = true\n  ConfigPath ='),
                shared.replace('"dev.gvisor.spec.mount.runner.options"', '"dev.gvisor.spec.mount.runner.*"'),
                shared + '\n[plugins."io.containerd.cri.v1.runtime".containerd.runtimes.runc]\n'
                         'pod_annotations = ["dev.gvisor.spec.mount.runner.*"]\n'):
            with self.subTest(changed=changed[-120:]), self.assertRaises(ValueError):
                actions_runtime.verify_generated_config(changed, root, actions_runtime.WORKSPACE_PROFILE)
        with self.assertRaises(ValueError):
            actions_runtime.verify_generated_config(shared + ' ' * 262144, root, actions_runtime.WORKSPACE_PROFILE)

    def root_owned(self):
        original_lstat, original_stat = Path.lstat, Path.stat

        def owned(result):
            return SimpleNamespace(st_mode=result.st_mode, st_uid=0, st_nlink=result.st_nlink,
                                   st_size=result.st_size, st_dev=result.st_dev, st_ino=result.st_ino)

        return patch.multiple(Path,
            lstat=lambda path: owned(original_lstat(path)),
            stat=lambda path, **kwargs: owned(original_stat(path, **kwargs)))

    def test_installed_runtime_rejects_content_mode_link_and_hardlink_changes(self):
        with tempfile.TemporaryDirectory() as temp:
            base = Path(temp)
            expected, installed = base / 'expected', base / 'installed'
            expected.mkdir(); installed.mkdir()
            for directory in (expected, installed):
                (directory / 'runsc').write_bytes(b'runsc')
                (directory / 'runsc').chmod(0o755)
                (directory / 'runsc-actions.toml').write_text('profile\n')
                (directory / 'runsc-actions.toml').chmod(0o644)
            completed = SimpleNamespace(stdout=actions_runtime.VERSION.encode(), stderr=b'')
            with self.root_owned(), patch.object(actions_runtime.subprocess, 'run', return_value=completed):
                actions_runtime.verify_installed_runtime(installed, expected)
                for mutate in (
                        lambda: (installed / 'runsc-actions.toml').write_text('changed\n'),
                        lambda: (installed / 'runsc-actions.toml').chmod(0o666)):
                    mutate()
                    with self.assertRaises(ValueError):
                        actions_runtime.verify_installed_runtime(installed, expected)
                    (installed / 'runsc-actions.toml').write_text('profile\n')
                    (installed / 'runsc-actions.toml').chmod(0o644)
                (installed / 'runsc-actions.toml').unlink()
                (installed / 'runsc-actions.toml').symlink_to(expected / 'runsc-actions.toml')
                with self.assertRaises(ValueError):
                    actions_runtime.verify_installed_runtime(installed, expected)
                (installed / 'runsc-actions.toml').unlink()
                (installed / 'runsc-actions.toml').mkdir()
                with self.assertRaises(ValueError):
                    actions_runtime.verify_installed_runtime(installed, expected)
                (installed / 'runsc-actions.toml').rmdir()
                os.link(expected / 'runsc-actions.toml', installed / 'runsc-actions.toml')
                with self.assertRaises(ValueError):
                    actions_runtime.verify_installed_runtime(installed, expected)

    def test_runtime_text_read_is_bounded_private_and_regular(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            value = root / 'config.toml'
            value.write_text('owned\n'); value.chmod(0o600)
            with self.root_owned():
                self.assertEqual(actions_runtime.read_runtime_text(value, 16), 'owned\n')
                value.chmod(0o622)
                with self.assertRaises(ValueError):
                    actions_runtime.read_runtime_text(value, 16)
                value.chmod(0o600); value.write_text('x' * 17)
                with self.assertRaises(ValueError):
                    actions_runtime.read_runtime_text(value, 16)
                value.unlink(); value.symlink_to(root / 'target')
                (root / 'target').write_text('owned\n')
                with self.assertRaises(ValueError):
                    actions_runtime.read_runtime_text(value, 16)

    def test_install_withdraws_both_labels_and_hands_off_lock_only_after_verification(self):
        for profile, failure in [(actions_runtime.WORKSPACE_PROFILE, value)
                for value in (None, 'node-before', 'restart', 'config', 'runtimeclass', 'node', 'restore', 'probe')] + [('vfs', None)]:
            with self.subTest(profile=profile, failure=failure), tempfile.TemporaryDirectory() as temp:
                base = Path(temp) / 'actions-runtime'; base.mkdir()
                template = Path(temp) / 'containerd/config-v3.toml.tmpl'; template.parent.mkdir()
                template.write_text('{{ template "base" . }}\n'); template.chmod(0o644)
                (base / 'owner').write_text('install-id'); (base / 'owner').chmod(0o600)
                node = {'metadata': {'name': 'node', 'uid': 'node-uid', 'resourceVersion': '7',
                    'labels': {'hakopod.com/installation': 'install-id', actions_runtime.WORKSPACE_LABEL: 'old',
                               actions_runtime.RUNTIME_LABEL: 'ready'}}}
                events = []
                state = SimpleNamespace(held=False, runtime=None, node_reads=0)

                class Maintenance:
                    def __enter__(self):
                        state.held = True; events.append('lock')
                        return self

                    def close(self):
                        if state.held:
                            state.held = False; events.append('unlock')

                    def __exit__(self, *_):
                        self.close()

                def read(*args):
                    self.assertTrue(state.held)
                    if args[:2] == ('get', 'nodes'):
                        return {'items': [node]}
                    if args[:2] == ('get', 'node'):
                        state.node_reads += 1
                        events.append('node-check')
                        current = {'metadata': {'name': 'node', 'uid': 'node-uid',
                            'resourceVersion': str(7 + state.node_reads),
                            'labels': {'hakopod.com/installation': 'install-id'}}}
                        if failure == 'node' and state.node_reads == 2:
                            current['metadata']['uid'] = 'replacement'
                        if failure == 'node-before' and state.node_reads == 1:
                            current['metadata']['labels'][actions_runtime.RUNTIME_LABEL] = 'ready'
                        return current
                    if state.runtime is not None and args[:2] == ('get', 'runtimeclass'):
                        events.append('runtimeclass-check')
                        installed = json.loads(json.dumps(state.runtime))
                        if failure == 'runtimeclass':
                            installed['handler'] = 'runc'
                        return installed
                    return {}

                def unpack(_archive, destination):
                    destination.mkdir()
                    for name in ('runsc', 'containerd-shim-runsc-v1', 'gvisor-bin'):
                        (destination / name).write_bytes(b'x')
                        (destination / name).chmod(0o755)

                def run(args, **_kwargs):
                    command = ' '.join(args)
                    probe = args and args[0].endswith('hakopod-server')
                    self.assertEqual(state.held, not probe)
                    if actions_runtime.WORKSPACE_LABEL + '-' in command:
                        self.assertEqual(args, ['kubectl', 'label', 'node', 'node',
                            actions_runtime.RUNTIME_LABEL + '-', actions_runtime.WORKSPACE_LABEL + '-',
                            '--resource-version=7'])
                        events.append('withdraw')
                    elif args[:2] == ['systemctl', 'restart']:
                        events.append('restart')
                        if failure == 'restart':
                            raise RuntimeError('restart failed')
                        generated = actions_runtime.runtime_section(base / actions_runtime.VERSION, profile)
                        if failure == 'config':
                            generated = generated.replace('io.containerd.runsc.v1', 'io.containerd.runc.v2')
                        (template.parent / 'config.toml').write_text(
                            generated)
                        (template.parent / 'config.toml').chmod(0o644)
                    elif args[:2] == ['kubectl', 'apply']:
                        events.append('runtimeclass-apply')
                        state.runtime = json.loads(Path(args[-1]).read_text())
                    elif actions_runtime.RUNTIME_LABEL + '=ready' in args:
                        events.append('restore')
                        self.assertEqual(args[-1], '--resource-version=9')
                        if failure == 'restore':
                            raise RuntimeError('restore failed')
                    elif probe:
                        events.append('probe')
                        self.assertFalse((base / 'workspace-profile').exists())
                        if failure == 'probe':
                            raise RuntimeError('probe failed')
                    return SimpleNamespace(stdout=b'', stderr=b'')

                original_write = actions_runtime.atomic_write
                def write(path, value, mode=0o644):
                    if path == base / 'workspace-profile':
                        self.assertEqual(state.held, profile == 'vfs')
                        events.append('persist')
                    else:
                        self.assertTrue(state.held)
                        self.assertGreater(state.node_reads, 0)
                    return original_write(path, value, mode)

                def verify(*_args):
                    self.assertTrue(state.held)
                    events.append('binary-check')

                empty_sha = actions_runtime.hashlib.sha256(b'').hexdigest()
                with self.root_owned(), \
                     patch.object(actions_runtime, 'RuntimeMaintenanceLock', Maintenance), \
                     patch.object(actions_runtime, 'BASE', base), patch.object(actions_runtime, 'TEMPLATE', template), \
                     patch.object(actions_runtime.platform, 'system', return_value='Linux'), \
                     patch.object(actions_runtime.platform, 'machine', return_value='aarch64'), \
                     patch.dict(actions_runtime.DIGESTS, {'aarch64': empty_sha}), \
                     patch.object(actions_runtime.urllib.request, 'urlopen', return_value=io.BytesIO(b'')), \
                     patch.object(actions_runtime, 'unpack_runtime', side_effect=unpack), \
                     patch.object(actions_runtime, 'verify_installed_runtime', side_effect=verify), \
                     patch.object(actions_runtime, 'atomic_write', side_effect=write), \
                     patch.object(actions_runtime.subprocess, 'check_output', side_effect=['/opt/hakopod/tools/k3s --config /etc/hakopod/k3s.yaml', 'k3d-hakopod-dev']), \
                     patch.object(actions_runtime.subprocess, 'run', side_effect=run):
                    if failure:
                        with self.assertRaises((RuntimeError, ValueError)):
                            actions_runtime.install({'node_name': 'node'}, {'id': 'install-id'}, ['kubectl'], read,
                                                    profile)
                        self.assertNotIn('persist', events)
                        if failure not in ('probe', 'restore'):
                            self.assertNotIn('restore', events)
                        if failure != 'probe':
                            self.assertNotIn('probe', events)
                        if failure == 'node-before':
                            self.assertFalse((base / actions_runtime.VERSION).exists())
                            self.assertEqual(template.read_text(), '{{ template "base" . }}\n')
                    else:
                        actions_runtime.install({'node_name': 'node'}, {'id': 'install-id'}, ['kubectl'], read,
                                                profile)
                        self.assertLess(events.index('withdraw'), events.index('restart'))
                        self.assertLess(events.index('runtimeclass-check'), events.index('restore'))
                        self.assertEqual(events.count('binary-check'), 2)
                        self.assertEqual((base / 'workspace-profile').read_text(), profile + '\n')
                        if profile == 'vfs':
                            self.assertNotIn('probe', events)
                            self.assertLess(events.index('persist'), events.index('unlock'))
                        else:
                            self.assertLess(events.index('restore'), events.index('unlock'))
                            self.assertLess(events.index('unlock'), events.index('probe'))
                            self.assertLess(events.index('probe'), events.index('persist'))
                    self.assertEqual(events[:2], ['lock', 'withdraw'])
                    self.assertEqual(events.count('unlock'), 1)
                    self.assertFalse(state.held)


class MultiNodeTests(unittest.TestCase):
    IDENT = 'install-id'
    OWNED = {'hakopod.com/installation': 'install-id'}
    MACHINE = 'm-' + 'a' * 30
    VERSION = 'v1.33.4+k3s1'

    def node(self, name, labels=None, machine=MACHINE, role=None, version=VERSION, ready=True, taints=None):
        labels = dict(labels or {})
        if role:
            labels[actions_runtime.CONTROL_PLANE_LABEL] = 'true'
        return {'metadata': {'name': name, 'uid': name + '-uid', 'resourceVersion': '7', 'labels': labels},
                'spec': {'taints': taints or []},
                'status': {'nodeInfo': {'machineID': machine, 'kubeletVersion': version},
                           'conditions': [{'type': 'Ready', 'status': 'True' if ready else 'False'}]}}

    def server(self):
        return self.node('server', self.OWNED, machine='server-machine', role=True)

    @contextmanager
    def host(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            template = root / 'containerd/config-v3.toml.tmpl'; template.parent.mkdir()
            (root / 'machine-id').write_text(self.MACHINE + '\n')
            with patch.object(actions_runtime.platform, 'system', return_value='Linux'), \
                 patch.object(actions_runtime.platform, 'machine', return_value='aarch64'), \
                 patch.object(actions_runtime, 'TEMPLATE', template), \
                 patch.object(actions_runtime, 'BASE', root / 'actions-runtime'), \
                 patch.object(actions_runtime, 'MACHINE_ID', root / 'machine-id'):
                yield root, {'unit': 'k3s-agent', 'template': template, 'kube': ['kubectl']}

    def test_server_accepts_owned_agents_and_refuses_unowned_published_nodes(self):
        owned = self.OWNED
        cases = [
            ([self.server(), self.node('spot-1'), self.node('spot-2', {**owned, actions_runtime.RUNTIME_LABEL: 'ready'})], 'unit could not be verified'),
            ([self.server(), self.node('stray', {actions_runtime.RUNTIME_LABEL: 'ready'})], 'without this installation'),
            ([self.server(), self.node('stray', {'hakopod.com/installation': 'other', actions_runtime.WORKSPACE_LABEL: 'shared-overlay2-v1'})], 'without this installation'),
            ([self.node('spot-1', owned)], 'installer-owned server node'),
        ]
        for nodes, message in cases:
            with self.subTest(message=message, nodes=[n['metadata']['name'] for n in nodes]), self.host(), \
                 patch.object(actions_runtime.subprocess, 'check_output', return_value='/usr/bin/other-k3s server') as unit, \
                 patch.object(actions_runtime.subprocess, 'run') as run, self.assertRaisesRegex(ValueError, message):
                actions_runtime.install_locked({'node_name': 'server'}, {'id': self.IDENT}, ['kubectl'],
                                               lambda *args: {'items': nodes}, None, None)
            run.assert_not_called()
            if message != 'unit could not be verified':
                unit.assert_not_called()

    def test_runtime_class_only_publishes_class_without_touching_nodes(self):
        applied = {}

        def read(*args):
            if args[:2] == ('get', 'nodes'):
                return {'items': [self.server(), self.node('spot-1', {**self.OWNED, actions_runtime.RUNTIME_LABEL: 'ready'})]}
            if '--ignore-not-found' in args:
                return {}
            return applied['value']

        def run(args, **_kwargs):
            self.assertEqual(args[:2], ['kubectl', 'apply'])
            applied['value'] = json.loads(Path(args[-1]).read_text())

        class Lock:
            def __enter__(self): return self
            def __exit__(self, *_): pass

        with patch.object(actions_runtime, 'RuntimeMaintenanceLock', Lock), \
             patch.object(actions_runtime.subprocess, 'run', side_effect=run) as called:
            actions_runtime.publish_runtime_class({'id': self.IDENT}, ['kubectl'], read)
        called.assert_called_once()
        self.assertEqual(applied['value'], actions_runtime.runtime_class(self.IDENT))

    def test_adoption_labels_only_a_ready_agent_on_the_server_release(self):
        taint = [{'key': actions_runtime.DEDICATED_TAINT, 'value': 'true', 'effect': 'NoSchedule'}]
        cases = [
            ('agent', self.node('spot-1', taints=taint), None),
            ('already adopted', self.node('spot-1', self.OWNED), None),
            ('server', self.server(), 'Only K3s agents'),
            ('other installation', self.node('spot-1', {'hakopod.com/installation': 'other'}), 'another installation'),
            ('not ready', self.node('spot-1', ready=False), 'not ready'),
            ('older release', self.node('spot-1', version='v1.32.1+k3s1'), 'exact K3s release'),
            ('not k3s', self.node('spot-1', version='v1.33.4'), 'exact K3s release'),
        ]
        for name, node, message in cases:
            nodes = [self.server()] + ([] if node['metadata']['name'] == 'server' else [node])
            target = node['metadata']['name']
            with self.subTest(name), patch.object(actions_runtime.subprocess, 'run') as run, \
                 patch('builtins.print') as printed:
                if message:
                    with self.assertRaisesRegex(ValueError, message):
                        actions_runtime.adopt_node(target, self.IDENT, ['kubectl'], lambda *args: {'items': nodes})
                    run.assert_not_called()
                    continue
                actions_runtime.adopt_node(target, self.IDENT, ['kubectl'], lambda *args: {'items': nodes})
                if name == 'agent':
                    run.assert_called_once_with(['kubectl', 'label', 'node', 'spot-1', 'hakopod.com/installation=install-id',
                                                 '--resource-version=7'], check=True, timeout=30)
                else:
                    run.assert_not_called()
                    self.assertIn('Warning', printed.call_args_list[0].args[0])
        with self.assertRaisesRegex(ValueError, 'not joined'):
            actions_runtime.adopt_node('missing', self.IDENT, ['kubectl'], lambda *args: {'items': [self.server()]})

    def test_agent_host_comes_from_the_running_service(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp).resolve()
            binary = root / 'bin/k3s'; binary.parent.mkdir(); binary.write_bytes(b'k3s'); binary.chmod(0o755)
            proc = root / 'proc'; proc.mkdir()

            def process(pid, cgroup, *argv):
                (proc / pid).mkdir()
                (proc / pid / 'cgroup').write_text(cgroup + '\n')
                (proc / pid / 'cmdline').write_bytes(b'\0'.join(arg.encode() for arg in argv) + b'\0')

            process('10', '0::/system.slice/k3s-agent.service', str(binary), 'agent')
            process('11', '0::/system.slice/k3s-agent.service', 'containerd', '-c', '/data/k3s/agent/etc/containerd/config.toml',
                    '-a', '/run/k3s/containerd/containerd.sock')
            process('12', '0::/system.slice/other.service', 'containerd', '-c', '/var/lib/other/agent/etc/containerd/config.toml')
            (proc / 'self').mkdir()
            unit = '{ path=' + str(binary) + ' ; argv[]=' + str(binary) + ' agent ; ignore_errors=no ; }'
            original_lstat = Path.lstat

            def owned(path):
                info = original_lstat(path)
                return SimpleNamespace(st_mode=info.st_mode, st_uid=0)

            with patch.object(actions_runtime, 'PROC', proc), patch.object(Path, 'lstat', owned), \
                 patch.object(actions_runtime.subprocess, 'check_output', return_value=unit):
                host = actions_runtime.agent_host('k3s-agent')
                self.assertEqual(host['template'], Path('/data/k3s/agent/etc/containerd/config-v3.toml.tmpl'))
                self.assertEqual(host['kube'], [str(binary), 'kubectl', '--kubeconfig=/data/k3s/agent/kubelet.kubeconfig',
                                                '--request-timeout=15s'])
                with self.assertRaisesRegex(ValueError, 'containerd configuration'):
                    actions_runtime.agent_host('missing')
                with self.assertRaises(ValueError):
                    actions_runtime.agent_host('k3s-agent; rm')
                binary.chmod(0o775)
                with self.assertRaisesRegex(ValueError, 'only root can write'):
                    actions_runtime.agent_host('k3s-agent')
                binary.chmod(0o755)
                process('13', '0::/system.slice/k3s-agent.service', 'containerd', '--config', '/other/agent/etc/containerd/config.toml')
                with self.assertRaisesRegex(ValueError, 'containerd configuration'):
                    actions_runtime.agent_host('k3s-agent')
            with patch.object(actions_runtime.subprocess, 'check_output', return_value='{ argv[]=/usr/local/bin/k3s server ; }'), \
                 self.assertRaisesRegex(ValueError, 'one K3s agent'):
                actions_runtime.agent_host('k3s-agent')

    def test_agent_refuses_before_mutation(self):
        owned = self.OWNED
        runtime = actions_runtime.runtime_class(self.IDENT)
        cases = [
            ('unadopted', self.node('spot-1'), runtime, 'another installation'),
            ('other host', self.node('spot-1', owned, machine='m-' + 'b' * 30), runtime, 'host of the named node'),
            ('server', self.node('spot-1', owned, role=True), runtime, 'only to K3s agents'),
            ('release', self.node('spot-1', owned, version='v1.32.1+k3s1'), runtime, 'exact K3s release'),
            ('no class', self.node('spot-1', owned), {}, 'RuntimeClass verification'),
            ('changed class', self.node('spot-1', owned), {**runtime, 'handler': 'runc'}, 'RuntimeClass verification'),
        ]
        for name, node, klass, message in cases:
            def read(*args, node=node, klass=klass):
                if args[:2] == ('get', 'nodes'):
                    return {'items': [self.server(), node]}
                return node if args[:2] == ('get', 'node') else klass
            with self.subTest(name), self.host() as (_root, agent), \
                 patch.object(actions_runtime.subprocess, 'run') as run, \
                 patch.object(actions_runtime.urllib.request, 'urlopen') as download, \
                 self.assertRaisesRegex(ValueError, message):
                actions_runtime.install_locked({'node_name': 'spot-1'}, {'id': self.IDENT}, ['kubectl'], read,
                                               'vfs', None, agent=agent)
            run.assert_not_called()
            download.assert_not_called()
        with patch.object(actions_runtime, 'RuntimeMaintenanceLock') as lock, \
             self.assertRaisesRegex(ValueError, 'only the VFS'):
            actions_runtime.install_agent('spot-1', self.IDENT, {'kube': ['kubectl']}, None, actions_runtime.WORKSPACE_PROFILE)
        lock.assert_not_called()

    def test_agent_restarts_its_own_unit_and_publishes_only_after_verification(self):
        owned = self.OWNED
        runtime = actions_runtime.runtime_class(self.IDENT)
        events = []
        with self.host() as (root, agent):
            template = agent['template']
            template.write_text('{{ template "base" . }}\n'); template.chmod(0o644)

            def read(*args):
                if args[:2] == ('get', 'nodes'):
                    return {'items': [self.server(), self.node('spot-1', owned)]}
                if args[:2] == ('get', 'node'):
                    events.append('node-check')
                    first = events.count('node-check') == 1
                    return self.node('spot-1', {**owned, actions_runtime.RUNTIME_LABEL: 'ready'} if first else owned)
                events.append('runtimeclass-check')
                return runtime

            def unpack(_archive, destination):
                destination.mkdir()
                for name in ('runsc', 'containerd-shim-runsc-v1', 'gvisor-bin'):
                    (destination / name).write_bytes(b'x'); (destination / name).chmod(0o755)

            def run(args, **_kwargs):
                command = ' '.join(args)
                if args[:2] == ['systemctl', 'restart']:
                    self.assertEqual(args[2], 'k3s-agent')
                    events.append('restart')
                    generated = template.parent / 'config.toml'
                    generated.write_text(actions_runtime.runtime_section(root / 'actions-runtime' / actions_runtime.VERSION, 'vfs'))
                    generated.chmod(0o644)
                elif actions_runtime.RUNTIME_LABEL + '=ready' in command:
                    events.append('publish')
                elif actions_runtime.RUNTIME_LABEL + '-' in command:
                    events.append('withdraw')
                self.assertNotIn('apply', args)
                self.assertFalse(args[0].endswith('hakopod-server'))
                return SimpleNamespace(stdout=b'', stderr=b'')

            empty_sha = actions_runtime.hashlib.sha256(b'').hexdigest()
            original_lstat, original_stat = Path.lstat, Path.stat

            def owned_stat(result):
                return SimpleNamespace(st_mode=result.st_mode, st_uid=0, st_nlink=result.st_nlink,
                                       st_size=result.st_size, st_dev=result.st_dev, st_ino=result.st_ino)

            with patch.multiple(Path, lstat=lambda path: owned_stat(original_lstat(path)),
                                stat=lambda path, **kwargs: owned_stat(original_stat(path, **kwargs))), \
                 patch.dict(actions_runtime.DIGESTS, {'aarch64': empty_sha}), \
                 patch.object(actions_runtime.urllib.request, 'urlopen', return_value=io.BytesIO(b'')), \
                 patch.object(actions_runtime, 'unpack_runtime', side_effect=unpack), \
                 patch.object(actions_runtime, 'verify_installed_runtime'), \
                 patch.object(actions_runtime.subprocess, 'check_output') as unit, \
                 patch.object(actions_runtime.subprocess, 'run', side_effect=run):
                actions_runtime.install_locked({'node_name': 'spot-1'}, {'id': self.IDENT}, ['kubectl'], read,
                                               'vfs', None, agent=agent)
            unit.assert_not_called()
            self.assertEqual((root / 'actions-runtime/workspace-profile').read_text(), 'vfs\n')
            self.assertIn('hakopod-actions', template.read_text())
        self.assertLess(events.index('withdraw'), events.index('restart'))
        self.assertLess(events.index('restart'), events.index('publish'))
        self.assertEqual(events[-1], 'publish')
        self.assertLess(events.index('restart'), len(events) - 1 - events[::-1].index('runtimeclass-check'))


class RuntimeMaintenanceLockTests(unittest.TestCase):
    @contextmanager
    def isolated_lock(self):
        # Use actual temporary file descriptors/flock while making ownership
        # portable to non-root CI. No test opens the production lock path.
        original_stat, original_fstat = os.stat, os.fstat

        def owned(info):
            return SimpleNamespace(st_mode=info.st_mode, st_uid=0, st_nlink=info.st_nlink,
                                   st_dev=info.st_dev, st_ino=info.st_ino, st_size=info.st_size)

        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp).resolve()
            path = root / 'runtime.lock'
            with patch.object(actions_runtime, 'MAINTENANCE_LOCK', path), \
                 patch.object(actions_runtime.os, 'geteuid', return_value=0), \
                 patch.object(actions_runtime.os, 'fstat', side_effect=lambda fd: owned(original_fstat(fd))), \
                 patch.object(actions_runtime.os, 'stat', side_effect=lambda *a, **kw: owned(original_stat(*a, **kw))):
                yield root, path

    def test_lock_contends_preserves_inode_and_contents_and_releases_idempotently(self):
        with self.isolated_lock() as (_root, path):
            path.write_text('preserve'); path.chmod(0o600)
            before = path.stat()
            with actions_runtime.RuntimeMaintenanceLock() as first:
                with self.assertRaisesRegex(ValueError, 'already active'):
                    actions_runtime.RuntimeMaintenanceLock()
                self.assertEqual(path.read_text(), 'preserve')
                self.assertEqual(path.stat().st_ino, before.st_ino)
                first.close(); first.close()
                with actions_runtime.RuntimeMaintenanceLock():
                    self.assertEqual(path.read_text(), 'preserve')
            self.assertTrue(path.exists())

    def test_lock_rejects_unsafe_files_and_parents(self):
        for kind in ('symlink', 'hardlink', 'fifo', 'mode', 'owner', 'directory', 'parent-mode', 'parent-symlink'):
            with self.subTest(kind=kind), self.isolated_lock() as (root, path):
                if kind == 'symlink':
                    path.symlink_to(root / 'target')
                elif kind == 'fifo':
                    os.mkfifo(path, 0o600)
                elif kind == 'directory':
                    path.mkdir()
                else:
                    path.write_text('preserve'); path.chmod(0o600)
                if kind == 'hardlink':
                    os.link(path, root / 'second')
                elif kind == 'mode':
                    path.chmod(0o644)
                elif kind == 'parent-mode':
                    root.chmod(0o777)
                elif kind == 'parent-symlink':
                    (root / 'alias').symlink_to(root, target_is_directory=True)
                    actions_runtime.MAINTENANCE_LOCK = root / 'alias' / path.name
                current_fstat = actions_runtime.os.fstat
                inode = path.lstat().st_ino

                def observed(fd):
                    info = current_fstat(fd)
                    if kind == 'owner' and info.st_ino == inode and stat.S_ISREG(info.st_mode):
                        info.st_uid = 1
                    return info

                with patch.object(actions_runtime.os, 'fstat', side_effect=observed), \
                     self.assertRaises((ValueError, OSError)):
                    actions_runtime.RuntimeMaintenanceLock()

    def test_lock_accepts_root_owned_sticky_parent(self):
        with self.isolated_lock() as (root, _path):
            root.chmod(0o1777)
            with actions_runtime.RuntimeMaintenanceLock():
                pass

    def test_lock_rechecks_path_safety_and_releases_failed_descriptor(self):
        for change in ('replace', 'mode', 'hardlink'):
            with self.subTest(change=change), self.isolated_lock() as (root, path):
                real_flock = actions_runtime.fcntl.flock
                acquired = []

                def replace(fd, flags):
                    real_flock(fd, flags)
                    acquired.append(fd)
                    if change == 'replace':
                        path.unlink()
                        path.write_text('replacement'); path.chmod(0o600)
                    elif change == 'mode':
                        path.chmod(0o644)
                    else:
                        os.link(path, root / 'second')

                with patch.object(actions_runtime.fcntl, 'flock', side_effect=replace), \
                     self.assertRaisesRegex(ValueError, 'changed'):
                    actions_runtime.RuntimeMaintenanceLock()
                with self.assertRaises(OSError):
                    os.fstat(acquired[0])
                if change == 'replace':
                    self.assertEqual(path.read_text(), 'replacement')
                elif change == 'mode':
                    path.chmod(0o600)
                else:
                    (root / 'second').unlink()
                with actions_runtime.RuntimeMaintenanceLock():
                    pass

    def test_non_root_fails_before_opening_any_path(self):
        with patch.object(actions_runtime.os, 'geteuid', return_value=1000), \
             patch.object(actions_runtime.os, 'open') as opened, self.assertRaises(ValueError):
            actions_runtime.RuntimeMaintenanceLock()
        opened.assert_not_called()


class ModuleLockTests(unittest.TestCase):
    def test_outer_install_lock_spans_module_and_failure(self):
        for fail in (False, True):
            with self.subTest(fail=fail):
                lock = io.StringIO()

                def apply(module, profile):
                    self.assertEqual((module, profile), ('managed-actions', actions_runtime.WORKSPACE_PROFILE))
                    self.assertFalse(lock.closed)
                    flock.assert_called_once_with(lock, modules.fcntl.LOCK_EX | modules.fcntl.LOCK_NB)
                    if fail:
                        raise RuntimeError('module failed')

                with patch.object(modules.os, 'geteuid', return_value=0), \
                     patch.object(modules.os, 'umask') as umask, \
                     patch('builtins.open', return_value=lock), \
                     patch.object(modules.fcntl, 'flock') as flock, \
                     patch.object(modules, 'apply_module', side_effect=apply):
                    if fail:
                        with self.assertRaisesRegex(RuntimeError, 'module failed'):
                            modules.main('managed-actions', actions_runtime.WORKSPACE_PROFILE)
                    else:
                        modules.main('managed-actions', actions_runtime.WORKSPACE_PROFILE)
                self.assertTrue(lock.closed)
                umask.assert_called_once_with(0o077)

    def test_non_root_fails_before_opening_outer_lock(self):
        with patch.object(modules.os, 'geteuid', return_value=1000), \
             patch('builtins.open') as opened, self.assertRaises(ValueError):
            modules.main('managed-actions')
        opened.assert_not_called()


if __name__ == '__main__':
    unittest.main()
