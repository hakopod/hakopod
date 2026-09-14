"""Owner-only maintenance on self-hosted installations."""
schemas['InstallationOperation'] = obj({'status':S,'message':S,'version':S,'previous_version':S},['status','message','version'])
schemas['InstallationStatus'] = obj({'current_version':S,'latest_version':S,'update_available':B,'checked_at':S,'check_error':S,'operation':ref('InstallationOperation')},['current_version','latest_version','update_available','checked_at','check_error','operation'])
schemas['InstallationSetup'] = obj({'readiness_image':S,'storage_classes':array(S),'default_storage_class':S},['readiness_image','storage_classes','default_storage_class'])
schemas['InstallationLogs'] = obj({'entries':array(obj({'timestamp':S,'message':S},['timestamp','message'])),'observed_at':T,'source':S,'started_at':T},['entries','observed_at'])
for path,operation,response in [('status','getInstallationStatus','InstallationStatus'),('logs','getInstallationLogs','InstallationLogs'),('setup','getInstallationSetup','InstallationSetup')]:
    route('/installation/'+path,'get',operation,ref(response))
route('/installation/upgrade','post','upgradeInstallation',obj({'accepted':B},['accepted']),obj({'version':S,'confirmation':S},['version','confirmation']),'202')

route('/applications/{id}/domains/{hostname}','delete','discardDomainVerification',obj({'discarded':B},['discarded']),obj({'expected_revision':I},['expected_revision']))
paths['/applications/{id}/domains/{hostname}']['delete']['parameters'].append({'name':'hostname','in':'path','required':True,'schema':S})

schemas['InstallationLogQuery'] = obj({'query':S,'since_seconds':I,'limit':I},[])
schemas['InstallationLogQueryResult'] = {'allOf':[ref('LogQueryResult'),obj({'source':S,'observed_at':T,'started_at':T},['source','observed_at'])]}
route('/installation/logs/query','post','queryInstallationLogs',ref('InstallationLogQueryResult'),ref('InstallationLogQuery'))
