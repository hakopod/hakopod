"""Build fixtures exercise release admission without downloads or Kubernetes."""
import json
import hashlib
from pathlib import Path
import runpy
import sys
import tempfile
import unittest
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[1]
with patch.object(sys, 'path', [str(ROOT / 'installer'), *sys.path]):
    import build_database_controllers as builder

AVAILABILITY = runpy.run_path(str(ROOT / 'release/managed-runtime-availability.py'))
RENDER = builder.render


class BuildDatabaseControllersTest(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)
        self.here = self.root / 'installer'
        self.here.mkdir()
        (self.here / 'database-controller-sources.json').write_text(json.dumps(dict.fromkeys(builder.RELEASE_ENGINES, {})))
        (self.here / 'pins.json').write_text('{"helm": {}}')
        self.set_gates(False)
        self.patchers = [
            patch.object(builder, 'HERE', self.here),
            patch.object(builder.runpy, 'run_path', return_value={**AVAILABILITY, 'source_revision': lambda root: 'a' * 40}),
            patch.object(builder, 'helm_binary', return_value=Path('/fixture/helm')),
            patch.object(builder, 'render', side_effect=lambda engine, *args: [{'apiVersion': 'v1', 'kind': 'Namespace', 'metadata': {'name': engine}}]),
            patch.object(builder, 'qualify_vitess'),
            patch.object(builder, 'render_vitess', return_value=([], {'fixture': True})),
            patch.object(builder, 'qualify_oracle_free'),
            patch.object(builder, 'render_oracle_free', return_value=([], {'fixture': True})),
        ]
        self.mocks = [item.start() for item in self.patchers]
        for item in self.patchers:
            self.addCleanup(item.stop)

    def set_gates(self,enabled):
        for relative,name,kind in AVAILABILITY['GATES'].values():
            path=self.root/relative
            path.parent.mkdir(parents=True,exist_ok=True)
            value='true' if enabled else 'false'
            source=f'const {name} = {value}\n' if kind=='const' else f'func {name}() bool {{ return {value} }}\n'
            path.write_text('package fixture\n'+source)

    def test_default_bundle_omits_held_vitess_without_qualification_access(self):
        output=self.root/'held'
        builder.build(output,builder.REDIS_CONTROLLER_IMAGE)
        manifest=json.loads((output/'manifest.json').read_text())
        self.assertEqual(manifest['schema_version'],3)
        self.assertEqual(manifest['managed_runtimes'],dict.fromkeys(AVAILABILITY['GATES'],False))
        self.assertEqual(manifest['source_revision'],'a'*40)
        self.assertEqual(set(manifest['files']),{'postgresql.json','redis.json','mysql.json','mongodb.json','clickhouse.json'})
        self.assertTrue((output/'mysql.json').is_file())
        self.assertTrue((output/'clickhouse.json').is_file())
        self.assertFalse((output/'vitess.json').exists())
        self.mocks[4].assert_not_called()
        self.mocks[5].assert_not_called()
        self.mocks[6].assert_not_called()
        self.mocks[7].assert_not_called()
        self.assertFalse((output/'oracle-free.json').exists())

    def test_caller_and_environment_cannot_enable_a_held_runtime(self):
        with patch.dict(builder.os.environ,{'HAKOPOD_INCLUDE_VITESS':'true','HAKOPOD_VITESS_RELEASE_QUALIFIED':'true'}):
            with self.assertRaisesRegex(ValueError,'compiled release gate'):
                builder.build(self.root/'override',builder.REDIS_CONTROLLER_IMAGE,True)
        self.mocks[2].assert_not_called()
        self.mocks[4].assert_not_called()

    def test_enabled_runtime_requires_qualification_and_cannot_be_omitted(self):
        self.set_gates(True)
        with self.assertRaisesRegex(ValueError,'compiled release gate'):
            builder.build(self.root/'omitted',builder.REDIS_CONTROLLER_IMAGE,False)
        output=self.root/'qualified'
        builder.build(output,builder.REDIS_CONTROLLER_IMAGE)
        self.mocks[4].assert_called_once_with(self.root)
        self.mocks[5].assert_called_once()
        self.assertTrue((output/'vitess.json').is_file())
        self.assertTrue(json.loads((output/'manifest.json').read_text())['managed_runtimes']['vitess'])
        self.mocks[6].assert_called_once_with(self.root)
        self.mocks[7].assert_called_once()
        self.assertTrue((output/'oracle-free.json').is_file())

    def test_oracle_qualification_failure_stops_before_downloads(self):
        relative,name,_=AVAILABILITY['GATES']['oracle-free']
        (self.root/relative).write_text('package fixture\nconst '+name+' = true\n')
        self.mocks[6].side_effect=ValueError('Oracle native evidence is absent')
        output=self.root/'oracle-unqualified'
        with self.assertRaisesRegex(ValueError,'Oracle native evidence is absent'):
            builder.build(output,builder.REDIS_CONTROLLER_IMAGE)
        self.assertFalse(output.exists())
        self.mocks[2].assert_not_called()

    def test_enabled_qualification_failure_cannot_create_a_bundle(self):
        self.set_gates(True)
        self.mocks[4].side_effect=ValueError('native evidence is absent')
        output=self.root/'unqualified'
        with self.assertRaisesRegex(ValueError,'native evidence is absent'):
            builder.build(output,builder.REDIS_CONTROLLER_IMAGE)
        self.assertFalse(output.exists())
        self.mocks[2].assert_not_called()

    def test_missing_source_gate_stops_before_downloads(self):
        (self.root/AVAILABILITY['GATES']['neon'][0]).unlink()
        with self.assertRaisesRegex(ValueError,'Missing'):
            builder.build(self.root/'missing',builder.REDIS_CONTROLLER_IMAGE)
        self.mocks[2].assert_not_called()

    def test_mysql_renderer_emits_the_runtime_admission_contract(self):
        crd={'apiVersion':'apiextensions.k8s.io/v1','kind':'CustomResourceDefinition','metadata':{'name':'innodbclusters.mysql.oracle.com'}}
        deployment={'apiVersion':'apps/v1','kind':'Deployment','metadata':{'name':'mysql-operator','namespace':'mysql-operator'},'spec':{'template':{'spec':{'containers':[{'name':'mysql-operator','image':'registry.example/mysql-operator:fixture','env':[{'name':'MYSQL_OPERATOR_DEBUG','value':'1'},{'name':'MYSQLSH_CREDENTIAL_STORE_SAVE_PASSWORDS','valueFrom':{'secretKeyRef':{'name':'unsafe','key':'value'}}}],'readinessProbe':{'exec':{'command':['true']}}}]}}}}
        sources={'deploy-crds.yaml':json.dumps(crd).encode(),'deploy-operator.yaml':json.dumps(deployment).encode()}
        pin={'base':'https://example.invalid/','files':{name:hashlib.sha256(data).hexdigest() for name,data in sources.items()},'image_sha256':'a'*64}
        def fetch(url,expected,path,*_):
            data=sources[path.name]
            self.assertEqual(hashlib.sha256(data).hexdigest(),expected)
            path.parent.mkdir(parents=True,exist_ok=True)
            path.write_bytes(data)
            return data
        with patch.object(builder,'fetch',side_effect=fetch):
            objects=RENDER('mysql',pin,self.root/'mysql-render',builder.REDIS_CONTROLLER_IMAGE,Path('/unused'))
        rendered=next(item for item in objects if item['kind']=='Deployment')
        pod=rendered['spec']['template']['spec']
        container=pod['containers'][0]
        self.assertEqual(pod['nodeSelector'],{'kubernetes.io/arch':'amd64'})
        self.assertEqual(container['image'],'registry.example/mysql-operator:fixture@sha256:'+'a'*64)
        self.assertEqual(container['resources'],{'requests':{'cpu':'100m','memory':'256Mi'},'limits':{'cpu':'500m','memory':'512Mi'}})
        self.assertEqual(container['readinessProbe']['timeoutSeconds'],5)
        self.assertEqual({item['name']:item.get('value') for item in container['env']},{'MYSQL_OPERATOR_DEBUG':'0','MYSQLSH_CREDENTIAL_STORE_SAVE_PASSWORDS':'never'})

    def test_mysql_renderer_rejects_changed_container_topology_or_identity(self):
        base={'apiVersion':'apps/v1','kind':'Deployment','metadata':{'name':'mysql-operator','namespace':'mysql-operator'},'spec':{'template':{'spec':{'containers':[{'name':'mysql-operator','image':'registry.example/mysql-operator:fixture','readinessProbe':{}}]}}}}
        crd=json.dumps({'apiVersion':'apiextensions.k8s.io/v1','kind':'CustomResourceDefinition','metadata':{'name':'innodbclusters.mysql.oracle.com'}}).encode()
        cases=(
            ('helper container',lambda pod:pod['containers'].append({'name':'helper','image':'registry.example/helper:fixture'}),'exactly one operator container'),
            ('init container',lambda pod:pod.update({'initContainers':[{'name':'helper','image':'registry.example/helper:fixture'}]}),'exactly one operator container'),
            ('changed name',lambda pod:pod['containers'][0].update({'name':'another-operator'}),'container identity changed'),
        )
        for name,change,error in cases:
            with self.subTest(name=name):
                deployment=json.loads(json.dumps(base))
                change(deployment['spec']['template']['spec'])
                data=json.dumps(deployment).encode()
                pin={'base':'https://example.invalid/','files':{'deploy-crds.yaml':hashlib.sha256(crd).hexdigest(),'deploy-operator.yaml':hashlib.sha256(data).hexdigest()},'image_sha256':'a'*64}
                def fetch(url,expected,path,*_):
                    raw=crd if path.name=='deploy-crds.yaml' else data
                    path.parent.mkdir(parents=True,exist_ok=True)
                    path.write_bytes(raw)
                    return raw
                with patch.object(builder,'fetch',side_effect=fetch),self.assertRaisesRegex(ValueError,error):
                    RENDER('mysql',pin,self.root/('mysql-invalid-'+name.replace(' ','-')),builder.REDIS_CONTROLLER_IMAGE,Path('/unused'))


if __name__=='__main__':
    unittest.main()
