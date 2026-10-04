"""Controller installer safety fixtures; no Kubernetes or network calls."""
import hashlib
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import time
import unittest
from unittest.mock import Mock,patch

import database_controllers as controllers


class ControllerPlanTests(unittest.TestCase):
    def test_mysql_peering_is_infrastructure_with_one_exact_identity(self):
        peering={'apiVersion':'zalando.org/v1','kind':'ClusterKopfPeering','metadata':{'name':'mysql-operator','labels':{'app.kubernetes.io/name':'mysql-operator','app.kubernetes.io/instance':'mysql-operator','app.kubernetes.io/component':'controller'}}}
        controllers.validate_controller_objects([peering])
        for changed in (
            dict(peering,spec={}),
            dict(peering,metadata=dict(peering['metadata'],name='another-controller')),
            dict(peering,metadata=dict(peering['metadata'],namespace='customer')),
            dict(peering,metadata=dict(peering['metadata'],labels={})),
        ):
            with self.subTest(changed=changed),self.assertRaisesRegex(ValueError,'peering identity'):
                controllers.validate_controller_objects([changed])
        with patch.object(controllers,'read',return_value={}) as read:
            self.assertEqual(controllers.current(['kubectl'],peering),{})
            read.assert_called_once_with(['kubectl'],'get','crd','clusterkopfpeerings.zalando.org','--ignore-not-found')

    def bundle(self, root, objects, engine='clickhouse'):
        data=json.dumps({'apiVersion':'v1','kind':'List','items':objects}).encode()
        (root/(engine+'.json')).write_bytes(data)
        (root/'manifest.json').write_text(json.dumps({'schema_version':1,'files':{engine+'.json':hashlib.sha256(data).hexdigest()}}))

    def release_bundle(self,root,vitess=False,vitess_objects=None):
        files={}
        engines=['postgresql','redis','mysql','mongodb']+(['vitess'] if vitess else [])
        for engine in engines:
            data=json.dumps({'apiVersion':'v1','kind':'List','items':(vitess_objects or []) if engine=='vitess' else []}).encode()
            (root/(engine+'.json')).write_bytes(data)
            files[engine+'.json']=hashlib.sha256(data).hexdigest()
        manifest={'schema_version':2,'source_revision':'a'*40,'managed_runtimes':{'vitess':vitess,'supabase':False,'neon':False},'files':files}
        (root/'manifest.json').write_text(json.dumps(manifest))
        return manifest

    def test_vitess_cannot_install_a_shared_controller_or_partial_crds(self):
        for objects in ([], [{'apiVersion':'apps/v1','kind':'Deployment','metadata':{'name':'unexpected-controller'}}]):
            with tempfile.TemporaryDirectory() as tmp:
                root=Path(tmp)
                self.release_bundle(root,True,objects)
                with self.assertRaisesRegex(ValueError,'eight'):
                    controllers.load_bundle(root,['vitess'])

    def test_legacy_bundles_cannot_enable_vitess(self):
        with tempfile.TemporaryDirectory() as tmp:
            root=Path(tmp)
            self.bundle(root,[],'vitess')
            with self.assertRaisesRegex(ValueError,'availability record in schema 2'):
                controllers.load_bundle(root,['vitess'])

    def test_schema2_enforces_boolean_gates_and_exact_controller_files(self):
        with tempfile.TemporaryDirectory() as tmp:
            root=Path(tmp)
            original=self.release_bundle(root)
            controllers.load_bundle(root,['postgresql','redis','mysql','mongodb'])
            for changed in ({'managed_runtimes':{}}, {'managed_runtimes':{'vitess':0,'supabase':False,'neon':False}},
                            {'managed_runtimes':{'vitess':False,'supabase':False,'neon':False,'extra':False}},
                            {'source_revision':'HEAD'}, {'source_revision':None}):
                (root/'manifest.json').write_text(json.dumps(dict(original,**changed)))
                with self.subTest(changed=changed),self.assertRaisesRegex(ValueError,'availability and source revision'):
                    controllers.load_bundle(root,['postgresql'])
            (root/'manifest.json').write_text(json.dumps(original))
            (root/'mysql.json').unlink()
            with self.assertRaisesRegex(ValueError,'files differ'):
                controllers.load_bundle(root,['postgresql'])
            mysql=json.dumps({'apiVersion':'v1','kind':'List','items':[]}).encode()
            (root/'mysql.json').write_bytes(mysql)
            (root/'vitess.json').write_text('{}')
            with self.assertRaisesRegex(ValueError,'files differ'):
                controllers.load_bundle(root,['postgresql'])
            original['files']['vitess.json']=hashlib.sha256(b'{}').hexdigest()
            (root/'manifest.json').write_text(json.dumps(original))
            with self.assertRaisesRegex(ValueError,'files differ'):
                controllers.load_bundle(root,['vitess'])

    def test_schema2_checks_unselected_files_and_duplicate_keys(self):
        with tempfile.TemporaryDirectory() as tmp:
            root=Path(tmp)
            self.release_bundle(root)
            manifest=root/'manifest.json'
            original=manifest.read_text()
            manifest.write_text(original.replace('"vitess": false','"vitess": false, "vitess": true'))
            with self.assertRaisesRegex(ValueError,'Duplicate'):
                controllers.load_bundle(root,['postgresql'])
            manifest.write_text(original)
            (root/'mongodb.json').write_text('{}')
            with self.assertRaisesRegex(ValueError,'checksum'):
                controllers.load_bundle(root,['postgresql'])

    def test_installed_helper_validates_schema2_without_release_source(self):
        with tempfile.TemporaryDirectory() as tmp:
            root=Path(tmp)
            bundle=root/'bundle';bundle.mkdir()
            self.release_bundle(bundle)
            shutil.copyfile(Path(controllers.__file__),root/'database_controllers.py')
            command=[sys.executable,'-B','-c',"from pathlib import Path; import database_controllers; database_controllers.load_bundle(Path('bundle'), ['postgresql', 'redis', 'mysql', 'mongodb'])"]
            env=dict(os.environ,PYTHONDONTWRITEBYTECODE='1',PYTHONPATH=str(root))
            subprocess.run(command,cwd=root,env=env,check=True,capture_output=True,timeout=15)

    def test_existing_engine_install_does_not_require_vitess_qualification(self):
        with tempfile.TemporaryDirectory() as tmp:
            root=Path(tmp)
            self.bundle(root,[])
            _,objects=controllers.load_bundle(root,['clickhouse'])
            self.assertEqual(objects,[])
            with self.assertRaisesRegex(ValueError,'does not include vitess'):
                controllers.load_bundle(root,['vitess'])

    def test_manifest_must_be_regular_bounded_and_strictly_versioned(self):
        with tempfile.TemporaryDirectory() as tmp:
            root=Path(tmp)
            self.bundle(root,[])
            manifest=root/'manifest.json'
            original=manifest.read_text()
            for value in ([], {}, {'schema_version':True,'files':{}}, {'schema_version':1,'files':[]}):
                manifest.write_text(json.dumps(value))
                with self.assertRaisesRegex(ValueError,'schema 1'):
                    controllers.load_bundle(root,['clickhouse'])
            manifest.write_text(' '*(256*1024+1))
            with self.assertRaisesRegex(ValueError,'oversized'):
                controllers.load_bundle(root,['clickhouse'])
            manifest.unlink()
            outside=root/'outside.json';outside.write_text(original)
            manifest.symlink_to(outside)
            with self.assertRaisesRegex(ValueError,'symbolic'):
                controllers.load_bundle(root,['clickhouse'])

    def test_tampered_bundle_and_customer_workload_are_refused(self):
        with tempfile.TemporaryDirectory() as tmp:
            root=Path(tmp)
            self.bundle(root,[{'apiVersion':'v1','kind':'PersistentVolumeClaim','metadata':{'name':'customer-data'}}])
            with self.assertRaisesRegex(ValueError,'customer data'):
                controllers.load_bundle(root,['clickhouse'])
            self.bundle(root,[])
            (root/'clickhouse.json').write_text('{}')
            with self.assertRaisesRegex(ValueError,'checksum'):
                controllers.load_bundle(root,['clickhouse'])

    def test_existing_foreign_resources_cannot_be_adopted(self):
        obj={'kind':'Deployment','metadata':{'name':'controller','uid':'one','resourceVersion':'2','labels':{controllers.OWNER:'another-installation'}}}
        with self.assertRaisesRegex(ValueError,'unowned'):
            controllers.fingerprint(obj,'owned')

    def test_unlisted_workloads_and_custom_resources_cannot_enter_bundle(self):
        for version,kind in [('batch/v1','Job'),('batch/v1','CronJob'),('apps/v1','DaemonSet'),('apps/v1','ReplicaSet'),('clickhouse.altinity.com/v1','ClickHouseInstallation'),('mysql.oracle.com/v2','InnoDBCluster'),('untrusted.example/v1','Deployment')]:
            with self.subTest(kind=kind),tempfile.TemporaryDirectory() as tmp:
                root=Path(tmp)
                self.bundle(root,[{'apiVersion':version,'kind':kind,'metadata':{'name':'unexpected'}}])
                with self.assertRaisesRegex(ValueError,'explicit infrastructure'):
                    controllers.load_bundle(root,['clickhouse'])

    def test_only_controller_infrastructure_is_accepted(self):
        objects=[{'apiVersion':version,'kind':kind,'metadata':{'name':'controller-'+kind.lower()}} for version,kinds in controllers.CONTROLLER_RESOURCES.items() for kind in sorted(kinds)]
        with tempfile.TemporaryDirectory() as tmp:
            root=Path(tmp)
            self.bundle(root,objects)
            _,actual=controllers.load_bundle(root,['clickhouse'])
            self.assertEqual(actual,objects)

    def test_changed_cluster_bundle_or_object_stops_before_mutation(self):
        plan={'schema_version':1,'created_at':int(time.time()),'installation':'owned','cluster_uid':'cluster-a','bundle_sha256':'a','engines':['clickhouse'],'resources':[{'identity':['v1','Secret','clickhouse-operator','controller'],'existing':{'uid':'one','resource_version':'2'}}]}
        for field,value in [('cluster_uid','cluster-b'),('bundle_sha256','b'),('resources',[])]:
            changed=dict(plan,**{field:value})
            with patch.object(controllers,'make_plan',return_value=changed),patch.object(controllers.subprocess,'run') as run:
                with self.assertRaisesRegex(ValueError,'changed'):
                    controllers.apply_plan(Path('/unused'),plan,['kubectl'])
                run.assert_not_called()

    def test_controller_credentials_survive_an_upgrade(self):
        secret={'apiVersion':'v1','kind':'Secret','metadata':{'name':'controller','namespace':'clickhouse-operator','annotations':{'hakopod.io/generate-controller-credential':'clickhouse-v1'}}}
        existing={'apiVersion':'v1','kind':'Secret','metadata':{'name':'controller','namespace':'clickhouse-operator','uid':'stable','resourceVersion':'5','labels':{controllers.OWNER:'owned'}},'data':{'password':'must-not-change'}}
        plan={'schema_version':1,'created_at':int(time.time()),'installation':'owned','cluster_uid':'cluster-a','bundle_sha256':'a','engines':['clickhouse'],'resources':[{'identity':list(controllers.key(secret)),'existing':{'uid':'stable','resource_version':'5'}}]}
        with patch.object(controllers,'make_plan',return_value=plan),patch.object(controllers,'load_bundle',return_value=('a',[secret])),patch.object(controllers,'current',return_value=existing),patch.object(controllers.subprocess,'run') as run:
            controllers.apply_plan(Path('/unused'),plan,['kubectl'])
            run.assert_not_called()

    def test_failed_apply_uses_review_bound_replace_and_preserves_foreign_fields(self):
        desired={'apiVersion':'apps/v1','kind':'Deployment','metadata':{'name':'controller','namespace':'operators','annotations':{'hakopod.io/policy':'reviewed'}},'spec':{'template':{'metadata':{'annotations':{'hakopod.io/runtime':'reviewed'}},'spec':{'containers':[{'name':'controller','image':'example.invalid/controller:new'}]}}}}
        existing={'apiVersion':'apps/v1','kind':'Deployment','metadata':{'name':'controller','namespace':'operators','uid':'stable','resourceVersion':'5','labels':{controllers.OWNER:'owned','app.kubernetes.io/managed-by':'hakopod','foreign':'preserved'},'annotations':{'hakopod.io/policy':'reviewed','foreign':'preserved'},'managedFields':[{'manager':'another'}]},'spec':{'template':{'metadata':{'annotations':{'hakopod.io/runtime':'reviewed','foreign':'preserved'}},'spec':{'containers':[{'name':'controller','image':'example.invalid/controller:old','env':[{'name':'FOREIGN','value':'preserved'}]},{'name':'foreign-sidecar','image':'example.invalid/sidecar:1'}]}}},'status':{'availableReplicas':1}}
        plan={'schema_version':1,'created_at':int(time.time()),'installation':'owned','cluster_uid':'cluster-a','bundle_sha256':'a','engines':['clickhouse'],'resources':[{'identity':list(controllers.key(desired)),'existing':{'uid':'stable','resource_version':'5'}}]}
        results=[Mock(returncode=1),Mock(returncode=0),Mock(returncode=0)]
        with patch.object(controllers,'make_plan',return_value=plan),patch.object(controllers,'load_bundle',return_value=('a',[desired])),patch.object(controllers,'current',side_effect=[existing,existing]),patch.object(controllers.subprocess,'run',side_effect=results) as run:
            controllers.apply_plan(Path('/unused'),plan,['kubectl'])
        self.assertIn('apply',run.call_args_list[0].args[0])
        self.assertNotIn('--force-conflicts',run.call_args_list[0].args[0])
        self.assertIn('replace',run.call_args_list[1].args[0])
        replacement=json.loads(run.call_args_list[1].kwargs['input'])
        self.assertEqual(replacement['metadata']['uid'],'stable')
        self.assertEqual(replacement['metadata']['resourceVersion'],'5')
        self.assertEqual(replacement['metadata']['labels']['foreign'],'preserved')
        self.assertEqual(replacement['metadata']['annotations']['foreign'],'preserved')
        self.assertNotIn('managedFields',replacement['metadata'])
        self.assertNotIn('status',replacement)
        containers={item['name']:item for item in replacement['spec']['template']['spec']['containers']}
        self.assertEqual(containers['controller']['image'],'example.invalid/controller:new')
        self.assertEqual(containers['controller']['env'],[{'name':'FOREIGN','value':'preserved'}])
        self.assertEqual(containers['foreign-sidecar']['image'],'example.invalid/sidecar:1')

    def test_replace_fallback_rejects_identity_ownership_and_annotation_changes(self):
        desired={'apiVersion':'apps/v1','kind':'Deployment','metadata':{'name':'controller','namespace':'operators','labels':{controllers.OWNER:'owned','app.kubernetes.io/managed-by':'hakopod'},'annotations':{'hakopod.io/policy':'reviewed'}}}
        existing={'apiVersion':'apps/v1','kind':'Deployment','metadata':{'name':'controller','namespace':'operators','uid':'stable','resourceVersion':'5','labels':{controllers.OWNER:'owned','app.kubernetes.io/managed-by':'hakopod'},'annotations':{'hakopod.io/policy':'reviewed'}}}
        reviewed={'uid':'stable','resource_version':'5'}
        changed=[
            dict(existing,metadata=dict(existing['metadata'],resourceVersion='6')),
            dict(existing,metadata=dict(existing['metadata'],labels={controllers.OWNER:'owned','app.kubernetes.io/managed-by':'foreign'})),
            dict(existing,metadata=dict(existing['metadata'],annotations={'hakopod.io/policy':'changed'})),
        ]
        for current in changed:
            with self.subTest(current=current),self.assertRaisesRegex(ValueError,'replace fallback|changed'):
                controllers.owned_replace_object(current,desired,'owned',reviewed)

    def test_expired_plan_does_not_contact_cluster(self):
        with patch.object(controllers,'make_plan') as make:
            with self.assertRaisesRegex(ValueError,'expired'):
                controllers.apply_plan(Path('/unused'),{'schema_version':1,'created_at':int(time.time())-1801},['kubectl'])
            make.assert_not_called()
