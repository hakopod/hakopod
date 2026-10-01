ALTER TABLE managed_databases
 ADD COLUMN observation_lease text NOT NULL DEFAULT '',
 ADD COLUMN observation_lease_until timestamptz,
 ADD COLUMN observation_next_at timestamptz NOT NULL DEFAULT now();
CREATE INDEX managed_database_observation_queue ON managed_databases(observation_next_at,id)
 WHERE deleted_at IS NULL AND status='ready';
INSERT INTO schema_migrations(version) VALUES(61);
