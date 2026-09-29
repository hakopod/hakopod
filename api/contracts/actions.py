schemas['ServiceStatus']['properties']['images'] = array(S)
schemas["Actions"] = obj({"repository": S, "organization": S, "runner_group_id": {"type": "integer", "minimum": 0, "maximum": 9007199254740991, "description": "Organization runner group ID; omit or use zero for the GitHub default group."}, "credential": S, "labels": array(S), "timeout_minutes": I}, ["credential", "labels"])
schemas["Actions"]["description"] = "GitHub pools select exactly one of organization or repository. Runner groups apply only to organization pools. Native provider execution requires an exact installation binding qualified for its coordinator, image and architecture. The server validates runtime availability before accepting a deployment."
schemas["Actions"]["properties"]["provider"] = {"type": "string", "enum": ["github", "gitlab", "bitbucket"], "description": "Defaults to github when omitted. Declaring a provider does not enable its managed runtime."}
schemas["Actions"]["properties"]["gitlab"] = obj({"url": S, "project_id": {"type": "integer", "minimum": 1, "maximum": 9007199254740991}, "group_id": {"type": "integer", "minimum": 1, "maximum": 9007199254740991}, "trust_policy": S}, ["url"])
schemas["Actions"]["properties"]["gitlab"]["description"] = "GitLab instance base and exactly one project_id or group_id. Custom instances require an installation-approved trust_policy and native execution qualification for that exact coordinator."
schemas["Actions"]["properties"]["bitbucket"] = obj({"workspace": S, "repository": S}, ["workspace"])
schemas["Actions"]["properties"]["bitbucket"]["description"] = "Bitbucket workspace UUID and required repository UUID. Execution remains unavailable until the installation qualifies its native lifecycle."
schemas["Actions"]["properties"]["workspace_size_gib"] = {"type": "integer", "minimum": 2, "maximum": 16, "description": "Defaults to 2 GiB. Temporary disk reserved on the runner node per slot, shared by source, tools, Docker images and build files. Deleted after each job."}
schemas["Actions"]["properties"]["jobs_credential"] = {"type": "string", "description": "Optional application secret for provider job details and logs. Defaults to credential when omitted or empty."}
schemas["Actions"]["properties"]["cache"] = obj({"credential": S}, ["credential"])
schemas["Actions"]["properties"]["cache"]["description"] = "Optional GitLab cache using installation-approved S3 storage. credential names an application secret containing access_key, secret_key and optional session_token. Storage credentials remain on the manager. GitHub uses its own workflow cache configuration."
schemas["Service"]["properties"]["actions"] = ref("Actions")
schemas["ActionsPool"] = obj({"application_id":S,"service":S,"project":S,"environment":S,"application_name":S,"revision":I,"config":ref("Service"),"removed":B,"message":S,"updated_at":T},["application_id","service","project","environment","application_name","revision","config","removed","message","updated_at"])
schemas["ActionsSlot"] = obj({"id":S,"application_id":S,"service":S,"runner_id":I,"phase":S,"created_at":T,"updated_at":T},["id","application_id","service","runner_id","phase","created_at","updated_at"])
schemas["ActionsSlot"]["properties"]["provider_runner_id"] = {"type": "string", "description": "Opaque native provider runner identifier when present; retains UUIDs without numeric conversion."}
route("/applications/{id}/actions","get","getManagedActions",obj({"items":array(obj({"pool":ref("ActionsPool"),"slots":array(ref("ActionsSlot"))},["pool","slots"]))},["items"]))
schemas["ActionsProviderCapabilities"] = obj({
    "provider": {"type": "string", "enum": ["github", "gitlab", "bitbucket"]},
    "lifecycle": {"type": "string", "enum": ["ephemeral", "dedicated"]},
    "available": B, "reason": S, "image": S,
    "bindings": array(obj({"application": S, "service": S, "image": S, "architecture": S, "gitlab": schemas["Actions"]["properties"]["gitlab"], "bitbucket": schemas["Actions"]["properties"]["bitbucket"], "cache": B, "cross_architecture": B}, ["application", "service", "image", "architecture", "cache", "cross_architecture"])),
    "minimum_resources": obj({"cpu_request": S, "cpu_limit": S, "memory_request": S, "memory_limit": S, "workspace_gib": I}, ["cpu_request", "cpu_limit", "memory_request", "memory_limit", "workspace_gib"]),
    "cache": obj({"persistent": B, "backend": S, "reason": S}, ["persistent"]),
    "build": obj({"native_architectures": array(S), "cross_architecture": B, "reason": S}, ["native_architectures", "cross_architecture"]),
    "isolation": obj({"single_job": B, "manager_credentials_isolated": B, "reason": S}, ["single_job", "manager_credentials_isolated"]),
    "cancellation_scope": {"type": "string", "enum": ["none", "job", "pipeline"]},
}, ["provider", "lifecycle", "available", "minimum_resources", "cache", "build", "isolation", "cancellation_scope"])
schemas["ActionsProviderCapabilities"]["description"] = "Qualified provider behavior, separate from scoped licensing, runtime readiness and node availability. An unavailable provider cannot be deployed."
route("/actions/capabilities","get","getManagedActionsCapabilities",obj({"licensed":B,"runtime_ready":B,"message":S,"runner_image":S,"resource_profiles":schemas["Plan"]["properties"]["resource_profiles"],"providers":array(ref("ActionsProviderCapabilities"))},["licensed","runtime_ready","message","runner_image","providers"]),scope=True)
schemas['ActionsObservation'] = obj({'repository':S,'workflow':S,'run_id':I,'run_number':I,'attempt':I,'job_key':S,'branch':S,'sha':S},['repository','workflow','run_id','run_number','attempt','job_key','branch','sha'])
schemas['ActionsStep'] = obj({'name':S,'status':S,'conclusion':S,'number':I,'started_at':{'type':['string','null'],'format':'date-time'},'completed_at':{'type':['string','null'],'format':'date-time'}},['name','status','conclusion','number','started_at','completed_at'])
schemas['ActionsWorkflowJob'] = obj({'id':I,'run_id':I,'run_attempt':I,'name':S,'status':S,'conclusion':S,'runner_id':I,'runner_name':S,'started_at':{'type':['string','null'],'format':'date-time'},'completed_at':{'type':['string','null'],'format':'date-time'},'steps':array(ref('ActionsStep')),'steps_truncated':B},['id','run_id','run_attempt','name','status','conclusion','runner_id','runner_name','started_at','completed_at','steps'])
schemas['ActionsJob'] = obj({'slot_id':S,'runner_id':I,'observation':ref('ActionsObservation'),'job':{'anyOf':[ref('ActionsWorkflowJob'),{'type':'null'}]},'created_at':T,'updated_at':T},['slot_id','runner_id','observation','job','created_at','updated_at'])
schemas['ActionsProviderJobIdentity'] = obj({'runner_id':S,'runner_name':S,'repository':S,'run_id':S,'job_id':S,'attempt':I},['runner_id','runner_name','repository','run_id','job_id'])
schemas['ActionsProviderJobIdentity']['description'] = 'Opaque provider identifiers bound to the original runner and target. Native identifiers are strings and must not be converted to JavaScript numbers.'
schemas['ActionsProviderWorkflowJob'] = obj({'identity':ref('ActionsProviderJobIdentity'),'name':S,'status':S,'conclusion':S,'started_at':{'type':['string','null'],'format':'date-time'},'completed_at':{'type':['string','null'],'format':'date-time'},'steps':array(ref('ActionsStep')),'steps_truncated':B},['identity','name','status','conclusion','steps','steps_truncated'])
schemas['ActionsJob']['properties'].update({'provider':{'type':'string','enum':['github','gitlab','bitbucket']},'provider_runner_id':S,'native_job':ref('ActionsProviderWorkflowJob'),'discovery_state':{'type':'string','enum':['pending','observed','unavailable','reuse_detected']},'can_cancel':B})
route('/applications/{id}/actions/{service}/jobs','get','listActionsJobs',obj({'items':array(ref('ActionsJob')),'state':S,'message':S,'observed_at':T,'limit':I,'truncated':B},['items','state','message','observed_at','limit','truncated']))
route('/applications/{id}/actions/{service}/jobs/{slot}/logs','get','getActionsJobLogs',obj({'lines':array(obj({'number':I,'text':S},['number','text'])),'truncated':B,'source':{'type':'string','enum':['github','gitlab','runner','none']},'state':S,'message':S,'observed_at':T},['lines','truncated','source','state','message','observed_at']))
route('/applications/{id}/actions/{service}/jobs/{slot}/cancel','post','cancelActionsJob',obj({'status':S,'scope':{'type':'string','enum':['job']}},['status','scope']),status='202')
for name in ['service','slot']:
    paths['/applications/{id}/actions/{service}/jobs/{slot}/cancel']['post']['parameters'].append({'name':name,'in':'path','required':True,'schema':S})
for endpoint in ['/applications/{id}/actions/{service}/jobs','/applications/{id}/actions/{service}/jobs/{slot}/logs']:
    for name in ['service'] + (['slot'] if '{slot}' in endpoint else []):
        paths[endpoint]['get']['parameters'].append({'name':name,'in':'path','required':True,'schema':S})
schemas['ActionsProviderHold'] = obj({'id':{'type':'string','format':'uuid'},'reason':S,'observed_at':T,'slot_id':S,'provider':{'type':'string','enum':['gitlab']},'instance_url':S,'jobs':{'type':'array','items':ref('ActionsProviderWorkflowJob'),'minItems':2,'maxItems':2}},['id','reason','observed_at','slot_id','provider','instance_url','jobs'])
route('/applications/{id}/actions/{service}/hold','get','getActionsProviderHold',obj({'hold':{'anyOf':[ref('ActionsProviderHold'),{'type':'null'}]},'active_slots':I},['hold','active_slots']))
route('/applications/{id}/actions/{service}/hold/release','post','releaseActionsProviderHold',obj({'status':{'type':'string','enum':['released']},'hold_id':{'type':'string','format':'uuid'}},['status','hold_id']),obj({'hold_id':{'type':'string','format':'uuid'},'acknowledge':{'type':'boolean','const':True}},['hold_id','acknowledge']))
for endpoint, method in [('/applications/{id}/actions/{service}/hold','get'),('/applications/{id}/actions/{service}/hold/release','post')]:
    paths[endpoint][method]['parameters'].append({'name':'service','in':'path','required':True,'schema':S})
