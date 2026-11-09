-- A GitHub pool may run each job on a single-use cloud VM. instance_reserved_at
-- holds the slot's place under its capacity's max_instances before the cloud
-- is called; the instance is recorded once launched.
ALTER TABLE actions_slots
 ADD COLUMN instance_id text NOT NULL DEFAULT '' CHECK(length(instance_id)<=64),
 ADD COLUMN instance_type text NOT NULL DEFAULT '' CHECK(length(instance_type)<=64),
 ADD COLUMN instance_lifecycle text NOT NULL DEFAULT '' CHECK(instance_lifecycle IN ('','spot','on-demand')),
 ADD COLUMN instance_reserved_at timestamptz;
CREATE INDEX actions_slots_capacity ON actions_slots((config->'actions'->>'capacity')) WHERE instance_reserved_at IS NOT NULL;
-- Each rerun after a spot interruption grants its pool one on-demand launch.
ALTER TABLE actions_pools ADD COLUMN on_demand_launches integer NOT NULL DEFAULT 0 CHECK(on_demand_launches BETWEEN 0 AND 100);
-- One row per spot-interrupted instance. The run is filled in from the
-- workflow_job delivery that reports the lost job; rerun_at marks the rerun.
CREATE TABLE actions_evictions (
 slot_id text PRIMARY KEY CHECK(slot_id ~ '^[a-f0-9]{32}$'),
 application_id text NOT NULL,
 service text NOT NULL,
 capacity text NOT NULL,
 instance_id text NOT NULL,
 instance_type text NOT NULL,
 evicted_at timestamptz NOT NULL DEFAULT now(),
 repository text NOT NULL DEFAULT '',
 run_id bigint NOT NULL DEFAULT 0,
 run_attempt bigint NOT NULL DEFAULT 0,
 rerun_at timestamptz,
 rerun_error text NOT NULL DEFAULT '' CHECK(length(rerun_error)<=512),
 FOREIGN KEY(application_id,service) REFERENCES actions_pools(application_id,service) ON DELETE CASCADE
);
CREATE INDEX actions_evictions_type ON actions_evictions(capacity,instance_type,evicted_at);
CREATE INDEX actions_evictions_run ON actions_evictions(repository,run_id);
CREATE INDEX actions_evictions_pending ON actions_evictions(evicted_at) WHERE run_id>0 AND rerun_at IS NULL;
