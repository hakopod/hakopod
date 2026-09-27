ALTER TABLE managed_databases ADD COLUMN reserved_memory_bytes bigint NOT NULL DEFAULT 0 CHECK(reserved_memory_bytes>=0);
ALTER TABLE managed_databases ADD COLUMN reserved_storage_gib bigint NOT NULL DEFAULT 0 CHECK(reserved_storage_gib>=0);
INSERT INTO schema_migrations(version) VALUES(49);
