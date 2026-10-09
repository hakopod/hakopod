ALTER TABLE managed_database_operations ADD COLUMN progress jsonb NOT NULL DEFAULT '[]'::jsonb;
INSERT INTO schema_migrations(version) VALUES(90);
