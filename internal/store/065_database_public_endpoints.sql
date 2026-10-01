CREATE TABLE managed_database_public_endpoints (
 id text PRIMARY KEY, database_id text NOT NULL REFERENCES managed_databases(id), revision bigint NOT NULL DEFAULT 0 CHECK(revision>=0),
 spec jsonb NOT NULL, allocation jsonb NOT NULL,
 status text NOT NULL DEFAULT 'review' CHECK(status IN ('review','pending','active','revoking','error','revoked')),
 observation jsonb NOT NULL DEFAULT '{}',
 created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now(), revoked_at timestamptz,
 CHECK(jsonb_typeof(spec)='object' AND jsonb_typeof(allocation)='object'),
 CHECK(jsonb_typeof(allocation->'port')='number' AND (allocation->>'port')::integer BETWEEN 1 AND 65535),
 CHECK((status='revoked')=(revoked_at IS NOT NULL))
);
CREATE UNIQUE INDEX managed_database_public_endpoint_purpose
 ON managed_database_public_endpoints(database_id,(spec->>'purpose')) WHERE revoked_at IS NULL;
CREATE UNIQUE INDEX managed_database_public_endpoint_host
 ON managed_database_public_endpoints((allocation->>'host')) WHERE revoked_at IS NULL;
CREATE UNIQUE INDEX managed_database_public_endpoint_address_port
 ON managed_database_public_endpoints((allocation->>'address'),((allocation->>'port')::integer)) WHERE revoked_at IS NULL;

CREATE TABLE managed_database_public_endpoint_reviews (
 id text PRIMARY KEY, endpoint_id text NOT NULL REFERENCES managed_database_public_endpoints(id),
 database_id text NOT NULL REFERENCES managed_databases(id), identity_id text NOT NULL REFERENCES identities(id),
 database_revision bigint NOT NULL, endpoint_revision bigint NOT NULL, payload jsonb NOT NULL,
 expires_at timestamptz NOT NULL, consumed_at timestamptz
);
CREATE INDEX managed_database_public_endpoint_review_expiry ON managed_database_public_endpoint_reviews(expires_at);

CREATE TABLE managed_database_public_endpoint_operations (
 id text PRIMARY KEY, endpoint_id text NOT NULL REFERENCES managed_database_public_endpoints(id),
 database_id text NOT NULL REFERENCES managed_databases(id), revision bigint NOT NULL,
 identity_id text NOT NULL REFERENCES identities(id), key_id text NOT NULL REFERENCES api_keys(id),
 idempotency_key text NOT NULL, request_hash bytea NOT NULL, kind text NOT NULL CHECK(kind IN ('publish','revoke')),
 review jsonb, next_attempt_at timestamptz NOT NULL DEFAULT now(),
 status text NOT NULL DEFAULT 'queued' CHECK(status IN ('queued','running','succeeded','failed','cancelled')),
 phase text NOT NULL DEFAULT 'accepted', message text NOT NULL DEFAULT '', lease text NOT NULL DEFAULT '', lease_until timestamptz,
 created_at timestamptz NOT NULL DEFAULT now(), started_at timestamptz, finished_at timestamptz,
 UNIQUE(identity_id,idempotency_key), UNIQUE(endpoint_id,revision),
 CHECK((kind='publish')=(review IS NOT NULL))
);
CREATE UNIQUE INDEX managed_database_public_endpoint_active_operation
 ON managed_database_public_endpoint_operations(endpoint_id) WHERE status IN ('queued','running');
CREATE INDEX managed_database_public_endpoint_work
 ON managed_database_public_endpoint_operations(next_attempt_at,created_at) WHERE status IN ('queued','running');

INSERT INTO schema_migrations(version) VALUES(65);
