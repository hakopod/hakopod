ALTER TABLE managed_platform_recovery_reviews ADD COLUMN compatibility jsonb NOT NULL DEFAULT '{"generated_at":"0001-01-01T00:00:00Z","checks":[],"blocked":false}' CHECK(octet_length(compatibility::text)<=131072);
ALTER TABLE backup_artifacts ADD COLUMN compatibility_evidence jsonb NOT NULL DEFAULT '{}' CHECK(octet_length(compatibility_evidence::text)<=32768);
