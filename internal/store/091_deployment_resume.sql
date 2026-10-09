ALTER TABLE deployments ADD COLUMN resume_generation integer NOT NULL DEFAULT 0;
ALTER TABLE deployments ADD COLUMN resume_services text[] NOT NULL DEFAULT '{}';
ALTER TABLE deployments ADD COLUMN resume_identity_id text NOT NULL DEFAULT '';
ALTER TABLE deployments ADD COLUMN resume_key_id text NOT NULL DEFAULT '';
CREATE TABLE deployment_resume_attempts (
  deployment_id text NOT NULL REFERENCES deployments(id) ON DELETE CASCADE,
  generation integer NOT NULL,
  identity_id text NOT NULL,
  key_id text NOT NULL,
  idempotency_key text NOT NULL,
  request_hash bytea NOT NULL,
  services text[] NOT NULL DEFAULT '{}',
  status text NOT NULL CHECK (status IN ('queued','running','succeeded','failed','cancelled')),
  error text NOT NULL DEFAULT '',
  result jsonb NOT NULL DEFAULT '{}'::jsonb,
  created_at timestamptz NOT NULL DEFAULT now(),
  started_at timestamptz,
  finished_at timestamptz,
  PRIMARY KEY(deployment_id,generation),
  UNIQUE(identity_id,idempotency_key)
);
INSERT INTO schema_migrations(version) VALUES(91);
