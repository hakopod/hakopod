CREATE TABLE advisory_feed_state (
  source text PRIMARY KEY CHECK (source IN ('hakopod')),
  body bytea,
  etag text NOT NULL DEFAULT '',
  revision bigint NOT NULL DEFAULT 0,
  issued_at timestamptz,
  expires_at timestamptz,
  checked_at timestamptz,
  next_check_at timestamptz NOT NULL DEFAULT now(),
  lease_owner text NOT NULL DEFAULT '',
  lease_until timestamptz,
  failures integer NOT NULL DEFAULT 0,
  error text NOT NULL DEFAULT ''
);
INSERT INTO advisory_feed_state(source) VALUES ('hakopod');

CREATE TABLE osv_cache (
  runtime text NOT NULL,
  version text NOT NULL,
  body jsonb NOT NULL,
  checked_at timestamptz NOT NULL,
  PRIMARY KEY (runtime, version)
);

CREATE TABLE advisory_scanner (
  id smallint PRIMARY KEY CHECK (id = 1),
  lease_owner text NOT NULL DEFAULT '',
  lease_until timestamptz
);
INSERT INTO advisory_scanner(id) VALUES (1);

CREATE TABLE application_security (
  application_id text PRIMARY KEY REFERENCES applications(id) ON DELETE CASCADE,
  deployment_id text NOT NULL DEFAULT '',
  revision bigint NOT NULL DEFAULT 0,
  inventory jsonb,
  findings jsonb NOT NULL DEFAULT '[]',
  feed_revision bigint NOT NULL DEFAULT 0,
  osv_checked_at timestamptz,
  checked_at timestamptz,
  evaluated_at timestamptz,
  next_evaluation_at timestamptz,
  evaluation_failures integer NOT NULL DEFAULT 0,
  auto_patch boolean NOT NULL DEFAULT false,
  approved_by text NOT NULL DEFAULT '',
  grant_id text NOT NULL DEFAULT '',
  policy_revision bigint NOT NULL DEFAULT 1,
  updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX application_security_evaluated ON application_security(evaluated_at);

CREATE TABLE security_patch_attempts (
  id text PRIMARY KEY,
  application_id text NOT NULL REFERENCES applications(id) ON DELETE CASCADE,
  advisory_id text NOT NULL,
  advisory_revision bigint NOT NULL,
  service text NOT NULL,
  from_deployment_id text NOT NULL,
  deployment_id text NOT NULL DEFAULT '',
  status text NOT NULL CHECK (status IN ('pending','accepted','verified','failed','superseded')),
  error text NOT NULL DEFAULT '',
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX security_patch_attempts_app ON security_patch_attempts(application_id, created_at DESC);
CREATE INDEX security_patch_attempts_open ON security_patch_attempts(created_at) WHERE status IN ('pending','accepted');
