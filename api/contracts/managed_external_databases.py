schemas['ExternalDatabaseSpec'] = obj({
    'schema_version': {'type': 'integer', 'const': 1},
    'name': {'type': 'string', 'pattern': '^[a-z]([a-z0-9-]{0,38}[a-z0-9])?$'},
    'provider': {'type': 'string', 'enum': ['planetscale']},
    'engine': {'type': 'string', 'enum': ['mysql', 'postgresql']},
    'host': S, 'port': I, 'database': S,
}, ['schema_version', 'name', 'provider', 'engine', 'host', 'port', 'database'])
schemas['ExternalDatabaseCredentials'] = obj({
    'username': {'type': 'string', 'minLength': 1, 'maxLength': 256, 'writeOnly': True},
    'password': {'type': 'string', 'minLength': 1, 'maxLength': 4096, 'writeOnly': True},
}, ['username', 'password'])
schemas['ExternalDatabaseObservation'] = obj({
    'observed_at': T, 'revision': I, 'status': S, 'message': S,
    'tls_verified': B, 'query_verified': B, 'latency_ms': I,
    'verified_ips': array(S),
}, ['observed_at', 'revision', 'status', 'message', 'tls_verified', 'query_verified'])
schemas['ExternalDatabase'] = obj({
    'id': S, 'project': S, 'environment': S, 'revision': I, 'credential_revision': I,
    'spec': ref('ExternalDatabaseSpec'), 'status': S,
    'observation': ref('ExternalDatabaseObservation'),
    'created_at': T, 'updated_at': T, 'deleted_at': T,
}, ['id', 'project', 'environment', 'revision', 'credential_revision', 'spec', 'status', 'observation', 'created_at', 'updated_at'])
schemas['ExternalDatabaseOperation'] = obj({
    'id': S, 'database_id': S, 'revision': I, 'kind': S, 'status': S, 'message': S,
    'spec': ref('ExternalDatabaseSpec'), 'created_at': T, 'finished_at': T,
}, ['id', 'database_id', 'revision', 'kind', 'status', 'message', 'spec', 'created_at'])
schemas['ExternalDatabaseTrust'] = obj({
    'mode': S, 'hostname': S, 'minimum_protocol': S, 'trust_source': S,
    'verified': B, 'observed_at': T, 'message': S,
}, ['mode', 'hostname', 'minimum_protocol', 'trust_source', 'verified', 'observed_at', 'message'])
schemas['ServiceBinding']['properties'].update({'external_database': S, 'external_database_revision': I})
schemas['ExternalDatabaseConnectionPlan'] = obj({
    'id': S, 'kind': {'type': 'string', 'enum': ['refresh', 'disconnect']}, 'database_id': S, 'database_name': S, 'database_revision': I,
    'credential_revision': I, 'application_id': S, 'application_name': S,
    'application_revision': I, 'service': S, 'variable': S,
    'binding': ref('ServiceBinding'), 'expires_at': T, 'warnings': array(S),
}, ['id', 'kind', 'database_id', 'database_name', 'database_revision', 'credential_revision', 'application_id', 'application_name', 'application_revision', 'service', 'variable', 'expires_at', 'warnings'])
route('/external-databases', 'get', 'listExternalDatabases', items('ExternalDatabase'), scope=True)
route('/external-databases/{id}', 'get', 'getExternalDatabase', ref('ExternalDatabase'))
route('/external-databases/{id}', 'put', 'rotateExternalDatabaseCredentials', ref('ExternalDatabaseOperation'), obj({
    'spec': ref('ExternalDatabaseSpec'), 'credentials': ref('ExternalDatabaseCredentials'), 'expected_revision': I, 'confirm_name': S,
}, ['spec', 'credentials', 'expected_revision', 'confirm_name']), '202', idem=True)
route('/external-databases/{id}', 'delete', 'deleteExternalDatabase', ref('ExternalDatabaseOperation'), obj({
    'expected_revision': I, 'confirm_name': S,
}, ['expected_revision', 'confirm_name']), '202', idem=True)
route('/external-databases/{id}/connections', 'get', 'listExternalDatabaseConnections', ref('DatabaseConnections'))
route('/external-databases/{id}/trust', 'get', 'getExternalDatabaseTrust', ref('ExternalDatabaseTrust'))
route('/external-databases/{id}/connection-plan', 'post', 'reviewLegacyExternalDatabaseBinding', ref('ExternalDatabaseConnectionPlan'), obj({
    'application_id': S, 'service': S, 'variable': S, 'disconnect': B,
}, ['application_id', 'service', 'variable', 'disconnect']), '200')
route('/external-databases/{id}/connect', 'post', 'applyLegacyExternalDatabaseBindingReview', ref('Deployment'), obj({
    'review_id': S, 'confirm_application': S,
}, ['review_id', 'confirm_application']), '202', idem=True)
route('/external-database-operations/{id}', 'get', 'getExternalDatabaseOperation', ref('ExternalDatabaseOperation'))
