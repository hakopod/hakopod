CREATE TABLE service_transfers (
 id text PRIMARY KEY REFERENCES deployments(id) ON DELETE CASCADE,
 source_id text NOT NULL REFERENCES applications(id) ON DELETE CASCADE,
 destination_id text NOT NULL REFERENCES applications(id) ON DELETE CASCADE,
 source_revision bigint NOT NULL,
 destination_revision bigint NOT NULL,
 service text NOT NULL,
 destination_service text NOT NULL,
 removal_id text REFERENCES deployments(id) ON DELETE CASCADE,
 created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX service_transfers_source ON service_transfers(source_id,created_at DESC);
