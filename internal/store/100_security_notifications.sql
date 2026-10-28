-- Security advisory notifications reuse the per-application targets and the
-- Slack outbox. One row per (application, advisory, advisory revision, service)
-- makes delivery idempotent; seq gives the Slack outbox a numeric source id.
CREATE TABLE security_notifications (
 key text PRIMARY KEY,
 seq bigserial NOT NULL UNIQUE,
 application_id text NOT NULL REFERENCES applications(id) ON DELETE CASCADE,
 advisory_id text NOT NULL,
 advisory_revision bigint NOT NULL,
 service text NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX security_notifications_app ON security_notifications(application_id);

-- On by default. queue_deployment_notifications returns early unless
-- NEW.status is succeeded/failed/cancelled, so this event never fires there.
UPDATE deployment_notification_targets SET events=array_append(events,'security_advisory')
WHERE NOT 'security_advisory'=ANY(events);

ALTER TABLE slack_integration DROP CONSTRAINT IF EXISTS slack_integration_events_check;
ALTER TABLE slack_integration ADD CONSTRAINT slack_integration_events_check CHECK (
 cardinality(events) BETWEEN 1 AND 64 AND events <@ ARRAY[
  'alarm.opened','alarm.resolved','audit',
  'deployment.queued','deployment.started','deployment.succeeded','deployment.failed','deployment.cancelled','deployment.superseded','deployment.rollback.requested','deployment.cancellation.requested',
  'application.created','application.configuration.updated','application.renamed','application.deleted',
  'service.added','service.removed','service.renamed','service.configuration.updated','service.image.updated','service.variables.updated','service.resources.updated','service.scale.updated','service.suspended','service.resumed','service.restart.requested','service.network.updated','service.storage.updated','service.healthcheck.updated','service.placement.updated','service.command.updated','service.delivery.updated',
  'service.update.started','service.ready','service.failed','service.job.scheduled','service.job.completed','service.certificate.renewed',
  'security.advisory'
 ]::text[]
);
ALTER TABLE slack_event_outbox DROP CONSTRAINT IF EXISTS slack_event_outbox_event_kind_check;
ALTER TABLE slack_event_outbox ADD CONSTRAINT slack_event_outbox_event_kind_check CHECK (event_kind IN (
 'alarm.opened','alarm.resolved','audit','test',
 'deployment.queued','deployment.started','deployment.succeeded','deployment.failed','deployment.cancelled','deployment.superseded','deployment.rollback.requested','deployment.cancellation.requested',
 'application.created','application.configuration.updated','application.renamed','application.deleted',
 'service.added','service.removed','service.renamed','service.configuration.updated','service.image.updated','service.variables.updated','service.resources.updated','service.scale.updated','service.suspended','service.resumed','service.restart.requested','service.network.updated','service.storage.updated','service.healthcheck.updated','service.placement.updated','service.command.updated','service.delivery.updated',
 'service.update.started','service.ready','service.failed','service.job.scheduled','service.job.completed','service.certificate.renewed',
 'security.advisory'
));
UPDATE slack_integration SET events=array_append(events,'security.advisory')
WHERE NOT 'security.advisory'=ANY(events) AND cardinality(events)<64;
