#!/usr/bin/env python3
"""Generate exact public agent routes from authoritative OpenAPI exposure metadata."""
import json
from pathlib import Path
root=Path(__file__).resolve().parents[1]
spec=json.loads((root/'api/openapi.json').read_text())
rows=[]
for path,methods in spec['paths'].items():
 for method,operation in methods.items():
  policy=operation.get('x-hakopod-agent',{})
  if policy.get('exposure') in ['generic','installation']:
   rows.append({'id':operation['operationId'],'method':method.upper(),'path':path,'category':policy['category'],'boundary':policy['boundary']})
(root/'web/src/server/agent-routes.generated.ts').write_text('// Generated from api/openapi.json x-hakopod-agent. Do not edit.\nexport const agentRoutes = '+json.dumps(rows,indent=2)+' as const\n')
