ALTER TABLE managed_databases ADD COLUMN reserved_cpu_milli bigint NOT NULL DEFAULT 0 CHECK (reserved_cpu_milli >= 0);
