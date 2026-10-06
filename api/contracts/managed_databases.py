schemas['DatabasePlacement'] = obj({'spread': {'type': 'string', 'enum': ['', 'nodes', 'zones']}, 'node_names': {'type': 'array', 'items': S, 'maxItems': 48, 'uniqueItems': True}})
schemas['DatabaseTLSConfig'] = obj({'mode': {'type': 'string', 'enum': ['required']}}, ['mode'])
schemas['DatabaseOracleConfig'] = obj({'edition': {'type': 'string', 'enum': ['free', 'enterprise']}, 'image': S, 'registry_credential': S, 'license_confirmed': B}, ['edition'])
schemas['DatabaseVitessTable'] = obj({'name': {'type': 'string', 'pattern': '^[A-Za-z_][A-Za-z0-9_]{0,63}$'}, 'sharding_column': {'type': 'string', 'pattern': '^[A-Za-z_][A-Za-z0-9_]{0,63}$'}}, ['name', 'sharding_column'])
schemas['DatabaseVitessConfig'] = obj({'tables': {'type': 'array', 'items': ref('DatabaseVitessTable'), 'maxItems': 128}, 'backup_destination_id': {'type': 'string', 'pattern': '^[a-f0-9]{32}$'}, 'backup_destination_revision': {'type': 'integer', 'minimum': 1}}, ['backup_destination_id', 'backup_destination_revision'])
schemas['DatabaseTLSObservation'] = obj({'required': B, 'verified': B, 'plaintext_rejected': B, 'minimum_version': S, 'issuer': S, 'dns_names': array(S), 'fingerprint': S, 'ca_fingerprint': S, 'not_before': T, 'expires_at': T, 'checked_at': T, 'message': S}, ['required', 'verified', 'plaintext_rejected'])
schemas['DatabasePublicTrust'] = obj({'certificate_pem': S, 'fingerprint': S, 'issuer': S, 'not_before': T, 'expires_at': T}, ['certificate_pem', 'fingerprint', 'issuer', 'not_before', 'expires_at'])
schemas['DatabasePooling'] = obj({'mode': {'type': 'string', 'enum': ['session', 'transaction']}, 'instances': {'type': 'integer', 'minimum': 1, 'maximum': 3}, 'max_client_connections': {'type': 'integer', 'minimum': 20, 'maximum': 2000}, 'default_pool_size': {'type': 'integer', 'minimum': 1, 'maximum': 20}, 'read_only': B}, ['mode', 'instances', 'max_client_connections', 'default_pool_size', 'read_only'])
schemas['ManagedDatabaseSpec'] = obj({
    'schema_version': {'type': 'integer', 'const': 1}, 'name': S,
    'engine': {'type': 'string', 'enum': ['postgresql', 'redis', 'mysql', 'mongodb', 'clickhouse', 'oracle', 'vitess']},
    'version': S, 'mode': {'type': 'string', 'enum': ['standalone', 'cluster']},
    'replicas': I, 'shards': I, 'cpu': S, 'memory': S, 'storage_gib': I, 'placement': ref('DatabasePlacement'), 'tls': ref('DatabaseTLSConfig'), 'pooling': ref('DatabasePooling'), 'oracle': ref('DatabaseOracleConfig'), 'vitess': ref('DatabaseVitessConfig'),
}, ['schema_version', 'name', 'engine', 'version', 'mode', 'replicas', 'shards', 'cpu', 'memory', 'storage_gib'])
schemas['DatabaseMember'] = obj({'name': S, 'uid': S, 'role': S, 'shard': S, 'ready': B, 'node': S, 'zone': S, 'region': S, 'provider': S, 'phase': S, 'restarts': I, 'created_at': T, 'image': S, 'metrics': ref('RuntimeMetrics')}, ['name', 'uid', 'role', 'ready'])
schemas['DatabasePlacementObservation'] = obj({'verified': B, 'message': S, 'nodes': I, 'zones': I, 'regions': I, 'providers': I}, ['verified', 'message', 'nodes', 'zones', 'regions', 'providers'])
schemas['DatabaseEndpoint'] = obj({'purpose': S, 'host': S, 'port': I}, ['purpose', 'host', 'port'])
schemas['DatabaseObservation'] = obj({'observed_at': T, 'revision': I, 'status': S, 'message': S, 'members': array(ref('DatabaseMember')), 'endpoints': array(ref('DatabaseEndpoint')), 'primary': S, 'slots_assigned': I, 'slots_healthy': B, 'topology_fingerprint': S, 'metrics': ref('RuntimeMetrics'), 'placement': ref('DatabasePlacementObservation')}, ['observed_at', 'revision', 'status', 'message', 'members', 'endpoints', 'slots_healthy'])
schemas['DatabaseObservation']['properties']['tls'] = ref('DatabaseTLSObservation')
schemas['DatabaseRecovery'] = obj({'artifact_id': S, 'job_id': S, 'source_id': S, 'source_revision': I, 'captured_at': T, 'restored_at': T, 'inspected_at': T}, ['artifact_id', 'job_id'])
schemas['ManagedDatabase'] = obj({'id': S, 'project': S, 'environment': S, 'revision': I, 'spec': ref('ManagedDatabaseSpec'), 'status': S, 'observation': ref('DatabaseObservation'), 'recovery': ref('DatabaseRecovery'), 'created_at': T, 'updated_at': T, 'deleted_at': T}, ['id', 'project', 'environment', 'revision', 'spec', 'status', 'observation', 'created_at', 'updated_at'])
schemas['DatabasePublicEndpointSpec'] = obj({
    'purpose': {'type': 'string', 'enum': ['read_write', 'read_only', 'pooled_read_write', 'pooled_read_only', 'native', 'https', 'cluster']},
    'source_cidrs': {'type': 'array', 'items': {'type': 'string', 'format': 'ipv4'}, 'minItems': 1, 'maxItems': 16, 'uniqueItems': True},
    'max_connections': {'type': 'integer', 'minimum': 1, 'maximum': 256},
}, ['purpose', 'source_cidrs', 'max_connections'])
schemas['DatabasePublicEndpointAllocation'] = obj({'id': S, 'host': S, 'address': {'type': 'string', 'format': 'ipv4'}, 'port': {'type': 'integer', 'minimum': 1, 'maximum': 65535}}, ['id', 'host', 'address', 'port'])
schemas['DatabasePublicEndpointMemberAllocation'] = obj({'member_name': S, 'member_uid': S, 'allocation': ref('DatabasePublicEndpointAllocation')}, ['member_name', 'member_uid', 'allocation'])
schemas['DatabasePublicEndpointClientAddress'] = obj({'member_name': S, 'member_uid': S, 'advertised_addresses': {'type': 'array', 'items': S, 'minItems': 2, 'maxItems': 2}, 'public_host': S, 'public_port': {'type': 'integer', 'minimum': 1, 'maximum': 65535}}, ['member_name', 'member_uid', 'advertised_addresses', 'public_host', 'public_port'])
schemas['DatabasePublicEndpointObservation'] = obj({'configured': B, 'externally_verified': B, 'message': S, 'checked_at': T, 'client_address_map': {'type': 'array', 'items': ref('DatabasePublicEndpointClientAddress'), 'maxItems': 48}}, ['configured', 'externally_verified', 'message'])
schemas['DatabasePublicEndpoint'] = obj({
    'id': S, 'database_id': S, 'revision': {'type': 'integer', 'minimum': 0},
    'spec': ref('DatabasePublicEndpointSpec'), 'allocation': ref('DatabasePublicEndpointAllocation'),
    'member_allocations': {'type': 'array', 'items': ref('DatabasePublicEndpointMemberAllocation'), 'maxItems': 48},
    'status': {'type': 'string', 'enum': ['review', 'pending', 'active', 'revoking', 'error', 'revoked']},
    'observation': ref('DatabasePublicEndpointObservation'), 'created_at': T, 'updated_at': T, 'revoked_at': T,
}, ['id', 'database_id', 'revision', 'spec', 'allocation', 'status', 'observation', 'created_at', 'updated_at'])
schemas['DatabasePublicEndpointRoute'] = obj({
    'purpose': {'type': 'string', 'enum': ['read_write', 'read_only', 'pooled_read_write', 'pooled_read_only', 'native', 'https', 'cluster']},
    'protocol': {'type': 'string', 'enum': ['postgresql', 'mysql', 'clickhouse_native', 'https', 'oracle_tcps', 'mongodb', 'redis']},
    'routing': {'type': 'string', 'enum': ['direct', 'pgbouncer', 'mysql_router', 'vitess_gateway', 'replica_set_horizons', 'cluster_discovery', 'client_address_mapping']},
    'read_only': B, 'pooled': B,
}, ['purpose', 'protocol', 'routing', 'read_only', 'pooled'])
schemas['DatabasePublicEndpointCapabilities'] = obj({
    'engine': S, 'available': B, 'unavailable_reason': S,
    'routes': {'type': 'array', 'items': ref('DatabasePublicEndpointRoute'), 'maxItems': 4},
}, ['engine', 'available', 'unavailable_reason', 'routes'])
schemas['DatabasePublicEndpointReview'] = obj({
    'database_id': S, 'project': S, 'environment': S,
    'database_revision': {'type': 'integer', 'minimum': 1}, 'endpoint_id': S,
    'endpoint_revision': {'type': 'integer', 'minimum': 0}, 'spec': ref('DatabasePublicEndpointSpec'),
    'allocation': ref('DatabasePublicEndpointAllocation'), 'route': ref('DatabasePublicEndpointRoute'), 'route_fingerprint': S, 'topology_fingerprint': S,
    'member_allocations': {'type': 'array', 'items': ref('DatabasePublicEndpointMemberAllocation'), 'maxItems': 48},
    'tls_fingerprint': S, 'authority_fingerprint': {'type': 'string', 'pattern': '^[a-f0-9]{64}$'}, 'blocked_reasons': array(S), 'warnings': array(S), 'expires_at': T,
}, ['database_id', 'project', 'environment', 'database_revision', 'endpoint_id', 'endpoint_revision', 'spec', 'allocation', 'topology_fingerprint', 'tls_fingerprint', 'blocked_reasons', 'warnings', 'expires_at'])
schemas['DatabasePublicEndpointPlan'] = obj({'id': S, 'plan': ref('DatabasePublicEndpointReview')}, ['id', 'plan'])
schemas['DatabasePublicEndpointOperation'] = obj({
    'id': S, 'endpoint_id': S, 'database_id': S, 'revision': {'type': 'integer', 'minimum': 1},
    'kind': {'type': 'string', 'enum': ['publish', 'revoke']},
    'status': {'type': 'string', 'enum': ['queued', 'running', 'succeeded', 'failed', 'cancelled']},
    'phase': S, 'message': S, 'review': ref('DatabasePublicEndpointReview'),
    'created_at': T, 'started_at': T, 'finished_at': T,
}, ['id', 'endpoint_id', 'database_id', 'revision', 'kind', 'status', 'phase', 'message', 'created_at'])
schemas['DatabaseBackupEvidence'] = obj({'artifact_id': S, 'database_id': S, 'revision': I, 'captured_at': T, 'verified_at': T, 'sha256': S}, ['artifact_id', 'database_id', 'revision', 'captured_at', 'verified_at', 'sha256'])
schemas['DatabaseResizePlan'] = obj({'current': ref('ManagedDatabaseSpec'), 'proposed': ref('ManagedDatabaseSpec'), 'expected_revision': I, 'topology_fingerprint': S, 'backup': ref('DatabaseBackupEvidence'), 'blocked_reasons': array(S), 'warnings': array(S), 'expires_at': T}, ['current', 'proposed', 'expected_revision', 'topology_fingerprint', 'blocked_reasons', 'warnings', 'expires_at'])
schemas['DatabaseResizeRetryReview'] = obj({'operation_id': S, 'database_id': S, 'revision': {'type': 'integer', 'minimum': 1}, 'state': {'type': 'string', 'enum': ['accepted', 'prior']}, 'resize': ref('DatabaseResizePlan'), 'expires_at': T}, ['operation_id', 'database_id', 'revision', 'state', 'resize', 'expires_at'])
schemas['DatabaseOracleSwitchoverReview'] = obj({
    'request_id': S, 'database_id': S, 'project': S, 'environment': S,
    'revision': {'type': 'integer', 'minimum': 1}, 'broker_uid': S,
    'topology_fingerprint': S, 'primary': S, 'target': S,
    'target_controller': S, 'target_unique_name': S, 'member_uids': array(S), 'expires_at': T,
}, ['request_id', 'database_id', 'project', 'environment', 'revision', 'broker_uid', 'topology_fingerprint', 'primary', 'target', 'target_controller', 'target_unique_name', 'member_uids', 'expires_at'])
schemas['DatabaseOracleSwitchoverPlan'] = obj({'id': S, 'plan': ref('DatabaseOracleSwitchoverReview'), 'warnings': array(S)}, ['id', 'plan', 'warnings'])
schemas['DatabaseOperation'] = obj({'id': S, 'database_id': S, 'revision': I, 'kind': S, 'status': S, 'phase': S, 'message': S, 'spec': ref('ManagedDatabaseSpec'), 'review': ref('DatabaseResizePlan'), 'switchover': ref('DatabaseOracleSwitchoverReview'), 'created_at': T, 'started_at': T, 'finished_at': T}, ['id', 'database_id', 'revision', 'kind', 'status', 'phase', 'message', 'spec', 'created_at'])
route('/databases', 'get', 'listManagedDatabases', items('ManagedDatabase'), scope=True)
schemas['DatabasePlacementNode'] = obj({'name': S, 'architecture': S, 'available': B, 'reason': S, 'zone': S, 'region': S, 'provider': S, 'reserved_cpu_milli': I, 'reserved_memory_bytes': I}, ['name', 'architecture', 'available', 'reason'])
route('/database-placement/nodes', 'get', 'listDatabasePlacementNodes', obj({'items': array(ref('DatabasePlacementNode')), 'limit': I}, ['items', 'limit']), scope=True)
route('/databases', 'post', 'createManagedDatabase', ref('DatabaseOperation'), obj({'project': S, 'environment': S, 'spec': ref('ManagedDatabaseSpec')}, ['project', 'environment', 'spec']), '202', idem=True)
route('/databases/{id}', 'get', 'getManagedDatabase', ref('ManagedDatabase'))
route('/databases/{id}', 'delete', 'deleteManagedDatabase', ref('DatabaseOperation'), obj({'expected_revision': I, 'confirm_name': S}, ['expected_revision', 'confirm_name']), '202', idem=True)
route('/databases/{id}/operations', 'get', 'listDatabaseOperations', items('DatabaseOperation'))
route('/databases/{id}/public-endpoint-capabilities', 'get', 'getDatabasePublicEndpointCapabilities', ref('DatabasePublicEndpointCapabilities'))
paths['/databases/{id}/public-endpoint-capabilities']['get']['description'] = 'Read authorized, bounded route capabilities. Availability requires database TLS, an exact supported database shape and completed native qualification for the selected engine. MySQL, ClickHouse, MongoDB, Redis, Vitess and Oracle Free publication remain disabled until their native transport, identity transition and revocation acceptance gates pass. Oracle Enterprise and Data Guard use a separate licensed acceptance gate. Existing endpoint inventory, operation lookup and revocation remain available while new publication is disabled.'
route('/databases/{id}/public-endpoints', 'get', 'listDatabasePublicEndpoints', items('DatabasePublicEndpoint'))
route('/databases/{id}/public-endpoint-plan', 'post', 'reviewDatabasePublicEndpoint', ref('DatabasePublicEndpointPlan'), ref('DatabasePublicEndpointSpec'))
route('/databases/{id}/public-endpoints', 'post', 'publishDatabasePublicEndpoint', ref('DatabasePublicEndpointOperation'), obj({'review_id': S, 'expected_database_revision': {'type': 'integer', 'minimum': 1}, 'expected_endpoint_revision': {'type': 'integer', 'minimum': 0}}, ['review_id', 'expected_database_revision', 'expected_endpoint_revision']), '202', idem=True)
route('/databases/{id}/public-endpoints/{endpoint}', 'delete', 'revokeDatabasePublicEndpoint', ref('DatabasePublicEndpointOperation'), obj({'expected_endpoint_revision': {'type': 'integer', 'minimum': 0}}, ['expected_endpoint_revision']), '202', idem=True)
route('/database-public-endpoint-operations/{id}', 'get', 'getDatabasePublicEndpointOperation', ref('DatabasePublicEndpointOperation'))
for action in ['public-endpoint-plan', 'public-endpoints']:
    paths[f'/databases/{{id}}/{action}']['post']['description'] = 'Public database access uses operator-owned address, hostname and port inventory. The caller controls only a supported route purpose, explicit IPv4 source networks and the connection cap. Route capabilities and native qualification gate publication. New reviews include the selected immutable route descriptor and private backend fingerprint; legacy PostgreSQL reviews may omit them.'
paths['/databases/{id}/public-endpoints/{endpoint}']['delete']['description'] = 'Close and acknowledge the owned HAProxy route and its existing sessions before releasing the endpoint allocation.'
schemas['DatabaseConnectionReference'] = obj({'application_id': S, 'application_name': S, 'application_display_name': S, 'project': S, 'environment': S, 'service': S, 'variable': S, 'endpoint': S, 'saved_revision': I, 'last_successful_revision': I, 'latest_attempt_revision': I, 'latest_attempt_status': S}, ['application_id', 'application_name', 'application_display_name', 'project', 'environment', 'service', 'variable', 'endpoint', 'saved_revision', 'last_successful_revision', 'latest_attempt_revision', 'latest_attempt_status'])
schemas['DatabaseConnections'] = obj({'items': array(ref('DatabaseConnectionReference')), 'truncated': B, 'limit': I}, ['items', 'truncated', 'limit'])
route('/databases/{id}/connections', 'get', 'listDatabaseConnections', ref('DatabaseConnections'))
route('/databases/{id}/trust', 'get', 'getDatabasePublicTrust', ref('DatabasePublicTrust'))
route('/databases/{id}/credentials', 'post', 'revealDatabaseCredentials', obj({'username': S, 'password': S, 'database': S}, ['username', 'password', 'database']), obj({}))
route('/databases/{id}/resize-plan', 'post', 'reviewDatabaseResize', obj({'id': S, 'plan': ref('DatabaseResizePlan')}, ['id', 'plan']), obj({'spec': ref('ManagedDatabaseSpec')}, ['spec']))
route('/databases/{id}/resize', 'post', 'resizeManagedDatabase', ref('DatabaseOperation'), obj({'review_id': S, 'expected_revision': I, 'spec': ref('ManagedDatabaseSpec')}, ['review_id', 'expected_revision', 'spec']), '202', idem=True)
route('/databases/{id}/resize-retry-plan', 'post', 'reviewManagedDatabaseResizeRetry', obj({'id': S, 'plan': ref('DatabaseResizeRetryReview')}, ['id', 'plan']), obj({'operation_id': S, 'expected_revision': {'type': 'integer', 'minimum': 1}}, ['operation_id', 'expected_revision']))
route('/databases/{id}/resize-retry', 'post', 'retryManagedDatabaseResize', ref('DatabaseOperation'), obj({'review_id': S, 'operation_id': S, 'expected_revision': {'type': 'integer', 'minimum': 1}, 'confirm_name': S}, ['review_id', 'operation_id', 'expected_revision', 'confirm_name']), '202', idem=True)
for action in ['resize-retry-plan', 'resize-retry']:
    paths[f'/databases/{{id}}/{action}']['post']['description'] = 'Review and retry the same failed clustered MySQL or MongoDB replica change at its existing desired revision. The review distinguishes an exact accepted controller revision from a fully healthy prior reviewed configuration. Retry creates a new durable attempt without changing desired capacity or replaying stale topology.'
route('/databases/{id}/switchover-plan', 'post', 'reviewOracleDatabaseSwitchover', ref('DatabaseOracleSwitchoverPlan'), obj({'target_member': {'type': 'string', 'minLength': 1, 'maxLength': 253}}, ['target_member']))
route('/databases/{id}/switchover', 'post', 'switchoverOracleDatabase', ref('DatabaseOperation'), obj({'review_id': S, 'expected_revision': {'type': 'integer', 'minimum': 1}, 'confirm_name': S}, ['review_id', 'expected_revision', 'confirm_name']), '202', idem=True)
route('/databases/{id}/switchover-retry', 'post', 'retryOracleDatabaseSwitchover', ref('DatabaseOperation'), obj({'operation_id': S, 'expected_revision': {'type': 'integer', 'minimum': 1}, 'confirm_name': S}, ['operation_id', 'expected_revision', 'confirm_name']), '202')
for action in ['switchover-plan', 'switchover', 'switchover-retry']:
    paths[f'/databases/{{id}}/{action}']['post']['description'] = 'Graceful Oracle Enterprise Data Guard switchover. Requires the gated Enterprise runtime and database write permission. Existing connections close; forced failover is unavailable.'
paths['/databases/{id}/switchover-retry']['post']['description'] += ' Resumes the existing approved operation and target after a worker timeout. The operation ID makes retries idempotent; no new review, target or request token is created.'
route('/databases/{id}/restore-plan', 'post', 'reviewManagedDatabaseRecovery', ref('BackupRestorePlan'), obj({'artifact_id': S}, ['artifact_id']))
schemas['BackupSource']['properties']['kind']['enum'].append('managed_database')
schemas['BackupSource']['properties']['managed_database_id'] = S
schemas['BackupSource']['properties']['engine']['enum'].append('redis')
schemas['BackupSource']['properties']['engine']['enum'].append('mongodb')
schemas['BackupSource']['properties']['engine']['enum'].append('oracle')
schemas['BackupSource']['properties']['engine']['enum'].append('vitess')
schemas['BackupTarget']['properties'].update({'managed_database_id': S, 'managed_database_name': S, 'runtime_fingerprint': S})
schemas['BackupArtifact']['properties'].update({'source_revision': I, 'captured_at': T, 'verified_at': T})

binding_ssl_mode = {'type': 'string', 'enum': ['', 'disable', 'require', 'verify-ca', 'verify-full'], 'description': 'Omit to follow the managed database TLS policy. PostgreSQL supports require, verify-ca and verify-full. Other TLS engines support verify-full except MySQL/Vitess, whose driver must configure TLS separately. disable is allowed only for legacy plaintext PostgreSQL or Redis.'}
schemas['ServiceBinding']['properties'].update({'managed_database': S, 'endpoint': S, 'cluster_aware': B, 'ssl_mode': binding_ssl_mode})
schemas['ServiceBinding']['properties']['protocol']['enum'].append('mongodb')
schemas['ServiceBinding']['properties']['protocol']['enum'].append('clickhouse')
schemas['ServiceBinding']['properties']['protocol']['enum'].append('oracle')
schemas['DatabaseConnectionPlan'] = obj({'id': S, 'database_id': S, 'database_name': S, 'database_revision': I, 'application_id': S, 'application_name': S, 'application_revision': I, 'service': S, 'variable': S, 'previous_kind': S, 'binding': ref('ServiceBinding'), 'recovery': ref('DatabaseRecovery'), 'expires_at': T, 'warnings': array(S)}, ['id', 'database_id', 'database_name', 'database_revision', 'application_id', 'application_name', 'application_revision', 'service', 'variable', 'previous_kind', 'binding', 'expires_at', 'warnings'])
route('/databases/{id}/connection-plan', 'post', 'reviewDatabaseConnection', ref('DatabaseConnectionPlan'), obj({'application_id': S, 'service': S, 'variable': S, 'endpoint': S, 'cluster_aware': B, 'username': S, 'database': S, 'password': ref('SecretRef'), 'ssl_mode': binding_ssl_mode}, ['application_id', 'service', 'variable', 'endpoint', 'cluster_aware']))
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

# Durable monitoring contains aggregate numeric statistics only.
schemas['DatabaseEngineMetrics'] = obj({'available': B, 'reason': S, 'sampled_at': T, **{key: I for key in ['connections', 'active_connections', 'max_connections', 'data_bytes', 'transactions', 'commands', 'replication_lag_bytes', 'evicted_keys', 'rejected_connections', 'uptime_seconds']}, 'cache_hit_ratio': {'type': 'number', 'minimum': 0, 'maximum': 1}}, ['available'])
schemas['DatabaseObservation']['properties']['engine_metrics'] = ref('DatabaseEngineMetrics')
schemas['DatabaseMetricPoint'] = obj({'observed_at': T, 'revision': I, 'status': S, 'resources': ref('RuntimeMetrics'), 'engine': ref('DatabaseEngineMetrics')}, ['observed_at', 'revision', 'status'])
schemas['DatabaseMetricHistory'] = obj({'items': {'type': 'array', 'items': ref('DatabaseMetricPoint'), 'maxItems': 1441}, 'from': T, 'until': T, 'resolution_seconds': I, 'retention_hours': I}, ['items', 'from', 'until', 'resolution_seconds', 'retention_hours'])
route('/databases/{id}/metrics', 'get', 'getDatabaseMetricHistory', ref('DatabaseMetricHistory'))
paths['/databases/{id}/metrics']['get']['parameters'].append({'name': 'range', 'in': 'query', 'schema': {'type': 'string', 'enum': ['1h', '6h', '24h'], 'default': '1h'}})

schemas['DatabasePoolingObservation'] = obj({'ready': B, 'message': S, 'members': array(ref('DatabaseMember'))}, ['ready', 'members'])
schemas['DatabaseObservation']['properties']['pooling'] = ref('DatabasePoolingObservation')

schemas['DatabaseRoutingObservation'] = obj({'kind': S, 'ready': B, 'message': S, 'members': array(ref('DatabaseMember'))}, ['kind', 'ready', 'members'])
schemas['DatabaseObservation']['properties']['routing'] = ref('DatabaseRoutingObservation')
schemas['DatabaseCoordinationObservation'] = obj({'ready': B, 'message': S, 'members': {'type': 'array', 'items': ref('DatabaseMember'), 'maxItems': 3}}, ['ready', 'members'])
schemas['DatabaseObservation']['properties']['coordination'] = ref('DatabaseCoordinationObservation')
