schemas['SourceImportInput']=obj({'project':S,'environment':S,'provider':{'type':'string','enum':['github','gitlab']},'repository':S,'branch':S,'path':S,'auto_deploy':B},['project','environment','provider','repository','branch','path','auto_deploy'])
schemas['SourceImportPlan']=obj({**schemas['Plan']['properties'],'source':ref('SourceImportInput'),'commit_sha':S,'review_token':S,'expires_at':T},[*schemas['Plan']['required'],'source','commit_sha','review_token','expires_at'])
route('/sources/plan','post','planSourceImport',ref('SourceImportPlan'),ref('SourceImportInput'))
route('/sources/deploy','post','deploySourceImport',ref('Deployment'),obj({'review_token':S},['review_token']),'202',idem=True)
