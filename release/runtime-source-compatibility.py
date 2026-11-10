#!/usr/bin/env python3
"""Validate exact reviewed source deltas for retained managed-runtime evidence."""
import hashlib
import json
import os
from pathlib import Path, PurePosixPath
import re
import subprocess
import tempfile

DIGEST = re.compile(r'[0-9a-f]{64}')
COMMIT = re.compile(r'[0-9a-f]{40}')
TAG = re.compile(r'v[0-9]+\.[0-9]+\.[0-9]+-alpha\.[0-9]+')
BOOTSTRAP_BASE = {'tag': 'v0.1.0-alpha.56', 'commit': 'e45bcf285eabec001e9161878c6696ce7814e54f'}
BOOTSTRAP_RELEASE = 'v0.1.0-alpha.57'
ALPHA59_BASE = {'tag': 'v0.1.0-alpha.58', 'commit': '2040ac12564eb4d8d769463fb9cd3e5d989b2c48'}
ALPHA59_RELEASE = 'v0.1.0-alpha.59'
ALPHA59_GO_MOD = {
    'before': 'cdd09b01abe6d0371b89d566f77ae1b87ebbd3f74bf5c801a6c591898162b174',
    'after': 'a5ad64b13b23e366dccc9d560cbecb97cb6ab32ab8f7e3385171a8f89e96c010',
}
ALPHA59_ORACLE_ENTERPRISE_HEALTH = {
    'path': 'internal/cluster/database_oracle_enterprise_health.go',
    'before': 'a7b27af19abf00c77e24a55df485f2cbb4c0351e41af2d6d2323f3f21ebc221d',
    'after': 'cf1a5a2cd010f24a97ca5ce746f76f3835c01ae51441e720d9f9db54464f44a6',
}
TRUST_ROOTS = {
    'myduck': {
        'release/runtime-source-compatibility.py',
        'release/verify-myduck-runtime.py',
        'release/verify-oracle-free-runtime.py',
        'release/record-myduck-qualification.py',
    },
    'oracle-free': {
        'release/runtime-source-compatibility.py',
        'release/verify-oracle-free-runtime.py',
        'release/record-oracle-free-qualification.py',
    },
}
BOOTSTRAP_CHANGED = {
    'myduck': TRUST_ROOTS['myduck'] - {'release/record-myduck-qualification.py'},
    'oracle-free': TRUST_ROOTS['oracle-free'],
}
ALPHA59_BOOTSTRAP_CHANGED = {
    'myduck': {'release/runtime-source-compatibility.py', 'release/verify-oracle-free-runtime.py'},
    'oracle-free': {'release/runtime-source-compatibility.py', 'release/verify-oracle-free-runtime.py'},
}


def canonical_hash(value):
    return hashlib.sha256(json.dumps(value, sort_keys=True, separators=(',', ':')).encode()).hexdigest()


def unique(pairs):
    result = {}
    for key, value in pairs:
        if key in result:
            raise ValueError('Duplicate runtime compatibility field')
        result[key] = value
    return result


def validate(root, runtime, recorded, current, protected, artifact_sha256):
    if recorded == current:
        return False
    path = Path(root) / 'release' / ('managed-' + runtime) / 'source-compatibility.json'
    if path.is_symlink() or not path.is_file() or not 1 <= path.stat().st_size <= 512 * 1024:
        raise ValueError(runtime + ' source changed without an exact compatibility review')
    record = json.loads(path.read_text(), object_pairs_hook=unique)
    if isinstance(record, dict) and record.get('schema_version') == 2:
        return validate_chain(root, runtime, record, recorded, current, protected, artifact_sha256)
    fields = {'schema_version', 'runtime', 'qualified_release', 'reviewed_release',
              'control_plane_commit', 'regression_evidence', 'delta', 'bootstrap'}
    if (not isinstance(record, dict) or set(record) != fields
            or type(record.get('schema_version')) is not int or record['schema_version'] != 1
            or record.get('runtime') != runtime):
        raise ValueError('Invalid ' + runtime + ' compatibility review')
    baseline = record['qualified_release']
    if (not isinstance(baseline, dict) or set(baseline) != {'tag', 'commit', 'manifest_sha256'}
            or not TAG.fullmatch(baseline.get('tag', '')) or not COMMIT.fullmatch(baseline.get('commit', ''))
            or not DIGEST.fullmatch(baseline.get('manifest_sha256', ''))
            or baseline['manifest_sha256'] != artifact_sha256
            or not TAG.fullmatch(record.get('reviewed_release', ''))
            or not COMMIT.fullmatch(record.get('control_plane_commit', ''))):
        raise ValueError(runtime + ' compatibility review lacks exact release identities')
    bootstrap = record['bootstrap']
    if bootstrap is not None:
        alpha59 = record['reviewed_release'] == ALPHA59_RELEASE
        base = ALPHA59_BASE if alpha59 else BOOTSTRAP_BASE
        release = ALPHA59_RELEASE if alpha59 else BOOTSTRAP_RELEASE
        paths = ALPHA59_BOOTSTRAP_CHANGED[runtime] if alpha59 else BOOTSTRAP_CHANGED[runtime]
        expected_bootstrap = {'base_release': base['tag'], 'base_commit': base['commit'],
                              'reviewed_release': release, 'trust_anchor': 'authenticated-git-review',
                              'trust_paths': sorted(paths)}
        if bootstrap != expected_bootstrap or baseline['tag'] != base['tag'] \
                or baseline['commit'] != base['commit'] or record['reviewed_release'] != release:
            raise ValueError(runtime + ' compatibility bootstrap identity is invalid')
    evidence = record['regression_evidence']
    if (not isinstance(evidence, dict) or set(evidence) != {'url', 'tests'}
            or not re.fullmatch(r'https://github\.com/hakopod/hakopod/actions/runs/[0-9]+', evidence.get('url', ''))
            or not isinstance(evidence.get('tests'), list) or not evidence['tests']
            or len(evidence['tests']) > 32 or len(set(evidence['tests'])) != len(evidence['tests'])
            or any(not isinstance(item, str) or not re.fullmatch(r'Test[A-Za-z0-9_/.-]{1,200}', item)
                   for item in evidence['tests'])):
        raise ValueError(runtime + ' compatibility review lacks real regression evidence')
    delta = record['delta']
    if (not isinstance(delta, dict) or set(delta) != {'baseline_sha256', 'current_sha256', 'changes'}
            or delta.get('baseline_sha256') != canonical_hash(recorded)
            or delta.get('current_sha256') != canonical_hash(current)
            or not isinstance(delta.get('changes'), list) or not 1 <= len(delta['changes']) <= 1024):
        raise ValueError(runtime + ' source changed outside the reviewed delta')
    expected, seen = dict(recorded), set()
    for change in delta['changes']:
        if not isinstance(change, dict) or set(change) != {'path', 'before', 'after', 'reason'}:
            raise ValueError('Invalid ' + runtime + ' compatibility change')
        name, before, after, reason = (change[key] for key in ('path', 'before', 'after', 'reason'))
        pure = PurePosixPath(name) if isinstance(name, str) else PurePosixPath('.')
        if (not isinstance(name, str) or not name or len(name) > 512
                or any(ord(character) < 32 for character in name)
                or name in seen or pure.is_absolute() or '..' in pure.parts
                or str(pure) != name or '\\' in name or before == after
                or any(value is not None and (not isinstance(value, str) or not DIGEST.fullmatch(value))
                       for value in (before, after))
                or not isinstance(reason, str) or not 20 <= len(reason.strip()) <= 500):
            raise ValueError('Invalid, duplicate or unsafe ' + runtime + ' compatibility change')
        exact_go_mod_reclassification = (record['reviewed_release'] == ALPHA59_RELEASE
                                         and name == 'go.mod' and before == ALPHA59_GO_MOD['before']
                                         and after == ALPHA59_GO_MOD['after']
                                         and recorded.get('go.sum') == current.get('go.sum'))
        exact_oracle_enterprise_observer = (runtime == 'oracle-free' and record['reviewed_release'] == ALPHA59_RELEASE
                                            and name == ALPHA59_ORACLE_ENTERPRISE_HEALTH['path']
                                            and before == ALPHA59_ORACLE_ENTERPRISE_HEALTH['before']
                                            and after == ALPHA59_ORACLE_ENTERPRISE_HEALTH['after'])
        if name in TRUST_ROOTS[runtime]:
            if bootstrap is None:
                raise ValueError(runtime + ' trust-root changes require the reviewed alpha.57 bootstrap')
            allowed = ALPHA59_BOOTSTRAP_CHANGED[runtime] if record['reviewed_release'] == ALPHA59_RELEASE else BOOTSTRAP_CHANGED[runtime]
            if name not in allowed:
                suffix = 'alpha.59 compatibility bootstrap' if record['reviewed_release'] == ALPHA59_RELEASE else 'alpha.57 bootstrap'
                raise ValueError(runtime + ' trust root is outside the fixed ' + suffix)
        elif protected(name) and not (exact_go_mod_reclassification or exact_oracle_enterprise_observer):
            raise ValueError(runtime + ' runtime, image or native harness changes require new qualification')
        if expected.get(name) != before:
            raise ValueError(runtime + ' compatibility change differs from qualified baseline')
        seen.add(name)
        if after is None:
            expected.pop(name)
        else:
            expected[name] = after
    if expected != current:
        raise ValueError(runtime + ' source changed outside the reviewed delta')
    changed_trust = {change['path'] for change in delta['changes'] if change['path'] in TRUST_ROOTS[runtime]}
    expected_trust = (ALPHA59_BOOTSTRAP_CHANGED[runtime] if record['reviewed_release'] == ALPHA59_RELEASE
                      else BOOTSTRAP_CHANGED[runtime])
    if bootstrap is not None and changed_trust != expected_trust:
        raise ValueError(runtime + ' compatibility bootstrap trust-path set differs')
    return True


# The next review extends this published review, not the old native test claim.
# Its hash anchors the exact historical exceptions accepted in alpha.59.
CHAIN_BASE = {'tag': 'v0.1.0-alpha.59', 'commit': '869cb4186566199d8611c4ffb30212d42d0daa1d'}
CHAIN_RELEASE = 'v0.1.0-alpha.61'
CHAIN_PREDECESSORS = {'myduck': '45170d7d57580267df5515e373eb7debf0879bac6e53a4bbeef9abb6826390f0', 'oracle-free': 'b59a0847286257ae0870b2a863a5f557860c9f204b1b9323eb55fbf0cffdec08', 'vitess': '6ccc96b680e076acd49f9f0d1d7192018d453ec5baa4baa80358f9946ffb7f3b'}
CHAIN_TRUST_PATHS = {
    'myduck': {'release/runtime-source-compatibility.py'},
    'oracle-free': {'release/runtime-source-compatibility.py'},
    'vitess': {'release/runtime-source-compatibility.py', 'release/verify-vitess-runtime.py'},
}


NEXT_CHAIN_BASE = {'tag': 'v0.1.0-alpha.61', 'commit': 'bc3b46803d3bba40c6438ddb27e190c628d8622a'}
NEXT_CHAIN_PREDECESSORS = {'myduck': '085a8c279f0326940b0c8dcf135e4867d20bd6b62d60c281670730ea62fe3449', 'oracle-free': 'f5f198616153525fc6a1f30cddef2d80eb28d09d93bdfc39b577b52cddf009cd', 'vitess': '6d2881634cf4befdea15bc74d05a1c1ecc3c535472b76c467b120dc1c28ae8eb'}
ALPHA63_CHAIN_BASE = {'tag': 'v0.1.0-alpha.62', 'commit': '641141e4013cee35e85931ce44377ba3c5719e72'}
ALPHA63_CHAIN_PREDECESSORS = {'myduck': 'd1dccdaf71c403d8edf6c9f74b418f61a65502f6a31ec11d16e4d72b0e1b5a4b', 'oracle-free': 'cceffdfb89ae29fdc53d1a997793abaff6b096e216fd3f73e36129a67efece89', 'vitess': '23022f9aef64fb1f6827f9ec3e159dc5c746ce02afa394a7457d43037ae9388c'}
ALPHA64_CHAIN_BASE = {'tag': 'v0.1.0-alpha.63', 'commit': '5838d12e4121c1815890227fbbd7d09e8327194d'}
ALPHA64_CHAIN_PREDECESSORS = {'myduck': 'e47df078156e9f8771bd8742b7782baee3af5b25408ef5ff107579354eec6727', 'oracle-free': '80b00fd6af9d74c8f453519103de7cdf247e6976833b1298de5349895edcc0e4', 'vitess': '1aa65c2eee336322c46bded1aaf6622bb7b1b0ce18de532110ecf29533c53981'}
ALPHA65_CHAIN_BASE = {'tag': 'v0.1.0-alpha.64', 'commit': '0e711c2c2e06490c972e522785da7d5c3097d08d'}
ALPHA65_CHAIN_PREDECESSORS = {'myduck': '12469e1441bfb190118871f47269ee42b2a1e5b8d1f7d5454726e89c027fb275', 'oracle-free': '2031bafcb9d2b5fd47d905ed738a4cd3edfa053a295668b63cfdd7ad714417e8', 'vitess': '885495fdeab29abb2d3d9fb312381dfae7e622a986c142e7eae448564a01edb1'}
ALPHA66_CHAIN_BASE = {'tag': 'v0.1.0-alpha.65', 'commit': '80189221c202147567246a33b12d6bdff3bf8b5e'}
ALPHA66_CHAIN_PREDECESSORS = {'myduck': 'f5cf2bdcbf94720a2ab76cb7ca4672fe2a34135d0a274c704bba7cf212b1675c', 'oracle-free': '35a4fce0965d6b75c743bf3cd841874c14198cfd0fb77af30c13571e83310683', 'vitess': '3cc762129b3d9dfe2cae11bca023b92719842e95ee26fa22868d2fdc6f4f430d'}
ALPHA67_CHAIN_BASE = {'tag': 'v0.1.0-alpha.66', 'commit': '96bc2b7fb326ce086d6a84aaf7db0fe1aa705fc6'}
ALPHA67_CHAIN_PREDECESSORS = {'myduck': 'd01dbf3d1964e5713050a45f0ec9e51025d7c5eaa7b435229a842d2db1a6fd8f', 'oracle-free': 'defdd4546f08a2c19e734654fc7e32efd3e490122e2bb0a9d337aa9cad7c4c85', 'vitess': 'b9b2065f690fa25e4a15f78206ab314f51986372497a4bb81d1782b8fd980349'}
ALPHA68_CHAIN_BASE = {'tag': 'v0.1.0-alpha.67', 'commit': 'cd26c751c310d909dd5bc43d75a08b857e495a21'}
ALPHA68_CHAIN_PREDECESSORS = {'myduck': 'e24aef2c2b7f5f69b2a7d8ffe88155193e6501561f0d8b48b6d3f40b42b85a45', 'oracle-free': '6799be0fc88841aa0ed3b5c7fe043025ee801bc9c5e34229878b0cb1f88f2b48', 'vitess': '082edfdf929410ac2764b33ca087a75026337e48d8de23729e047c9ad8c70c57'}
ALPHA69_CHAIN_BASE = {'tag': 'v0.1.0-alpha.68', 'commit': 'c02d3069bcb46966f613c03da1dbde9eaeecaaaf'}
ALPHA69_CHAIN_PREDECESSORS = {'myduck': '91828fb0c9a89e098011542340087580af43051c449097e84e5614bc960dc382', 'oracle-free': 'efd3935ec3e765681ae71a5b1055719aa99aa404e5077043c255d6089ee961d9', 'vitess': '95cebfba35cb3cb23865f791133d57ec61e29180604cc366fb1984479d381433'}
ALPHA70_CHAIN_BASE = {'tag': 'v0.1.0-alpha.69', 'commit': '93c8caf011098ce5bc2fd00f06bd5fc6fa59c6a6'}
ALPHA70_CHAIN_PREDECESSORS = {'myduck': '0e97a1b1d085c52e252f0d5b6b7c82f01377e5b42774d19d176291cd66d4b83b', 'oracle-free': 'c21a08695a35b7fbfb19d811b6d04b1a9b08a8d9dbc232b7aac5917e8640b1db', 'vitess': '2f986f76ad9baac8d6899c3f4877cd3de70fb08949c6ba77ff68fe43b438adc1'}
ALPHA71_CHAIN_BASE = {'tag': 'v0.1.0-alpha.70', 'commit': '69ea2ab8bea432ff2a9bea03a517c52d08cde7b3'}
ALPHA71_CHAIN_PREDECESSORS = {'myduck': '4f5e83d2a634ecf5379b06c43302378856f808bba1196f79bed11ba3f6f5301b', 'oracle-free': '289b64a757696e9f46d653e2374328f38096d61f6ad19e0aba4150f5b247e54d', 'vitess': 'd60667bae0cb5a44aeca3fa310fbc3bc2fb62301ae24f67b8449187753cec0ea'}
ALPHA72_CHAIN_BASE = {'tag': 'v0.1.0-alpha.71', 'commit': '94ca091e9a5bdfda25e80e87561445054ff0660e'}
ALPHA72_CHAIN_PREDECESSORS = {'myduck': '85394037da245effe912cf9cdede40081e6ed9a384e74e941bb900d79f816a16', 'oracle-free': 'fab439a11a8e2ab2784364d54f5c2f1ae96d3e474967dee124690ff79b7a8fe0', 'vitess': '7e473e316125af356acb902126528a6dff93cef08bd5c436f16eb7f0541807c5'}
ALPHA73_CHAIN_BASE = {'tag': 'v0.1.0-alpha.72', 'commit': '808e4d70fe65b96df5a3cf1c9a80d482c55ce25d'}
ALPHA73_CHAIN_PREDECESSORS = {'myduck': '346ae16a968208ae6efd69fad7a09338f090ef238f6098d85695ae53d095fd48', 'oracle-free': '3b2a55d60a22bb04da820db5f0183f2005825d9faaf60d1595253e8730f2324f', 'vitess': '3e02343cd10c314f9306b25e3a96686c8b7c0f75abdfd4d79712f25030a05c89'}
ALPHA74_CHAIN_BASE = {'tag': 'v0.1.0-alpha.73', 'commit': '62954d81675d5ca50518362d1a0d09bbcebb93a8'}
ALPHA74_CHAIN_PREDECESSORS = {'myduck': 'b53aa51d65f0d1ee762c3f34371fec69a3c67986711392826fe423d3a44a2fc3', 'oracle-free': '33d3e55613ede778c6f4b78479e0979c7c1b2cf85aaa72540263eb3d93790fe6', 'vitess': '49e4bce6060fd976f9eb93349ed7c2e001b21f6147a0fa2d04529b82f6838aad'}
ALPHA75_CHAIN_BASE = {'tag': 'v0.1.0-alpha.74', 'commit': '42f1fa8f9edead951964fd0991698d82474e6f61'}
ALPHA75_CHAIN_PREDECESSORS = {'myduck': 'f89a5caeb8528ea6d4e4701791b9bdef99178463e3f0a006e419f22fc451707b', 'oracle-free': '679a68a6a68c05bc7f2e7486ba59b1e9b595fba2ab8fa2ef76a9d4551b1c12d1', 'vitess': 'ffeae4b417b61603e99a0cdb76b391e979411493b3a1fef786c5ec705b03ae40'}
ALPHA76_CHAIN_BASE = {'tag': 'v0.1.0-alpha.75', 'commit': '71452cd9ecc37857a5f5444f6afafe4bfc03e0a5'}
ALPHA76_CHAIN_PREDECESSORS = {'myduck': '6c67122d3957d0012fcafd9d694ef988e0c7f426688fbf78d2116553c2b1d727', 'oracle-free': '5970a8f87bdbc189775613393804bfa2da96a423f9d90eef893fdc1331814036', 'vitess': '3640be54f4e9c32de826cb5d91d56cf76409e24303637925152ec0335a1207de'}
ALPHA77_CHAIN_BASE = {'tag': 'v0.1.0-alpha.76', 'commit': '2d7537b78a684dd4150c8c504a5ee21c2753710b'}
ALPHA77_CHAIN_PREDECESSORS = {'myduck': '8534c07e5a79b708b33af2f05f0e1289efd3c2471fb52c640d00ced331097179', 'oracle-free': 'd6ee5b0301ec219f0a8fc037969254750dd950a9a3ed730bcbeba324465258e7', 'vitess': '19e3d9a7f30026d9c30eb1ac5eff8de706643b3b8c3ea5d843d80b91c3a9d744'}
ALPHA78_CHAIN_BASE = {'tag': 'v0.1.0-alpha.77', 'commit': '5fc8326f5a69c425366f36c8c6683f272f86e577'}
ALPHA78_CHAIN_PREDECESSORS = {'myduck': '6e6985c816ebda9662e026632c01f353a68d9f6ae5a53fda90f95bbd599c4f2c', 'oracle-free': 'e4003462764fb0453b4bda0f8c5acf1b29b6a343f8bec092e534e6808da438f7', 'vitess': 'ee3b0d45a5f790c10c866bfb2903972467667bafc1800bc0e1947c5442ef8f60'}
ALPHA79_CHAIN_BASE = {'tag': 'v0.1.0-alpha.78', 'commit': '8d2dd18b4290259c47ce832b24811dba73aeab07'}
ALPHA79_CHAIN_PREDECESSORS = {'myduck': '3c63a898a3dc71365790c3f2fd9d38c5f44365b990a1ee46e746ca53ce1bd58e', 'oracle-free': '5fd74722f065809a2fd3f15fa2858a715c2b9c0189739c2e91898050066a2778', 'vitess': 'fb6580b9f67e85394cac9377deb2783ca1a3fe9dd4ef3945ca8ff83d7ea1e1bf'}
ALPHA80_CHAIN_BASE = {'tag': 'v0.1.0-alpha.79', 'commit': '677dbae1ae106c58e789d3b452ce64e4d41c94da'}
ALPHA80_CHAIN_PREDECESSORS = {'myduck': 'd947b1a8d60dab9e7ad4238aebbe8f91757fa7533153cfb1818763c293462c70', 'oracle-free': '31141af61511bc709dee5ae7ebc3c79c3a74b47969958b7c374e746c6120de15', 'vitess': 'bbd05192799ba7110030f79536790ae233c05a1f3c281837d33f1261e806ea4d'}
ALPHA81_CHAIN_BASE = {'tag': 'v0.1.0-alpha.80', 'commit': '4d347265ad346fb7eb9c6aa8e5e797dfdcd22feb'}
ALPHA81_CHAIN_PREDECESSORS = {'myduck': 'e01078f5017fb37c86c682cc816d0d03942faad7d5b6b8bda54fd85ae5a6add2', 'oracle-free': '5af61e9eeb6fc0d95b513c3e630ccd2b65e09360f9fe1d5aa53470da47ea3047', 'vitess': '2d9e9e6d02123930849c71537cf906f27908b9a9ab76a9694e4b9bba1e6e6ebd'}
ALPHA82_CHAIN_BASE = {'tag': 'v0.1.0-alpha.81', 'commit': '94e37145ddf337f3cc1f6ab2383f6331a1101403'}
ALPHA82_CHAIN_PREDECESSORS = {'myduck': '4be43ba5b9434c1cea903bbc684f76ef315d4c77a4f667730823d78ee18b3716', 'oracle-free': '5f1d7aa3f6396d4d7d4666982f2a96ae71fd304a2636fb556d0a803898edd76a', 'vitess': '4428182fba81c7342ccbe28f1d31fc3b85c6b58a682cbd04e6b7c8e66dcdfc1b'}
ALPHA83_CHAIN_BASE = {'tag': 'v0.1.0-alpha.82', 'commit': '8cb3f579a6a19a3540c0bb06e9bd59fe66ccc99d'}
ALPHA83_CHAIN_PREDECESSORS = {'myduck': '70a2176fc1ada87f3109a9e36d2e9ea2336c610a35f08615b14494e5f129f70e', 'oracle-free': '32fa98989e44656ed15ad49be7633b1cc3a6cc9ff87cedc269912143d8319746', 'vitess': '1a3d3f7803317604e54d08154ac1c279a279821dfacf7a09fcbb7859cf7302ca'}
ALPHA84_CHAIN_BASE = {'tag': 'v0.1.0-alpha.83', 'commit': 'a0511f7d393a68b20abefbecd5bff4609f8a5ebd'}
ALPHA84_CHAIN_PREDECESSORS = {'myduck': '4b8906add1057c6141eea03e36411c123c2b96b3b5c1c7e9132b1e0fe1554ff2', 'oracle-free': '4d224d74a5990eb974be553d614bf1bd286f6ebbd4ee336ab3e791460595aebf', 'vitess': '2b7f4dbc57bd47ba8f66a9dae467a391a26add9654b7b48b11390b6bbbd62c35'}
ALPHA85_CHAIN_BASE = {'tag': 'v0.1.0-alpha.84', 'commit': '55fc9dd255fdd1e32d0a01cfcc39b94d058b53de'}
ALPHA85_CHAIN_PREDECESSORS = {'myduck': '3ca0d9b240ec1ad6251869349d117e926c826fcf29e4cfdc27232782d4193c86', 'oracle-free': 'b47bd1de5843009223ff6a204f8d14b5951a88a8598c21405489acdb6b57dbc2', 'vitess': '981ca19c2e59dd47cda6dea05d029ec66c9ca4d5b433603b1e186576cc6bba94'}
ALPHA86_CHAIN_BASE = {'tag': 'v0.1.0-alpha.85', 'commit': 'd948e9b360eb2202df5228a565c987fd95dc3a02'}
ALPHA86_CHAIN_PREDECESSORS = {'myduck': '150b19e47f30daf0dcaf4b9075ffb4f55940e9e66fa3cfc92dfb918669d034ee', 'oracle-free': 'a768e75f0aa7cd837f2c113c5f50079cb5460e715219fea24a25e287475f635f', 'vitess': 'ba93896305a73e54d6d0b2fce810e5acfc6f7fa1e1b0b3c6a9ff817cb76bc97e'}
# Each reviewed release names its published predecessor review, oldest first.
# Built on each call so the constants stay the single source of truth.
def chain_links():
    return {
        CHAIN_RELEASE: (CHAIN_BASE, CHAIN_PREDECESSORS, 'source-compatibility-alpha59.json'),
        'v0.1.0-alpha.62': (NEXT_CHAIN_BASE, NEXT_CHAIN_PREDECESSORS, 'source-compatibility-alpha61.json'),
        'v0.1.0-alpha.63': (ALPHA63_CHAIN_BASE, ALPHA63_CHAIN_PREDECESSORS, 'source-compatibility-alpha62.json'),
        'v0.1.0-alpha.64': (ALPHA64_CHAIN_BASE, ALPHA64_CHAIN_PREDECESSORS, 'source-compatibility-alpha63.json'),
        'v0.1.0-alpha.65': (ALPHA65_CHAIN_BASE, ALPHA65_CHAIN_PREDECESSORS, 'source-compatibility-alpha64.json'),
        'v0.1.0-alpha.66': (ALPHA66_CHAIN_BASE, ALPHA66_CHAIN_PREDECESSORS, 'source-compatibility-alpha65.json'),
        'v0.1.0-alpha.67': (ALPHA67_CHAIN_BASE, ALPHA67_CHAIN_PREDECESSORS, 'source-compatibility-alpha66.json'),
        'v0.1.0-alpha.68': (ALPHA68_CHAIN_BASE, ALPHA68_CHAIN_PREDECESSORS, 'source-compatibility-alpha67.json'),
        'v0.1.0-alpha.69': (ALPHA69_CHAIN_BASE, ALPHA69_CHAIN_PREDECESSORS, 'source-compatibility-alpha68.json'),
        'v0.1.0-alpha.70': (ALPHA70_CHAIN_BASE, ALPHA70_CHAIN_PREDECESSORS, 'source-compatibility-alpha69.json'),
        'v0.1.0-alpha.71': (ALPHA71_CHAIN_BASE, ALPHA71_CHAIN_PREDECESSORS, 'source-compatibility-alpha70.json'),
        'v0.1.0-alpha.72': (ALPHA72_CHAIN_BASE, ALPHA72_CHAIN_PREDECESSORS, 'source-compatibility-alpha71.json'),
        'v0.1.0-alpha.73': (ALPHA73_CHAIN_BASE, ALPHA73_CHAIN_PREDECESSORS, 'source-compatibility-alpha72.json'),
        'v0.1.0-alpha.74': (ALPHA74_CHAIN_BASE, ALPHA74_CHAIN_PREDECESSORS, 'source-compatibility-alpha73.json'),
        'v0.1.0-alpha.75': (ALPHA75_CHAIN_BASE, ALPHA75_CHAIN_PREDECESSORS, 'source-compatibility-alpha74.json'),
        'v0.1.0-alpha.76': (ALPHA76_CHAIN_BASE, ALPHA76_CHAIN_PREDECESSORS, 'source-compatibility-alpha75.json'),
        'v0.1.0-alpha.77': (ALPHA77_CHAIN_BASE, ALPHA77_CHAIN_PREDECESSORS, 'source-compatibility-alpha76.json'),
        'v0.1.0-alpha.78': (ALPHA78_CHAIN_BASE, ALPHA78_CHAIN_PREDECESSORS, 'source-compatibility-alpha77.json'),
        'v0.1.0-alpha.79': (ALPHA79_CHAIN_BASE, ALPHA79_CHAIN_PREDECESSORS, 'source-compatibility-alpha78.json'),
        'v0.1.0-alpha.80': (ALPHA80_CHAIN_BASE, ALPHA80_CHAIN_PREDECESSORS, 'source-compatibility-alpha79.json'),
        'v0.1.0-alpha.81': (ALPHA81_CHAIN_BASE, ALPHA81_CHAIN_PREDECESSORS, 'source-compatibility-alpha80.json'),
        'v0.1.0-alpha.82': (ALPHA82_CHAIN_BASE, ALPHA82_CHAIN_PREDECESSORS, 'source-compatibility-alpha81.json'),
        'v0.1.0-alpha.83': (ALPHA83_CHAIN_BASE, ALPHA83_CHAIN_PREDECESSORS, 'source-compatibility-alpha82.json'),
        'v0.1.0-alpha.84': (ALPHA84_CHAIN_BASE, ALPHA84_CHAIN_PREDECESSORS, 'source-compatibility-alpha83.json'),
        'v0.1.0-alpha.85': (ALPHA85_CHAIN_BASE, ALPHA85_CHAIN_PREDECESSORS, 'source-compatibility-alpha84.json'),
        'v0.1.0-alpha.86': (ALPHA86_CHAIN_BASE, ALPHA86_CHAIN_PREDECESSORS, 'source-compatibility-alpha85.json'),
    }

# Go packages each runtime image ships that are compiled against this
# repository's go.mod. Oracle Free and Vitess images contain only upstream builds.
GO_BINARY_PACKAGES = {'myduck': ('./cmd/hakopod-myduck-storage',)}
GO_MODULE_FILES = ('go.mod', 'go.sum')
GO_LIST_TEMPLATE = ('{{if not .Standard}}{{.ImportPath}}\t{{with .Module}}{{.Path}}\t{{.Version}}\t{{.Main}}'
                    '{{with .Replace}}\t{{.Path}}\t{{.Version}}{{end}}{{end}}\n{{end}}')


def go_build_inputs(root, packages, modfile=None):
    """Return what go.mod and go.sum contribute to the linux/amd64 build of packages."""
    env = dict(os.environ, GOWORK='off', GOFLAGS='-mod=readonly', GOOS='linux', GOARCH='amd64', CGO_ENABLED='0')
    modfile = Path(modfile) if modfile else Path(root) / 'go.mod'
    def go(*args):
        return subprocess.run(['go', *args], cwd=root, env=env, capture_output=True, text=True,
                              check=True, timeout=600).stdout
    edit = json.loads(go('mod', 'edit', '-json', str(modfile)))
    main = edit['Module']['Path']
    own = {main + '/' + package.removeprefix('./') for package in packages}
    modules = set()
    for line in go('list', '-modfile=' + str(modfile), '-deps', '-f', GO_LIST_TEMPLATE, *packages).splitlines():
        fields = line.split('\t')
        if len(fields) not in (4, 6):
            raise ValueError('Go package outside module mode: ' + line)
        if fields[3] == 'true':
            # Other packages of this repository are not pinned by the native manifest.
            if fields[0] not in own:
                raise ValueError('Runtime Go binary imports unpinned repository package ' + fields[0])
            continue
        if len(fields) == 6 and not fields[5]:
            raise ValueError('Runtime Go dependency is replaced by a local directory')
        modules.add(tuple(fields[1:]))
    versions = {(item[0], item[1]) for item in modules} | {(item[3], item[4]) for item in modules if len(item) == 5}
    sums = sorted(line for line in modfile.with_suffix('.sum').read_text().splitlines()
                  if len(line.split()) == 3 and (line.split()[0], line.split()[1].removesuffix('/go.mod')) in versions)
    return {'directives': [edit.get('Go'), edit.get('Toolchain'), edit.get('Godebug')],
            'modules': sorted(modules), 'sums': sums}


def go_build_unchanged(root, runtime, commit, qualified, current):
    """True only when go.mod and go.sum still give the runtime's Go binaries their qualified inputs."""
    packages = GO_BINARY_PACKAGES.get(runtime)
    if not packages:
        return True
    try:
        if not isinstance(commit, str) or not COMMIT.fullmatch(commit):
            return False
        with tempfile.TemporaryDirectory(prefix='qualified-go-') as temporary:
            for name, target in (('go.mod', 'qualified.mod'), ('go.sum', 'qualified.sum')):
                # Git only supplies bytes; the native manifest's hash decides whether they are qualified.
                blob = subprocess.run(['git', 'cat-file', 'blob', commit + ':' + name], cwd=root,
                                      capture_output=True, check=True, timeout=60).stdout
                if hashlib.sha256(blob).hexdigest() != qualified.get(name):
                    return False
                if hashlib.sha256((Path(root) / name).read_bytes()).hexdigest() != current.get(name):
                    return False
                (Path(temporary) / target).write_bytes(blob)
            return (go_build_inputs(root, packages, Path(temporary) / 'qualified.mod')
                    == go_build_inputs(root, packages))
    except (OSError, subprocess.SubprocessError, ValueError, KeyError, TypeError):
        return False


def chain_header(root, runtime, record):
    release = record.get('reviewed_release') if isinstance(record, dict) else None
    links = chain_links()
    if release not in links:
        raise ValueError('Invalid chained runtime compatibility review')
    base, anchors, predecessor_file = links[release]
    scope_key = 'scopes' if runtime == 'vitess' else 'delta'
    version = 3 if runtime == 'vitess' else 2
    fields = {'schema_version', 'runtime', 'qualified_release', 'reviewed_release',
              'control_plane_commit', 'regression_evidence', 'predecessor', 'bootstrap', scope_key}
    if (runtime not in CHAIN_PREDECESSORS or not isinstance(record, dict)
            or set(record) != fields or type(record['schema_version']) is not int
            or record['schema_version'] != version or record['runtime'] != runtime
            or record['reviewed_release'] != release
            or not isinstance(record['control_plane_commit'], str)
            or not COMMIT.fullmatch(record['control_plane_commit'])):
        raise ValueError('Invalid chained runtime compatibility review')
    expected = dict(base, review_sha256=anchors[runtime])
    if record['predecessor'] != expected:
        raise ValueError('Runtime compatibility predecessor identity changed')
    path = Path(root) / 'release' / ('managed-' + runtime) / predecessor_file
    if path.is_symlink() or not path.is_file() or not 1 <= path.stat().st_size <= 512 * 1024:
        raise ValueError('Published runtime predecessor review is unavailable')
    predecessor = json.loads(path.read_text(), object_pairs_hook=unique)
    if canonical_hash(predecessor) != anchors[runtime]:
        raise ValueError('Published runtime predecessor review changed')
    if record['qualified_release'] != predecessor['qualified_release']:
        raise ValueError('Chained review cannot replace the historical native evidence identity')
    expected_bootstrap = dict(base, reviewed_release=release,
                              trust_anchor='authenticated-git-review',
                              trust_paths=sorted(CHAIN_TRUST_PATHS[runtime]))
    if record['bootstrap'] != expected_bootstrap:
        raise ValueError('Chained review trust-root bootstrap changed')
    evidence = record['regression_evidence']
    if (not isinstance(evidence, dict) or set(evidence) != {'url', 'tests'}
            or not isinstance(evidence['url'], str)
            or not re.fullmatch(r'https://github\.com/hakopod/hakopod/actions/runs/[0-9]+', evidence['url'])
            or not isinstance(evidence['tests'], list) or not 1 <= len(evidence['tests']) <= 32
            or any(not isinstance(item, str) or not re.fullmatch(r'Test[A-Za-z0-9_/.-]{1,200}', item) for item in evidence['tests'])
            or len(set(evidence['tests'])) != len(evidence['tests'])):
        raise ValueError('Chained review lacks regression evidence')
    if runtime == 'vitess' and (not isinstance(record['scopes'], dict) or set(record['scopes']) != {'runtime', 'http_harness'}):
        raise ValueError('Chained Vitess review requires both source scopes')
    return predecessor


def apply_chain_delta(recorded, delta):
    if (not isinstance(recorded, dict) or not 1 <= len(recorded) <= 4096
            or not isinstance(delta, dict) or set(delta) != {'baseline_sha256', 'current_sha256', 'changes'}
            or delta['baseline_sha256'] != canonical_hash(recorded)
            or not isinstance(delta['changes'], list) or len(delta['changes']) > 1024):
        raise ValueError('Chained compatibility source inventory differs')
    result, seen = dict(recorded), set()
    for change in delta['changes']:
        if not isinstance(change, dict) or set(change) != {'path', 'before', 'after', 'reason'}:
            raise ValueError('Invalid chained compatibility change')
        name, before, after, reason = (change[key] for key in ('path', 'before', 'after', 'reason'))
        path = PurePosixPath(name) if isinstance(name, str) else PurePosixPath('.')
        if (not isinstance(name, str) or not name or len(name) > 512 or name in seen
                or path.is_absolute() or '..' in path.parts or str(path) != name or '\\' in name
                or any(ord(character) < 32 for character in name) or before == after
                or any(value is not None and (not isinstance(value, str) or not DIGEST.fullmatch(value)) for value in (before, after))
                or not isinstance(reason, str) or not 20 <= len(reason.strip()) <= 500
                or result.get(name) != before):
            raise ValueError('Unsafe or unreviewed chained compatibility change')
        seen.add(name)
        if after is None:
            result.pop(name, None)
        else:
            result[name] = after
    if canonical_hash(result) != delta['current_sha256']:
        raise ValueError('Chained compatibility output inventory differs')
    return result


def validate_chain(root, runtime, record, recorded, current, protected, artifact_sha256=None, scope='runtime'):
    predecessor = chain_header(root, runtime, record)
    manifest_key = 'native_manifest_sha256' if runtime == 'vitess' else 'manifest_sha256'
    if artifact_sha256 is not None and record['qualified_release'][manifest_key] != artifact_sha256:
        raise ValueError('Chained compatibility review belongs to another native manifest')
    prior_delta = predecessor['scopes'][scope] if runtime == 'vitess' else predecessor['delta']
    qualified = recorded
    # Replay every published review older than the immediate predecessor.
    links = chain_links()
    releases = list(links)
    for older_release in releases[:releases.index(record['reviewed_release'])]:
        _, older_anchors, older_file = links[older_release]
        older = json.loads((Path(root) / 'release' / ('managed-' + runtime) / older_file).read_text(), object_pairs_hook=unique)
        if canonical_hash(older) != older_anchors[runtime]:
            raise ValueError('Historical runtime predecessor changed')
        recorded = apply_chain_delta(recorded, older['scopes'][scope] if runtime == 'vitess' else older['delta'])
    previous = apply_chain_delta(recorded, prior_delta)
    delta = record['scopes'][scope] if runtime == 'vitess' else record['delta']
    result = apply_chain_delta(previous, delta)
    if result != current:
        raise ValueError('Runtime source changed outside the chained review')
    changed_trust, go_reviewable = set(), None
    for change in delta['changes']:
        name = change['path']
        if name in CHAIN_TRUST_PATHS[runtime]:
            changed_trust.add(name)
        elif name in GO_MODULE_FILES:
            # A go.mod or go.sum change is reviewable only while every module, replacement,
            # checksum and directive compiled into the runtime's own Go binaries is unchanged.
            if go_reviewable is None:
                go_reviewable = go_build_unchanged(root, runtime, record['qualified_release'].get('commit'),
                                                   qualified, current)
            if not go_reviewable:
                raise ValueError('Runtime, dependency, image or native harness changes require new qualification')
        elif protected(name) or name in TRUST_ROOTS.get(runtime, set()):
            raise ValueError('Runtime, dependency, image or native harness changes require new qualification')
    if scope == 'runtime' and changed_trust != CHAIN_TRUST_PATHS[runtime]:
        raise ValueError('Chained review trust-root inventory differs')
    return True
