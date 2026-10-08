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
        expected_bootstrap = {'base_release': BOOTSTRAP_BASE['tag'], 'base_commit': BOOTSTRAP_BASE['commit'],
                              'reviewed_release': BOOTSTRAP_RELEASE,
                              'trust_anchor': 'authenticated-git-review',
                              'trust_paths': sorted(BOOTSTRAP_CHANGED[runtime])}
        if bootstrap != expected_bootstrap or baseline['tag'] != BOOTSTRAP_BASE['tag'] \
                or baseline['commit'] != BOOTSTRAP_BASE['commit'] \
                or record['reviewed_release'] != BOOTSTRAP_RELEASE:
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
        if name in TRUST_ROOTS[runtime]:
            if bootstrap is None:
                raise ValueError(runtime + ' trust-root changes require the reviewed alpha.57 bootstrap')
            if name not in BOOTSTRAP_CHANGED[runtime]:
                raise ValueError(runtime + ' trust root is outside the fixed alpha.57 bootstrap')
        elif protected(name):
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
    if bootstrap is not None and changed_trust != BOOTSTRAP_CHANGED[runtime]:
        raise ValueError(runtime + ' compatibility bootstrap trust-path set differs')
    return True
