DROP INDEX managed_database_configuration_operation;
CREATE UNIQUE INDEX managed_database_configuration_operation
 ON managed_database_operations(database_id,revision)
 WHERE kind NOT IN ('switchover','resize-retry');

INSERT INTO schema_migrations(version) VALUES(82);
