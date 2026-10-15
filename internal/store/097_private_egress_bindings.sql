-- Administrator-approved private destinations, read on every plan, accept and
-- reconcile. Validation of the combined file and database set happens in Go.
CREATE TABLE private_egress_bindings (
  name text PRIMARY KEY CHECK (name ~ '^[a-z]([a-z0-9-]{0,38}[a-z0-9])?$'),
  revision bigint NOT NULL CHECK (revision > 0),
  config jsonb NOT NULL CHECK (octet_length(config::text) <= 16384),
  created_by text NOT NULL DEFAULT '',
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);
