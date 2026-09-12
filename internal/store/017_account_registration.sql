ALTER TABLE identities ADD COLUMN onboarding_required boolean NOT NULL DEFAULT false;
CREATE TABLE personal_workspaces (
 identity_id text PRIMARY KEY REFERENCES identities(id),
 project text NOT NULL UNIQUE REFERENCES projects(name) ON DELETE CASCADE
);
CREATE TABLE auth_mail_limits (
 digest bytea PRIMARY KEY,
 window_start timestamptz NOT NULL,
 last_sent timestamptz NOT NULL,
 attempts integer NOT NULL
);
CREATE INDEX auth_mail_limits_expiry ON auth_mail_limits(window_start);
