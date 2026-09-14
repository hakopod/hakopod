"""Named source credentials, separate from user sign-in providers."""
schemas['GitConnectionCapabilities']=obj({'read_source':B,'builds':B},['read_source','builds'])
schemas['GitConnection']=obj({'id':S,'name':S,'provider':{'type':'string','enum':['github','gitlab']},'auth_kind':{'type':'string','enum':['token','github_app','gitlab_oauth']},'revision':I,'enabled':B,'account':S,'subject_id':I,'github_app_id':I,'installation_id':I,'oauth_client_id':S,'oauth_scopes':S,'legacy':B,'configured':B,'token_configured':B,'private_repositories':B,'webhook_path':S,'status':S,'capabilities':ref('GitConnectionCapabilities'),'updated_at':T,'webhook_secret':S},['id','name','provider','auth_kind','revision','enabled','account','subject_id','github_app_id','installation_id','legacy','configured','token_configured','private_repositories','webhook_path','status','capabilities','updated_at'])
schemas['GitConnectionInput']=obj({'name':S,'provider':{'type':'string','enum':['github','gitlab']},'auth_kind':{'type':'string','enum':['token','github_app','gitlab_oauth']},'expected_revision':I,'enabled':B,'token':S,'webhook_secret':S,'github_app_id':S,'github_private_key':S,'github_installation_id':I,'oauth_client_id':S,'oauth_client_secret':S,'oauth_scopes':{'type':'string','enum':['api','read_api']}},['name','provider','auth_kind'])
route('/git/connections','get','listGitConnections',items('GitConnection'))
route('/git/connections','post','createGitConnection',ref('GitConnection'),ref('GitConnectionInput'),'201')
route('/git/connections/{id}','get','getGitConnection',ref('GitConnection'))
route('/git/connections/{id}','put','updateGitConnection',ref('GitConnection'),ref('GitConnectionInput'))
route('/git/connections/{id}','delete','deleteGitConnection',obj({'status':S},['status']))
paths['/git/connections/{id}']['delete']['parameters'].append({'name':'expected_revision','in':'query','required':True,'schema':I})
for endpoint,param,operation in [('/webhooks/git/{connection}','connection','namedGitWebhook'),('/webhooks/github-app/{app}','app','gitHubAppWebhook')]:
    route(endpoint,'post',operation,obj({'accepted':B,'ignored':B}),obj({}),'202')
    paths[endpoint]['post']['security']=[]
    paths[endpoint]['post']['parameters'].append({'name':param,'in':'path','required':True,'schema':S})

schemas['GitOAuthStart']=obj({'authorization_url':S,'callback_url':S,'expires_at':T},['authorization_url','callback_url','expires_at'])
schemas['GitOAuthComplete']=obj({'code':S,'state':S},['code','state'])
route('/git/connections/{id}/authorize','post','startGitSourceOAuth',ref('GitOAuthStart'),obj({}))
route('/git/connections/{id}/oauth/complete','post','completeNamedGitSourceOAuth',ref('GitConnection'),ref('GitOAuthComplete'))
route('/git/oauth/complete','post','completeGitSourceOAuth',ref('GitConnection'),ref('GitOAuthComplete'))

schemas['GitConnection']['properties']['managed_app']=B
schemas['GitAppSetup']=obj({'connection_id':S,'phase':{'type':'string','enum':['manifest','install']},'action_url':S,'manifest':S,'expires_at':T},['connection_id','phase','action_url','expires_at'])
route('/git/github/start','post','startGitHubAppManifest',ref('GitAppSetup'),obj({'name':S,'organization':S,'builds':B},['name','builds']))
route('/git/github/complete','post','completeGitHubAppManifest',ref('GitAppSetup'),ref('GitOAuthComplete'))
route('/git/connections/{id}/github/setup','post','resumeGitHubAppSetup',ref('GitAppSetup'),obj({}))
route('/git/github/install/complete','post','completeGitHubAppInstallation',ref('GitConnection'),obj({'state':S,'installation_id':I},['state','installation_id']))

schemas['GitProviderSetup']=obj({'github_available':B,'public_url':S,'gitlab_callback_url':S,'github_notice':S,'gitlab_notice':S},['github_available','public_url','gitlab_callback_url'])
route('/git/setup','get','getGitProviderSetup',ref('GitProviderSetup'))
