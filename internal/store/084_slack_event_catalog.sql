-- Expand the Slack catalog without changing existing integration selections.
ALTER TABLE deployments ADD COLUMN rollback_requested boolean NOT NULL DEFAULT false;
ALTER TABLE slack_integration DROP CONSTRAINT IF EXISTS slack_integration_events_check;
ALTER TABLE slack_integration ADD CONSTRAINT slack_integration_events_check CHECK (
 cardinality(events) BETWEEN 1 AND 64 AND events <@ ARRAY[
  'alarm.opened','alarm.resolved','audit',
  'deployment.queued','deployment.started','deployment.succeeded','deployment.failed','deployment.cancelled','deployment.superseded','deployment.rollback.requested','deployment.cancellation.requested',
  'application.created','application.configuration.updated','application.renamed','application.deleted',
  'service.added','service.removed','service.renamed','service.configuration.updated','service.image.updated','service.variables.updated','service.resources.updated','service.scale.updated','service.suspended','service.resumed','service.restart.requested','service.network.updated','service.storage.updated','service.healthcheck.updated','service.placement.updated','service.command.updated','service.delivery.updated',
  'service.update.started','service.ready','service.failed','service.job.scheduled','service.job.completed','service.certificate.renewed'
 ]::text[]
);
ALTER TABLE slack_event_outbox DROP CONSTRAINT IF EXISTS slack_event_outbox_event_kind_check;
ALTER TABLE slack_event_outbox ADD CONSTRAINT slack_event_outbox_event_kind_check CHECK (event_kind IN (
 'alarm.opened','alarm.resolved','audit','test',
 'deployment.queued','deployment.started','deployment.succeeded','deployment.failed','deployment.cancelled','deployment.superseded','deployment.rollback.requested','deployment.cancellation.requested',
 'application.created','application.configuration.updated','application.renamed','application.deleted',
 'service.added','service.removed','service.renamed','service.configuration.updated','service.image.updated','service.variables.updated','service.resources.updated','service.scale.updated','service.suspended','service.resumed','service.restart.requested','service.network.updated','service.storage.updated','service.healthcheck.updated','service.placement.updated','service.command.updated','service.delivery.updated',
 'service.update.started','service.ready','service.failed','service.job.scheduled','service.job.completed','service.certificate.renewed'
));

-- Deployment records are the source of truth for lifecycle and accepted
-- configuration notifications. Payloads intentionally contain identifiers,
-- scope and a fixed event name only; messages may include operational details.
CREATE FUNCTION hakopod_slack_deployment_outbox() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE event_kind_value text;
BEGIN
 event_kind_value := CASE NEW.type
  WHEN 'queued' THEN 'deployment.queued'
  WHEN 'deployment.started' THEN 'deployment.started'
  WHEN 'deployment.succeeded' THEN 'deployment.succeeded'
  WHEN 'deployment.failed' THEN 'deployment.failed'
  WHEN 'deployment.cancelled' THEN 'deployment.cancelled'
  WHEN 'deployment.superseded' THEN 'deployment.superseded'
  WHEN 'deployment.rollback.requested' THEN 'deployment.rollback.requested'
  WHEN 'deployment.cancellation.requested' THEN 'deployment.cancellation.requested'
  WHEN 'failed' THEN CASE WHEN NEW.service<>'' THEN 'service.failed' END
  WHEN 'app.created' THEN 'application.created'
  WHEN 'application.created' THEN 'application.created'
  WHEN 'application.configuration.updated' THEN 'application.configuration.updated'
  WHEN 'service.added' THEN 'service.added'
  WHEN 'service.removed' THEN 'service.removed'
  WHEN 'service.configuration.updated' THEN 'service.configuration.updated'
  WHEN 'service.image.updated' THEN 'service.image.updated'
  WHEN 'service.variables.updated' THEN 'service.variables.updated'
  WHEN 'service.resources.updated' THEN 'service.resources.updated'
  WHEN 'service.scale.updated' THEN 'service.scale.updated'
  WHEN 'service.suspended' THEN 'service.suspended'
  WHEN 'service.resumed' THEN 'service.resumed'
  WHEN 'service.restart.requested' THEN 'service.restart.requested'
  WHEN 'service.network.updated' THEN 'service.network.updated'
  WHEN 'service.storage.updated' THEN 'service.storage.updated'
  WHEN 'service.healthcheck.updated' THEN 'service.healthcheck.updated'
  WHEN 'service.placement.updated' THEN 'service.placement.updated'
  WHEN 'service.command.updated' THEN 'service.command.updated'
  WHEN 'service.delivery.updated' THEN 'service.delivery.updated'
  WHEN 'applying' THEN 'service.update.started'
  WHEN 'ready' THEN 'service.ready'
  WHEN 'scheduled' THEN 'service.job.scheduled'
  WHEN 'completed' THEN 'service.job.completed'
  WHEN 'certificate_renewed' THEN 'service.certificate.renewed'
 END;
 IF event_kind_value IS NULL THEN RETURN NEW; END IF;
 INSERT INTO slack_event_outbox(delivery_target,integration_revision,event_kind,source_id,payload)
 SELECT 'self_hosted',i.revision,event_kind_value,NEW.id,jsonb_strip_nulls(jsonb_build_object(
  'schema_version',1,'event_id',NEW.id,'deployment_id',d.id,'project',a.project,'environment',a.environment,'application_id',a.id,'application_name',a.name,'revision',d.revision,
  'runtime_phase',CASE WHEN d.recovery_state<>'' THEN 'recovery' ELSE 'deployment' END,'recovery_revision',d.recovery_revision,
  'service',NULLIF(NEW.service,''),'occurred_at',NEW.time))
 FROM deployments d JOIN applications a ON a.id=d.application_id CROSS JOIN slack_integration i
 WHERE d.id=NEW.deployment_id AND event_kind_value=ANY(i.events)
 ON CONFLICT DO NOTHING;
 INSERT INTO slack_event_outbox(delivery_target,runtime_source_id,event_kind,source_id,payload)
 SELECT 'cloud',source.source_id,event_kind_value,NEW.id,jsonb_strip_nulls(jsonb_build_object(
  'schema_version',1,'event_id',NEW.id,'deployment_id',d.id,'project',a.project,'environment',a.environment,'application_id',a.id,'application_name',a.name,'revision',d.revision,
  'runtime_phase',CASE WHEN d.recovery_state<>'' THEN 'recovery' ELSE 'deployment' END,'recovery_revision',d.recovery_revision,
  'service',NULLIF(NEW.service,''),'occurred_at',NEW.time))
 FROM deployments d JOIN applications a ON a.id=d.application_id CROSS JOIN slack_cloud_event_source source
 WHERE d.id=NEW.deployment_id AND (source.project='' OR (source.project=a.project AND source.environment=a.environment))
 ON CONFLICT DO NOTHING;
 RETURN NEW;
END $$;
CREATE TRIGGER hakopod_slack_deployment_outbox AFTER INSERT ON deployment_events
 FOR EACH ROW EXECUTE FUNCTION hakopod_slack_deployment_outbox();

-- A status transition is authoritative even when a worker emits a provisional
-- progress event first. These records also cover direct cancellation paths.
CREATE FUNCTION hakopod_slack_deployment_transition() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE event_type text;
BEGIN
 IF TG_OP='INSERT' THEN
  IF NEW.rollback_requested THEN INSERT INTO deployment_events(deployment_id,type,message) VALUES(NEW.id,'deployment.rollback.requested','Rollback deployment accepted'); END IF;
  IF NEW.status='running' THEN INSERT INTO deployment_events(deployment_id,type,message) VALUES(NEW.id,'deployment.started','Deployment status changed'); END IF;
 ELSIF TG_OP='UPDATE' THEN
  IF NEW.status IS DISTINCT FROM OLD.status THEN
   event_type := CASE NEW.status
    WHEN 'running' THEN 'deployment.started'
    WHEN 'succeeded' THEN 'deployment.succeeded'
    WHEN 'failed' THEN 'deployment.failed'
    WHEN 'cancelled' THEN 'deployment.cancelled'
    WHEN 'superseded' THEN 'deployment.superseded'
   END;
   IF event_type IS NOT NULL THEN INSERT INTO deployment_events(deployment_id,type,message) VALUES(NEW.id,event_type,'Deployment status changed'); END IF;
  END IF;
  IF NEW.cancel_requested AND NOT OLD.cancel_requested THEN
   INSERT INTO deployment_events(deployment_id,type,message) VALUES(NEW.id,'deployment.cancellation.requested','Deployment cancellation requested');
  END IF;
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER hakopod_slack_deployment_insert AFTER INSERT ON deployments
 FOR EACH ROW EXECUTE FUNCTION hakopod_slack_deployment_transition();
CREATE TRIGGER hakopod_slack_deployment_transition AFTER UPDATE OF status,cancel_requested ON deployments
 FOR EACH ROW EXECUTE FUNCTION hakopod_slack_deployment_transition();

-- These audit records are produced inside the application deletion and rename
-- transactions. They retain the only safe scope once an application is gone.
CREATE FUNCTION hakopod_slack_application_audit_outbox() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE event_kind_value text; scope_project text; scope_environment text; application_name text; service_name text;
BEGIN
 event_kind_value := CASE NEW.action WHEN 'application.delete' THEN 'application.deleted' WHEN 'application.rename' THEN CASE WHEN COALESCE(NEW.metadata->>'service','')='' THEN 'application.renamed' ELSE 'service.renamed' END END;
 IF event_kind_value IS NULL THEN RETURN NEW; END IF;
 IF NEW.action='application.delete' THEN
  scope_project := NEW.metadata->>'project'; scope_environment := NEW.metadata->>'environment'; application_name := NEW.metadata->>'name';
 ELSE
  SELECT project,environment,name INTO scope_project,scope_environment,application_name FROM applications WHERE id=NEW.resource;
 END IF;
 IF COALESCE(scope_project,'')='' OR COALESCE(scope_environment,'')='' THEN RETURN NEW; END IF;
 service_name := NULLIF(NEW.metadata->>'service','');
 INSERT INTO slack_event_outbox(delivery_target,integration_revision,event_kind,source_id,payload)
 SELECT 'self_hosted',i.revision,event_kind_value,NEW.id,jsonb_build_object('schema_version',1,'event_id',NEW.id,'project',scope_project,'environment',scope_environment,'application_id',NEW.resource,'application_name',application_name,'service',service_name,'occurred_at',NEW.time)
 FROM slack_integration i WHERE event_kind_value=ANY(i.events) ON CONFLICT DO NOTHING;
 INSERT INTO slack_event_outbox(delivery_target,runtime_source_id,event_kind,source_id,payload)
 SELECT 'cloud',source.source_id,event_kind_value,NEW.id,jsonb_build_object('schema_version',1,'event_id',NEW.id,'project',scope_project,'environment',scope_environment,'application_id',NEW.resource,'application_name',application_name,'service',service_name,'occurred_at',NEW.time)
 FROM slack_cloud_event_source source WHERE source.project='' OR (source.project=scope_project AND source.environment=scope_environment) ON CONFLICT DO NOTHING;
 RETURN NEW;
END $$;
CREATE TRIGGER hakopod_slack_application_audit_outbox AFTER INSERT ON audit_events
 FOR EACH ROW EXECUTE FUNCTION hakopod_slack_application_audit_outbox();
