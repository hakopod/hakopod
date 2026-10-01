ALTER TABLE managed_database_operations ADD COLUMN switchover jsonb;
ALTER TABLE managed_database_operations ADD CONSTRAINT managed_database_switchover_payload
 CHECK ((kind='switchover')=(switchover IS NOT NULL));

-- Role changes retain the immutable configuration revision. Configuration
-- operations still have one durable operation per database revision.
ALTER TABLE managed_database_operations DROP CONSTRAINT managed_database_operations_database_id_revision_key;
CREATE UNIQUE INDEX managed_database_configuration_operation
 ON managed_database_operations(database_id,revision) WHERE kind<>'switchover';
CREATE UNIQUE INDEX managed_database_active_operation
 ON managed_database_operations(database_id) WHERE status IN ('queued','running');

INSERT INTO schema_migrations(version) VALUES(63);
