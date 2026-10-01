"""Package Vitess CRDs after the exact namespace operator is qualified.

The database reconciler creates one scoped operator per database. Installation
therefore provides only the eight native resource definitions, with no shared
controller deployment or cluster-wide workload credentials.
"""
import hashlib
from pathlib import Path
import runpy


SOURCE = '10a3b742c02c38f97d554739d5a257197daa48f9'
BASE = 'https://raw.githubusercontent.com/planetscale/vitess-operator/' + SOURCE + '/deploy/crds/'
FILES = {
    'planetscale.com_etcdlockservers.yaml': '1b372a8a88ebc3300058aa5edc793419050a7ebb2eda044f30c16ea12086eded',
    'planetscale.com_vitessbackups.yaml': '6188d7888ddaff9bf89ac292d0d57b9828f9c83b67d5f949d3bebde50497409b',
    'planetscale.com_vitessbackupschedules.yaml': '8cccd7f8dccf70e9796b42ccaca31ef65525998fc389647e2e361e68b5f27694',
    'planetscale.com_vitessbackupstorages.yaml': '02c9d253a7eda930493e770613360eec463e663e6bb743ff288e6ffcfe8afe34',
    'planetscale.com_vitesscells.yaml': 'e1cfac5605929ce4bc00b374cb653b54953f05e7e3a39de30d1ceb76fcce289f',
    'planetscale.com_vitessclusters.yaml': '27fe3fec6ce5896ac21507c8256b7434cbf13d9871f4d79f2f262ce214aed058',
    'planetscale.com_vitesskeyspaces.yaml': 'ac25ba1aea93d1f3e6b7b71ad9df4cdc90d3e85cefce09360ab137176c423253',
    'planetscale.com_vitessshards.yaml': '2d1a85bdbc5c2f6535d28d18efcb5036703074383658a296d1e5ccc6c9dd6d06',
}


def qualification(source):
    verifier = runpy.run_path(str(source / 'release/verify-vitess-runtime.py'))
    directory = source / 'release/managed-vitess'
    return verifier['validate_metadata'](directory, source), verifier['file_hash'](directory / 'manifest.json')


def validate_objects(objects):
    expected = {name.removeprefix('planetscale.com_').removesuffix('.yaml') + '.planetscale.com' for name in FILES}
    if not isinstance(objects, list) or len(objects) != len(expected):
        raise ValueError('Vitess installation requires exactly eight native resource definitions')
    names = set()
    for obj in objects:
        if not isinstance(obj, dict) or obj.get('apiVersion') != 'apiextensions.k8s.io/v1' or obj.get('kind') != 'CustomResourceDefinition' or obj.get('metadata', {}).get('name') not in expected or obj.get('spec', {}).get('group') != 'planetscale.com' or obj.get('spec', {}).get('scope') != 'Namespaced':
            raise ValueError('Vitess installation accepts only its eight namespace resource definitions')
        names.add(obj['metadata']['name'])
    if names != expected:
        raise ValueError('Vitess installation has missing or duplicate native resource definitions')


def render(source, scratch, fetch, validate_qualification=qualification):
    import yaml
    manifest, qualification_hash = validate_qualification(source)
    if manifest['sources']['operator'] != {
            'repository': 'https://github.com/planetscale/vitess-operator', 'revision': SOURCE}:
        raise ValueError('Vitess CRDs must match the qualified namespace operator source')
    objects = []
    for name, digest in FILES.items():
        raw = fetch(BASE + name, digest, scratch / name)
        if len(raw) > 8 * 1024 * 1024 or hashlib.sha256(raw).hexdigest() != digest:
            raise ValueError('Vitess CRD source checksum or size changed')
        items = list(yaml.safe_load_all(raw))
        expected = name.removeprefix('planetscale.com_').removesuffix('.yaml') + '.planetscale.com'
        if len(items) != 1 or not isinstance(items[0], dict):
            raise ValueError('Vitess installation requires one native CRD per source file')
        obj = items[0]
        if obj.get('apiVersion') != 'apiextensions.k8s.io/v1' or obj.get('kind') != 'CustomResourceDefinition' or obj.get('metadata', {}).get('name') != expected or obj.get('spec', {}).get('group') != 'planetscale.com' or obj.get('spec', {}).get('scope') != 'Namespaced':
            raise ValueError('Vitess installation accepts only its eight namespace resource definitions')
        objects.append(obj)
    validate_objects(objects)
    return objects, {'base': BASE, 'files': dict(FILES), 'source': SOURCE,
                     'runtime_image': manifest['images']['runtime']['reference'],
                     'operator_image': manifest['images']['operator']['reference'],
                     'qualification_sha256': qualification_hash}
