CREATE TABLE alarm_settings (
 project text NOT NULL DEFAULT '', environment text NOT NULL DEFAULT '', application_id text NOT NULL DEFAULT '',
 enabled boolean NOT NULL DEFAULT true, hold_seconds integer NOT NULL DEFAULT 120 CHECK(hold_seconds BETWEEN 0 AND 3600),
 email_enabled boolean NOT NULL DEFAULT false, revision bigint NOT NULL DEFAULT 1,
 updated_at timestamptz NOT NULL DEFAULT now(), updated_by text NOT NULL REFERENCES identities(id),
 PRIMARY KEY(project,environment,application_id),
 CHECK(project<>'' OR (environment='' AND application_id='')),
 CHECK(application_id='' OR environment<>'')
);
CREATE TABLE alarm_incidents (
 id text PRIMARY KEY, fingerprint text NOT NULL, rule text NOT NULL,
 resource_type text NOT NULL CHECK(resource_type IN ('application','service','node')),
 resource_id text NOT NULL, resource_name text NOT NULL, project text NOT NULL DEFAULT '',
 environment text NOT NULL DEFAULT '', application_id text NOT NULL DEFAULT '', service text NOT NULL DEFAULT '',
 status text NOT NULL CHECK(status IN ('active','recovered')), summary text NOT NULL,
 observation_status text NOT NULL DEFAULT 'unhealthy' CHECK(observation_status IN ('healthy','unhealthy','unknown')),
 first_observed_at timestamptz NOT NULL, fired_at timestamptz NOT NULL, recovered_at timestamptz,
 last_observed_at timestamptz NOT NULL, updated_at timestamptz NOT NULL, last_event_id bigint NOT NULL DEFAULT 0
);
CREATE UNIQUE INDEX alarm_incidents_active ON alarm_incidents(fingerprint) WHERE status='active';
CREATE INDEX alarm_incidents_scope ON alarm_incidents(project,environment,application_id,status,updated_at DESC,id DESC);
CREATE INDEX alarm_incidents_latest ON alarm_incidents(updated_at DESC,id DESC);
CREATE INDEX alarm_incidents_fingerprint_history ON alarm_incidents(fingerprint,fired_at DESC);
CREATE INDEX alarm_incidents_retention ON alarm_incidents(recovered_at) WHERE status='recovered';
CREATE TABLE alarm_states (
 fingerprint text PRIMARY KEY, application_id text NOT NULL DEFAULT '',
 unhealthy_since timestamptz, last_checked_at timestamptz NOT NULL,
 observation_status text NOT NULL CHECK(observation_status IN ('healthy','unhealthy','unknown')),
 active_incident_id text REFERENCES alarm_incidents(id) ON DELETE SET NULL
);
CREATE INDEX alarm_states_application ON alarm_states(application_id);
CREATE UNIQUE INDEX alarm_states_active ON alarm_states(active_incident_id) WHERE active_incident_id IS NOT NULL;
CREATE INDEX alarm_states_retention ON alarm_states(last_checked_at) WHERE active_incident_id IS NULL;
CREATE TABLE alarm_events (
 id bigserial PRIMARY KEY, incident_id text NOT NULL REFERENCES alarm_incidents(id) ON DELETE CASCADE,
 transition text NOT NULL CHECK(transition IN ('active','recovered')), summary text NOT NULL,
 created_at timestamptz NOT NULL, email_requested boolean NOT NULL DEFAULT false,
 email_cursor text NOT NULL DEFAULT '', email_complete boolean NOT NULL DEFAULT false,
 UNIQUE(incident_id,transition)
);
CREATE INDEX alarm_events_fanout ON alarm_events(id) WHERE email_requested AND NOT email_complete;
CREATE TABLE alarm_reads (
 incident_id text NOT NULL REFERENCES alarm_incidents(id) ON DELETE CASCADE,
 identity_id text NOT NULL REFERENCES identities(id) ON DELETE CASCADE,
 read_event_id bigint NOT NULL DEFAULT 0, acknowledged_event_id bigint NOT NULL DEFAULT 0,
 updated_at timestamptz NOT NULL DEFAULT now(), PRIMARY KEY(incident_id,identity_id)
);
CREATE INDEX alarm_reads_identity ON alarm_reads(identity_id,incident_id);
CREATE TABLE alarm_email_deliveries (
 event_id bigint NOT NULL REFERENCES alarm_events(id) ON DELETE CASCADE,
 identity_id text NOT NULL REFERENCES identities(id) ON DELETE CASCADE,
 status text NOT NULL DEFAULT 'pending' CHECK(status IN ('pending','sending','sent','skipped','failed')),
 attempts integer NOT NULL DEFAULT 0 CHECK(attempts BETWEEN 0 AND 6), next_attempt_at timestamptz NOT NULL DEFAULT now(),
 lease_id text NOT NULL DEFAULT '', lease_until timestamptz, delivered_at timestamptz,
 last_error text NOT NULL DEFAULT '', PRIMARY KEY(event_id,identity_id)
);
CREATE INDEX alarm_email_pending ON alarm_email_deliveries(next_attempt_at,event_id) WHERE status IN ('pending','sending');
CREATE TABLE alarm_evaluator (
 singleton boolean PRIMARY KEY DEFAULT true CHECK(singleton), application_cursor text NOT NULL DEFAULT '',
 nodes_due_at timestamptz NOT NULL DEFAULT now(), lease_id text NOT NULL DEFAULT '', lease_until timestamptz
);
INSERT INTO alarm_evaluator(singleton) VALUES(true);
