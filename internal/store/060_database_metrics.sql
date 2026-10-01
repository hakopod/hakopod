CREATE TABLE managed_database_metric_samples (
 database_id text NOT NULL REFERENCES managed_databases(id) ON DELETE CASCADE,
 bucket timestamptz NOT NULL,
 point jsonb NOT NULL CHECK (octet_length(point::text)<=8192),
 PRIMARY KEY(database_id,bucket)
);
CREATE INDEX managed_database_metric_samples_expiry ON managed_database_metric_samples(bucket);
INSERT INTO schema_migrations(version) VALUES(60);
