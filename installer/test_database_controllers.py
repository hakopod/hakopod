"""Controller installer safety fixtures; no Kubernetes or network calls."""
import hashlib
import json
from pathlib import Path
import tempfile
import time
import unittest
from unittest.mock import patch

import database_controllers as controllers


class ControllerPlanTests(unittest.TestCase):
    def bundle(self, root, objects, engine='clickhouse'):
        data=json.dumps({'apiVersion':'v1','kind':'List','items':objects}).encode()
        (root/(engine+'.json')).write_bytes(data)
        (root/'manifest.json').write_text(json.dumps({'schema_version':1,'files':{engine+'.json':hashlib.sha256(data).hexdigest()}}))

    def test_vitess_cannot_install_a_shared_controller_or_partial_crds(self):
        for objects in ([], [{'apiVersion':'apps/v1','kind':'Deployment','metadata':{'name':'unexpected-controller'}}]):
            with tempfile.TemporaryDirectory() as tmp:
                root=Path(tmp)
                self.bundle(root,objects,'vitess')
                with self.assertRaisesRegex(ValueError,'eight'):
                    controllers.load_bundle(root,['vitess'])

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

    def test_expired_plan_does_not_contact_cluster(self):
        with patch.object(controllers,'make_plan') as make:
            with self.assertRaisesRegex(ValueError,'expired'):
                controllers.apply_plan(Path('/unused'),{'schema_version':1,'created_at':int(time.time())-1801},['kubectl'])
            make.assert_not_called()
