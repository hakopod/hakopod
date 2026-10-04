#!/usr/bin/env python3
"""Record source-bound Supabase native evidence and its release boundary."""
import argparse, json, runpy, shutil, tempfile
from pathlib import Path
VERIFIER=runpy.run_path(str(Path(__file__).with_name('verify-supabase-runtime.py')))

def assemble(source,report_path,cleanup_path,output):
    source,output=Path(source),Path(output)
    sources=VERIFIER['source_files'](source); report=VERIFIER['read_json'](report_path); cleanup=VERIFIER['read_json'](cleanup_path)
    if set(report)!=VERIFIER['REPORT_FIELDS'] or type(report.get('schema_version')) is not int or report['schema_version']!=2:
        raise ValueError('Supabase native report is missing or malformed; legacy receipts are unsupported')
    images=VERIFIER['validate_images'](report.get('images')); identities=VERIFIER['validate_identities'](report.get('identities'),images); assets=VERIFIER['embedded_assets'](source)
    runner_hash=VERIFIER['file_hash'](source/VERIFIER['PRODUCER']['runner_path']); producer_hash=VERIFIER['file_hash'](source/VERIFIER['PRODUCER']['producer_path'])
    VERIFIER['validate_acceptance'](report,cleanup,sources,images,identities,runner_hash,producer_hash,assets)
    temporary,output=VERIFIER['atomic_directory'](output)
    try:
        shutil.copyfile(report_path,temporary/'native-acceptance.json'); shutil.copyfile(cleanup_path,temporary/'cleanup-receipt.json')
        tooling={'recorder_sha256':VERIFIER['file_hash'](source/'release/record-supabase-qualification.py'),'verifier_sha256':VERIFIER['file_hash'](source/'release/verify-supabase-runtime.py'),'runner_sha256':runner_hash,'producer_sha256':producer_hash}
        manifest={'schema_version':2,'platform':'linux/amd64','run_id':report['run_id'],'environment':report['environment'],'source_files':sources,'images':images,'identities':identities,'embedded_assets':assets,'tooling':tooling,'evidence':{'producer':report['producer'],'event_file_sha256':report['event_file_sha256'],'log_sha256':report['log_sha256']},'files':{name:VERIFIER['file_hash'](temporary/name,VERIFIER['MAX_REPORT_BYTES']) for name in ('native-acceptance.json','cleanup-receipt.json')},'capability':VERIFIER['capabilities'](source,images)}
        (temporary/'manifest.json').write_text(json.dumps(manifest,indent=2,sort_keys=True)+'\n')
        VERIFIER['validate_metadata'](temporary,source); temporary.rename(output); return manifest
    except Exception:
        shutil.rmtree(temporary,ignore_errors=True); raise

if __name__=='__main__':
    parser=argparse.ArgumentParser(description=__doc__); parser.add_argument('--source',type=Path,required=True); parser.add_argument('--report',type=Path,required=True); parser.add_argument('--cleanup',type=Path,required=True); parser.add_argument('--output',type=Path,required=True); args=parser.parse_args(); assemble(args.source,args.report,args.cleanup,args.output)
