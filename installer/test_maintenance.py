import io
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

import maintenance as m

class MaintenanceTests(unittest.TestCase):
    def test_versions_channels_and_order(self):
        self.assertLess(m.version_key('0.1.0-alpha.4'),m.version_key('0.1.0-alpha.10'))
        self.assertLess(m.version_key('0.1.0-alpha.10'),m.version_key('0.1.0'))
        for bad in ['../../x','1.2','1.2.3-01','v1.2.3','1.2.3-','1.2.3-foo..x']:
            with self.assertRaises(ValueError):m.version_key(bad)
        def release(v,pre=False):return dict(tag_name='v'+v,prerelease=pre,assets=[{'name':'upgrade.json'},{'name':'SHA256SUMS'}])
        self.assertIsNone(m.eligible([release('0.2.0'),release('0.1.1-alpha.1',True)],'0.1.0'))
        self.assertEqual(m.eligible([release('0.1.0-alpha.5',True)],'0.1.0-alpha.4')['tag_name'],'v0.1.0-alpha.5')

    def test_checksum_and_asset_authority(self):
        digest='a'*64
        self.assertEqual(m.checksum_map((digest+'  upgrade.json\n').encode()),{'upgrade.json':digest})
        for data in [digest+'  ../x',digest+'  x\n'+digest+'  x','bad']:
            with self.assertRaises(ValueError):m.checksum_map(data.encode())
        with self.assertRaises(ValueError):m.asset_url({'tag_name':'v1.0.0','assets':[{'name':'upgrade.json','browser_download_url':'https://evil.test/upgrade.json'}]},'upgrade.json')

    def test_manifest_requires_explicit_compatible_source_and_pins(self):
        manifest={'schema_version':1,'version':'0.1.0-alpha.5','from_versions':['0.1.0-alpha.4'],'runtime_pins_sha256':m.host.digest(Path(m.__file__).with_name('pins.json'))}
        m.validate_manifest(manifest,'0.1.0-alpha.4','0.1.0-alpha.5')
        for update in [{'from_versions':[]},{'runtime_pins_sha256':'wrong'},{'schema_version':2}]:
            with self.assertRaises(ValueError):m.validate_manifest(dict(manifest,**update),'0.1.0-alpha.4','0.1.0-alpha.5')

    def test_accepted_preflight_failure_is_terminal(self):
        with tempfile.TemporaryDirectory() as tmp,patch.object(m,'STATE',Path(tmp)),patch.object(m,'INSTALL_LOCK',Path(tmp)/'install.lock'),patch.object(m,'perform_upgrade',side_effect=ValueError('unsupported source')):
            m.save_state({'status':'queued','version':'0.1.0-alpha.5'})
            with self.assertRaises(ValueError):m.upgrade('0.1.0-alpha.5')
            state=json.loads((Path(tmp)/'status.json').read_text())
            self.assertEqual(state['status'],'failed')
            self.assertFalse(m.LOCK.locked())

    def test_concurrent_dispatch_is_rejected(self):
        m.LOCK.acquire()
        try:
            with self.assertRaises(ValueError):m.upgrade('0.1.0-alpha.5')
        finally:m.LOCK.release()


class UpgradeLifecycleTests(unittest.TestCase):
    def run_upgrade(self, failure=None):
        import contextlib
        from types import SimpleNamespace
        with tempfile.TemporaryDirectory() as tmp, contextlib.ExitStack() as stack:
            root=Path(tmp).resolve(); releases=root/'releases';releases.mkdir()
            old=releases/'0.1.0-alpha.4';old.mkdir()
            current=root/'current';current.symlink_to('releases/'+old.name)
            config=root/'config.json';config.write_text(json.dumps({'dashboard_origin':'http://localhost:3000','dashboard_port':3000,'dashboard_mode':'ssh'}))
            marker=root/'installation.json';marker.write_text(json.dumps({'completed': True, 'id': 'a'*32}))
            for key,value in {'STATE':root/'state','CONFIG':config,'MARKER':marker,'CURRENT':current,'RELEASE_DIR':releases,'INSTALL_LOCK':root/'lock'}.items():stack.enter_context(patch.object(m,key,value))
            m.STATE.mkdir(); events=[]
            target='0.1.0-alpha.5'
            manifest={'schema_version':1,'version':target,'from_versions':[old.name],'runtime_pins_sha256':m.host.digest(Path(m.__file__).with_name('pins.json'))}
            release={'tag_name':'v'+target,'assets':[{'name':'SHA256SUMS','browser_download_url':'https://github.com/hakopod/hakopod/releases/download/v'+target+'/SHA256SUMS'}]}
            def fetch(url,limit):return json.dumps(release).encode() if '/tags/' in url else b'a'*64+b'  upgrade.json\n'
            stack.enter_context(patch.object(m,'fetch',side_effect=fetch))
            def download(release,name,hashes,stage,limit):
                path=stage/name;path.write_text(json.dumps(manifest) if name=='upgrade.json' else 'fixture');return path
            stack.enter_context(patch.object(m,'download',side_effect=download))
            def unpack(source,destination,name):
                path=destination/name;path.mkdir(parents=True)
                if 'dashboard' in name:(path/'dist/server').mkdir(parents=True);(path/'dist/server/server.js').write_text('fixture');(path/'serve.mjs').write_text('fixture')
                else:(path/'hakopod').write_text('fixture')
            stack.enter_context(patch.object(m.host,'unpack',side_effect=unpack))
            stack.enter_context(patch.object(m,'validate_installed_runtime'))
            stack.enter_context(patch.object(m.os,'uname',return_value=SimpleNamespace(machine='aarch64')))
            stack.enter_context(patch.object(m.shutil,'disk_usage',return_value=SimpleNamespace(free=20<<30)))
            stack.enter_context(patch.object(m.subprocess,'check_output',return_value=target.encode()))
            def command(args,**kwargs):
                events.append(args)
                if failure=='stop' and args[1]=='stop':raise RuntimeError('partial stop')
            stack.enter_context(patch.object(m,'command',side_effect=command))
            def backup(c,d):
                events.append(['backup'])
                if failure=='backup':raise RuntimeError('backup failed')
            stack.enter_context(patch.object(m,'backup',side_effect=backup))
            stack.enter_context(patch.object(m,'enable'))
            def repair(installation):
                self.assertEqual(installation, 'a'*32)
                events.append(['repair-credentials'])
            stack.enter_context(patch.object(m.credentials,'repair_namespace',side_effect=repair))
            response=contextlib.nullcontext(SimpleNamespace(status=200))
            stack.enter_context(patch.object(m.urllib.request,'urlopen',return_value=response))
            stack.enter_context(patch.object(m,'dashboard_health',return_value=failure!='health'))
            stack.enter_context(patch.object(m.time,'sleep'))
            if failure:
                with self.assertRaises((ValueError,RuntimeError)):m.upgrade(target)
            else:m.upgrade(target)
            state=json.loads((m.STATE/'status.json').read_text())
            self.assertEqual((releases/target).exists(), not failure or failure=='health')
            return state,current.resolve().name,events

    def test_success_switches_only_after_backup(self):
        state,current,events=self.run_upgrade()
        self.assertEqual(state['status'],'succeeded');self.assertEqual(current,'0.1.0-alpha.5')
        self.assertLess(events.index(['backup']),events.index(['systemctl','start','hakopod-api','hakopod-dashboard']))
        self.assertLess(events.index(['backup']),events.index(['repair-credentials']))
        self.assertLess(events.index(['repair-credentials']),events.index(['systemctl','start','hakopod-api','hakopod-dashboard']))

    def test_partial_stop_and_backup_failure_restart_old_version(self):
        for failure in ['stop','backup']:
            state,current,events=self.run_upgrade(failure)
            self.assertEqual(state['status'],'failed');self.assertEqual(current,'0.1.0-alpha.4')
            self.assertIn(['systemctl','start','hakopod-api','hakopod-dashboard'],events)

    def test_failure_after_switch_does_not_restore_older_binary(self):
        state,current,events=self.run_upgrade('health')
        self.assertEqual(state['status'],'failed');self.assertEqual(current,'0.1.0-alpha.5')
        self.assertIn('administrator recovery',state['message'])

    def test_sensitive_log_entries_are_omitted(self):
        for value in ['{"token":"sample-token-value"}','{"client_secret": "sample-secret-value"}','Authorization: Bearer private','password=private']:
            self.assertNotIn('private',m.redact_log(value));self.assertNotIn('sample-',m.redact_log(value))
        self.assertEqual(m.redact_log('API ready'),'API ready')

class MaintenanceRegressionTests(unittest.TestCase):
    def test_existing_database_backup_uses_explicit_libpq_fields(self):
        from types import SimpleNamespace
        for mode, host in [('local', '127.0.0.1'), ('external', 'database.example.test')]:
            sslmode = 'disable' if mode=='local' else 'verify-full'
            url = f'postgresql://backup:private%3Apassword@{host}:55432/hakopod?sslmode={sslmode}'
            with tempfile.TemporaryDirectory() as tmp, patch.object(m.tarfile, 'open'), \
                    patch.object(m.shutil, 'disk_usage', return_value=SimpleNamespace(free=20<<30)), \
                    patch.object(m.host.database, 'read_url_file', return_value=url), \
                    patch.dict(m.os.environ, {'PGHOST':'wrong-host', 'PGSERVICE':'wrong-service', 'PGOPTIONS':'-c role=other'}):
                def command(argv, **kwargs):
                    env = kwargs['env']
                    self.assertEqual(env['PGDATABASE'], 'hakopod')
                    self.assertEqual(env['PGHOST'], host)
                    self.assertEqual(env['PGPORT'], '55432')
                    self.assertEqual(env['PGUSER'], 'backup')
                    self.assertEqual(env['PGPASSWORD'], 'private:password')
                    self.assertEqual(env['PGSSLMODE'], sslmode)
                    self.assertNotIn('PGSERVICE', env)
                    self.assertNotIn('PGOPTIONS', env)
                    self.assertNotIn('private', str(argv))
                    if mode == 'external': self.assertTrue(env['PGSSLROOTCERT'])
                    kwargs['stdout'].write(b'PGDMPfixture')
                with patch.object(m, 'command', side_effect=command):
                    m.backup({'database_mode': mode}, Path(tmp))

    def test_installed_runtime_mismatch_blocks_new_bootstrap(self):
        from types import SimpleNamespace
        with patch.object(m.os,'uname',return_value=SimpleNamespace(machine='aarch64')), patch.object(m.Path,'exists',return_value=True), patch.object(m.host,'digest',side_effect=['old-runtime-pins','new-runtime-pins']):
            with self.assertRaisesRegex(ValueError,'Installed runtime pins differ'):m.validate_installed_runtime()

    def test_host_system_exit_becomes_terminal_failure(self):
        with tempfile.TemporaryDirectory() as tmp,patch.object(m,'STATE',Path(tmp)),patch.object(m,'INSTALL_LOCK',Path(tmp)/'install.lock'),patch.object(m,'perform_upgrade',side_effect=SystemExit('archive rejected')):
            m.save_state({'status':'downloading','version':'0.1.0-alpha.5'})
            with self.assertRaises(SystemExit):m.upgrade('0.1.0-alpha.5')
            self.assertEqual(json.loads((Path(tmp)/'status.json').read_text())['status'],'failed')
            self.assertFalse(m.LOCK.locked())

    def test_unpack_permissions_allow_service_users_to_traverse(self):
        import tarfile
        import os
        with tempfile.TemporaryDirectory() as tmp:
            root=Path(tmp);archive=root/'bundle.tar.gz'
            with tarfile.open(archive,'w:gz') as tar:
                file=tarfile.TarInfo('release/nested/file');file.size=7;file.mode=0o644;tar.addfile(file,io.BytesIO(b'fixture'))
            old=os.umask(0o077)
            try:m.host.unpack(archive,root/'out','release')
            finally:os.umask(old)
            for path in [root/'out/release',root/'out/release/nested']:
                self.assertEqual(path.stat().st_mode & 0o777,0o755)

    def test_kubectl_multi_document_output(self):
        import modules
        obj={'kind':'Namespace','metadata':{'name':'one'}}
        self.assertEqual(len(modules.documents(json.dumps(obj)+'\n'+json.dumps(obj))['items']),2)
        self.assertEqual(len(modules.documents(json.dumps({'kind':'List','items':[obj]}))['items']),1)

class CrossProcessLockTests(unittest.TestCase):
    def test_busy_file_lock_never_overwrites_running_status(self):
        import fcntl
        with tempfile.TemporaryDirectory() as tmp,patch.object(m,'STATE',Path(tmp)),patch.object(m,'INSTALL_LOCK',Path(tmp)/'install.lock'):
            original={'status':'downloading','version':'0.1.0-alpha.5','message':'First upgrade running'}
            m.save_state(original)
            with m.INSTALL_LOCK.open('a') as lock:
                fcntl.flock(lock,fcntl.LOCK_EX|fcntl.LOCK_NB)
                with self.assertRaises(BlockingIOError):m.upgrade('0.1.0-alpha.5')
            self.assertEqual(json.loads((m.STATE/'status.json').read_text()),original)
            self.assertFalse(m.LOCK.locked())

if __name__=='__main__':unittest.main()
