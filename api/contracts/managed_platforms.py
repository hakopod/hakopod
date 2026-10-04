schemas['ManagedPlatformSecretReference'] = obj({'name': S, 'revision': {'type':'integer','minimum':1}}, ['name','revision'])
schemas['ManagedPlatformResources'] = obj({'cpu': S, 'memory': S}, ['cpu','memory'])
schemas['ManagedPlatformPlacement'] = obj({'node_names': {'type':'array','items':S,'minItems':1,'maxItems':48,'uniqueItems':True}, 'spread': {'type':'string','enum':['','nodes','zones']}}, ['node_names'])
schemas['SupabaseConfig'] = obj({
    'public_url': S, 'site_url': S, 'redirect_urls': {'type':'array','items':S,'maxItems':32,'uniqueItems':True},
    'database_name': {'type':'string','const':'postgres'}, 'jwt_expiry_seconds': I, 'rest_max_rows': I,
    'storage_file_limit_bytes': I, 'pool_size': I, 'pool_max_clients': I,
    'email_signup': {'type':'boolean','const':False},
    'anonymous_signup': B, 'smtp_secret': ref('ManagedPlatformSecretReference'),
}, ['public_url','site_url','database_name','jwt_expiry_seconds','rest_max_rows','storage_file_limit_bytes','pool_size','pool_max_clients','email_signup'])
schemas['NeonConfig'] = obj({
    'postgres_version': {'type':'string','const':'17'},
    'compute_replicas': {'type':'integer','minimum':1,'maximum':6},
    'pageservers': {'type':'integer','minimum':2,'maximum':8},
    'safekeepers': {'type':'integer','const':3},
    'branch_limit': {'type':'integer','const':1},
    'object_storage_url': {'type':'string','maxLength':2048,'format':'uri','description':'Exact HTTPS object-storage origin on port 443. The server rejects credentials, paths, query strings and unsafe network destinations.'},
    'object_storage_bucket': {'type':'string','pattern':'^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$'},
    'object_storage_region': {'type':'string','pattern':'^[a-z0-9][a-z0-9-]{0,62}$'},
    'object_storage_prefix': {'type':'string','pattern':'^[a-z0-9][a-z0-9_-]{0,62}$'},
    'proxy_control_plane_patch_sha256': {'type':'string','const':'e9a1df309106d166adfc0982500f6500df220dbc6173761c48c7c2038563fbd6'},
}, ['postgres_version','compute_replicas','pageservers','safekeepers','branch_limit','object_storage_url','object_storage_bucket','object_storage_region','object_storage_prefix','proxy_control_plane_patch_sha256'])
schemas['ManagedPlatformSpec'] = obj({
    'schema_version': {'type':'integer','const':1}, 'name': S,
    'kind': {'type':'string','enum':['supabase','neon']}, 'version': {'type':'string','enum':['0.8.2','fa504217c61bbcaf5c512d75830564541f917f8f']},
    'tls_mode': {'type':'string','enum':['managed','operator'],'description':'Managed mode issues and renews resource-specific TLS certificates. Omitted values preserve operator-supplied certificates.'},
    'resources': mapping(ref('ManagedPlatformResources')), 'storage': mapping(I),
    'secrets': mapping(ref('ManagedPlatformSecretReference')),
    'placement': ref('ManagedPlatformPlacement'), 'supabase': ref('SupabaseConfig'), 'neon': ref('NeonConfig'),
}, ['schema_version','name','kind','version','resources','storage','secrets','placement'])
platform_alternatives = [
    {'properties':{'kind':{'const':'supabase'},'version':{'const':'0.8.2'},'placement':{'properties':{'node_names':{'minItems':1,'maxItems':1},'spread':{'const':''}}}},'required':['kind','version','placement','supabase'],'not':{'required':['neon']}},
    {'properties':{'kind':{'const':'neon'},'version':{'const':'fa504217c61bbcaf5c512d75830564541f917f8f'},'placement':{'properties':{'node_names':{'minItems':3}}}},'required':['kind','version','placement','neon'],'not':{'required':['supabase']},'allOf':[
        {'if':{'properties':{'neon':{'properties':{'pageservers':{'const':count}}}}},'then':{'properties':{'placement':{'properties':{'node_names':{'minItems':max(3,count)}}}}}}
        for count in range(2,9)
    ]},
]
for platform_kind, alternative in zip(['supabase','neon'], platform_alternatives):
    properties = dict(schemas['ManagedPlatformSpec']['properties'])
    properties.pop('neon' if platform_kind == 'supabase' else 'supabase')
    for name, constraint in alternative['properties'].items():
        if name == 'placement':
            placement_properties = dict(schemas['ManagedPlatformPlacement']['properties'])
            for field, bounds in constraint['properties'].items():
                placement_properties[field] = {**placement_properties[field], **bounds}
            properties[name] = obj(placement_properties, ['node_names'])
        else:
            properties[name] = {**properties[name], **constraint}
    schema_name = 'SupabasePlatformSpec' if platform_kind == 'supabase' else 'NeonPlatformSpec'
    schemas[schema_name] = obj(properties, schemas['ManagedPlatformSpec']['required'] + [platform_kind])
    if 'allOf' in alternative:
        schemas[schema_name]['allOf'] = alternative['allOf']
schemas['ManagedPlatformSpec'] = {'oneOf':[ref('SupabasePlatformSpec'),ref('NeonPlatformSpec')]}
for platform_kind in ['Supabase','Neon']:
    draft_properties = dict(schemas[platform_kind+'PlatformSpec']['properties'])
    draft_properties['placement'] = obj({'node_names':{'type':'array','items':S,'maxItems':48,'uniqueItems':True},'spread':{'type':'string','enum':['','nodes','zones']}})
    if platform_kind == 'Neon':
        draft_config = dict(schemas['NeonConfig']['properties'])
        for field in ['object_storage_url','object_storage_bucket','object_storage_region','object_storage_prefix']:
            draft_config[field] = S
        draft_properties['neon'] = obj(draft_config, schemas['NeonConfig']['required'])
    schemas[platform_kind+'PlatformDefaults'] = obj(draft_properties, schemas[platform_kind+'PlatformSpec']['required'])
schemas['ManagedPlatformDefaults'] = {'oneOf':[ref('SupabasePlatformDefaults'),ref('NeonPlatformDefaults')]}
schemas['ManagedPlatformCapability'] = obj({'available':B,'cluster_qualified':B,'public_qualified':B,'reason':S}, ['available','cluster_qualified','public_qualified','reason'])
schemas['ManagedPlatformComponent'] = obj({'name':S,'image':S,'resources':ref('ManagedPlatformResources'),'replicas':I,'ports':array(I),'secret_keys':array(S),'storage_keys':array(S)}, ['name','image','resources','replicas','ports','secret_keys','storage_keys'])
schemas['ManagedPlatformPlan'] = obj({'namespace':S,'components':array(ref('ManagedPlatformComponent')),'public_service':S,'storage_class':S,'capability':ref('ManagedPlatformCapability')}, ['namespace','components','public_service','storage_class','capability'])
schemas['ManagedPlatformCatalogNode'] = obj({'name':S,'uid':S,'architecture':S,'operating_system':S}, ['name','uid','architecture','operating_system'])
schemas['ManagedPlatformCatalogEntry'] = obj({'kind':{'type':'string','enum':['neon','supabase']},'version':S,'minimum_nodes':I,'maximum_nodes':I,'required_secret_keys':array(S),'default_spec':ref('ManagedPlatformDefaults'),'capability':ref('ManagedPlatformCapability')}, ['kind','version','minimum_nodes','maximum_nodes','required_secret_keys','default_spec','capability'])
schemas['ManagedPlatformCatalog'] = obj({'project':S,'environment':S,'storage_class':S,'nodes':{'type':'array','items':ref('ManagedPlatformCatalogNode'),'maxItems':48},'secret_references':{'type':'array','items':ref('ManagedPlatformSecretReference'),'maxItems':64},'items':{'type':'array','items':ref('ManagedPlatformCatalogEntry'),'maxItems':2}}, ['project','environment','storage_class','nodes','secret_references','items'])
schemas['ManagedPlatformReview'] = obj({'id':S,'expected_revision':I,'kind':{'type':'string','enum':['create','update','delete']},'request_hash':S,'authority_fingerprint':S,'capacity_fingerprint':S,'expires_at':T,'blocked_reasons':array(S)}, ['id','expected_revision','kind','request_hash','expires_at','blocked_reasons'])
schemas['ManagedPlatformTLSCertificate'] = obj({'component':S,'fingerprint':S,'expires_at':T,'verified':B}, ['component','fingerprint','expires_at','verified'])
schemas['ManagedPlatformTLSObservation'] = obj({'mode':{'type':'string','const':'managed'},'issuer_fingerprint':S,'certificates':{'type':'array','items':ref('ManagedPlatformTLSCertificate'),'maxItems':32},'verified_at':T}, ['mode','issuer_fingerprint','certificates','verified_at'])
schemas['ManagedPlatformMaintenanceObservation'] = obj({'status':{'type':'string','enum':['pending','failed','succeeded']},'phase':S,'message':S,'checked_at':T}, ['status','phase','message','checked_at'])
schemas['ManagedPlatformObservation'] = obj({'status':S,'phase':S,'revision':I,'namespace_uid':S,'ready_components':I,'expected_components':I,'pending':array(S),'tenant_id':S,'timeline_id':S,'safekeeper_count':I,'attached_computes':array(S),'proxy_endpoint_id':S,'proxy_generation':I,'tls':ref('ManagedPlatformTLSObservation'),'maintenance':ref('ManagedPlatformMaintenanceObservation')})
schemas['ManagedPlatform'] = obj({'id':S,'project':S,'environment':S,'revision':I,'spec':ref('ManagedPlatformSpec'),'status':S,'observation':ref('ManagedPlatformObservation'),'reserved_cpu_milli':I,'reserved_memory_bytes':I,'reserved_storage_gib':I,'created_at':T,'updated_at':T,'deleted_at':{'anyOf':[T,{'type':'null'}]}}, ['id','project','environment','revision','spec','status','observation','reserved_cpu_milli','reserved_memory_bytes','reserved_storage_gib','created_at','updated_at'])
schemas['ManagedPlatformOperation'] = obj({'id':S,'platform_id':S,'revision':I,'kind':S,'status':S,'phase':S,'message':S,'spec':ref('ManagedPlatformSpec'),'plan':ref('ManagedPlatformPlan'),'review':ref('ManagedPlatformReview'),'review_id':S,'created_at':T,'started_at':T,'finished_at':T,'attempt':I}, ['id','platform_id','revision','kind','status','phase','message','spec','plan','review','review_id','created_at','attempt'])
schemas['ManagedPlatformIntent'] = obj({'id':S,'project':S,'environment':S,'expected_revision':I,'kind':{'type':'string','enum':['create','update','delete']},'spec':ref('ManagedPlatformSpec'),'review':ref('ManagedPlatformReview'),'confirm_name':S}, ['project','environment','expected_revision','kind','spec'])
schemas['ManagedPlatformAcceptIntent'] = obj(schemas['ManagedPlatformIntent']['properties'], ['id','project','environment','expected_revision','kind','spec','review'])
schemas['ManagedPlatformReviewResponse'] = obj({'platform':ref('ManagedPlatform'),'plan':ref('ManagedPlatformPlan'),'review':{'anyOf':[ref('ManagedPlatformReview'),{'type':'null'}]},'blocked':B}, ['platform','plan','review','blocked'])
platform_id = {'type':'string','pattern':'^[0-9a-f]{32}$'}
schemas['ManagedPlatformRecoveryIntent'] = obj({
    'kind':{'type':'string','enum':['backup','restore']}, 'project':S, 'environment':S,
    'source_platform_id':platform_id, 'target_platform_id':platform_id,
    'artifact_id':platform_id, 'destination_id':platform_id,
    'destination_revision':{'type':'integer','minimum':1},
    'expected_source_revision':{'type':'integer','minimum':1},
    'expected_target_revision':{'type':'integer','minimum':1},
}, ['kind','project','environment','source_platform_id','expected_source_revision'])
schemas['ManagedPlatformRecoveryReview'] = obj({
    'id':S, 'intent':ref('ManagedPlatformRecoveryIntent'), 'request_hash':S,
    'authority_fingerprint':S, 'expires_at':T,
}, ['id','intent','request_hash','authority_fingerprint','expires_at'])
schemas['ManagedPlatformRecoveryRequest'] = {'allOf':[
    ref('ManagedPlatformRecoveryIntent'), obj({'confirm_target_name':S}),
]}
schemas['ManagedPlatformRecoveryAcceptRequest'] = {'allOf':[
    ref('ManagedPlatformRecoveryRequest'), obj({'review':ref('ManagedPlatformRecoveryReview')}, ['review']),
]}
schemas['ManagedPlatformRecoveryOperation'] = obj({
    'id':S, 'kind':{'type':'string','enum':['backup','restore']}, 'project':S,
    'environment':S, 'status':S, 'phase':S, 'message':S,
    'source_platform_id':S, 'target_platform_id':S, 'artifact_id':S,
    'result_artifact_id':S, 'destination_id':S, 'destination_revision':I,
    'expected_source_revision':I, 'expected_target_revision':I,
    'cancel_requested':B,
}, ['id','kind','project','environment','status','phase','message','source_platform_id','expected_source_revision','cancel_requested'])

route('/managed-platforms','get','listManagedPlatforms',items('ManagedPlatform'),scope=True)
route('/managed-platforms/catalog','get','getManagedPlatformCatalog',ref('ManagedPlatformCatalog'),scope=True)
paths['/managed-platforms/catalog']['get']['description'] = 'Read editable platform defaults, configured capacity node references and immutable secret references for one authorized existing environment. References contain no secret values. Configured nodes do not assert live health or physical-zone qualification. Native qualification gates still control creation.'
for parameter in paths['/managed-platforms']['get']['parameters']:
    parameter['required'] = False
paths['/managed-platforms']['get']['description'] = 'List up to 64 readable platforms across all accessible projects. Omit both scope parameters for the global list; otherwise supply both for an exact scope. Partial, empty or repeated scope parameters are rejected.'
route('/managed-platforms/{id}','get','getManagedPlatform',ref('ManagedPlatform'))
route('/managed-platforms/{id}/trust','get','getManagedPlatformPublicTrust',ref('DatabasePublicTrust'))
paths['/managed-platforms/{id}/trust']['get']['description'] = 'Read the current public certificate authority for an accessible managed platform. Returns no private keys or credentials. Missing or changed runtime ownership keeps trust unavailable.'
route('/managed-platforms/{id}/operations','get','listManagedPlatformOperations',items('ManagedPlatformOperation'))
route('/managed-platform-operations/{id}','get','getManagedPlatformOperation',ref('ManagedPlatformOperation'))
route('/managed-platforms/reviews','post','reviewManagedPlatform',ref('ManagedPlatformReviewResponse'),ref('ManagedPlatformIntent'))
route('/managed-platforms/operations','post','acceptManagedPlatform',ref('ManagedPlatformOperation'),ref('ManagedPlatformAcceptIntent'),'202',idem=True)
for action in ['/managed-platforms/reviews','/managed-platforms/operations']:
    paths[action]['post']['description'] = 'Review and accept an immutable managed-platform revision. Neon and Supabase availability remain false until native acceptance is complete. Delete remains available for an owned existing resource.'
route('/managed-platforms/{id}/recovery-operations','get','listManagedPlatformRecoveryOperations',items('ManagedPlatformRecoveryOperation'))
route('/managed-platform-recovery-operations/{id}','get','getManagedPlatformRecoveryOperation',ref('ManagedPlatformRecoveryOperation'))
route('/managed-platform-recovery/reviews','post','reviewManagedPlatformRecovery',ref('ManagedPlatformRecoveryReview'),ref('ManagedPlatformRecoveryRequest'))
route('/managed-platform-recovery/operations','post','acceptManagedPlatformRecovery',ref('ManagedPlatformRecoveryOperation'),ref('ManagedPlatformRecoveryAcceptRequest'),'202',idem=True)
route('/managed-platform-recovery-operations/{id}/cancel','post','cancelManagedPlatformRecoveryOperation',obj({'cancel_requested':B}, ['cancel_requested']),status='202')
for action in ['/managed-platforms/{id}/recovery-operations','/managed-platform-recovery-operations/{id}','/managed-platform-recovery-operations/{id}/cancel']:
    paths[action][next(iter(paths[action]))]['parameters'][0]['schema'] = platform_id
paths['/managed-platform-recovery/operations']['post']['responses']['202']['description'] = 'Accepted'
paths['/managed-platform-recovery-operations/{id}/cancel']['post']['responses']['202']['description'] = 'Accepted'
