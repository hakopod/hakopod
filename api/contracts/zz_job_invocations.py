schemas['JobInvocation'] = obj({'allowed_identities':array(S),'input_keys':array(S),'max_input_bytes':{'type':'integer','minimum':1,'maximum':131072,'default':65536},'queue_limit':{'type':'integer','minimum':1,'maximum':64,'default':16}},['allowed_identities','input_keys'])
schemas['DeploymentJob']['properties']['invocation'] = ref('JobInvocation')
schemas['JobInvocationReceipt'] = obj({'id':S,'application_id':S,'service':S,'revision':I,'image':S,'correlation_id':S,'status':{'type':'string','enum':['queued','starting','running','succeeded','failed','cancelled']},'message':S,'cancel_requested':B,'cleanup_pending':B,'input_bytes':I,'log_truncated':B,'exit_code':I,'created_at':T,'started_at':{'anyOf':[T,{'type':'null'}]},'finished_at':{'anyOf':[T,{'type':'null'}]},'expires_at':T},['id','application_id','service','revision','image','correlation_id','status','cleanup_pending'])
schemas['JobInvocationCreate'] = obj({'expected_revision':I,'expected_image':S,'correlation_id':S,'owner_scope':S,'inputs':mapping({})},['expected_revision','expected_image','correlation_id','owner_scope','inputs'])
base='/applications/{id}/services/{service}/invocations'
route(base,'post','createJobInvocation',ref('JobInvocationReceipt'),ref('JobInvocationCreate'),'202',idem=True)
route(base,'get','listJobInvocations',obj({'items':array(ref('JobInvocationReceipt'))},['items']))
route(base+'/{invocation}','get','getJobInvocation',ref('JobInvocationReceipt'))
route(base+'/{invocation}/cancel','post','cancelJobInvocation',ref('JobInvocationReceipt'),status='202')
route(base+'/{invocation}/logs','get','getJobInvocationLogs',obj({'text':S,'truncated':B},['text','truncated']))
for path in [base,base+'/{invocation}',base+'/{invocation}/cancel',base+'/{invocation}/logs']:
    for operation in paths[path].values():
        operation['parameters'].append({'name':'X-Hakopod-Owner-Scope','in':'header','required':True,'schema':{'type':'string','minLength':1,'maxLength':128}})
        operation['description']='Requires an application-scoped machine key, an explicit jobs permission and a template identity grant. Reads and cancellation require the creating identity and owner scope. Public terminal status is withheld until owned runtime cleanup completes.'
paths[base]['get']['parameters'] += [{'name':'correlation_id','in':'query','required':True,'schema':S},{'name':'active','in':'query','schema':B},{'name':'Idempotency-Key','in':'header','schema':S}]
