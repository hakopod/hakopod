package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/hakopod/hakopod/internal/advisory"
	"github.com/jackc/pgx/v5"
)

// SecurityNotificationEvent is the per-application target event that gates
// both advisory and automatic-patch notifications.
const SecurityNotificationEvent = "security_advisory"

// SecurityNotificationKey identifies one notification per application,
// advisory, advisory revision and service.
func SecurityNotificationKey(applicationID, advisoryID string, revision int64, service string) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%s\x00%d\x00%s", applicationID, advisoryID, revision, service)))
	return hex.EncodeToString(sum[:])
}

// QueueSecurityNotification queues one delivery per enabled target that
// selected security_advisory, plus Slack outbox rows. It returns false when the
// finding was already notified or the queue is full (a later scan retries).
func (s *Store) QueueSecurityNotification(ctx context.Context, app SecurityCandidate, f advisory.Finding, link string) (bool, error) {
	payload := securityPayload(app, "security_advisory", f.Service, link)
	payload["advisory_id"], payload["advisory_revision"], payload["source"] = f.AdvisoryID, f.AdvisoryRevision, f.Source
	payload["severity"], payload["title"], payload["url"] = f.Severity, f.Title, f.URL
	payload["confidence"], payload["evidence"], payload["fix"], payload["fix_reason"] = f.Confidence, f.Evidence, f.Fix, f.FixReason
	return s.queueSecurity(ctx, SecurityNotificationKey(app.ApplicationID, f.AdvisoryID, f.AdvisoryRevision, f.Service), app.ApplicationID, f.AdvisoryID, f.AdvisoryRevision, f.Service, payload)
}

// QueuePatchResultNotification reports a failed automatic patch attempt once.
func (s *Store) QueuePatchResultNotification(ctx context.Context, app SecurityCandidate, a PatchAttempt, link string) (bool, error) {
	payload := securityPayload(app, "security_patch_failed", a.Service, link)
	payload["advisory_id"], payload["advisory_revision"] = a.AdvisoryID, a.AdvisoryRevision
	payload["attempt_id"], payload["patch_deployment_id"], payload["error"] = a.ID, a.DeploymentID, a.Error
	return s.queueSecurity(ctx, "patch:"+a.ID, app.ApplicationID, a.AdvisoryID, a.AdvisoryRevision, a.Service, payload)
}

func securityPayload(app SecurityCandidate, kind, service, link string) map[string]any {
	return map[string]any{"schema_version": 1, "type": kind, "application_id": app.ApplicationID, "application_name": app.Name,
		"project": app.Project, "environment": app.Environment, "revision": app.Revision, "deployment_id": app.DeploymentID,
		"service": service, "link": link, "occurred_at": time.Now().UTC()}
}

func (s *Store) queueSecurity(ctx context.Context, key, appID, advisoryID string, revision int64, service string, payload map[string]any) (bool, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	var seq int64
	if err = tx.QueryRow(ctx, "INSERT INTO security_notifications(key,application_id,advisory_id,advisory_revision,service) VALUES($1,$2,$3,$4,$5) ON CONFLICT DO NOTHING RETURNING seq", key, appID, advisoryID, revision, service).Scan(&seq); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	payload["event_id"] = key
	var destinations int
	if err = tx.QueryRow(ctx, "SELECT count(*) FROM deployment_notification_targets WHERE application_id=$1 AND enabled AND $2=ANY(events)", appID, SecurityNotificationEvent).Scan(&destinations); err != nil {
		return false, err
	}
	if destinations > 0 {
		// Same lock and cap as queue_deployment_notifications.
		var capacity int
		if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended('hakopod.deployment.notifications',0))"); err != nil {
			return false, err
		}
		if err = tx.QueryRow(ctx, "SELECT count(*) FROM (SELECT 1 FROM deployment_notification_deliveries WHERE status IN ('pending','sending') LIMIT 10000) q").Scan(&capacity); err != nil {
			return false, err
		}
		if capacity+destinations > 10000 {
			return false, nil // rolled back: not marked notified, retried on a later scan
		}
		if _, err = tx.Exec(ctx, `INSERT INTO deployment_notification_deliveries(id,target_id,target_revision,payload)
 SELECT $1 || '-' || t.id,t.id,t.revision,$2::jsonb || jsonb_build_object('event_id',$1 || '-' || t.id)
 FROM deployment_notification_targets t WHERE t.application_id=$3 AND t.enabled AND $4=ANY(t.events)
 ON CONFLICT(id) DO NOTHING`, key, JSON(payload), appID, SecurityNotificationEvent); err != nil {
			return false, err
		}
	}
	// The outbox bound trigger enforces the Slack queue cap per row.
	if _, err = tx.Exec(ctx, `INSERT INTO slack_event_outbox(delivery_target,integration_revision,event_kind,source_id,payload)
 SELECT 'self_hosted',i.revision,'security.advisory',$1,$2::jsonb FROM slack_integration i WHERE 'security.advisory'=ANY(i.events)
 ON CONFLICT DO NOTHING`, seq, JSON(payload)); err != nil {
		return false, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO slack_event_outbox(delivery_target,runtime_source_id,event_kind,source_id,payload)
 SELECT 'cloud',source.source_id,'security.advisory',$1,$2::jsonb FROM slack_cloud_event_source source
 WHERE source.project='' OR (source.project=$3 AND source.environment=$4)
 ON CONFLICT DO NOTHING`, seq, JSON(payload), payload["project"], payload["environment"]); err != nil {
		return false, err
	}
	return true, tx.Commit(ctx)
}
