-- A migration of one Railway or Vercel project into a Hakopod application.
-- The source token and any values the user typed for unreadable variables are
-- sealed with the installation auth key and set to NULL as soon as the
-- migration finishes, fails or is cancelled, or once expires_at passes.
CREATE TABLE platform_migrations (
 id text PRIMARY KEY,
 identity_id text NOT NULL REFERENCES identities(id),
 project text NOT NULL,
 environment text NOT NULL,
 application_name text NOT NULL CHECK(length(application_name)<=63),
 provider text NOT NULL CHECK(provider IN ('railway','vercel')),
 source_project_id text NOT NULL CHECK(length(source_project_id)<=128),
 source_project_name text NOT NULL DEFAULT '' CHECK(length(source_project_name)<=256),
 source_environment_id text NOT NULL DEFAULT '' CHECK(length(source_environment_id)<=128),
 source_team_id text NOT NULL DEFAULT '' CHECK(length(source_team_id)<=128),
 token_sealed bytea,
 variables_sealed bytea,
 proposal jsonb NOT NULL CHECK(octet_length(proposal::text)<1048576),
 status text NOT NULL DEFAULT 'planned' CHECK(status IN ('planned','queued','running','succeeded','failed','cancelled')),
 steps jsonb NOT NULL DEFAULT '[]' CHECK(octet_length(steps::text)<262144),
 error text NOT NULL DEFAULT '' CHECK(length(error)<=2048),
 application_id text NOT NULL DEFAULT '',
 idempotency_key text NOT NULL DEFAULT '',
 cancel_requested boolean NOT NULL DEFAULT false,
 lease text NOT NULL DEFAULT '',
 lease_until timestamptz,
 created_at timestamptz NOT NULL DEFAULT now(),
 started_at timestamptz,
 finished_at timestamptz,
 expires_at timestamptz NOT NULL
);
CREATE INDEX platform_migrations_scope ON platform_migrations(project, environment, created_at DESC);
CREATE INDEX platform_migrations_queue ON platform_migrations(created_at) WHERE status IN ('queued','running');
CREATE INDEX platform_migrations_secrets ON platform_migrations(expires_at) WHERE token_sealed IS NOT NULL OR variables_sealed IS NOT NULL;
