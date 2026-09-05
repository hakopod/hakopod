CREATE TABLE showcase (
  singleton boolean PRIMARY KEY DEFAULT true CHECK(singleton),
  id text NOT NULL UNIQUE,
  identity_id text NOT NULL REFERENCES identities(id),
  grant_id text NOT NULL REFERENCES api_keys(id),
  spec jsonb NOT NULL,
  state text NOT NULL DEFAULT 'queued' CHECK(state IN ('queued','accepted','removing','removed','blocked','skipped')),
  revision bigint NOT NULL DEFAULT 1,
  application_id text NOT NULL DEFAULT '',
  deployment_id text NOT NULL DEFAULT '',
  remove_application_revision bigint NOT NULL DEFAULT 0,
  message text NOT NULL DEFAULT 'The sample shop is waiting for deployment.',
  attempts integer NOT NULL DEFAULT 0,
  next_attempt_at timestamptz NOT NULL DEFAULT now(),
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);
-- The durable marker deliberately has no application foreign key. Removing the
-- sample must not erase the fact that this installation already seeded it.
