CREATE TABLE secret_providers (
 name text PRIMARY KEY,
 revision bigint NOT NULL CHECK (revision > 0),
 config jsonb NOT NULL,
 credentials bytea NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE secret_provider_scopes (
 provider text NOT NULL REFERENCES secret_providers(name) ON DELETE CASCADE,
 project text NOT NULL REFERENCES projects(name),
 environments text[] NOT NULL DEFAULT '{}',
 PRIMARY KEY(provider, project)
);
