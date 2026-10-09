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
    'loaded_matches_snapshot': {'type': ['boolean', 'null'], 'description': 'Whether the helper read the last resolved workload snapshot in the current revision. Null means the comparison could not be verified. This does not inspect unsynchronized external secret-provider values.'},
    'stages': {'type': 'array', 'items': ref('BindingTestStage'), 'maxItems': 8},
}, ['schema_version', 'application_id', 'service', 'variable', 'revision', 'observed_at', 'outcome', 'snapshot_resolved', 'loaded_matches_snapshot', 'stages'])
route('/applications/{id}/services/{service}/bindings/{variable}/test', 'post', 'testServiceBinding', ref('BindingTestResult'), ref('BindingTestInput'))
paths['/applications/{id}/services/{service}/bindings/{variable}/test']['post']['summary'] = 'Test one declared database binding from its running application container. Requires deployments:write in the application scope because the test executes a helper and authenticates to the database. The fixed helper reads the container environment and performs a bounded connection and read-only query check. TLS bindings require certificate and hostname verification; explicitly plaintext bindings report TLS as not configured. No URL, credential, command or SQL is accepted or returned.'
