CREATE TABLE database_application_provisioning_operations (
 id text PRIMARY KEY, database_id text NOT NULL REFERENCES managed_databases(id), database_revision bigint NOT NULL,
 application_id text NOT NULL REFERENCES applications(id), application_revision bigint NOT NULL,
 identity_id text NOT NULL REFERENCES identities(id), key_id text NOT NULL REFERENCES api_keys(id),
 idempotency_key text NOT NULL, request_hash bytea NOT NULL, kind text NOT NULL DEFAULT 'provision-application',
 plan jsonb NOT NULL, encrypted_password bytea NOT NULL CHECK(octet_length(encrypted_password) BETWEEN 29 AND 1024),
 status text NOT NULL DEFAULT 'queued' CHECK(status IN ('queued','running','succeeded','failed','cancelled')),
 phase text NOT NULL DEFAULT 'accepted', message text NOT NULL DEFAULT '', next_attempt_at timestamptz NOT NULL DEFAULT now(),
 lease text NOT NULL DEFAULT '', lease_until timestamptz, created_at timestamptz NOT NULL DEFAULT now(), started_at timestamptz, finished_at timestamptz,
 UNIQUE(identity_id,idempotency_key)
);
CREATE UNIQUE INDEX database_application_provisioning_active ON database_application_provisioning_operations(database_id,application_id) WHERE status IN ('queued','running');
CREATE INDEX database_application_provisioning_work ON database_application_provisioning_operations(next_attempt_at,created_at) WHERE status IN ('queued','running');
INSERT INTO schema_migrations(version) VALUES(92);
