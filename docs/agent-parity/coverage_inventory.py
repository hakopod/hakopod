#!/usr/bin/env python3
"""Create an auditable contract and dashboard call inventory. Does not infer runtime verification."""
import json,re,pathlib,collections,hashlib
root=pathlib.Path(__file__).resolve().parents[2]
contract=json.loads((root/'api/openapi.json').read_text())
calls=collections.defaultdict(list)
pattern=re.compile(r"\bclient\.(GET|POST|PUT|PATCH|DELETE)\s*\(\s*(['\"])([^'\"]+)\2")
for file in (root/'web/src').rglob('*'):
 if file.suffix not in ('.ts','.tsx') or '.test.' in file.name or '.generated.' in file.name:continue
 text=file.read_text()
 for match in pattern.finditer(text):calls[(match[1],match[3])].append(f'{file.relative_to(root)}:{text.count(chr(10),0,match.start())+1}')
def state(p,o):
 policy=contract['paths'][p][o.lower()].get('x-hakopod-agent',{})
 exposure=policy.get('exposure','unavailable')
 return {'generic':'generic scoped invocation','installation':'separate installation invocation','dedicated':'dedicated workflow','prerequisite':'human or protocol prerequisite','pending':'gap: policy and workflow pending','unavailable':'gap: unclassified'}.get(exposure,'gap: unknown exposure')
rows=[]
for p,methods in contract['paths'].items():
 for method,o in methods.items():
  if method.upper() not in ('GET','POST','PUT','PATCH','DELETE'):continue
  rows.append({'operation':o['operationId'],'method':method.upper(),'path':p,'dashboard_calls':calls.pop((method.upper(),p),[]),'current_interface':state(p,method.upper()),'policy':o.get('x-hakopod-agent',{}),'request_media':list(o.get('requestBody',{}).get('content',{})),'response_media':sorted({m for r in o.get('responses',{}).values() for m in r.get('content',{})})})
rows.sort(key=lambda x:x['operation'])
out={'contract_sha256':hashlib.sha256((root/'api/openapi.json').read_bytes()).hexdigest(),'contract_operation_count':len(rows),'literal_dashboard_call_count':sum(len(r['dashboard_calls']) for r in rows),'dashboard_contract_gaps':[{'method':k[0],'path':k[1],'calls':v} for k,v in calls.items()],'limitations':['Literal typed client calls are captured with file and line evidence. Dynamic endpoint expressions and alternate auth/upload/stream transports require manual review.','Current interface states describe source implementation, not production availability.'],'operations':rows}
(root/'docs/agent-parity/coverage-matrix.json').write_text(json.dumps(out,indent=2)+'\n')
lines=['# Interface coverage inventory','',f"Contract operations: {len(rows)}. Literal dashboard calls: {out['literal_dashboard_call_count']}.",'','This is source coverage. It is not runtime verification. Dynamic and alternate transports require manual review.','','| Operation | Method and path | Current interface | Dashboard call sites |','| --- | --- | --- | --- |']
for r in rows:lines.append(f"| {r['operation']} | {r['method']} {r['path']} | {r['current_interface']} | {'<br>'.join(r['dashboard_calls']) or 'No literal typed call'} |")
(root/'docs/agent-parity/coverage-matrix.md').write_text('\n'.join(lines)+'\n')
(root/'docs/agent-parity/remaining-operations.json').write_text(json.dumps([{'operation':r['operation'],'method':r['method'],'path':r['path'],'policy':r['policy'],'reason':r['policy'].get('prerequisite') or r['current_interface']} for r in rows if r['policy'].get('exposure') not in ('generic','installation')],indent=2)+'\n')
print(json.dumps({'operations':len(rows),'states':dict(collections.Counter(r['current_interface'] for r in rows)),'dashboard_contract_gaps':out['dashboard_contract_gaps']},indent=2))
