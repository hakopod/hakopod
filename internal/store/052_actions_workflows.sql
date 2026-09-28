CREATE TABLE actions_jobs (
    slot_id text PRIMARY KEY,
    application_id text NOT NULL REFERENCES applications(id) ON DELETE CASCADE,
    service text NOT NULL,
    runner_id bigint NOT NULL,
    observation jsonb NOT NULL,
    provider_job jsonb,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX actions_jobs_pool ON actions_jobs(application_id, service, created_at DESC, slot_id);
