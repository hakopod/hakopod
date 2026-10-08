schemas['SandboxSession'] = obj({'allowed_identities':array(S),'helper_command':array(S),'idle_seconds':{'type':'integer','minimum':60,'maximum':900,'default':900},'lifetime_seconds':{'type':'integer','minimum':60,'maximum':3600,'default':3600}},['allowed_identities','helper_command'])
schemas['Service']['properties']['session'] = ref('SandboxSession')
schemas['SandboxSessionReceipt'] = obj({'id':S,'application_id':S,'service':S,'revision':I,'image':S,'generation':S,'status':{'type':'string','enum':['starting','ready','closing','closed']},'message':S,'cleanup_pending':B,'created_at':T,'expires_at':T,'idle_until':T,'closed_at':T},['id','application_id','service','revision','image','generation','status','cleanup_pending','created_at','expires_at','idle_until'])
schemas['SandboxSessionCreate'] = obj({'expected_revision':I,'expected_image':S,'runtime_key':S},['expected_revision','expected_image','runtime_key'])
base='/applications/{id}/services/{service}/sessions'
route(base,'post','createSandboxSession',ref('SandboxSessionReceipt'),ref('SandboxSessionCreate'),'202',idem=True)
route(base,'get','listSandboxSessions',obj({'items':array(ref('SandboxSessionReceipt'))},['items']))
route(base+'/{session}','get','getSandboxSession',ref('SandboxSessionReceipt'))
route(base+'/{session}','delete','deleteSandboxSession',ref('SandboxSessionReceipt'),status='202')
route(base+'/{session}/heartbeat','post','heartbeatSandboxSession',ref('SandboxSessionReceipt'))
route(base+'/{session}/call','post','callSandboxSession',S,idem=True)
for path in [base,base+'/{session}',base+'/{session}/heartbeat',base+'/{session}/call']:
    for operation in paths[path].values():
        operation['parameters'].append({'name':'X-Hakopod-Owner-Scope','in':'header','required':True,'schema':{'type':'string','minLength':1,'maxLength':128}})
        operation['description']='Requires an exact application-scoped machine key, an explicit sessions permission and a template identity grant. The creating identity and owner scope must match. Closed status confirms owned runtime cleanup.'
paths[base]['get']['parameters'].append({'name':'runtime_key','in':'query','required':True,'schema':S})
for suffix in ['heartbeat','call']:
    paths[base+'/{session}/'+suffix]['post']['parameters'].append({'name':'X-Hakopod-Session-Generation','in':'header','required':True,'schema':S})
call=paths[base+'/{session}/call']['post']
call['requestBody']={'required':True,'content':{'application/octet-stream':{'schema':{'type':'string','format':'binary','maxLength':50331648}}}}
call['responses']['200']['content']={'application/octet-stream':{'schema':{'type':'string','format':'binary','maxLength':54525952}}}
call['description']='Send bounded bytes to the fixed session helper and stream its output. Requires the expected generation and a unique Idempotency-Key. A repeated call is rejected. If the stream fails, its execution outcome is uncertain. Do not replay code automatically.'
