CREATE TABLE external_databases (
 id text PRIMARY KEY, project text NOT NULL, environment text NOT NULL, name text NOT NULL,
 revision bigint NOT NULL CHECK(revision>0), credential_revision bigint NOT NULL CHECK(credential_revision>0),
 spec jsonb NOT NULL, credentials bytea NOT NULL, credential_digest bytea NOT NULL,
 status text NOT NULL CHECK(status IN ('pending','ready','unreachable','deleting','deleted')),
 observation jsonb NOT NULL DEFAULT '{}',
 observation_lease text NOT NULL DEFAULT '', observation_lease_until timestamptz,
 observation_next_at timestamptz NOT NULL DEFAULT now(),
 created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now(), deleted_at timestamptz,
 FOREIGN KEY(project,environment) REFERENCES environments(project,name)
);
CREATE UNIQUE INDEX external_database_live_names ON external_databases(project,environment,name) WHERE deleted_at IS NULL;
CREATE TABLE external_database_operations (
 id text PRIMARY KEY, database_id text NOT NULL REFERENCES external_databases(id), revision bigint NOT NULL,
 identity_id text NOT NULL REFERENCES identities(id), key_id text NOT NULL REFERENCES api_keys(id),
 idempotency_key text NOT NULL, request_hash bytea NOT NULL, kind text NOT NULL CHECK(kind IN ('create','update','delete')),
 spec jsonb NOT NULL, status text NOT NULL DEFAULT 'queued' CHECK(status IN ('queued','running','succeeded','failed')),
 message text NOT NULL DEFAULT '', lease text NOT NULL DEFAULT '', lease_until timestamptz,
 created_at timestamptz NOT NULL DEFAULT now(), finished_at timestamptz,
 UNIQUE(identity_id,idempotency_key), UNIQUE(database_id,revision)
);
CREATE INDEX external_database_work ON external_database_operations(created_at) WHERE status IN ('queued','running');
CREATE TABLE external_database_reviews (
 id text PRIMARY KEY, database_id text NOT NULL REFERENCES external_databases(id), identity_id text NOT NULL REFERENCES identities(id),
 revision bigint NOT NULL, kind text NOT NULL CHECK(kind IN ('connect','disconnect')), payload jsonb NOT NULL,
 expires_at timestamptz NOT NULL, consumed_at timestamptz
);
CREATE INDEX external_database_review_expiry ON external_database_reviews(expires_at);
INSERT INTO schema_migrations(version) VALUES(62);
