CREATE TABLE deployment_notification_targets (
 id text PRIMARY KEY, application_id text NOT NULL REFERENCES applications(id) ON DELETE CASCADE,
 name text NOT NULL, kind text NOT NULL CHECK(kind IN ('email','slack','discord','webhook')),
 enabled boolean NOT NULL DEFAULT true, events text[] NOT NULL,
 revision bigint NOT NULL DEFAULT 1, destination bytea NOT NULL, updated_by text NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX deployment_notification_targets_app ON deployment_notification_targets(application_id);
CREATE TABLE deployment_notification_deliveries (
 id text PRIMARY KEY, target_id text NOT NULL REFERENCES deployment_notification_targets(id) ON DELETE CASCADE,
 target_revision bigint NOT NULL, payload jsonb NOT NULL,
 status text NOT NULL DEFAULT 'pending' CHECK(status IN ('pending','sending','sent','failed','skipped')),
 attempts integer NOT NULL DEFAULT 0, next_attempt_at timestamptz NOT NULL DEFAULT now(),
 lease_id text NOT NULL DEFAULT '', lease_until timestamptz, last_error text NOT NULL DEFAULT '',
 created_at timestamptz NOT NULL DEFAULT now(), finished_at timestamptz
);
CREATE INDEX deployment_notification_pending ON deployment_notification_deliveries(next_attempt_at,created_at) WHERE status IN ('pending','sending');
CREATE INDEX deployment_notification_history ON deployment_notification_deliveries(target_id,created_at DESC);
CREATE FUNCTION queue_deployment_notifications() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE a applications%ROWTYPE; capacity integer; destinations integer;
BEGIN
 IF NEW.status NOT IN ('succeeded','failed','cancelled') OR OLD.status=NEW.status THEN RETURN NEW; END IF;
 SELECT count(*) INTO destinations FROM deployment_notification_targets
 WHERE application_id=NEW.application_id AND enabled AND NEW.status=ANY(events);
 IF destinations=0 THEN RETURN NEW; END IF;
 PERFORM pg_advisory_xact_lock(hashtextextended('hakopod.deployment.notifications',0));
 SELECT count(*) INTO capacity FROM (SELECT 1 FROM deployment_notification_deliveries WHERE status IN ('pending','sending') LIMIT 10000) q;
 IF capacity+destinations>10000 THEN
  INSERT INTO deployment_events(deployment_id,type,message) VALUES(NEW.id,'notification_skipped','Deployment notification queue is full; no notification was queued.');
  RETURN NEW;
 END IF;
 SELECT * INTO a FROM applications WHERE id=NEW.application_id;
 INSERT INTO deployment_notification_deliveries(id,target_id,target_revision,payload)
 SELECT NEW.id || '-' || t.id,t.id,t.revision,jsonb_build_object(
  'schema_version',1,'event_id',NEW.id || '-' || t.id,'deployment_id',NEW.id,
  'application_id',a.id,'application_name',a.name,'project',a.project,'environment',a.environment,
  'revision',NEW.revision,'status',NEW.status,'recovery_state',NEW.recovery_state,'occurred_at',now())
 FROM deployment_notification_targets t
 WHERE t.application_id=NEW.application_id AND t.enabled AND NEW.status=ANY(t.events)
 LIMIT (10000-capacity)
 ON CONFLICT(id) DO NOTHING;
 RETURN NEW;
END $$;
CREATE TRIGGER deployment_notification_transition AFTER UPDATE OF status ON deployments
FOR EACH ROW EXECUTE FUNCTION queue_deployment_notifications();
