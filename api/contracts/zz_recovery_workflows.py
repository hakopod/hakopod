check = obj({'code':S,'status':{'type':'string','enum':['checked','warning','unknown','blocker']},'message':S,'evidence':S},['code','status','message'])
schemas['RestoreCompatibilityCheck'] = check
schemas['RestoreCompatibilityReport'] = obj({'generated_at':T,'checks':array(ref('RestoreCompatibilityCheck')),'blocked':B},['generated_at','checks','blocked'])
related = {'type':'array','items':S,'maxItems':16,'uniqueItems':True}
schemas['BackupCompatibilityEvidence'] = obj({'application_revision':I,'runtime_fingerprint':S,'application_images':mapping(S),'encryption_recipient':S,'dependencies':array(S),'related_recovery_points':mapping(T)})
schemas['BackupArtifact']['properties']['compatibility_evidence'] = ref('BackupCompatibilityEvidence')
schemas['BackupTarget']['properties'].update({'application_images':mapping(S),'dependencies':array(S)})
schemas['BackupRestorePlan']['properties'].update({'compatibility':ref('RestoreCompatibilityReport'),'related_artifact_ids':related})
schemas['BackupRestorePlan']['required'].append('compatibility')
schemas['ManagedPlatformRecoveryReview']['properties']['compatibility'] = ref('RestoreCompatibilityReport')
for path in ['/backup-artifacts/{id}/restore-plan','/databases/{id}/restore-plan']:
    paths[path]['post']['requestBody']['content']['application/json']['schema']['properties']['related_artifact_ids'] = related

schemas['ServerCleanupItem'] = obj({'id':S,'category':S,'path':S,'bytes':I},['id','category','path','bytes'])
schemas['ServerCleanupInventory'] = obj({'schema_version':{'type':'integer','const':1},'observed_at':T,'filesystem':obj({'capacity_bytes':I,'available_bytes':I},['capacity_bytes','available_bytes']),'items':{'type':'array','items':ref('ServerCleanupItem'),'maxItems':256},'protected':array(S),'planned_bytes':I},['schema_version','observed_at','filesystem','items','protected','planned_bytes'])
schemas['ServerCleanupReview'] = obj({'id':S,'inventory':ref('ServerCleanupInventory'),'expires_at':T},['id','inventory','expires_at'])
schemas['ServerCleanupReceipt'] = obj({'schema_version':I,'operation_id':S,'status':{'type':'string','enum':['not_started','running','interrupted','succeeded']},'request_hash':S,'pending_id':S,'removed':array(obj({'id':S,'bytes':I},['id','bytes'])),'skipped':array(obj({'id':S,'reason':S},['id','reason'])),'uncertain':array(obj({'id':S,'reason':S},['id','reason'])),'planned_bytes':I,'removed_bytes':I,'available_before_bytes':I,'available_after_bytes':I,'reclaimed_bytes':I,'completed_at':T},['schema_version','operation_id','status'])
schemas['ServerCleanupOperation'] = obj({'id':S,'status':{'type':'string','enum':['running','succeeded','failed']},'receipt':ref('ServerCleanupReceipt'),'message':S},['id','status'])
route('/installation/cleanup/review','post','reviewServerCleanup',ref('ServerCleanupReview'),obj({}))
route('/installation/cleanup/execute','post','executeServerCleanup',ref('ServerCleanupOperation'),obj({'review_id':S,'confirmation':S},['review_id','confirmation']),'202',idem=True)
route('/installation/cleanup/operations/{id}','get','getServerCleanupOperation',ref('ServerCleanupOperation'),status='202')
for path in ['/installation/cleanup/execute','/installation/cleanup/operations/{id}']:
    method='post' if path.endswith('/execute') else 'get'
    paths[path][method]['responses']['200']=paths[path][method]['responses']['202']
for path in ['/installation/cleanup/review','/installation/cleanup/execute','/installation/cleanup/operations/{id}']:
    for operation in paths[path].values():
        operation['description']='Requires a self-hosted installation owner browser session. Reviews are bounded to 256 exact installer cache files and expire after ten minutes. Reuse the same retry key after interruption. GET only observes progress; POST resumes the same reviewed operation. Application and database volumes, backups, installed releases and rollback material are protected.'
