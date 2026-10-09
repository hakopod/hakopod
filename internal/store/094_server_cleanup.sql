CREATE TABLE server_cleanup_reviews (
 id text PRIMARY KEY CHECK(length(id)=32), identity_id text NOT NULL REFERENCES identities(id), key_id text NOT NULL REFERENCES api_keys(id),
 inventory jsonb NOT NULL CHECK(octet_length(inventory::text)<=262144), expires_at timestamptz NOT NULL,
 consumed_at timestamptz, created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE server_cleanup_operations (
 id text PRIMARY KEY CHECK(length(id)=32), review_id text NOT NULL UNIQUE REFERENCES server_cleanup_reviews(id),
 identity_id text NOT NULL REFERENCES identities(id), key_id text NOT NULL REFERENCES api_keys(id),
 idempotency_key text NOT NULL CHECK(length(idempotency_key) BETWEEN 8 AND 128), status text NOT NULL CHECK(status IN ('running','succeeded','failed')),
 receipt jsonb NOT NULL DEFAULT '{}' CHECK(octet_length(receipt::text)<=262144), created_at timestamptz NOT NULL DEFAULT now(), finished_at timestamptz,
 UNIQUE(identity_id,idempotency_key)
);
CREATE INDEX server_cleanup_review_expiry ON server_cleanup_reviews(expires_at) WHERE consumed_at IS NULL;

