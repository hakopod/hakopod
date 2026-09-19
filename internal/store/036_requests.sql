CREATE TABLE request_entries (
 id text PRIMARY KEY, observed_at timestamptz NOT NULL,
 application_id text REFERENCES applications(id) ON DELETE CASCADE,
 service text NOT NULL, method text NOT NULL, host text NOT NULL, path text NOT NULL,
 status integer NOT NULL, duration_ms bigint NOT NULL, payload jsonb NOT NULL
);
CREATE INDEX request_entries_time ON request_entries(observed_at DESC,id DESC);
CREATE INDEX request_entries_app ON request_entries(application_id,service,observed_at DESC,id DESC);
CREATE TABLE request_sources (
 id text PRIMARY KEY, cursor_at timestamptz NOT NULL, updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE request_collection (
 singleton boolean PRIMARY KEY DEFAULT true CHECK(singleton),
 state text NOT NULL DEFAULT 'starting', message text NOT NULL DEFAULT '',
 checked_at timestamptz NOT NULL DEFAULT now(), last_success_at timestamptz,
 gap_count bigint NOT NULL DEFAULT 0
);
INSERT INTO request_collection(singleton) VALUES(true);
