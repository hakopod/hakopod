schemas['DeploymentResumeInput'] = obj({'expected_revision': {'type':'integer','minimum':1}}, ['expected_revision'])
route('/deployments/{id}/resume','post','resumeDeployment',ref('Deployment'),ref('DeploymentResumeInput'),'202',idem=True)
schemas['Deployment']['properties'].update({'resume_generation': I, 'resume_services': array(S)})
