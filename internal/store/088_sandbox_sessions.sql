CREATE TABLE sandbox_sessions (
 id text PRIMARY KEY,
 application_id text NOT NULL REFERENCES applications(id),
 deployment_id text NOT NULL REFERENCES deployments(id),
 service text NOT NULL,
 revision bigint NOT NULL,
 image text NOT NULL,
 generation text NOT NULL,
 identity_id text NOT NULL,
 key_id text NOT NULL,
 owner_hash text NOT NULL,
 runtime_hash text NOT NULL,
 idempotency_key text NOT NULL,
 status text NOT NULL DEFAULT 'starting' CHECK (status IN ('starting','ready','closing','closed')),
 message text NOT NULL DEFAULT '',
 cleanup_pending boolean NOT NULL DEFAULT false,
 namespace_uid text NOT NULL DEFAULT '',
 pod_uid text NOT NULL DEFAULT '',
 container_id text NOT NULL DEFAULT '',
 image_id text NOT NULL DEFAULT '',
 source jsonb NOT NULL,
 call_token text NOT NULL DEFAULT '',
 call_request_id text NOT NULL DEFAULT '',
 call_until timestamptz NOT NULL DEFAULT '1970-01-01 UTC',
 lease_token text NOT NULL DEFAULT '',
 lease_until timestamptz NOT NULL DEFAULT '1970-01-01 UTC',
 created_at timestamptz NOT NULL DEFAULT now(),
 idle_until timestamptz NOT NULL,
 expires_at timestamptz NOT NULL,
 closed_at timestamptz,
 UNIQUE(identity_id,application_id,service,owner_hash,idempotency_key)
);
CREATE UNIQUE INDEX sandbox_session_owner ON sandbox_sessions(application_id,service,identity_id,owner_hash,runtime_hash) WHERE status <> 'closed' OR cleanup_pending;
CREATE INDEX sandbox_session_cleanup ON sandbox_sessions(lease_until,idle_until,expires_at) WHERE status <> 'closed' OR cleanup_pending;
CREATE TABLE sandbox_session_calls (
 session_id text NOT NULL REFERENCES sandbox_sessions(id) ON DELETE CASCADE,
 request_id text NOT NULL,
 input_hash text NOT NULL,
 outcome text NOT NULL DEFAULT 'started' CHECK (outcome IN ('started','complete','interrupted')),
 created_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(session_id,request_id)
);
