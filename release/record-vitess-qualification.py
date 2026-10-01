#!/usr/bin/env python3
"""Assemble public Vitess qualification records from successful native runs.

Raw test output and storage credentials remain on the development VM. This
copies only structural test events, source patches and binary checksums.
"""
import argparse
import json
import math
from pathlib import Path
import runpy
import shutil


VERIFIER = runpy.run_path(str(Path(__file__).with_name('verify-vitess-runtime.py')))
CASES = {'lifecycle': 'TestManagedVitessLive',
         'recovery': 'TestManagedVitessRecoveryLive',
         'reseed': 'TestManagedVitessNativeReseedLive',
         'revocation': 'TestManagedVitessBackupRevocationLive'}


def collect_reports(reports, sources, images, verifier_hash, runner_hash, etcd_image):
    if len(reports) != len(CASES):
        raise ValueError('Use one successful native report for each Vitess acceptance case')
    events, attempts, seen = [], [], set()
    for report in reports:
        if not isinstance(report, dict) or type(report.get('schema_version')) is not int or report['schema_version'] != 1:
            raise ValueError('Vitess native reports require schema 1')
        case = report.get('case')
        if not isinstance(case, str) or case not in CASES or case in seen:
            raise ValueError('Vitess native report cases are missing or duplicated')
        seen.add(case)
        if report.get('passed') is not True or type(report.get('exit_code')) is not int or report['exit_code'] != 0 or report.get('limit_error') != '':
            raise ValueError('A failed or bounded-out Vitess run cannot qualify a release')
        if report.get('context') != 'k3d-hakopod-dev' or report.get('execution') != 'native' or report.get('platform') != 'linux/amd64':
            raise ValueError('Vitess qualification requires the native development cluster')
        if report.get('source_files') != sources or report.get('source_files_after') != sources or report.get('images') != images:
            raise ValueError('Vitess source or image identity changed across native acceptance')
        if report.get('verifier_sha256') != verifier_hash:
            raise ValueError('Vitess native reports used another evidence verifier')
        if report.get('runner_sha256') != runner_hash:
            raise ValueError('Vitess native reports used another acceptance runner')
        VERIFIER['validate_native_environment'](report.get('environment'), case, [*images.values(), etcd_image])
        for name in ('log_sha256', 'runner_sha256'):
            value = report.get(name)
            if not isinstance(value, str) or not VERIFIER['DIGEST'].fullmatch(value):
                raise ValueError('Vitess native report is missing its evidence hashes')
        if report.get('failed_tests') != [] or not isinstance(report.get('test_events'), list):
            raise ValueError('Vitess native report contains failed or missing test events')
        elapsed = report.get('elapsed_seconds')
        if type(elapsed) not in (int, float) or not math.isfinite(elapsed) or not 0 < elapsed <= 95 * 60:
            raise ValueError('Vitess native report has an invalid execution duration')
        name = CASES[case]
        expected = {name}
        if case == 'lifecycle':
            expected.update(name + '/' + mode for mode in ('standalone', 'cluster'))
        passed = VERIFIER['accepted_test_events'](report['test_events'], expected)
        if report.get('test') != name or report.get('passed_tests') != sorted(passed) or any(
                test != name and not test.startswith(name + '/') for test in passed):
            raise ValueError('Vitess native test events do not belong to their reported case')
        events.extend(report['test_events'])
        attempts.append({key: report[key] for key in (
            'case', 'log_sha256', 'runner_sha256', 'verifier_sha256', 'elapsed_seconds', 'environment')})
    passed = VERIFIER['accepted_test_events'](events)
    return {'schema_version': 1, 'context': 'k3d-hakopod-dev', 'execution': 'native',
            'platform': 'linux/amd64', 'images': images, 'source_files': sources,
            'passed': True, 'failed_tests': [], 'passed_tests': sorted(passed),
            'test_events': events, 'attempts': attempts}


def assemble(source, build, reports, output):
    if output.exists():
        raise ValueError('Use a fresh Vitess qualification output directory')
    sources = VERIFIER['source_files'](source)
    images, provenance = {}, {}
    for kind in VERIFIER['PACKAGES']:
        symbol = 'vitessServer' if kind == 'runtime' else 'vitessOperator'
        reference = VERIFIER['source_constant'](source, symbol + 'Image')
        folder = 'runtime' if kind == 'runtime' else 'controller'
        binaries = {path: VERIFIER['file_hash'](build / folder / 'bin' / Path(path).name, 512 * 1024 * 1024)
                    for path in sorted(VERIFIER['BINARIES'][kind])}
        base = (source / ('Dockerfile.vitess-' + kind)).read_text().splitlines()[0]
        if not base.startswith('FROM '):
            raise ValueError('Vitess base image declaration changed')
        images[kind] = {'reference': reference, 'base_image': base[5:], 'binaries': binaries}
        provenance[kind] = {'repository': VERIFIER['SOURCE_REPOSITORIES'][kind],
                            'revision': VERIFIER['source_constant'](source, symbol + 'Source')}
    acceptance = collect_reports([VERIFIER['read_json'](path) for path in reports], sources,
        {kind: item['reference'] for kind, item in images.items()},
        VERIFIER['file_hash'](source / 'release/verify-vitess-runtime.py'),
        VERIFIER['file_hash'](source / 'scripts/run-development-vitess-acceptance.py'),
        VERIFIER['source_constant'](source, 'vitessEtcdImage'))
    output.mkdir(parents=True)
    for before, after in (
        ('runtime/upstream.patch', 'runtime-upstream.patch'),
        ('controller/upstream.patch', 'operator-upstream.patch'),
        ('binary-sha256.txt', 'binary-sha256.txt')):
        VERIFIER['file_hash'](build / before)
        shutil.copyfile(build / before, output / after)
    (output / 'native-acceptance.json').write_text(json.dumps(acceptance, indent=2) + '\n')
    manifest = {'schema_version': 1, 'platform': 'linux/amd64', 'source_files': sources,
                'sources': provenance, 'images': images,
                'files': {name: VERIFIER['file_hash'](output / name) for name in (
                    'runtime-upstream.patch', 'operator-upstream.patch', 'binary-sha256.txt', 'native-acceptance.json')}}
    (output / 'manifest.json').write_text(json.dumps(manifest, indent=2) + '\n')
    VERIFIER['validate_metadata'](output, source)
    return manifest


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--source', type=Path, required=True)
    parser.add_argument('--build', type=Path, required=True)
    parser.add_argument('--report', type=Path, action='append', required=True)
    parser.add_argument('--output', type=Path, required=True)
    args = parser.parse_args()
    assemble(args.source, args.build, args.report, args.output)
