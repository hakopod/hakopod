ALTER TABLE actions_pools ADD COLUMN next_reconcile_at timestamptz NOT NULL DEFAULT now();
ALTER TABLE actions_pools ADD COLUMN last_served_at timestamptz NOT NULL DEFAULT now();
CREATE INDEX actions_pools_due ON actions_pools(next_reconcile_at, application_id, service);
CREATE INDEX actions_pools_fair ON actions_pools(last_served_at, next_reconcile_at, application_id, service);
