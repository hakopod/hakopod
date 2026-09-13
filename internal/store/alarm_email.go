package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// Keep recipient authorization equivalent to a current human browser identity:
// disabled/unverified users, identity scopes and license-gated roles all apply.
const alarmRecipientWhere = ` FROM identities i WHERE NOT i.disabled AND i.email_verified AND COALESCE(i.email,'')<>''
	AND (($1='' AND i.admin AND i.project='' AND i.environment='') OR
	($1<>'' AND (i.project='' OR i.project=$1) AND (i.environment='' OR i.environment=$2) AND
	(i.admin OR EXISTS(SELECT 1 FROM personal_workspaces w WHERE w.identity_id=i.id AND w.project=$1)
	OR ($3 AND EXISTS(SELECT 1 FROM project_members pm WHERE pm.identity_id=i.id AND pm.project=$1))
	OR ($3 AND $4 AND EXISTS(SELECT 1 FROM project_teams pt JOIN team_members tm ON tm.team_id=pt.team_id WHERE tm.identity_id=i.id AND pt.project=$1)))))`

func (s *Store) alarmRecipientArguments(ctx context.Context, db alarmQueryer, scope AlarmScope) ([]any, error) {
	record, err := readLicense(ctx, db, "")
	if err != nil {
		return nil, err
	}
	license := s.licenseStatus(record)
	return []any{scope.Project, scope.Environment, licenseAllows(license, "project_rbac"), licenseAllows(license, "teams")}, nil
}

// FanoutAlarmEmail advances one durable recipient page. A global transaction
// lock bounds the pending queue at 10,000 even with several API processes.
// Events wait for capacity; a 24-hour deadline prevents stale-email floods.
func (s *Store) FanoutAlarmEmail(ctx context.Context) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var locked bool
	if err = tx.QueryRow(ctx, "SELECT pg_try_advisory_xact_lock(hashtext('hakopod.alarm.mail.fanout'))").Scan(&locked); err != nil || !locked {
		return err
	}
	var eventID int64
	var scope AlarmScope
	var cursor string
	var current bool
	var created time.Time
	err = tx.QueryRow(ctx, `SELECT e.id,a.project,a.environment,a.application_id,e.email_cursor,e.created_at,
		a.last_event_id=e.id AND NOT EXISTS(SELECT 1 FROM alarm_incidents newer WHERE newer.fingerprint=a.fingerprint AND newer.fired_at>a.fired_at) FROM alarm_events e JOIN alarm_incidents a ON a.id=e.incident_id
		WHERE e.email_requested AND NOT e.email_complete ORDER BY e.id LIMIT 1 FOR UPDATE OF e SKIP LOCKED`).Scan(&eventID, &scope.Project, &scope.Environment, &scope.ApplicationID, &cursor, &created, &current)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	settings, err := effectiveAlarmSettings(ctx, tx, scope)
	if err != nil {
		return err
	}
	if !current || !settings.Enabled || !settings.EmailEnabled || time.Since(created) > 24*time.Hour {
		_, err = tx.Exec(ctx, "UPDATE alarm_events SET email_complete=true WHERE id=$1", eventID)
		if err != nil {
			return err
		}
		return tx.Commit(ctx)
	}
	var queued int
	if err = tx.QueryRow(ctx, "SELECT count(*) FROM (SELECT 1 FROM alarm_email_deliveries WHERE status IN ('pending','sending') LIMIT 10000) q").Scan(&queued); err != nil {
		return err
	}
	capacity := min(25, 10000-queued)
	if capacity == 0 {
		return nil
	}
	args, err := s.alarmRecipientArguments(ctx, tx, scope)
	if err != nil {
		return err
	}
	args = append(args, cursor, capacity+1)
	rows, err := tx.Query(ctx, "SELECT i.id"+alarmRecipientWhere+" AND i.id>$5 ORDER BY i.id LIMIT $6", args...)
	if err != nil {
		return err
	}
	recipients := make([]string, 0, capacity+1)
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		recipients = append(recipients, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	complete := len(recipients) <= capacity
	if len(recipients) > capacity {
		recipients = recipients[:capacity]
	}
	if len(recipients) > 0 {
		cursor = recipients[len(recipients)-1]
		if _, err = tx.Exec(ctx, `INSERT INTO alarm_email_deliveries(event_id,identity_id) SELECT $1,id FROM unnest($2::text[]) id ON CONFLICT DO NOTHING`, eventID, recipients); err != nil {
			return err
		}
	}
	if _, err = tx.Exec(ctx, "UPDATE alarm_events SET email_cursor=$2,email_complete=$3 WHERE id=$1", eventID, cursor, complete); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

type AlarmEmail struct {
	AlarmScope
	EventID                           int64
	IdentityID, LeaseID, IncidentID   string
	Transition, Summary, ResourceName string
	CreatedAt                         time.Time
	Attempts                          int
}

func (s *Store) ClaimAlarmEmail(ctx context.Context) (*AlarmEmail, error) {
	job := &AlarmEmail{LeaseID: NewID()}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	err = tx.QueryRow(ctx, `WITH candidate AS (
		SELECT event_id,identity_id FROM alarm_email_deliveries WHERE next_attempt_at<=now() AND attempts<6
		AND (status='pending' OR (status='sending' AND lease_until<now())) ORDER BY next_attempt_at,event_id,identity_id LIMIT 1 FOR UPDATE SKIP LOCKED)
		UPDATE alarm_email_deliveries d SET status='sending',attempts=d.attempts+1,lease_id=$1,lease_until=now()+interval '45 seconds'
		FROM candidate c WHERE d.event_id=c.event_id AND d.identity_id=c.identity_id RETURNING d.event_id,d.identity_id,d.attempts`, job.LeaseID).Scan(&job.EventID, &job.IdentityID, &job.Attempts)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	err = tx.QueryRow(ctx, `SELECT e.incident_id,e.transition,e.summary,e.created_at,a.project,a.environment,a.application_id,a.resource_name
		FROM alarm_events e JOIN alarm_incidents a ON a.id=e.incident_id WHERE e.id=$1`, job.EventID).Scan(&job.IncidentID, &job.Transition, &job.Summary, &job.CreatedAt, &job.Project, &job.Environment, &job.ApplicationID, &job.ResourceName)
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return job, nil
}

// Recheck the current scope setting, license, membership and verified address
// on every attempt; neither queued addresses nor old Principal values are used.
func (s *Store) AlarmEmailRecipient(ctx context.Context, job AlarmEmail) (string, error) {
	if time.Since(job.CreatedAt) > 24*time.Hour {
		return "", ErrForbidden
	}
	var owned bool
	if err := s.Pool.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM alarm_email_deliveries WHERE event_id=$1 AND identity_id=$2 AND lease_id=$3 AND lease_until>now() AND status='sending')", job.EventID, job.IdentityID, job.LeaseID).Scan(&owned); err != nil {
		return "", err
	}
	if !owned {
		return "", ErrConflict
	}
	var current bool
	if err := s.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM alarm_incidents a WHERE a.id=$1 AND a.last_event_id=$2
		AND NOT EXISTS(SELECT 1 FROM alarm_incidents newer WHERE newer.fingerprint=a.fingerprint AND newer.fired_at>a.fired_at))`, job.IncidentID, job.EventID).Scan(&current); err != nil {
		return "", err
	}
	if !current {
		return "", ErrForbidden // Recovery/reopening superseded this queued transition.
	}
	settings, err := effectiveAlarmSettings(ctx, s.Pool, job.AlarmScope)
	if err != nil {
		return "", err
	}
	if !settings.Enabled || !settings.EmailEnabled {
		return "", ErrForbidden
	}
	args, err := s.alarmRecipientArguments(ctx, s.Pool, job.AlarmScope)
	if err != nil {
		return "", err
	}
	args = append(args, job.IdentityID)
	var recipient string
	err = s.Pool.QueryRow(ctx, "SELECT i.email"+alarmRecipientWhere+" AND i.id=$5", args...).Scan(&recipient)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrForbidden
	}
	return recipient, err
}

func (s *Store) FinishAlarmEmail(ctx context.Context, job AlarmEmail, outcome string) error {
	status, diagnostic := outcome, ""
	if outcome == "retry" {
		status, diagnostic = "pending", "SMTP delivery failed; retry scheduled."
		if job.Attempts >= 6 || time.Since(job.CreatedAt) > 24*time.Hour {
			status, diagnostic = "failed", "Email delivery stopped after the retry limit or delivery deadline."
		}
	}
	if status != "sent" && status != "skipped" && status != "pending" && status != "failed" {
		return fmt.Errorf("%w: invalid alarm delivery outcome", ErrInput)
	}
	delay := time.Duration(30*(1<<min(max(job.Attempts-1, 0), 5))) * time.Second
	result, err := s.Pool.Exec(ctx, `UPDATE alarm_email_deliveries SET status=$4,last_error=$5,next_attempt_at=now()+$6::interval,
		delivered_at=CASE WHEN $4='sent' THEN now() ELSE delivered_at END,lease_id='',lease_until=NULL
		WHERE event_id=$1 AND identity_id=$2 AND lease_id=$3 AND status='sending' AND lease_until>now()`, job.EventID, job.IdentityID, job.LeaseID, status, diagnostic, fmt.Sprintf("%d seconds", int(delay.Seconds())))
	if err == nil && result.RowsAffected() != 1 {
		return ErrConflict
	}
	return err
}
