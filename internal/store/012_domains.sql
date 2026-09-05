CREATE TABLE domain_verifications (
 application_id text NOT NULL REFERENCES applications(id) ON DELETE CASCADE,
 hostname text NOT NULL,
 service text NOT NULL,
 token text NOT NULL,
 verified_at timestamptz,
 created_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(application_id,hostname)
);
CREATE TABLE application_domains (
 hostname text PRIMARY KEY,
 application_id text NOT NULL REFERENCES applications(id) ON DELETE CASCADE,
 created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX application_domains_app ON application_domains(application_id);
INSERT INTO schema_migrations(version) VALUES(12);
