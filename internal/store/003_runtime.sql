CREATE TABLE runtime_resources (
 kind text NOT NULL, project text NOT NULL DEFAULT '', environment text NOT NULL DEFAULT '', name text NOT NULL,
 revision bigint NOT NULL CHECK(revision>0), metadata jsonb NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(kind,project,environment,name), CHECK(octet_length(metadata::text)<=65536)
);
CREATE TABLE runtime_resource_history (
 id bigserial PRIMARY KEY, kind text NOT NULL, project text NOT NULL, environment text NOT NULL, name text NOT NULL,
 revision bigint NOT NULL, identity_id text NOT NULL, metadata jsonb NOT NULL, created_at timestamptz NOT NULL DEFAULT now(),
 UNIQUE(kind,project,environment,name,revision)
);
