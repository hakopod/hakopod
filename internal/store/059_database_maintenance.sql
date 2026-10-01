ALTER TABLE managed_databases ADD COLUMN maintenance_lease text NOT NULL DEFAULT '';
ALTER TABLE managed_databases ADD COLUMN maintenance_lease_until timestamptz;
INSERT INTO schema_migrations(version) VALUES(59);
