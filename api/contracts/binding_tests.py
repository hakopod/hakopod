"""Bounded, credential-free diagnostics from an application's existing runtime."""

schemas['BindingTestStage'] = obj({
    'name': S,
    'status': {'type': 'string', 'enum': ['passed', 'failed', 'unsupported', 'skipped']},
    'code': S,
    'message': S,
}, ['name', 'status', 'code', 'message'])
schemas['BindingTestInput'] = obj({
    'expected_revision': {'type': 'integer', 'minimum': 1},
    'pod': {'type': 'string', 'maxLength': 253, 'description': 'Optional running pod in this service. Omit to select its newest running application container.'},
}, ['expected_revision'])
schemas['BindingTestResult'] = obj({
    'schema_version': {'type': 'integer', 'const': 1},
    'application_id': S, 'service': S, 'variable': S, 'revision': I,
    'pod': S, 'pod_uid': S, 'observed_at': T,
    'outcome': {'type': 'string', 'enum': ['passed', 'failed', 'unsupported', 'unavailable', 'stale']},
    'snapshot_resolved': B,
    'loaded_matches_snapshot': {'type': ['boolean', 'null'], 'description': 'Whether the container value, generated trust and current workload template match the binding resolved for this request. Null means the comparison could not be verified.'},
    'stages': {'type': 'array', 'items': ref('BindingTestStage'), 'maxItems': 8},
}, ['schema_version', 'application_id', 'service', 'variable', 'revision', 'observed_at', 'outcome', 'snapshot_resolved', 'loaded_matches_snapshot', 'stages'])
route('/applications/{id}/services/{service}/bindings/{variable}/test', 'post', 'testServiceBinding', ref('BindingTestResult'), ref('BindingTestInput'))
paths['/applications/{id}/services/{service}/bindings/{variable}/test']['post']['summary'] = 'Test one declared database binding from its running application container. Requires deployments:write in the application scope because the test executes a helper and authenticates to the database. The fixed helper reads the container environment and performs a bounded connection and read-only query check. TLS bindings require certificate and hostname verification; explicitly plaintext bindings report TLS as not configured. No URL, credential, command or SQL is accepted or returned.'

schemas['BindingInspectionStep'] = obj({'name': {'type': 'string', 'enum': ['saved','resolved','loaded','connection']}, 'status': {'type': 'string', 'enum': ['passed','failed','unknown','stale','unsupported']}, 'message': S}, ['name','status','message'])
schemas['BindingInspection'] = obj({'schema_version': {'type':'integer','const':1}, 'application_id':S, 'service':S, 'variable':S, 'revision':I, 'observed_at':T, 'steps': {'type':'array','items':ref('BindingInspectionStep'),'minItems':4,'maxItems':4}, 'last_test':ref('BindingTestResult')}, ['schema_version','application_id','service','variable','revision','observed_at','steps'])
route('/applications/{id}/services/{service}/bindings/{variable}', 'get', 'inspectServiceBinding', ref('BindingInspection'))
paths['/applications/{id}/services/{service}/bindings/{variable}']['get']['summary'] = 'Inspect saved, resolved, loaded and authenticated binding evidence. Requires deployments:read in the application scope. This operation executes no commands. A saved test expires after five minutes. Container, revision, credential or trust changes invalidate earlier verification. The response contains no connection values or fingerprints.'
