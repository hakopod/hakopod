-- Slack configuration exists only for self-hosted installations. Cloud owns
-- workspace credentials and delivery in its control plane.
-- This counter never resets. It fences OAuth setup states even after the
-- current integration row is deleted and its visible revision would otherwise restart.
CREATE TABLE slack_integration_generation (
 singleton boolean PRIMARY KEY DEFAULT true CHECK(singleton),
 generation bigint NOT NULL DEFAULT 0 CHECK(generation>=0)
);
INSERT INTO slack_integration_generation(singleton,generation) VALUES(true,0);

CREATE TABLE slack_integration (
 singleton boolean PRIMARY KEY DEFAULT true CHECK(singleton),
 client_configuration bytea NOT NULL,
 bot_configuration bytea NOT NULL,
 team_id text NOT NULL CHECK(length(team_id) BETWEEN 1 AND 64),
 team_name text NOT NULL CHECK(length(team_name) BETWEEN 1 AND 200),
 channel_id text NOT NULL DEFAULT '' CHECK(length(channel_id)<=64),
 channel_name text NOT NULL DEFAULT '' CHECK(length(channel_name)<=200),
 channel_private boolean NOT NULL DEFAULT false,
 events text[] NOT NULL DEFAULT ARRAY['alarm.opened','alarm.resolved']::text[]
  CHECK(cardinality(events) BETWEEN 1 AND 3 AND events <@ ARRAY['alarm.opened','alarm.resolved','audit']::text[]),
 revision bigint NOT NULL DEFAULT 1,
 connected_by text NOT NULL REFERENCES identities(id),
 created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now()
);

-- A transaction-created outbox ensures a committed source record has an event
-- record. A dispatcher may claim it, but this migration intentionally does not
-- select a transport implementation or promise global commit ordering.
CREATE TABLE slack_event_outbox (
 id bigserial PRIMARY KEY,
 delivery_target text NOT NULL DEFAULT 'self_hosted' CHECK(delivery_target IN ('self_hosted','cloud')),
 runtime_source_id text NOT NULL DEFAULT '' CHECK(length(runtime_source_id)<=128),
 integration_revision bigint NOT NULL DEFAULT 0,
 event_kind text NOT NULL CHECK(event_kind IN ('alarm.opened','alarm.resolved','audit','test')),
 source_id bigint NOT NULL,
 payload jsonb NOT NULL,
 status text NOT NULL DEFAULT 'pending' CHECK(status IN ('pending','sending','sent','skipped','failed')),
 attempts integer NOT NULL DEFAULT 0 CHECK(attempts BETWEEN 0 AND 8),
 next_attempt_at timestamptz NOT NULL DEFAULT now(),
 lease_id text NOT NULL DEFAULT '', lease_until timestamptz,
 finished_at timestamptz, last_error text NOT NULL DEFAULT '',
 created_at timestamptz NOT NULL DEFAULT now(),
 UNIQUE(delivery_target,runtime_source_id,event_kind,source_id)
);
CREATE INDEX slack_event_outbox_pending ON slack_event_outbox(next_attempt_at,id) WHERE status IN ('pending','sending');

-- Notification transport is best effort. Preserve platform writes under an
-- outage by bounding queued work instead of allowing an unbounded event table.
CREATE FUNCTION hakopod_slack_outbox_bound() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 PERFORM pg_advisory_xact_lock(hashtext('hakopod.slack.outbox'));
 IF (SELECT count(*) FROM (SELECT 1 FROM slack_event_outbox WHERE status IN ('pending','sending') LIMIT 10000) q)>=10000 THEN RETURN NULL; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER hakopod_slack_outbox_bound BEFORE INSERT ON slack_event_outbox FOR EACH ROW EXECUTE FUNCTION hakopod_slack_outbox_bound();

CREATE TABLE slack_test_requests (id bigserial PRIMARY KEY, requested_by text NOT NULL REFERENCES identities(id), created_at timestamptz NOT NULL DEFAULT now());
CREATE TABLE slack_cloud_event_source (singleton boolean PRIMARY KEY DEFAULT true CHECK(singleton), source_id text NOT NULL CHECK(length(source_id) BETWEEN 1 AND 128), project text NOT NULL DEFAULT '', environment text NOT NULL DEFAULT '', CHECK((project='')=(environment='')));

CREATE FUNCTION hakopod_slack_audit_outbox() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE scope_project text := ''; scope_environment text := ''; parts text[];
BEGIN
 IF EXISTS(SELECT 1 FROM slack_integration WHERE 'audit'=ANY(events))
    AND NEW.action NOT LIKE 'slack.%' THEN
  INSERT INTO slack_event_outbox(delivery_target,integration_revision,event_kind,source_id,payload)
  SELECT 'self_hosted',revision,'audit',NEW.id,jsonb_build_object('schema_version',1,'event_id',NEW.id,'actor_id',NEW.identity_id,'action',NEW.action,'resource',NEW.resource,'occurred_at',NEW.time) FROM slack_integration WHERE 'audit'=ANY(events)
  ON CONFLICT DO NOTHING;
 END IF;
 -- A shared Cloud runtime must never infer a workspace from unscoped audit data.
 -- Resolve scope only through durable application, database, platform or named
 -- runtime-resource records. Metadata is trusted only for the application
 -- deletion snapshot written by DeleteEmptyApplication after its scoped row is
 -- locked; arbitrary audit metadata must never select a Cloud workspace.
 SELECT project,environment INTO scope_project,scope_environment FROM applications
  WHERE id=COALESCE(NEW.metadata->>'application_id',NEW.resource) LIMIT 1;
 IF COALESCE(scope_project,'')='' THEN
  SELECT project,environment INTO scope_project,scope_environment FROM managed_databases
   WHERE id=COALESCE(NEW.metadata->>'database_id',NEW.resource) LIMIT 1;
 END IF;
 IF COALESCE(scope_project,'')='' THEN
  SELECT project,environment INTO scope_project,scope_environment FROM external_databases
   WHERE id=COALESCE(NEW.metadata->>'database_id',NEW.resource) LIMIT 1;
 END IF;
 IF COALESCE(scope_project,'')='' THEN
  SELECT project,environment INTO scope_project,scope_environment FROM managed_platforms
   WHERE id=COALESCE(NEW.metadata->>'platform_id',NEW.resource) LIMIT 1;
 END IF;
 IF COALESCE(scope_project,'')='' THEN
  parts := string_to_array(NEW.resource,'/');
  IF cardinality(parts)=3 THEN
   SELECT project,environment INTO scope_project,scope_environment FROM runtime_resources
    WHERE project=parts[1] AND environment=parts[2] AND name=parts[3] LIMIT 1;
  END IF;
 END IF;
 IF COALESCE(scope_project,'')='' AND NEW.action='application.delete' AND NEW.metadata ? 'project' AND NEW.metadata ? 'environment' THEN
  SELECT project,name INTO scope_project,scope_environment FROM environments
   WHERE project=NEW.metadata->>'project' AND name=NEW.metadata->>'environment' LIMIT 1;
 END IF;
 IF COALESCE(scope_project,'')<>'' AND COALESCE(scope_environment,'')<>'' THEN
  INSERT INTO slack_event_outbox(delivery_target,runtime_source_id,event_kind,source_id,payload)
   SELECT 'cloud',source_id,'audit',NEW.id,jsonb_build_object('schema_version',1,'event_id',NEW.id,'actor_id',NEW.identity_id,'action',NEW.action,'resource',NEW.resource,'project',scope_project,'environment',scope_environment,'occurred_at',NEW.time)
   FROM slack_cloud_event_source ON CONFLICT DO NOTHING;
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER hakopod_slack_audit_outbox AFTER INSERT ON audit_events
 FOR EACH ROW EXECUTE FUNCTION hakopod_slack_audit_outbox();

CREATE FUNCTION hakopod_slack_alarm_outbox() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE kind text;
BEGIN
 kind := CASE NEW.transition WHEN 'active' THEN 'alarm.opened' ELSE 'alarm.resolved' END;
 IF EXISTS(SELECT 1 FROM slack_integration WHERE kind=ANY(events)) THEN
  INSERT INTO slack_event_outbox(delivery_target,integration_revision,event_kind,source_id,payload)
  SELECT 'self_hosted',s.revision,kind,NEW.id,jsonb_build_object('schema_version',1,'event_id',NEW.id,'alarm_id',a.id,'project',a.project,'environment',a.environment,'application_id',a.application_id,'resource_name',a.resource_name,'occurred_at',NEW.created_at)
  FROM alarm_incidents a CROSS JOIN slack_integration s WHERE a.id=NEW.incident_id AND kind=ANY(s.events) ON CONFLICT DO NOTHING;
 END IF;
 INSERT INTO slack_event_outbox(delivery_target,runtime_source_id,event_kind,source_id,payload)
 SELECT 'cloud',s.source_id,kind,NEW.id,jsonb_build_object('schema_version',1,'event_id',NEW.id,'alarm_id',a.id,'project',a.project,'environment',a.environment,'application_id',a.application_id,'resource_name',a.resource_name,'occurred_at',NEW.created_at)
 FROM alarm_incidents a CROSS JOIN slack_cloud_event_source s WHERE a.id=NEW.incident_id ON CONFLICT DO NOTHING;
 RETURN NEW;
END $$;
CREATE TRIGGER hakopod_slack_alarm_outbox AFTER INSERT ON alarm_events
 FOR EACH ROW EXECUTE FUNCTION hakopod_slack_alarm_outbox();
