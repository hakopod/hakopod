CREATE TABLE managed_databases (
 id text PRIMARY KEY, project text NOT NULL, environment text NOT NULL, name text NOT NULL,
 revision bigint NOT NULL CHECK(revision>0), spec jsonb NOT NULL,
 status text NOT NULL DEFAULT 'pending' CHECK(status IN ('pending','ready','restoring','failed','deleting','deleted')),
 observation jsonb NOT NULL DEFAULT '{}', recovery jsonb, credentials bytea NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now(), deleted_at timestamptz,
 FOREIGN KEY(project,environment) REFERENCES environments(project,name), UNIQUE(project,environment,name)
);
CREATE TABLE managed_database_operations (
 id text PRIMARY KEY, database_id text NOT NULL REFERENCES managed_databases(id), revision bigint NOT NULL,
 identity_id text NOT NULL REFERENCES identities(id), key_id text NOT NULL REFERENCES api_keys(id),
 idempotency_key text NOT NULL, request_hash bytea NOT NULL, kind text NOT NULL,
 spec jsonb NOT NULL, review jsonb, next_attempt_at timestamptz NOT NULL DEFAULT now(), status text NOT NULL DEFAULT 'queued' CHECK(status IN ('queued','running','succeeded','failed','cancelled')),
 phase text NOT NULL DEFAULT 'accepted', message text NOT NULL DEFAULT '', lease text NOT NULL DEFAULT '', lease_until timestamptz,
 created_at timestamptz NOT NULL DEFAULT now(), started_at timestamptz, finished_at timestamptz,
 UNIQUE(identity_id,idempotency_key), UNIQUE(database_id,revision)
);
CREATE INDEX managed_database_work ON managed_database_operations(next_attempt_at,created_at) WHERE status IN ('queued','running');
CREATE TABLE managed_database_reviews (
 id text PRIMARY KEY, database_id text NOT NULL REFERENCES managed_databases(id), identity_id text NOT NULL REFERENCES identities(id),
 revision bigint NOT NULL, kind text NOT NULL, payload jsonb NOT NULL, expires_at timestamptz NOT NULL, consumed_at timestamptz
);
CREATE INDEX managed_database_review_expiry ON managed_database_reviews(expires_at);
CREATE TABLE managed_database_backup_verifications (
 artifact_id text PRIMARY KEY REFERENCES backup_artifacts(id), database_id text NOT NULL REFERENCES managed_databases(id),
 revision bigint NOT NULL, captured_at timestamptz NOT NULL, verified_at timestamptz NOT NULL, sha256 text NOT NULL
);
ALTER TABLE backup_artifacts ADD COLUMN source_revision bigint NOT NULL DEFAULT 0;
ALTER TABLE backup_artifacts ADD COLUMN captured_at timestamptz;
ALTER TABLE backup_artifacts ADD COLUMN verified_at timestamptz;
INSERT INTO schema_migrations(version) VALUES(46);
