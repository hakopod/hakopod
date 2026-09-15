ALTER TABLE deployments ADD COLUMN recovery_state text NOT NULL DEFAULT '' CHECK (recovery_state IN ('','running','succeeded','failed','skipped'));
ALTER TABLE deployments ADD COLUMN recovery_revision bigint NOT NULL DEFAULT 0;
ALTER TABLE deployments ADD COLUMN recovery_spec jsonb;
ALTER TABLE deployments ADD COLUMN recovery_error text NOT NULL DEFAULT '';
INSERT INTO schema_migrations(version) VALUES(29);
