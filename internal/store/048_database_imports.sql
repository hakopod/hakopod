CREATE TABLE backup_imports (
 id text PRIMARY KEY, identity_id text NOT NULL REFERENCES identities(id), key_id text NOT NULL REFERENCES api_keys(id),
 idempotency_key text NOT NULL, request_hash bytea NOT NULL, spec jsonb NOT NULL CHECK(octet_length(spec::text)<8192),
 destination_revision bigint NOT NULL, status text NOT NULL DEFAULT 'pending' CHECK(status IN ('pending','uploading','cleaning','completed','expired')),
 lease text NOT NULL DEFAULT '', lease_until timestamptz, expires_at timestamptz NOT NULL,
 artifact_id text NOT NULL DEFAULT '', created_at timestamptz NOT NULL DEFAULT now(), UNIQUE(identity_id,idempotency_key)
);
CREATE INDEX backup_import_expiry ON backup_imports(expires_at) WHERE status<>'completed';
INSERT INTO schema_migrations(version) VALUES(48);
