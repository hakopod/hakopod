CREATE TABLE installation_login_providers (
 provider text PRIMARY KEY CHECK (provider IN ('github','google','gitlab','oidc')),
 revision bigint NOT NULL CHECK (revision > 0),
 configuration bytea NOT NULL,
 updated_at timestamptz NOT NULL DEFAULT now()
);
