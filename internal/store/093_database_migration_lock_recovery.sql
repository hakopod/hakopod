CREATE TABLE database_migration_lock_recovery_operations (
 id text PRIMARY KEY, database_id text NOT NULL REFERENCES managed_databases(id), application_id text NOT NULL REFERENCES applications(id),
 database_revision bigint NOT NULL, application_revision bigint NOT NULL,
 identity_id text NOT NULL REFERENCES identities(id), key_id text NOT NULL REFERENCES api_keys(id),
 idempotency_key text NOT NULL, request_hash bytea NOT NULL, plan jsonb NOT NULL,
 before_evidence jsonb NOT NULL, after_evidence jsonb,
 status text NOT NULL DEFAULT 'queued' CHECK(status IN ('queued','running','succeeded','failed','cancelled')),
 phase text NOT NULL DEFAULT 'accepted', message text NOT NULL DEFAULT '', next_attempt_at timestamptz NOT NULL DEFAULT now(),
 lease text NOT NULL DEFAULT '', lease_until timestamptz, created_at timestamptz NOT NULL DEFAULT now(), started_at timestamptz, finished_at timestamptz,
 UNIQUE(identity_id,idempotency_key)
);
CREATE UNIQUE INDEX database_migration_lock_recovery_active ON database_migration_lock_recovery_operations(database_id,application_id) WHERE status IN ('queued','running');
CREATE INDEX database_migration_lock_recovery_work ON database_migration_lock_recovery_operations(next_attempt_at,created_at) WHERE status IN ('queued','running');
INSERT INTO schema_migrations(version) VALUES(93);
