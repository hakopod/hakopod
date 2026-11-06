-- Agent policies decide whether a machine key may run a matched operation now,
-- only after a person approves the exact request, or never. Published versions
-- are immutable; the policy row carries the editable draft and the status of
-- the current version.
CREATE TABLE agent_access_policies (
 id text PRIMARY KEY,
 name text NOT NULL CHECK(length(name) BETWEEN 1 AND 100),
 status text NOT NULL DEFAULT 'draft' CHECK(status IN ('draft','enforced','monitor')),
 current_version integer NOT NULL DEFAULT 0 CHECK(current_version >= 0),
 draft jsonb,
 revision bigint NOT NULL DEFAULT 1,
 created_by text NOT NULL REFERENCES identities(id),
 created_at timestamptz NOT NULL DEFAULT now(),
 updated_by text NOT NULL REFERENCES identities(id),
 updated_at timestamptz NOT NULL DEFAULT now(),
 CHECK((status = 'draft') = (current_version = 0))
);
CREATE TABLE agent_access_policy_versions (
 policy_id text NOT NULL REFERENCES agent_access_policies(id) ON DELETE CASCADE,
 version integer NOT NULL CHECK(version > 0),
 name text NOT NULL,
 status text NOT NULL CHECK(status IN ('enforced','monitor')),
 document jsonb NOT NULL,
 published_by text NOT NULL REFERENCES identities(id),
 published_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(policy_id,version)
);

-- One row per request held for approval. digest binds the approval to the key,
-- method, path, query and body; consumed_at makes the approval single-use.
CREATE TABLE agent_access_approvals (
 id text PRIMARY KEY,
 policy_id text NOT NULL REFERENCES agent_access_policies(id) ON DELETE CASCADE,
 policy_version integer NOT NULL,
 key_id text NOT NULL REFERENCES api_keys(id) ON DELETE CASCADE,
 identity_id text NOT NULL REFERENCES identities(id),
 operation text NOT NULL,
 category text NOT NULL,
 method text NOT NULL,
 path text NOT NULL CHECK(length(path) <= 4096),
 project text NOT NULL DEFAULT '',
 environment text NOT NULL DEFAULT '',
 digest bytea NOT NULL CHECK(length(digest) = 32),
 approvers jsonb NOT NULL DEFAULT '[]',
 state text NOT NULL DEFAULT 'pending' CHECK(state IN ('pending','approved','denied','expired','consumed')),
 decided_by text REFERENCES identities(id),
 decided_at timestamptz,
 reason text NOT NULL DEFAULT '' CHECK(length(reason) <= 500),
 expires_at timestamptz NOT NULL,
 consumed_at timestamptz,
 created_at timestamptz NOT NULL DEFAULT now()
);
-- A retried request reuses its pending approval instead of creating another.
CREATE UNIQUE INDEX agent_access_approvals_pending ON agent_access_approvals(key_id,digest) WHERE state = 'pending';
CREATE INDEX agent_access_approvals_state ON agent_access_approvals(state,created_at DESC,id DESC);

-- Every governed request records its decision. The impact preview replays only
-- these rows, so it never shows numbers for requests that were not observed.
-- Rows older than 30 days are pruned.
CREATE TABLE agent_access_evaluations (
 id bigserial PRIMARY KEY,
 observed_at timestamptz NOT NULL DEFAULT now(),
 key_id text NOT NULL,
 identity_id text NOT NULL,
 team_ids text[] NOT NULL DEFAULT '{}',
 operation text NOT NULL,
 category text NOT NULL,
 project text NOT NULL DEFAULT '',
 environment text NOT NULL DEFAULT '',
 active_incident boolean NOT NULL DEFAULT false,
 decision text NOT NULL CHECK(decision IN ('allow','block','require_approval','approved','exception','monitor_block','monitor_approval')),
 policy_id text NOT NULL DEFAULT '',
 policy_version integer NOT NULL DEFAULT 0
);
CREATE INDEX agent_access_evaluations_time ON agent_access_evaluations(observed_at DESC,id DESC);

ALTER TABLE slack_integration DROP CONSTRAINT IF EXISTS slack_integration_events_check;
ALTER TABLE slack_integration ADD CONSTRAINT slack_integration_events_check CHECK (
 cardinality(events) BETWEEN 1 AND 64 AND events <@ ARRAY[
  'alarm.opened','alarm.resolved','audit',
  'deployment.queued','deployment.started','deployment.succeeded','deployment.failed','deployment.cancelled','deployment.superseded','deployment.rollback.requested','deployment.cancellation.requested',
  'application.created','application.configuration.updated','application.renamed','application.deleted',
  'service.added','service.removed','service.renamed','service.configuration.updated','service.image.updated','service.variables.updated','service.resources.updated','service.scale.updated','service.suspended','service.resumed','service.restart.requested','service.network.updated','service.storage.updated','service.healthcheck.updated','service.placement.updated','service.command.updated','service.delivery.updated',
  'service.update.started','service.ready','service.failed','service.job.scheduled','service.job.completed','service.certificate.renewed',
  'security.advisory',
  'agent.approval.requested','agent.policy.exception'
 ]::text[]
);
ALTER TABLE slack_event_outbox DROP CONSTRAINT IF EXISTS slack_event_outbox_event_kind_check;
ALTER TABLE slack_event_outbox ADD CONSTRAINT slack_event_outbox_event_kind_check CHECK (event_kind IN (
 'alarm.opened','alarm.resolved','audit','test',
 'deployment.queued','deployment.started','deployment.succeeded','deployment.failed','deployment.cancelled','deployment.superseded','deployment.rollback.requested','deployment.cancellation.requested',
 'application.created','application.configuration.updated','application.renamed','application.deleted',
 'service.added','service.removed','service.renamed','service.configuration.updated','service.image.updated','service.variables.updated','service.resources.updated','service.scale.updated','service.suspended','service.resumed','service.restart.requested','service.network.updated','service.storage.updated','service.healthcheck.updated','service.placement.updated','service.command.updated','service.delivery.updated',
 'service.update.started','service.ready','service.failed','service.job.scheduled','service.job.completed','service.certificate.renewed',
 'security.advisory',
 'agent.approval.requested','agent.policy.exception'
));
