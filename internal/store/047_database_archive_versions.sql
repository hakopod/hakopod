ALTER TABLE backup_artifacts ADD COLUMN source_version text NOT NULL DEFAULT '';
INSERT INTO schema_migrations(version) VALUES(47);
