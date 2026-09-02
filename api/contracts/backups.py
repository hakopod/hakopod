secret = {'type': 'string', 'writeOnly': True}
schemas['BackupSource'] = obj({'kind': {'type':'string','enum':['database','management']}, 'application_id':S, 'service':S, 'engine':{'type':'string','enum':['postgresql','mysql']}, 'database':S}, ['kind','engine'])
schemas['BackupDestination'] = obj({'id':S,'name':S,'endpoint':S,'region':S,'bucket':S,'prefix':S,'path_style':B,'allow_http':B,'revision':I,'credential_ref':S,'encryption_recipient':S,'created_at':T,'updated_at':T}, ['id','name','endpoint','region','bucket','prefix','path_style','allow_http','revision','credential_ref','encryption_recipient','created_at','updated_at'])
schemas['BackupDestinationInput'] = obj({'name':S,'endpoint':S,'region':S,'bucket':S,'prefix':S,'path_style':B,'allow_http':B,'expected_revision':I,'access_key_id':secret,'secret_access_key':secret,'session_token':secret,'encryption_identity':secret}, ['name','endpoint','region','bucket','prefix','path_style','allow_http','expected_revision'])
schemas['BackupDestinationCreated'] = obj({'destination':ref('BackupDestination'),'recovery_key':S},['destination'])
schemas['BackupTarget'] = obj({**schemas['BackupSource']['properties'],'application_name':S,'revision':I,'pod':S,'pod_uid':S,'available':B,'message':S},['kind','engine','revision','available'])
schemas['BackupArtifact'] = obj({'id':S,'job_id':S,'destination_id':S,'source':ref('BackupSource'),'object_key':S,'sha256':S,'bytes':I,'format':S,'scope':S,'schedule_id':S,'created_at':T,'deleted_at':T},['id','job_id','destination_id','source','object_key','sha256','bytes','format','scope','created_at'])
schemas['BackupArtifact']['properties']['deletion_pending'] = B
schemas['BackupJob'] = obj({'id':S,'kind':{'type':'string','enum':['backup','restore']},'status':{'type':'string','enum':['queued','running','succeeded','failed','cancelled']},'destination_id':S,'source':ref('BackupSource'),'target':ref('BackupTarget'),'artifact_id':S,'schedule_id':S,'error':S,'bytes':I,'cancel_requested':B,'created_at':T,'started_at':T,'finished_at':T},['id','kind','status','destination_id','source','error','bytes','cancel_requested','created_at'])
schemas['BackupSchedule'] = obj({'id':S,'name':S,'destination_id':S,'source':ref('BackupSource'),'interval_hours':I,'retention_count':I,'enabled':B,'revision':I,'next_run_at':T,'created_at':T,'updated_at':T},['id','name','destination_id','source','interval_hours','retention_count','enabled','revision','next_run_at','created_at','updated_at'])
schemas['BackupScheduleInput'] = obj({'name':S,'destination_id':S,'source':ref('BackupSource'),'interval_hours':{'type':'integer','minimum':1,'maximum':8760},'retention_count':{'type':'integer','minimum':1,'maximum':100},'enabled':B,'expected_revision':I},['name','destination_id','source','interval_hours','retention_count','enabled','expected_revision'])
schemas['BackupRestorePlan'] = obj({'id':S,'artifact_id':S,'target':ref('BackupTarget'),'confirmation':S,'scope':S,'warnings':array(S),'expires_at':T},['id','artifact_id','target','confirmation','scope','warnings','expires_at'])
route('/backup-destinations','get','listBackupDestinations',items('BackupDestination'))
route('/backup-destinations','post','createBackupDestination',ref('BackupDestinationCreated'),ref('BackupDestinationInput'),'201')
route('/backup-destinations/{id}','put','updateBackupDestination',ref('BackupDestination'),ref('BackupDestinationInput'))
route('/backup-destinations/{id}','delete','deleteBackupDestination',obj({'deleted':B},['deleted']),obj({'expected_revision':I},['expected_revision']))
route('/backup-destinations/{id}/test','post','testBackupDestination',obj({'ok':B,'bytes':I,'message':S},['ok','bytes','message']),obj({}))
route('/backup-targets','get','listBackupTargets',obj({'items':array(ref('BackupTarget')),'truncated':B},['items','truncated']))
route('/backups','get','listBackups',obj({'items':array(ref('BackupJob')),'next_cursor':S},['items']))
route('/backups','post','createBackup',ref('BackupJob'),obj({'destination_id':S,'source':ref('BackupSource')},['destination_id','source']),'202',idem=True)
route('/backups/{id}','get','getBackup',ref('BackupJob'))
route('/backups/{id}/cancel','post','cancelBackup',ref('BackupJob'),obj({}),'202')
route('/backup-artifacts','get','listBackupArtifacts',obj({'items':array(ref('BackupArtifact')),'next_cursor':S},['items']))
route('/backup-artifacts/{id}','get','getBackupArtifact',ref('BackupArtifact'))
route('/backup-artifacts/{id}','delete','deleteBackupArtifact',obj({'deleted':B},['deleted']),obj({'confirmation':S},['confirmation']))
route('/backup-artifacts/{id}/restore-plan','post','planBackupRestore',ref('BackupRestorePlan'),obj({'application_id':S,'service':S},['application_id','service']))
route('/backup-artifacts/{id}/restore','post','restoreBackup',ref('BackupJob'),obj({'plan_id':S,'confirmation':S},['plan_id','confirmation']),'202',idem=True)
route('/backup-schedules','get','listBackupSchedules',items('BackupSchedule'))
route('/backup-schedules','post','createBackupSchedule',ref('BackupSchedule'),ref('BackupScheduleInput'),'201')
route('/backup-schedules/{id}','put','updateBackupSchedule',ref('BackupSchedule'),ref('BackupScheduleInput'))
route('/backup-schedules/{id}','delete','deleteBackupSchedule',obj({'deleted':B},['deleted']),obj({'expected_revision':I},['expected_revision']))
for path in ['/backups','/backup-artifacts']:
    paths[path]['get']['parameters'] += [{'name':'cursor','in':'query','schema':S},{'name':'destination_id','in':'query','schema':S}]
for path, methods in paths.items():
    if path.startswith(('/backup-', '/backups')):
        for operation in methods.values():
            operation['description'] = 'Administrator only. Lists are bounded: 32 destinations, 64 schedules, 100 jobs/artifacts per cursor page and 128 discovered targets. Destination storage location and encryption identity are immutable; PUT rotates credentials/name with expected_revision. Recovery keys are returned once on destination creation and must be saved separately. Restore always creates a fresh database and never overwrites an existing database.'
