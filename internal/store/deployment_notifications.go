package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/jackc/pgx/v5"
)

type NotificationTarget struct {
	ID            string   `json:"id"`
	ApplicationID string   `json:"application_id"`
	Name          string   `json:"name"`
	Kind          string   `json:"kind"`
	Enabled       bool     `json:"enabled"`
	Events        []string `json:"events"`
	Revision      int64    `json:"revision"`
	Destination   []byte   `json:"-"`
}

const notificationColumns = "id,application_id,name,kind,enabled,events,revision,destination"

func scanNotification(row scanner) (NotificationTarget, error) {
	var n NotificationTarget
	err := row.Scan(&n.ID, &n.ApplicationID, &n.Name, &n.Kind, &n.Enabled, &n.Events, &n.Revision, &n.Destination)
	return n, err
}
func (s *Store) NotificationTargets(ctx context.Context, p Principal, a Application) ([]NotificationTarget, error) {
	if !p.Allows("deployments:read", a.Project, a.Environment, a.Name) {
		return nil, ErrForbidden
	}
	rows, err := s.Pool.Query(ctx, "SELECT "+notificationColumns+" FROM deployment_notification_targets WHERE application_id=$1 ORDER BY created_at,id LIMIT 5", a.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []NotificationTarget{}
	for rows.Next() {
		n, err := scanNotification(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}
func (s *Store) NotificationTarget(ctx context.Context, app, id string) (NotificationTarget, error) {
	return scanNotification(s.Pool.QueryRow(ctx, "SELECT "+notificationColumns+" FROM deployment_notification_targets WHERE application_id=$1 AND id=$2", app, id))
}
func (s *Store) PutNotificationTarget(ctx context.Context, p Principal, a Application, n NotificationTarget, expected int64) (NotificationTarget, error) {
	if !p.Allows("deployments:write", a.Project, a.Environment, a.Name) {
		return n, ErrForbidden
	}
	if n.ApplicationID != a.ID || strings.TrimSpace(n.Name) == "" || len(n.Name) > 80 || strings.IndexFunc(n.Name, unicode.IsControl) >= 0 || !slices.Contains([]string{"email", "slack", "discord", "webhook"}, n.Kind) || len(n.Events) == 0 || len(n.Events) > 3 || len(n.Destination) < 28 || len(n.Destination) > 8192 {
		return n, fmt.Errorf("%w: invalid notification destination", ErrInput)
	}
	seen := map[string]bool{}
	for _, event := range n.Events {
		if seen[event] || !slices.Contains([]string{"succeeded", "failed", "cancelled"}, event) {
			return n, fmt.Errorf("%w: choose unique deployment events", ErrInput)
		}
		seen[event] = true
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return n, err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, "SELECT id FROM applications WHERE id=$1 FOR UPDATE", a.ID); err != nil {
		return n, err
	}
	if expected == 0 {
		var count int
		if err = tx.QueryRow(ctx, "SELECT count(*) FROM deployment_notification_targets WHERE application_id=$1", a.ID).Scan(&count); err != nil {
			return n, err
		}
		if count >= 5 {
			return n, fmt.Errorf("%w: an application supports up to five notification destinations", ErrInput)
		}
		n, err = scanNotification(tx.QueryRow(ctx, "INSERT INTO deployment_notification_targets(id,application_id,name,kind,enabled,events,destination,updated_by) VALUES($1,$2,$3,$4,$5,$6,$7,$8) RETURNING "+notificationColumns, n.ID, a.ID, n.Name, n.Kind, n.Enabled, n.Events, n.Destination, p.ID))
	} else {
		n, err = scanNotification(tx.QueryRow(ctx, "UPDATE deployment_notification_targets SET name=$3,kind=$4,enabled=$5,events=$6,destination=$7,revision=revision+1,updated_by=$8,updated_at=now() WHERE application_id=$1 AND id=$2 AND revision=$9 RETURNING "+notificationColumns, a.ID, n.ID, n.Name, n.Kind, n.Enabled, n.Events, n.Destination, p.ID, expected))
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return n, ErrConflict
	}
	if err != nil {
		return n, err
	}
	// Changed settings never redirect already queued messages to a new destination.
	if _, err = tx.Exec(ctx, "UPDATE deployment_notification_deliveries SET status='skipped',finished_at=now(),last_error='Destination settings changed.' WHERE target_id=$1 AND status='pending' AND target_revision<>$2", n.ID, n.Revision); err != nil {
		return n, err
	}
	_, err = tx.Exec(ctx, "INSERT INTO audit_events(identity_id,key_id,action,resource,metadata) VALUES($1,$2,'notification.save',$3,$4)", p.ID, p.KeyID, n.ID, JSON(map[string]any{"application_id": a.ID, "kind": n.Kind, "enabled": n.Enabled}))
	if err != nil {
		return n, err
	}
	return n, tx.Commit(ctx)
}
func (s *Store) DeleteNotificationTarget(ctx context.Context, p Principal, a Application, id string, revision int64) error {
	if !p.Allows("deployments:write", a.Project, a.Environment, a.Name) {
		return ErrForbidden
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	result, err := tx.Exec(ctx, "DELETE FROM deployment_notification_targets WHERE application_id=$1 AND id=$2 AND revision=$3", a.ID, id, revision)
	if err != nil {
		return err
	}
	if result.RowsAffected() != 1 {
		return ErrConflict
	}
	_, err = tx.Exec(ctx, "INSERT INTO audit_events(identity_id,key_id,action,resource) VALUES($1,$2,'notification.delete',$3)", p.ID, p.KeyID, id)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

type NotificationDelivery struct {
	ID             string          `json:"id"`
	TargetID       string          `json:"target_id"`
	TargetRevision int64           `json:"-"`
	Payload        json.RawMessage `json:"payload"`
	Status         string          `json:"status"`
	Attempts       int             `json:"attempts"`
	LastError      string          `json:"last_error"`
	CreatedAt      time.Time       `json:"created_at"`
	FinishedAt     *time.Time      `json:"finished_at"`
	LeaseID        string          `json:"-"`
}

func (s *Store) NotificationHistory(ctx context.Context, p Principal, a Application) ([]NotificationDelivery, error) {
	if !p.Allows("deployments:read", a.Project, a.Environment, a.Name) {
		return nil, ErrForbidden
	}
	rows, err := s.Pool.Query(ctx, `SELECT d.id,d.target_id,d.payload,d.status,d.attempts,d.last_error,d.created_at,d.finished_at
 FROM deployment_notification_deliveries d JOIN deployment_notification_targets t ON t.id=d.target_id WHERE t.application_id=$1 ORDER BY d.created_at DESC,d.id DESC LIMIT 25`, a.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []NotificationDelivery{}
	for rows.Next() {
		var d NotificationDelivery
		if err = rows.Scan(&d.ID, &d.TargetID, &d.Payload, &d.Status, &d.Attempts, &d.LastError, &d.CreatedAt, &d.FinishedAt); err != nil {
			return nil, err
		}
		result = append(result, d)
	}
	return result, rows.Err()
}
func (s *Store) TestNotification(ctx context.Context, p Principal, a Application, target string, revision int64) (string, error) {
	if !p.Allows("deployments:write", a.Project, a.Environment, a.Name) {
		return "", ErrForbidden
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended('hakopod.deployment.notifications',0))"); err != nil {
		return "", err
	}
	var enabled bool
	if err = tx.QueryRow(ctx, "SELECT enabled FROM deployment_notification_targets WHERE application_id=$1 AND id=$2 AND revision=$3 FOR UPDATE", a.ID, target, revision).Scan(&enabled); err != nil {
		return "", ErrConflict
	}
	if !enabled {
		return "", fmt.Errorf("%w: enable the destination before sending a test", ErrInput)
	}
	var recent bool
	if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM deployment_notification_deliveries WHERE target_id=$1 AND payload->>'status'='test' AND created_at>now()-interval '1 minute')", target).Scan(&recent); err != nil {
		return "", err
	}
	if recent {
		return "", fmt.Errorf("%w: wait one minute before sending another test", ErrInput)
	}
	var count int
	if err = tx.QueryRow(ctx, "SELECT count(*) FROM (SELECT 1 FROM deployment_notification_deliveries WHERE status IN ('pending','sending') LIMIT 10000) q").Scan(&count); err != nil {
		return "", err
	}
	if count >= 10000 {
		return "", fmt.Errorf("%w: notification queue is full", ErrInput)
	}
	id := NewID()
	payload := map[string]any{"schema_version": 1, "event_id": id, "application_id": a.ID, "application_name": a.Name, "project": a.Project, "environment": a.Environment, "revision": a.Revision, "status": "test", "occurred_at": time.Now().UTC()}
	_, err = tx.Exec(ctx, "INSERT INTO deployment_notification_deliveries(id,target_id,target_revision,payload) VALUES($1,$2,$3,$4)", id, target, revision, JSON(payload))
	if err != nil {
		return "", err
	}
	return id, tx.Commit(ctx)
}
func (s *Store) ClaimNotification(ctx context.Context) (*NotificationDelivery, error) {
	lease := NewID()
	d := &NotificationDelivery{LeaseID: lease}
	err := s.Pool.QueryRow(ctx, `UPDATE deployment_notification_deliveries SET status='sending',attempts=attempts+1,lease_id=$1,lease_until=now()+interval '45 seconds'
 WHERE id=(SELECT id FROM deployment_notification_deliveries WHERE (status='pending' AND next_attempt_at<=now() OR status='sending' AND lease_until<now())
 AND attempts<5 AND created_at>now()-interval '24 hours' ORDER BY created_at,id LIMIT 1 FOR UPDATE SKIP LOCKED)
 RETURNING id,target_id,target_revision,payload,attempts,created_at`, lease).Scan(&d.ID, &d.TargetID, &d.TargetRevision, &d.Payload, &d.Attempts, &d.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return d, err
}
func (s *Store) FinishNotification(ctx context.Context, d NotificationDelivery, outcome, message string) error {
	if !slices.Contains([]string{"sent", "failed", "skipped", "pending"}, outcome) {
		return ErrInput
	}
	if outcome == "pending" && d.Attempts >= 5 {
		outcome = "failed"
		message = "Delivery failed after five attempts."
	}
	_, err := s.Pool.Exec(ctx, `UPDATE deployment_notification_deliveries SET status=$3,last_error=$4,lease_id='',lease_until=NULL,
 next_attempt_at=now()+make_interval(secs=>$5),finished_at=CASE WHEN $3='pending' THEN NULL ELSE now() END
 WHERE id=$1 AND lease_id=$2 AND status='sending'`, d.ID, d.LeaseID, outcome, message, min(1800, 30*(1<<d.Attempts)))
	return err
}
func (s *Store) PruneNotifications(ctx context.Context) error {
	_, err := s.Pool.Exec(ctx, `UPDATE deployment_notification_deliveries SET status='failed',finished_at=now(),last_error='Delivery deadline or retry limit reached.'
 WHERE id IN (SELECT id FROM deployment_notification_deliveries WHERE (status='pending' OR status='sending' AND lease_until<now())
 AND (attempts>=5 OR created_at<now()-interval '24 hours') LIMIT 100)`)
	if err != nil {
		return err
	}
	_, err = s.Pool.Exec(ctx, "DELETE FROM deployment_notification_deliveries WHERE id IN (SELECT id FROM deployment_notification_deliveries WHERE status IN ('sent','failed','skipped') AND created_at<now()-interval '30 days' ORDER BY created_at LIMIT 100)")
	return err
}
