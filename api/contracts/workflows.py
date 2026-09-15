# Shared engine workflow contracts.
schemas['FrameworkPlan'] = {'properties': {'build_command': {'maxLength': 1024, 'type': 'string'},
                'framework': {'enum': ['astro',
                                       'nextjs',
                                       'sveltekit',
                                       'tanstack-start',
                                       'vite',
                                       'node',
                                       'static'],
                              'type': 'string'},
                'install_command': {'maxLength': 1024, 'type': 'string'},
                'output_directory': {'maxLength': 200, 'type': 'string'},
                'package_manager': {'enum': ['npm', 'pnpm', 'yarn', 'bun', 'none'], 'type': 'string'},
                'port': {'maximum': 65535, 'minimum': 1, 'type': 'integer'},
                'runtime': {'enum': ['node', 'static'], 'type': 'string'},
                'start_command': {'maxLength': 1024, 'type': 'string'}},
 'required': ['framework', 'runtime', 'package_manager', 'install_command', 'build_command', 'port'],
 'type': 'object'}
schemas['BuildDetection'] = {'properties': {'commit_sha': {'type': 'string'},
                'dockerfile': {'type': 'string'},
                'dockerfile_content': {'type': 'string'},
                'framework': {'$ref': '#/components/schemas/FrameworkPlan'},
                'mode': {'enum': ['dockerfile', 'framework'], 'type': 'string'},
                'warnings': {'items': {'type': 'string'}, 'type': 'array'}},
 'required': ['mode', 'commit_sha', 'warnings'],
 'type': 'object'}
schemas['JobSchedule'] = {'properties': {'cron': {'description': 'Five cron fields: minute hour day month weekday.', 'type': 'string'},
                'history_limit': {'default': 1, 'maximum': 2, 'minimum': 1, 'type': 'integer'},
                'timezone': {'default': 'UTC', 'type': 'string'}},
 'required': ['cron'],
 'type': 'object'}
schemas['JobRun'] = {'properties': {'created_at': {'format': 'date-time', 'type': 'string'},
                'finished_at': {'format': 'date-time', 'type': 'string'},
                'name': {'type': 'string'},
                'revision': {'type': 'integer'},
                'status': {'type': 'string'}},
 'required': ['name', 'status', 'revision', 'created_at'],
 'type': 'object'}
schemas['RecoveryPolicy'] = {'properties': {'on_failure': {'enum': ['safe', 'disabled'], 'type': 'string'}},
 'required': ['on_failure'],
 'type': 'object'}
schemas['BuildConfig']["properties"].update({'build_secrets': {'additionalProperties': {'type': 'string'},
                   'description': 'BuildKit mount IDs mapped to GitHub Actions secrets or GitLab CI variable '
                                  'names; never secret values.',
                   'maxProperties': 16,
                   'type': 'object'},
 'framework': {'$ref': '#/components/schemas/FrameworkPlan'},
 'mode': {'enum': ['dockerfile', 'buildpacks', 'framework'], 'type': 'string'}})
schemas['BuildInput']["properties"].update({'build_secrets': {'additionalProperties': {'type': 'string'},
                   'description': 'BuildKit mount IDs mapped to GitHub Actions secrets or GitLab CI variable '
                                  'names; never secret values.',
                   'maxProperties': 16,
                   'type': 'object'},
 'framework': {'$ref': '#/components/schemas/FrameworkPlan'},
 'mode': {'enum': ['dockerfile', 'buildpacks', 'framework'], 'type': 'string'}})
schemas['DeploymentJob']["properties"].update({'schedule': {'$ref': '#/components/schemas/JobSchedule'}})
schemas['ServiceStatus']["properties"].update({'job_runs': {'items': {'$ref': '#/components/schemas/JobRun'}, 'type': 'array'}})
schemas['Spec']["properties"].update({'recovery': {'$ref': '#/components/schemas/RecoveryPolicy'}})
schemas['Deployment']["properties"].update({'recovery_error': {'type': 'string'},
 'recovery_revision': {'type': 'integer'},
 'recovery_spec': {'$ref': '#/components/schemas/Spec'},
 'recovery_state': {'enum': ['', 'running', 'succeeded', 'failed', 'skipped'], 'type': 'string'}})
paths["/builds/detect"] = {'post': {'operationId': 'detectBuildFramework',
          'requestBody': {'content': {'application/json': {'schema': {'$ref': '#/components/schemas/BuildInput'}}},
                          'required': True},
          'responses': {'200': {'content': {'application/json': {'schema': {'$ref': '#/components/schemas/BuildDetection'}}},
                                'description': 'Reviewable suggestions from repository metadata; no '
                                               'repository code is executed.'}}}}

schemas['Preview'] = obj({'id':S,'parent_id':S,'application_id':{'anyOf':[S,{'type':'null'}]},'project':S,'environment':S,'name':S,'branch':S,'state':{'type':'string','enum':['active','deleting','deleted']},'created_at':T,'expires_at':T,'deleted_at':{'anyOf':[T,{'type':'null'}]},'cleanup_error':S},['id','parent_id','application_id','project','environment','name','branch','state','created_at','expires_at','deleted_at'])
schemas['PreviewInput'] = obj({'name':S,'branch':S,'ttl_hours':{'type':'integer','minimum':1,'maximum':72},'expected_parent_revision':I,'discard_on_expiry':B,'toml':S,'spec':ref('Spec')},['name','ttl_hours','expected_parent_revision','discard_on_expiry'])
schemas['PreviewCreated'] = obj({'preview':ref('Preview'),'deployment':ref('Deployment')},['preview','deployment'])
route('/applications/{id}/previews','get','listApplicationPreviews',obj({'items':array(ref('Preview')),'next_cursor':S},['items','next_cursor']))
paths['/applications/{id}/previews']['get']['parameters'].append({'name':'cursor','in':'query','schema':S})
route('/applications/{id}/previews','post','createPreview',ref('PreviewCreated'),ref('PreviewInput'),'202',idem=True)
route('/previews/{preview}','get','getPreview',ref('Preview'))
route('/previews/{preview}','delete','deletePreview',obj({'status':S},['status']),obj({'confirmation':S},['confirmation']),'202')
for method in ['get','delete']:
 paths['/previews/{preview}'][method]['parameters'].append({'name':'preview','in':'path','required':True,'schema':S})

schemas['Principal']['properties']['can_manage_previews'] = B

for name in ["BuildConfig", "BuildInput"]:
    schemas[name]["properties"]["secrets"] = mapping(ref("SecretRef"))
    schemas[name]["properties"]["reuse_services"] = {
        "type": "array", "maxItems": 19, "uniqueItems": True, "items": S,
        "description": "Additional existing services in the linked application that receive the same verified image. Their commands, variables, ports and volumes are retained.",
    }
