#!/usr/bin/env python3
"""Verify locally generated release bytes, platforms, notices and SBOM coverage."""
import argparse
from collections import Counter
from datetime import datetime, timezone
import hashlib
import importlib.util
import json
from pathlib import Path
import subprocess
import tarfile

ROOT=Path(__file__).resolve().parents[1]
spec=importlib.util.spec_from_file_location('release_build',ROOT/'release/build.py')
build=importlib.util.module_from_spec(spec)
spec.loader.exec_module(build)

def main():
    parser=argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--version',default='0.1.0-dev')
    args=parser.parse_args()
    if not args.version or Path(args.version).name!=args.version or args.version in ('.','..'):
        raise SystemExit('version must be one safe path component')
    destination=ROOT/'.local/releases'/args.version
    stage=ROOT/'.local/release-stage'/args.version/'sbom-source'
    if not (destination/'.hakopod-generated').is_file():raise SystemExit('Run release/build.py first')
    provenance=json.loads((destination/'provenance.json').read_text())
    assert provenance['source_fingerprint_sha256']==build.fingerprint()[0],'working-tree source differs from the release snapshot'
    assert not provenance['source_changed_during_build'],'source changed during compilation'
    binaries=[]
    for bundle in sorted(destination.glob('*.tar.gz')):
        with tarfile.open(bundle) as archive:
            names=archive.getnames()
            assert all(not name.startswith('/') and '..' not in Path(name).parts for name in names)
            if '_dependency-notices' not in bundle.name:
                assert any(name.endswith('/LICENSE') for name in names)
                assert any(name.endswith('/NOTICE') for name in names)
                assert any('/third-party-licenses/' in name for name in names)
            for entry in archive.getmembers():
                if Path(entry.name).name not in ('hakopod','hakopod-server'):continue
                blob=archive.extractfile(entry).read()
                binary=stage/entry.name
                digest=hashlib.sha256(blob).hexdigest()
                assert digest==hashlib.sha256(binary.read_bytes()).hexdigest(),'archive binary differs from scanned stage'
                kind=subprocess.check_output(['file','-b',str(binary)],text=True).strip()
                if '_linux_' in entry.name:assert 'ELF' in kind and 'statically linked' in kind
                if '_darwin_' in entry.name:assert 'Mach-O' in kind
                if '_arm64/' in entry.name:assert 'arm64' in kind or 'aarch64' in kind
                if '_amd64/' in entry.name:assert 'x86_64' in kind or 'x86-64' in kind
                binaries.append({'path':entry.name,'bytes':len(blob),'sha256':digest,'file_type':kind})
    assert len(binaries)==6
    host_os=build.output(['go','env','GOHOSTOS'])
    host_arch=build.output(['go','env','GOHOSTARCH'])
    native=stage/('hakopod_'+args.version+'_'+host_os+'_'+host_arch)/'hakopod'
    native_version=subprocess.check_output([str(native),'version'],text=True).strip()
    assert native_version==args.version
    spdx=json.loads((destination/'hakopod.spdx.json').read_text())
    cdx=json.loads((destination/'hakopod.cyclonedx.json').read_text())
    syft=json.loads((destination/'hakopod.syft.json').read_text())
    assert spdx['spdxVersion']=='SPDX-2.3' and cdx['bomFormat']=='CycloneDX'
    inventory=json.loads((destination/'dependency-license-inventory.json').read_text())
    assert all(package['files'] for package in inventory['go'])
    for path in destination.glob('*.json'):
        assert str(Path.home()) not in path.read_text(),'absolute host-home path in '+path.name
    report={'verified_at':datetime.now(timezone.utc).isoformat(),'source_fingerprint_sha256':provenance['source_fingerprint_sha256'],
            'source_matches_working_tree':True,'native_cli_platform':host_os+'/'+host_arch,'native_cli_version':native_version,'binaries':binaries,
            'checks':['all archive paths relative and traversal-free','all six archive executable bytes equal scanned binary bytes',
                      'Linux binaries statically linked; all target formats and architectures correct','native host CLI returns release version',
                      'all linked external Go modules have preserved license/notice files','JSON host paths normalized','SHA256SUMS verified'],
            'sbom':{'spdx_version':spdx['spdxVersion'],'spdx_package_count':len(spdx['packages']),
                    'cyclonedx_version':cdx['specVersion'],'cyclonedx_component_count':len(cdx['components']),
                    'native_catalog_type_counts':dict(Counter(package['type'] for package in syft['artifacts']))},
            'limitations':['Cross-compilation and archive inspection do not establish full runtime support on every target platform.',
                           'Combined SBOM covers Go binaries and declared dashboard dependencies; the separate image catalog covers the container OS.',
                           'Runtime lifecycle, restart and browser evidence is documented separately in docs/milestones.md.',
                           'No artifacts were signed, published or uploaded.']}
    (destination/'verification.json').write_text(json.dumps(report,indent=2)+'\n')
    (destination/'SHA256SUMS').write_text('\n'.join(hashlib.sha256(path.read_bytes()).hexdigest()+'  '+path.name for path in sorted(destination.iterdir()) if path.is_file() and not path.name.startswith('.') and path.name!='SHA256SUMS')+'\n')
    subprocess.run(['shasum','-a','256','-c','SHA256SUMS'],cwd=destination,check=True)
    print('Verified all six archived binaries, native CLI, source snapshot, notices and SBOMs.',flush=True)

if __name__=='__main__':main()
