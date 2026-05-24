CREATE TABLE IF NOT EXISTS installation_settings (
 name text PRIMARY KEY, value jsonb NOT NULL, revision bigint NOT NULL DEFAULT 1,
 updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE IF NOT EXISTS application_sources (
 application_id text PRIMARY KEY REFERENCES applications(id), repository text NOT NULL,
 branch text NOT NULL, path text NOT NULL, auto_deploy boolean NOT NULL DEFAULT false,
 grant_id text NOT NULL REFERENCES api_keys(id), revision bigint NOT NULL DEFAULT 1,
 last_commit text NOT NULL DEFAULT '', last_deployment text NOT NULL DEFAULT '',
 last_error text NOT NULL DEFAULT '', updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE IF NOT EXISTS source_jobs (
 id text PRIMARY KEY, application_id text NOT NULL REFERENCES applications(id),
 source_revision bigint NOT NULL, commit_sha text NOT NULL,
 delivery_id text NOT NULL, status text NOT NULL DEFAULT 'queued',
 created_at timestamptz NOT NULL DEFAULT now(), claimed_at timestamptz,
 attempts integer NOT NULL DEFAULT 0, next_attempt_at timestamptz NOT NULL DEFAULT now(),
 finished_at timestamptz, error text NOT NULL DEFAULT '',
 UNIQUE(application_id,delivery_id)
);
CREATE INDEX IF NOT EXISTS source_jobs_pending ON source_jobs(created_at) WHERE status IN ('queued','running');
INSERT INTO schema_migrations(version) VALUES(4) ON CONFLICT DO NOTHING;
