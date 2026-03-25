CREATE TABLE IF NOT EXISTS schema_migrations(version integer PRIMARY KEY, applied_at timestamptz NOT NULL DEFAULT now());
CREATE TABLE identities (
 id text PRIMARY KEY, name text NOT NULL, admin boolean NOT NULL DEFAULT false,
 disabled boolean NOT NULL DEFAULT false, project text NOT NULL DEFAULT '', environment text NOT NULL DEFAULT '',
 permissions text[] NOT NULL DEFAULT '{}', created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE api_keys (
 id text PRIMARY KEY, identity_id text NOT NULL REFERENCES identities(id), name text NOT NULL,
 digest bytea NOT NULL, prefix text NOT NULL, project text NOT NULL DEFAULT '', environment text NOT NULL DEFAULT '', application text NOT NULL DEFAULT '',
 permissions text[] NOT NULL, expires_at timestamptz NOT NULL, revoked_at timestamptz, last_used_at timestamptz,
 created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE projects (name text PRIMARY KEY, created_at timestamptz NOT NULL DEFAULT now());
CREATE TABLE environments (project text NOT NULL REFERENCES projects(name), name text NOT NULL, PRIMARY KEY(project,name));
CREATE TABLE applications (
 id text PRIMARY KEY, project text NOT NULL, environment text NOT NULL, name text NOT NULL,
 revision bigint NOT NULL DEFAULT 0, status text NOT NULL DEFAULT 'pending', spec jsonb NOT NULL,
 observed jsonb NOT NULL DEFAULT '{}', created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(project,environment) REFERENCES environments(project,name), UNIQUE(project,environment,name)
);
CREATE TABLE deployments (
 id text PRIMARY KEY, application_id text NOT NULL REFERENCES applications(id), identity_id text NOT NULL REFERENCES identities(id),
 key_id text NOT NULL REFERENCES api_keys(id), idempotency_key text NOT NULL, request_hash bytea NOT NULL,
 revision bigint NOT NULL, status text NOT NULL DEFAULT 'queued' CHECK(status IN ('queued','running','succeeded','failed','cancelled','superseded')),
 spec jsonb NOT NULL, resolved_spec jsonb, result jsonb NOT NULL DEFAULT '{}', error text NOT NULL DEFAULT '',
 cancel_requested boolean NOT NULL DEFAULT false, attempts integer NOT NULL DEFAULT 0,
 created_at timestamptz NOT NULL DEFAULT now(), started_at timestamptz, finished_at timestamptz,
 UNIQUE(identity_id,idempotency_key), UNIQUE(application_id,revision)
);
CREATE INDEX deployments_work ON deployments(created_at) WHERE status IN ('queued','running');
CREATE TABLE deployment_events (
 id bigserial PRIMARY KEY, deployment_id text NOT NULL REFERENCES deployments(id), time timestamptz NOT NULL DEFAULT now(),
 type text NOT NULL, message text NOT NULL, service text NOT NULL DEFAULT ''
);
CREATE INDEX deployment_events_lookup ON deployment_events(deployment_id,id);
CREATE TABLE audit_events (
 id bigserial PRIMARY KEY, identity_id text NOT NULL, key_id text NOT NULL DEFAULT '', action text NOT NULL,
 resource text NOT NULL, time timestamptz NOT NULL DEFAULT now(), metadata jsonb NOT NULL DEFAULT '{}'
);
INSERT INTO schema_migrations(version) VALUES(1);
