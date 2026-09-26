schemas['Spec']['properties']['domains']=mapping(S)
schemas['Domain'] = obj({'hostname':S,'service':S,'active':B,'verified':B,'verification_name':S,'verification_value':S,'verified_at':T,'target':S},['hostname','service','active','verified','verification_name','verification_value','target'])
route('/applications/{id}/domains','get','listApplicationDomains',obj({'items':array(ref('Domain')),'expected_revision':I},['items','expected_revision']))
route('/applications/{id}/domains','post','beginDomainVerification',ref('Domain'),obj({'hostname':S,'service':S},['hostname','service']),'201')
route('/applications/{id}/domains/{hostname}/verify','post','verifyApplicationDomain',ref('Domain'),obj({}))
paths['/applications/{id}/domains/{hostname}/verify']['post']['parameters'].append({'name':'hostname','in':'path','required':True,'schema':S})
route('/applications/{id}/domains/dns-providers','get','listApplicationDNSProviders',obj({'items':array(ref('DNSProviderSummary'))},['items']))
# One 200 carries a per-hostname outcome, including failures: no transaction spans a
# third party, so a request that created eight records and was refused on the ninth
# has to be able to say exactly that.
route('/applications/{id}/domains/dns-records','post','createApplicationDNSRecords',obj({'results':array(ref('DNSRecordResult'))},['results']),ref('DNSRecordsInput'),'200',idem=True)
