#!/usr/bin/env python3
"""Validate exact reviewed source deltas for retained managed-runtime evidence."""
import hashlib
import json
from pathlib import Path, PurePosixPath
import re

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
CHAIN_RELEASE = 'v0.1.0-alpha.60'
CHAIN_PREDECESSORS = {'myduck': '45170d7d57580267df5515e373eb7debf0879bac6e53a4bbeef9abb6826390f0', 'oracle-free': 'b59a0847286257ae0870b2a863a5f557860c9f204b1b9323eb55fbf0cffdec08', 'vitess': '6ccc96b680e076acd49f9f0d1d7192018d453ec5baa4baa80358f9946ffb7f3b'}
CHAIN_TRUST_PATHS = {
    'myduck': {'release/runtime-source-compatibility.py'},
    'oracle-free': {'release/runtime-source-compatibility.py'},
    'vitess': {'release/runtime-source-compatibility.py', 'release/verify-vitess-runtime.py'},
}


def chain_header(root, runtime, record):
    scope_key = 'scopes' if runtime == 'vitess' else 'delta'
    version = 3 if runtime == 'vitess' else 2
    fields = {'schema_version', 'runtime', 'qualified_release', 'reviewed_release',
              'control_plane_commit', 'regression_evidence', 'predecessor', 'bootstrap', scope_key}
    if (runtime not in CHAIN_PREDECESSORS or not isinstance(record, dict)
            or set(record) != fields or type(record['schema_version']) is not int
            or record['schema_version'] != version or record['runtime'] != runtime
            or record['reviewed_release'] != CHAIN_RELEASE
            or not isinstance(record['control_plane_commit'], str)
            or not COMMIT.fullmatch(record['control_plane_commit'])):
        raise ValueError('Invalid chained runtime compatibility review')
    expected = dict(CHAIN_BASE, review_sha256=CHAIN_PREDECESSORS[runtime])
    if record['predecessor'] != expected:
        raise ValueError('Runtime compatibility predecessor identity changed')
    path = Path(root) / 'release' / ('managed-' + runtime) / 'source-compatibility-alpha59.json'
    if path.is_symlink() or not path.is_file() or not 1 <= path.stat().st_size <= 512 * 1024:
        raise ValueError('Published runtime predecessor review is unavailable')
    predecessor = json.loads(path.read_text(), object_pairs_hook=unique)
    if canonical_hash(predecessor) != CHAIN_PREDECESSORS[runtime]:
        raise ValueError('Published runtime predecessor review changed')
    if record['qualified_release'] != predecessor['qualified_release']:
        raise ValueError('Chained review cannot replace the historical native evidence identity')
    expected_bootstrap = dict(CHAIN_BASE, reviewed_release=CHAIN_RELEASE,
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
    previous = apply_chain_delta(recorded, prior_delta)
    delta = record['scopes'][scope] if runtime == 'vitess' else record['delta']
    result = apply_chain_delta(previous, delta)
    if result != current:
        raise ValueError('Runtime source changed outside the chained review')
    changed_trust = set()
    for change in delta['changes']:
        name = change['path']
        if name in CHAIN_TRUST_PATHS[runtime]:
            changed_trust.add(name)
        elif protected(name) or name in TRUST_ROOTS.get(runtime, set()):
            raise ValueError('Runtime, dependency, image or native harness changes require new qualification')
    if scope == 'runtime' and changed_trust != CHAIN_TRUST_PATHS[runtime]:
        raise ValueError('Chained review trust-root inventory differs')
    return True
