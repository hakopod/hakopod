ALTER TABLE managed_databases DROP CONSTRAINT managed_databases_project_environment_name_key;
CREATE UNIQUE INDEX managed_database_live_names ON managed_databases(project,environment,name) WHERE deleted_at IS NULL;
INSERT INTO schema_migrations(version) VALUES(50);
