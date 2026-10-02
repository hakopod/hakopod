"""Vitess installer boundary fixtures; no network or cluster mutation."""
import copy
import hashlib
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import Mock, patch

import vitess_controller as vitess
import build_database_controllers as builder


class VitessControllerBundleTest(unittest.TestCase):
    def setUp(self):
        self.manifest = {'sources': {'operator': {
            'repository': 'https://github.com/planetscale/vitess-operator', 'revision': vitess.SOURCE}},
            'images': {kind: {'reference': 'ghcr.io/hakopod/fixture-' + kind + '@sha256:' + 'a' * 64}
                       for kind in ('runtime', 'operator')}}
        self.objects = {}
        for name in vitess.FILES:
            resource = name.removeprefix('planetscale.com_').removesuffix('.yaml')
            self.objects[name] = {'apiVersion': 'apiextensions.k8s.io/v1', 'kind': 'CustomResourceDefinition',
                                  'metadata': {'name': resource + '.planetscale.com'},
                                  'spec': {'group': 'planetscale.com', 'scope': 'Namespaced'}}

    def render(self, mutate=None):
        objects = copy.deepcopy(self.objects)
        if mutate:
            mutate(objects)
        encoded = {name: json.dumps(obj).encode() for name, obj in objects.items()}
        hashes = {name: hashlib.sha256(raw).hexdigest() for name, raw in encoded.items()}
        fetched = []
        def fetch(url, expected, destination):
            self.assertEqual(url, vitess.BASE + destination.name)
            self.assertEqual(expected, hashes[destination.name])
            fetched.append(destination.name)
            return encoded[destination.name]
        with patch.dict(vitess.FILES, hashes, clear=True):
            result = vitess.render(Path('/source'), Path('/scratch'), fetch,
                                   lambda _: (self.manifest, 'b' * 64))
        return result, fetched

    def test_only_eight_crds_and_exact_qualified_images_are_packaged(self):
        (objects, provenance), fetched = self.render()
        self.assertEqual(len(objects), 8)
        self.assertEqual(set(fetched), set(vitess.FILES))
        self.assertTrue(all(obj['kind'] == 'CustomResourceDefinition' for obj in objects))
        self.assertEqual(provenance['source'], vitess.SOURCE)
        self.assertEqual(provenance['qualification_sha256'], 'b' * 64)
        for kind in ('runtime', 'operator'):
            self.assertEqual(provenance[kind + '_image'], self.manifest['images'][kind]['reference'])

    def test_qualification_failure_or_source_change_stops_downloads(self):
        fetch = Mock()
        with self.assertRaisesRegex(ValueError, 'unqualified'):
            vitess.render(Path('/source'), Path('/scratch'), fetch,
                          Mock(side_effect=ValueError('unqualified source')))
        self.manifest['sources']['operator']['revision'] = 'c' * 40
        with self.assertRaisesRegex(ValueError, 'qualified namespace operator source'):
            vitess.render(Path('/source'), Path('/scratch'), fetch, lambda _: (self.manifest, 'b' * 64))
        fetch.assert_not_called()

    def test_source_checksum_is_verified_independently(self):
        with self.assertRaisesRegex(ValueError, 'checksum'):
            vitess.render(Path('/source'), Path('/scratch'), lambda *_: b'changed',
                          lambda _: (self.manifest, 'b' * 64))

    def test_other_resources_or_cluster_scoped_definitions_are_refused(self):
        name = next(iter(self.objects))
        def change_kind(objects):
            objects[name]['kind'] = 'Deployment'
        def change_scope(objects):
            objects[name]['spec']['scope'] = 'Cluster'
        def change_group(objects):
            objects[name]['spec']['group'] = 'foreign.example'
        def change_name(objects):
            objects[name]['metadata']['name'] = 'foreign.planetscale.com'
        for mutation in (change_kind, change_scope, change_group, change_name):
            with self.subTest(mutation=mutation.__name__):
                with self.assertRaisesRegex(ValueError, 'eight namespace resource definitions'):
                    self.render(mutation)

    def test_installer_rejects_missing_or_duplicate_definitions(self):
        objects=list(self.objects.values())
        vitess.validate_objects(objects)
        for invalid in (objects[:-1], [objects[0], *objects[:-1]]):
            with self.assertRaisesRegex(ValueError,'eight|missing or duplicate'):
                vitess.validate_objects(invalid)

    def test_bundle_build_requires_qualification_only_when_vitess_is_selected(self):
        with tempfile.TemporaryDirectory() as temporary:
            root=Path(temporary)
            source=root/'source'/'installer';source.mkdir(parents=True)
            (source/'database-controller-sources.json').write_text(json.dumps({engine:{} for engine in builder.NAMESPACES}))
            (source/'pins.json').write_text(json.dumps({'helm':{}}))
            with patch.object(builder,'HERE',source), patch.object(builder,'qualify_vitess') as qualify, patch.object(builder,'helm_binary',return_value=root/'helm') as helm, patch.object(builder,'render',return_value=[]), patch.object(builder,'render_vitess',return_value=(list(self.objects.values()),{'qualification_sha256':'b'*64})) as render:
                builder.build(root/'without-vitess','qualified-redis-fixture')
                qualify.assert_not_called();render.assert_not_called()
                manifest=json.loads((root/'without-vitess'/'manifest.json').read_text())
                self.assertEqual(set(manifest['files']),{'postgresql.json','redis.json','mongodb.json'})
                helm.reset_mock();qualify.side_effect=ValueError('unqualified Vitess')
                with self.assertRaisesRegex(ValueError,'unqualified'):
                    builder.build(root/'unqualified','qualified-redis-fixture',include_vitess=True)
                helm.assert_not_called();render.assert_not_called()
                qualify.side_effect=None
                builder.build(root/'with-vitess','qualified-redis-fixture',include_vitess=True)
                render.assert_called_once()
                manifest=json.loads((root/'with-vitess'/'manifest.json').read_text())
                self.assertEqual(set(manifest['files']),{'postgresql.json','redis.json','mongodb.json','vitess.json'})
                self.assertEqual(manifest['sources']['vitess']['qualification_sha256'],'b'*64)


if __name__ == '__main__':
    unittest.main()
