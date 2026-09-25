-- No cascading application foreign key: remote registration cleanup must survive
-- a crash and must complete before an application can lose its metadata.
CREATE TABLE actions_pools (
 application_id text NOT NULL,
 service text NOT NULL,
 project text NOT NULL,
 environment text NOT NULL,
 application_name text NOT NULL,
 revision bigint NOT NULL,
 config jsonb NOT NULL,
 removed boolean NOT NULL DEFAULT false,
 message text NOT NULL DEFAULT '',
 updated_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(application_id,service)
);
CREATE TABLE actions_slots (
 id text PRIMARY KEY CHECK(id ~ '^[a-f0-9]{32}$'),
 application_id text NOT NULL,
 service text NOT NULL,
 config jsonb NOT NULL,
 runner_id bigint NOT NULL DEFAULT 0,
 phase text NOT NULL DEFAULT 'intent' CHECK(phase IN ('intent','starting','online','busy','cleanup')),
 created_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(application_id,service) REFERENCES actions_pools(application_id,service)
);
CREATE INDEX actions_slots_pool ON actions_slots(application_id,service,created_at,id);
