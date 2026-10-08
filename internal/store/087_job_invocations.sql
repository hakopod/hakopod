CREATE TABLE job_invocations (
 id text PRIMARY KEY, application_id text NOT NULL REFERENCES applications(id) ON DELETE CASCADE,
 deployment_id text NOT NULL REFERENCES deployments(id) ON DELETE CASCADE, service text NOT NULL,
 revision bigint NOT NULL, image text NOT NULL, correlation_id text NOT NULL,
 identity_id text NOT NULL REFERENCES identities(id), key_id text NOT NULL REFERENCES api_keys(id),
 owner_hash text NOT NULL, idempotency_key text NOT NULL, input_hash text NOT NULL,
 encrypted_input bytea NOT NULL, input_bytes integer NOT NULL CHECK(input_bytes BETWEEN 0 AND 131072),
 encrypted_logs bytea, log_truncated boolean NOT NULL DEFAULT false,
 status text NOT NULL DEFAULT 'queued' CHECK(status IN ('queued','starting','running','succeeded','failed','cancelled')),
 message text NOT NULL DEFAULT '', cancel_requested boolean NOT NULL DEFAULT false,
 cleanup_pending boolean NOT NULL DEFAULT false, exit_code integer,
 namespace_uid text NOT NULL DEFAULT '', runtime_uid text NOT NULL DEFAULT '',
 lease_token text NOT NULL DEFAULT '', lease_until timestamptz NOT NULL DEFAULT now(),
 source jsonb NOT NULL, created_at timestamptz NOT NULL DEFAULT now(), started_at timestamptz,
 finished_at timestamptz, expires_at timestamptz NOT NULL DEFAULT now()+interval '7 days',
 UNIQUE(identity_id,application_id,service,owner_hash,revision,idempotency_key)
);
CREATE INDEX job_invocations_work ON job_invocations(created_at) WHERE status IN ('queued','starting','running') OR cleanup_pending;
CREATE INDEX job_invocations_owner ON job_invocations(identity_id,application_id,service,owner_hash,correlation_id,created_at DESC);
CREATE UNIQUE INDEX job_invocations_active_app ON job_invocations(application_id) WHERE status IN ('starting','running') OR cleanup_pending;
