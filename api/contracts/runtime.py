N = {'type': 'number'}
schemas['TLSConfig'] = obj({'certificate':S,'issuer':S})
schemas['Service']['properties'].update({'restart_nonce':S,'registry_credential':S,'tls':ref('TLSConfig')})
schemas['RuntimeMetrics'] = obj({'available':B,'reason':S,'cpu_millicores':N,'memory_bytes':I,'sampled_at':T,'window_seconds':N,'pods_sampled':I,'pods_expected':I},['available','pods_sampled','pods_expected'])
schemas['RuntimeCondition'] = obj({'type':S,'status':S,'reason':S,'message':S,'last_transition_time':T},['type','status','last_transition_time'])
schemas['RuntimeAllocation'] = obj({'cpu':S,'memory':S},['cpu','memory'])
schemas['RuntimeContainer'] = obj({'name':S,'ready':B,'restarts':I,'state':S,'reason':S,'message':S,'image':S,'image_id':S,'resources':obj({'requests':ref('RuntimeAllocation'),'limits':ref('RuntimeAllocation')},['requests','limits']),'cpu_millicores':N,'memory_bytes':I},['name','ready','restarts','state','image','resources'])
schemas['RuntimeEvent'] = obj({'type':S,'reason':S,'message':S,'count':I,'last_seen':T},['type','reason','message','count','last_seen'])
schemas['RuntimePod'] = obj({'name':S,'phase':S,'ready':B,'node_name':S,'pod_ip':S,'created_at':T,'conditions':array(ref('RuntimeCondition')),'containers':array(ref('RuntimeContainer')),'events':array(ref('RuntimeEvent'))},['name','phase','ready','node_name','pod_ip','created_at','conditions','containers','events'])
schemas['ServiceRuntime'] = obj({'application_id':S,'service':S,'observed_at':T,'metrics':ref('RuntimeMetrics'),'pods':array(ref('RuntimePod')),'truncated':B},['application_id','service','observed_at','metrics','pods','truncated'])
route('/applications/{id}/services/{service}/runtime','get','getServiceRuntime',ref('ServiceRuntime'))
route('/applications/{id}/services/{service}/restart','post','restartService',ref('Deployment'),obj({'expected_revision':I},['expected_revision']),'202',idem=True)
route('/applications/{id}/services/{service}/scale','post','scaleService',ref('Deployment'),obj({'expected_revision':I,'replicas':I},['expected_revision','replicas']),'202',idem=True)
schemas['RegistryInfo'] = obj({'name':S,'project':S,'environment':S,'registry':S,'token_realm':S,'revision':I,'created_at':T,'updated_at':T,'synchronized':B,'message':S},['name','project','environment','registry','revision','created_at','updated_at','synchronized'])
schemas['RegistryInput'] = obj({'name':S,'project':S,'environment':S,'registry':S,'username':S,'password':{'type':'string','writeOnly':True},'token_realm':S,'expected_revision':I},['project','environment','registry','username','password'])
route('/registries','get','listRegistries',items('RegistryInfo'),scope=True)
route('/registries','post','createRegistry',ref('RegistryInfo'),ref('RegistryInput'),'201')
route('/registries/{name}','put','updateRegistry',ref('RegistryInfo'),ref('RegistryInput'))
route('/registries/{name}','delete','deleteRegistry',obj({'deleted':B,'cleanup_pending':B},['deleted','cleanup_pending']),obj({'project':S,'environment':S,'expected_revision':I},['project','environment','expected_revision']))
route('/registries/{name}/sync','post','syncRegistry',ref('RegistryInfo'),scope=True)
schemas['TLSStatus'] = obj({'hostname':S,'enabled':B,'ready':B,'source':S,'secret_name':S,'issuer':S,'not_before':T,'expires_at':T,'message':S},['hostname','enabled','ready','source'])
schemas['TLSIssuer'] = obj({'name':S,'email':S,'server':S,'ready':B,'conditions':array(ref('RuntimeCondition'))},['name','email','server','ready','conditions'])
schemas['TLSIssuers'] = obj({'installed':B,'items':array(ref('TLSIssuer')),'message':S},['installed','items'])
route('/applications/{id}/services/{service}/tls','get','getServiceTLS',ref('TLSStatus'))
route('/applications/{id}/services/{service}/tls','post','attachServiceTLS',ref('Deployment'),obj({'expected_revision':I,'certificate_pem':S,'private_key_pem':{'type':'string','writeOnly':True},'issuer':S},['expected_revision']),'202',idem=True)
route('/tls/issuers','get','listTLSIssuers',ref('TLSIssuers'))
route('/tls/issuers','post','createTLSIssuer',ref('TLSIssuer'),obj({'name':S,'email':S,'production':B},['name','email']),'201')
schemas['NodeMetrics'] = obj({'available':B,'reason':S,'cpu_millicores':N,'memory_bytes':I,'sampled_at':T},['available'])
schemas['Node']['properties'].update({'resource_version':S,'control_plane':B,'metrics':ref('NodeMetrics'),'allocatable_gpu':I})
schemas['Node']['required'] += ['resource_version','control_plane','metrics','allocatable_gpu']
schemas['NodeDrain'] = obj({'node':S,'resource_version':S,'cordoned':B,'complete':B,'evicted':array(S),'blockers':array(S),'remaining':I},['node','resource_version','cordoned','complete','evicted','blockers','remaining'])
schemas['Enrollment'] = obj({'id':S,'created_at':T,'expires_at':T,'expired':B,'token':S,'server':S},['id','created_at','expires_at','expired'])
schemas['Enrollments'] = obj({'configured':B,'server':S,'message':S,'items':array(ref('Enrollment'))},['configured','items'])
route('/nodes/{name}/cordon','post','cordonNode',ref('NodeDrain'),obj({'expected_resource_version':S,'unschedulable':B},['expected_resource_version','unschedulable']))
route('/nodes/{name}/drain','post','drainNode',ref('NodeDrain'),obj({'expected_resource_version':S},['expected_resource_version']))
route('/nodes/enrollments','get','listNodeEnrollments',ref('Enrollments'))
route('/nodes/enrollments','post','createNodeEnrollment',ref('Enrollment'),obj({'ttl_minutes':I}),'201')
route('/nodes/enrollments/{id}','delete','revokeNodeEnrollment',obj({'revoked':B},['revoked']))
for path in ['/nodes/{name}/cordon','/nodes/{name}/drain']:
    for operation in paths[path].values():
        operation['parameters'].append({'name':'name','in':'path','required':True,'schema':S})
for path in paths:
    if path.startswith('/registries/'):
        for operation in paths[path].values():
            operation['parameters'].append({'name':'name','in':'path','required':True,'schema':S})
for path in paths:
    if '/services/{service}/' in path:
        for operation in paths[path].values():
            operation['parameters'].append({'name':'service','in':'path','required':True,'schema':S})

schemas["Service"]["properties"]["suspended"] = B
for action in ("stop", "resume"):
 route("/applications/{id}/services/{service}/"+action,"post",action+"Service",ref("Deployment"),obj({"expected_revision":I},["expected_revision"]),"202",idem=True)
