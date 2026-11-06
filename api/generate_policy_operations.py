#!/usr/bin/env python3
"""Generate the operations an API key can be refused or held for, for the agent policy editor."""
import json
from pathlib import Path
root=Path(__file__).resolve().parents[1]
spec=json.loads((root/'api/openapi.json').read_text())
rows=[]
for path,methods in spec['paths'].items():
 for method,operation in methods.items():
  policy=operation.get('x-hakopod-agent',{})
  # People-only flows and unclassified routes never reach the gate as a key request.
  if policy.get('category') in (None,'human','unclassified') or policy.get('exposure')=='unavailable':
   continue
  rows.append({'id':operation['operationId'],'method':method.upper(),'path':path,'category':policy['category'],'summary':operation.get('summary','')})
rows.sort(key=lambda row:row['id'])
(root/'web/src/lib/agent-policy-operations.generated.ts').write_text('// Generated from api/openapi.json by api/generate_policy_operations.py. Do not edit.\nexport const policyOperations = '+json.dumps(rows,indent=2)+' as const\n')
