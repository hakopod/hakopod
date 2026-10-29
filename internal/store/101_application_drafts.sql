CREATE TABLE application_drafts (
 application_id text PRIMARY KEY REFERENCES applications(id) ON DELETE CASCADE,
 base_revision bigint NOT NULL CHECK(base_revision>0),
 revision bigint NOT NULL CHECK(revision>0),
 base_spec jsonb NOT NULL,
 spec jsonb NOT NULL,
 provenance jsonb NOT NULL DEFAULT '{}',
 entries jsonb NOT NULL DEFAULT '[]',
 created_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now(),
 CHECK(octet_length(spec::text)<=524288 AND octet_length(base_spec::text)<=524288 AND octet_length(entries::text)<=131072)
);
