CREATE TABLE binding_test_evidence (
  application_id text NOT NULL REFERENCES applications(id) ON DELETE CASCADE,
  service text NOT NULL CHECK (length(service) BETWEEN 1 AND 63),
  variable text NOT NULL CHECK (length(variable) BETWEEN 1 AND 253),
  revision bigint NOT NULL CHECK (revision > 0),
  observed_at timestamptz NOT NULL,
  result jsonb NOT NULL CHECK (octet_length(result::text) <= 16384),
  runtime_evidence jsonb NOT NULL CHECK (octet_length(runtime_evidence::text) <= 2048),
  PRIMARY KEY (application_id, service, variable)
);
CREATE INDEX binding_test_evidence_recent ON binding_test_evidence(application_id, observed_at DESC);
