schemas['ManagedDatabaseSpec'] = obj({
    'schema_version': {'type': 'integer', 'const': 1}, 'name': S,
    'engine': {'type': 'string', 'enum': ['postgresql', 'redis']},
    'version': S, 'mode': {'type': 'string', 'enum': ['standalone', 'cluster']},
    'replicas': I, 'shards': I, 'cpu': S, 'memory': S, 'storage_gib': I,
}, ['schema_version', 'name', 'engine', 'version', 'mode', 'replicas', 'shards', 'cpu', 'memory', 'storage_gib'])
schemas['DatabaseMember'] = obj({'name': S, 'uid': S, 'role': S, 'shard': S, 'ready': B, 'node': S}, ['name', 'uid', 'role', 'ready'])
schemas['DatabaseEndpoint'] = obj({'purpose': S, 'host': S, 'port': I}, ['purpose', 'host', 'port'])
schemas['DatabaseObservation'] = obj({'observed_at': T, 'revision': I, 'status': S, 'message': S, 'members': array(ref('DatabaseMember')), 'endpoints': array(ref('DatabaseEndpoint')), 'primary': S, 'slots_assigned': I, 'slots_healthy': B, 'topology_fingerprint': S}, ['observed_at', 'revision', 'status', 'message', 'members', 'endpoints', 'slots_healthy'])
schemas['DatabaseRecovery'] = obj({'artifact_id': S, 'job_id': S, 'source_id': S, 'source_revision': I, 'captured_at': T, 'restored_at': T, 'inspected_at': T}, ['artifact_id', 'job_id'])
schemas['ManagedDatabase'] = obj({'id': S, 'project': S, 'environment': S, 'revision': I, 'spec': ref('ManagedDatabaseSpec'), 'status': S, 'observation': ref('DatabaseObservation'), 'recovery': ref('DatabaseRecovery'), 'created_at': T, 'updated_at': T, 'deleted_at': T}, ['id', 'project', 'environment', 'revision', 'spec', 'status', 'observation', 'created_at', 'updated_at'])
schemas['DatabaseBackupEvidence'] = obj({'artifact_id': S, 'database_id': S, 'revision': I, 'captured_at': T, 'verified_at': T, 'sha256': S}, ['artifact_id', 'database_id', 'revision', 'captured_at', 'verified_at', 'sha256'])
schemas['DatabaseResizePlan'] = obj({'current': ref('ManagedDatabaseSpec'), 'proposed': ref('ManagedDatabaseSpec'), 'expected_revision': I, 'topology_fingerprint': S, 'backup': ref('DatabaseBackupEvidence'), 'blocked_reasons': array(S), 'warnings': array(S), 'expires_at': T}, ['current', 'proposed', 'expected_revision', 'topology_fingerprint', 'blocked_reasons', 'warnings', 'expires_at'])
schemas['DatabaseOperation'] = obj({'id': S, 'database_id': S, 'revision': I, 'kind': S, 'status': S, 'phase': S, 'message': S, 'spec': ref('ManagedDatabaseSpec'), 'review': ref('DatabaseResizePlan'), 'created_at': T, 'started_at': T, 'finished_at': T}, ['id', 'database_id', 'revision', 'kind', 'status', 'phase', 'message', 'spec', 'created_at'])
route('/databases', 'get', 'listManagedDatabases', items('ManagedDatabase'), scope=True)
route('/databases', 'post', 'createManagedDatabase', ref('DatabaseOperation'), obj({'project': S, 'environment': S, 'spec': ref('ManagedDatabaseSpec')}, ['project', 'environment', 'spec']), '202', idem=True)
route('/databases/{id}', 'get', 'getManagedDatabase', ref('ManagedDatabase'))
route('/databases/{id}', 'delete', 'deleteManagedDatabase', ref('DatabaseOperation'), obj({'expected_revision': I, 'confirm_name': S}, ['expected_revision', 'confirm_name']), '202', idem=True)
route('/databases/{id}/operations', 'get', 'listDatabaseOperations', items('DatabaseOperation'))
route('/databases/{id}/credentials', 'post', 'revealDatabaseCredentials', obj({'username': S, 'password': S, 'database': S}, ['username', 'password', 'database']), obj({}))
route('/databases/{id}/resize-plan', 'post', 'reviewDatabaseResize', obj({'id': S, 'plan': ref('DatabaseResizePlan')}, ['id', 'plan']), obj({'spec': ref('ManagedDatabaseSpec')}, ['spec']))
route('/databases/{id}/resize', 'post', 'resizeManagedDatabase', ref('DatabaseOperation'), obj({'review_id': S, 'expected_revision': I, 'spec': ref('ManagedDatabaseSpec')}, ['review_id', 'expected_revision', 'spec']), '202', idem=True)
route('/databases/{id}/restore-plan', 'post', 'reviewManagedDatabaseRecovery', ref('BackupRestorePlan'), obj({'artifact_id': S}, ['artifact_id']))
schemas['BackupSource']['properties']['kind']['enum'].append('managed_database')
schemas['BackupSource']['properties']['managed_database_id'] = S
schemas['BackupSource']['properties']['engine']['enum'].append('redis')
schemas['BackupTarget']['properties'].update({'managed_database_id': S, 'managed_database_name': S, 'runtime_fingerprint': S})
schemas['BackupArtifact']['properties'].update({'source_revision': I, 'captured_at': T, 'verified_at': T})

schemas['ServiceBinding']['properties'].update({'managed_database': S, 'endpoint': S, 'cluster_aware': B})
schemas['DatabaseConnectionPlan'] = obj({'id': S, 'database_id': S, 'database_name': S, 'database_revision': I, 'application_id': S, 'application_name': S, 'application_revision': I, 'service': S, 'variable': S, 'previous_kind': S, 'binding': ref('ServiceBinding'), 'recovery': ref('DatabaseRecovery'), 'expires_at': T, 'warnings': array(S)}, ['id', 'database_id', 'database_name', 'database_revision', 'application_id', 'application_name', 'application_revision', 'service', 'variable', 'previous_kind', 'binding', 'expires_at', 'warnings'])
route('/databases/{id}/connection-plan', 'post', 'reviewDatabaseConnection', ref('DatabaseConnectionPlan'), obj({'application_id': S, 'service': S, 'variable': S, 'endpoint': S, 'cluster_aware': B}, ['application_id', 'service', 'variable', 'endpoint', 'cluster_aware']))
route('/databases/{id}/connect', 'post', 'replaceDatabaseConnection', ref('Deployment'), obj({'review_id': S, 'confirm_application': S}, ['review_id', 'confirm_application']), '202', idem=True)
route('/databases/{id}/inspect', 'post', 'attestDatabaseInspection', ref('ManagedDatabase'), obj({'job_id': S, 'confirm_name': S, 'expected_revision': I, 'inspected': B}, ['job_id', 'confirm_name', 'expected_revision', 'inspected']))

schemas['BackupArtifact']['properties']['source_version'] = S
schemas['BackupTarget']['properties']['source_version'] = S
schemas['BackupSource']['properties']['kind']['enum'].append('docker_import')
schemas['BackupSource']['properties']['external_name'] = S
schemas['DatabaseImportSpec'] = obj({'destination_id': S, 'source_name': S, 'engine': {'type': 'string', 'enum': ['postgresql', 'redis']}, 'source_version': S, 'captured_at': T, 'bytes': {'type': 'integer', 'minimum': 16, 'maximum': 2147483648}, 'sha256': S}, ['destination_id', 'source_name', 'engine', 'source_version', 'captured_at', 'bytes', 'sha256'])
schemas['DatabaseImport'] = obj({'id': S, 'spec': ref('DatabaseImportSpec'), 'status': S, 'expires_at': T, 'artifact_id': S}, ['id', 'spec', 'status', 'expires_at'])
route('/backup-imports', 'post', 'reviewDatabaseImport', ref('DatabaseImport'), ref('DatabaseImportSpec'), '201', idem=True)
route('/backup-imports/{id}', 'get', 'getDatabaseImport', ref('DatabaseImport'))
route('/backup-imports/{id}/archive', 'put', 'uploadDatabaseImport', ref('BackupArtifact'), S, '201')
paths['/backup-imports/{id}/archive']['put']['requestBody']['content'] = {'application/octet-stream': {'schema': {'type': 'string', 'format': 'binary'}}}

schemas['BackupDestination']['properties'].update({'project': S, 'environment': S})
