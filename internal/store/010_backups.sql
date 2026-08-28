CREATE TABLE backup_destinations (
 id text PRIMARY KEY, name text NOT NULL, revision bigint NOT NULL CHECK(revision>0),
 config jsonb NOT NULL CHECK(octet_length(config::text)<16384),
 credentials bytea NOT NULL CHECK(octet_length(credentials)<16384),
 created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE backup_schedules (
 id text PRIMARY KEY, name text NOT NULL, destination_id text NOT NULL REFERENCES backup_destinations(id),
 source jsonb NOT NULL CHECK(octet_length(source::text)<4096),
 interval_hours integer NOT NULL CHECK(interval_hours BETWEEN 1 AND 8760),
 retention_count integer NOT NULL CHECK(retention_count BETWEEN 1 AND 100),
 enabled boolean NOT NULL, revision bigint NOT NULL CHECK(revision>0), identity_id text NOT NULL,
 next_run_at timestamptz NOT NULL, created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE backup_jobs (
 id text PRIMARY KEY, kind text NOT NULL CHECK(kind IN ('backup','restore')),
 status text NOT NULL CHECK(status IN ('queued','running','succeeded','failed','cancelled')),
 destination_id text NOT NULL, source jsonb NOT NULL CHECK(octet_length(source::text)<4096),
 target jsonb CHECK(octet_length(target::text)<8192), artifact_id text NOT NULL DEFAULT '', schedule_id text NOT NULL DEFAULT '',
 identity_id text NOT NULL, key_id text NOT NULL DEFAULT '', idempotency_key text NOT NULL, request_hash text NOT NULL,
 error text NOT NULL DEFAULT '', bytes bigint NOT NULL DEFAULT 0,
 cancel_requested boolean NOT NULL DEFAULT false, lease text NOT NULL DEFAULT '', lease_until timestamptz,
 created_at timestamptz NOT NULL DEFAULT now(), started_at timestamptz, finished_at timestamptz,
 UNIQUE(identity_id,idempotency_key)
);
CREATE INDEX backup_jobs_queue ON backup_jobs(status,created_at);
CREATE INDEX backup_jobs_history ON backup_jobs(created_at DESC,id DESC);
CREATE INDEX backup_jobs_finished ON backup_jobs(finished_at) WHERE finished_at IS NOT NULL;
CREATE TABLE backup_artifacts (
 id text PRIMARY KEY, job_id text NOT NULL UNIQUE, destination_id text NOT NULL,
 source jsonb NOT NULL CHECK(octet_length(source::text)<4096), object_key text NOT NULL,
 sha256 text NOT NULL CHECK(length(sha256)=64), bytes bigint NOT NULL CHECK(bytes>0),
 format text NOT NULL, scope text NOT NULL, schedule_id text NOT NULL DEFAULT '',
 created_at timestamptz NOT NULL DEFAULT now(), deleted_at timestamptz,
 deletion_pending boolean NOT NULL DEFAULT false
);
CREATE INDEX backup_artifacts_history ON backup_artifacts(created_at DESC,id DESC);
CREATE INDEX backup_artifacts_retention ON backup_artifacts(schedule_id,created_at DESC) WHERE deleted_at IS NULL;
CREATE TABLE backup_restore_plans (
 id text PRIMARY KEY, identity_id text NOT NULL, artifact_id text NOT NULL,
 plan jsonb NOT NULL CHECK(octet_length(plan::text)<16384), expires_at timestamptz NOT NULL,
 used_at timestamptz, job_id text UNIQUE REFERENCES backup_jobs(id) ON DELETE CASCADE,
 created_at timestamptz NOT NULL DEFAULT now(), CHECK((used_at IS NULL)=(job_id IS NULL))
);
CREATE INDEX backup_restore_plans_expiry ON backup_restore_plans(expires_at) WHERE used_at IS NULL;
