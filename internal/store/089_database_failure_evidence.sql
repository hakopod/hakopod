CREATE TABLE managed_database_failure_evidence (
 database_id text NOT NULL REFERENCES managed_databases(id) ON DELETE CASCADE,
 fingerprint text NOT NULL CHECK(fingerprint ~ '^[a-f0-9]{64}$'),
 evidence jsonb NOT NULL CHECK(octet_length(evidence::text)<=8192),
 first_seen_at timestamptz NOT NULL,
 last_seen_at timestamptz NOT NULL,
 PRIMARY KEY(database_id,fingerprint)
);
CREATE INDEX managed_database_failure_evidence_history ON managed_database_failure_evidence(database_id,last_seen_at DESC,fingerprint);
INSERT INTO schema_migrations(version) VALUES(89);
