CREATE TABLE build_configs (
 id text PRIMARY KEY, project text NOT NULL, environment text NOT NULL,
 name text NOT NULL, service text NOT NULL, application_id text REFERENCES applications(id),
 config jsonb NOT NULL, revision bigint NOT NULL DEFAULT 1,
 installed_revision bigint NOT NULL DEFAULT 0, installed_commit text NOT NULL DEFAULT '',
 grant_id text NOT NULL REFERENCES api_keys(id), created_by text NOT NULL REFERENCES identities(id), created_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(project,environment) REFERENCES environments(project,name),
 UNIQUE(project,environment,name,service)
);
CREATE INDEX build_configs_scope ON build_configs(project,environment,created_at);
CREATE TABLE build_runs (
 id text PRIMARY KEY, build_id text NOT NULL REFERENCES build_configs(id),
 identity_id text NOT NULL REFERENCES identities(id), key_id text NOT NULL REFERENCES api_keys(id),
 idempotency_key text NOT NULL, request_hash bytea NOT NULL, config jsonb NOT NULL,
 config_revision bigint NOT NULL, commit_sha text NOT NULL, status text NOT NULL DEFAULT 'dispatching',
 github_run_id bigint NOT NULL DEFAULT 0, conclusion text NOT NULL DEFAULT '',
 image text NOT NULL DEFAULT '', run_url text NOT NULL DEFAULT '', message text NOT NULL DEFAULT '',
 deployment_id text NOT NULL DEFAULT '', automatic boolean NOT NULL DEFAULT false, auto_status text NOT NULL DEFAULT '', attempts integer NOT NULL DEFAULT 0, next_attempt_at timestamptz NOT NULL DEFAULT now(), created_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now(), UNIQUE(build_id,identity_id,idempotency_key)
);
CREATE INDEX build_runs_recent ON build_runs(build_id,created_at DESC);

CREATE INDEX build_runs_auto_pending ON build_runs(next_attempt_at,created_at) WHERE automatic AND auto_status IN ('queued','processing');
CREATE INDEX build_runs_retention ON build_runs(created_at);
