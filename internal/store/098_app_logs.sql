-- Index of historic application log segments, the pods that served each app,
-- collector cursors and the collector lease. Log text never lives here.
CREATE TABLE app_log_segments (
  id text PRIMARY KEY,
  application_id text NOT NULL REFERENCES applications(id) ON DELETE CASCADE,
  service text NOT NULL,
  pod_uid text NOT NULL,
  pod_name text NOT NULL,
  container text NOT NULL,
  restart integer NOT NULL,
  revision bigint NOT NULL DEFAULT 0,
  node text NOT NULL DEFAULT '',
  path text NOT NULL,
  first_time timestamptz NOT NULL,
  last_time timestamptz NOT NULL,
  lines bigint NOT NULL DEFAULT 0 CHECK (lines >= 0),
  bytes bigint NOT NULL DEFAULT 0 CHECK (bytes >= 0),
  sealed boolean NOT NULL DEFAULT false,
  updated_at timestamptz NOT NULL DEFAULT now(),
  CHECK (last_time >= first_time)
);
CREATE INDEX app_log_segments_first ON app_log_segments (application_id, first_time);
CREATE INDEX app_log_segments_last ON app_log_segments (application_id, last_time);
CREATE INDEX app_log_segments_last_time ON app_log_segments (last_time);

CREATE TABLE app_log_pods (
  pod_uid text PRIMARY KEY,
  application_id text NOT NULL REFERENCES applications(id) ON DELETE CASCADE,
  service text NOT NULL,
  pod_name text NOT NULL,
  revision bigint NOT NULL DEFAULT 0,
  node text NOT NULL DEFAULT '',
  source text NOT NULL CHECK (source IN ('file','api')),
  started_at timestamptz NOT NULL,
  ended_at timestamptz,
  end_reason text NOT NULL DEFAULT '' CHECK (end_reason IN ('','completed','error','oom_killed','evicted','replaced','deleted')),
  restarts integer NOT NULL DEFAULT 0,
  gap_seconds integer NOT NULL DEFAULT 0 CHECK (gap_seconds >= 0),
  gap_note text NOT NULL DEFAULT '' CHECK (char_length(gap_note) <= 200),
  last_seen timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX app_log_pods_app_started ON app_log_pods (application_id, started_at DESC);

CREATE TABLE app_log_cursors (
  key text PRIMARY KEY,
  pod_uid text NOT NULL,
  cursor jsonb NOT NULL,
  updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE app_log_collector (
  id smallint PRIMARY KEY CHECK (id = 1),
  lease_owner text NOT NULL DEFAULT '',
  lease_until timestamptz
);
INSERT INTO app_log_collector(id) VALUES (1);

INSERT INTO installation_settings(name,value) VALUES ('app_logs','{"enabled":true,"retention_days":7,"max_mib_per_app":512}') ON CONFLICT (name) DO NOTHING;
