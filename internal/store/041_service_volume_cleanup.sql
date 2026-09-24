CREATE TABLE deployment_volume_cleanup (
 deployment_id text PRIMARY KEY REFERENCES deployments(id) ON DELETE CASCADE,
 key_id text NOT NULL REFERENCES api_keys(id),
 claims text[] NOT NULL,
 completed boolean NOT NULL DEFAULT false,
 error text NOT NULL DEFAULT '',
 attempted_at timestamptz
);
