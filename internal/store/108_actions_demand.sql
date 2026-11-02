-- Scale-to-zero GitHub pools wake from verified workflow_job deliveries.
-- Each row is one queued job that a matching pool may pick up. Rows are
-- removed when the job starts or finishes, and expire with GitHub's 24-hour
-- queue limit.
ALTER TABLE actions_pools ADD COLUMN active_at timestamptz NOT NULL DEFAULT now();
CREATE TABLE actions_demand (
 application_id text NOT NULL,
 service text NOT NULL,
 job_id bigint NOT NULL CHECK(job_id > 0),
 queued_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(application_id,service,job_id),
 FOREIGN KEY(application_id,service) REFERENCES actions_pools(application_id,service) ON DELETE CASCADE
);
CREATE INDEX actions_demand_job ON actions_demand(job_id);
