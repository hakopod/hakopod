package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

// AlarmObservation contains fresh resource facts, never applications.status or
// a cached deployment result. Unknown keeps an active incident open and breaks
// a pending hold; elapsed downtime cannot prove continuous unhealthiness.
type AlarmObservation struct {
	AlarmScope
	Rule, ResourceType, ResourceID, ResourceName, Service string
	Health, Summary                                       string
	At                                                    time.Time
}

const AlarmObservationMaxGap = 5 * time.Minute

func (o AlarmObservation) fingerprint() string { return o.Rule + ":" + o.ResourceID }

func (s *Store) ApplyAlarmObservations(ctx context.Context, observations []AlarmObservation, emailAvailable bool, expectedRevision ...int64) error {
	if len(observations) == 0 {
		return nil
	}
	if len(observations) > 64 {
		return fmt.Errorf("%w: alarm observation batch exceeds 64", ErrInput)
	}
	// PostgreSQL timestamptz stores microseconds. Keep comparisons at the same
	// precision so a repeated JSON snapshot cannot look newer after each read.
	observations = append([]AlarmObservation(nil), observations...)
	for i := range observations {
		observations[i].At = observations[i].At.UTC().Truncate(time.Microsecond)
	}
	sameTime := true
	for _, observation := range observations {
		if observation.AlarmScope != observations[0].AlarmScope || observation.At.IsZero() || len(observation.ResourceID) > 512 || len(observation.Rule) > 80 || observation.ResourceID == "" {
			return fmt.Errorf("%w: invalid alarm observation scope or resource", ErrInput)
		}
		if !observation.At.Equal(observations[0].At) {
			sameTime = false
		}
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if len(expectedRevision) > 0 {
		var revision int64
		if err = tx.QueryRow(ctx, "SELECT revision FROM applications WHERE id=$1 FOR SHARE", observations[0].ApplicationID).Scan(&revision); err != nil {
			return err
		}
		if revision != expectedRevision[0] {
			return ErrConflict
		}
	}
	// One aggregate timestamp replaces per-service reads for an unchanged
	// Resync snapshot. The entire application batch commits atomically below.
	last := observations[len(observations)-1]
	if sameTime && last.ResourceType == "application" && last.ResourceID == last.ApplicationID {
		var checked time.Time
		err := tx.QueryRow(ctx, "SELECT last_checked_at FROM alarm_states WHERE fingerprint=$1", last.fingerprint()).Scan(&checked)
		if err == nil && !last.At.After(checked) {
			return nil
		}
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
	}
	settings, err := effectiveAlarmSettings(ctx, tx, observations[0].AlarmScope)
	if err != nil {
		return err
	}
	for _, observation := range observations {
		if !settings.Enabled {
			observation.Health = "unknown"
		}
		if err := applyAlarmObservation(ctx, tx, observation, settings, emailAvailable); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func applyAlarmObservation(ctx context.Context, tx pgx.Tx, observation AlarmObservation, settings AlarmSettings, emailAvailable bool) error {
	if observation.Health != "healthy" && observation.Health != "unhealthy" && observation.Health != "unknown" {
		return fmt.Errorf("%w: invalid alarm observation status", ErrInput)
	}
	observation.Summary = strings.ReplaceAll(strings.ToValidUTF8(observation.Summary, "�"), "\x00", "")
	if len(observation.Summary) > 1024 {
		end := 1024
		for end > 0 && !utf8.RuneStart(observation.Summary[end]) {
			end--
		}
		observation.Summary = observation.Summary[:end]
	}
	fingerprint := observation.fingerprint()
	if _, err := tx.Exec(ctx, `INSERT INTO alarm_states(fingerprint,application_id,last_checked_at,observation_status) VALUES($1,$2,'epoch','unknown') ON CONFLICT DO NOTHING`, fingerprint, observation.ApplicationID); err != nil {
		return err
	}
	var since *time.Time
	var checked time.Time
	var active *string
	if err := tx.QueryRow(ctx, `SELECT unhealthy_since,last_checked_at,active_incident_id FROM alarm_states WHERE fingerprint=$1 FOR UPDATE`, fingerprint).Scan(&since, &checked, &active); err != nil {
		return err
	}
	if !observation.At.After(checked) {
		return nil // Duplicate, concurrent, or out-of-order observation.
	}
	if active == nil && observation.At.Sub(checked) > AlarmObservationMaxGap {
		since = nil
	}
	email := settings.EmailEnabled && emailAvailable && observation.ResourceType != "service"
	switch observation.Health {
	case "unknown":
		if active == nil {
			since = nil
		} else if _, err := tx.Exec(ctx, "UPDATE alarm_incidents SET observation_status='unknown' WHERE id=$1", *active); err != nil {
			return err
		}
	case "unhealthy":
		if since == nil {
			at := observation.At
			since = &at
		}
		if active != nil {
			if _, err := tx.Exec(ctx, `UPDATE alarm_incidents SET last_observed_at=$2,summary=$3,observation_status='unhealthy' WHERE id=$1 AND status='active'`, *active, observation.At, observation.Summary); err != nil {
				return err
			}
		} else if observation.At.Sub(*since) >= time.Duration(settings.HoldSeconds)*time.Second {
			id := NewID()
			_, err := tx.Exec(ctx, `INSERT INTO alarm_incidents(id,fingerprint,rule,resource_type,resource_id,resource_name,project,environment,application_id,service,status,summary,first_observed_at,fired_at,last_observed_at,updated_at)
				VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,'active',$11,$12,$13,$13,$13)`, id, fingerprint, observation.Rule, observation.ResourceType, observation.ResourceID, observation.ResourceName, observation.Project, observation.Environment, observation.ApplicationID, observation.Service, observation.Summary, since, observation.At)
			if err != nil {
				return err
			}
			if err = alarmTransition(ctx, tx, id, "active", observation.Summary, observation.At, email); err != nil {
				return err
			}
			active = &id
		}
	case "healthy":
		since = nil
		if active != nil {
			if _, err := tx.Exec(ctx, `UPDATE alarm_incidents SET status='recovered',summary=$2,recovered_at=$3,last_observed_at=$3,updated_at=$3,observation_status='healthy' WHERE id=$1 AND status='active'`, *active, observation.Summary, observation.At); err != nil {
				return err
			}
			if err := alarmTransition(ctx, tx, *active, "recovered", observation.Summary, observation.At, email); err != nil {
				return err
			}
			active = nil
		}
	}
	_, err := tx.Exec(ctx, `UPDATE alarm_states SET unhealthy_since=$2,last_checked_at=$3,observation_status=$4,active_incident_id=$5 WHERE fingerprint=$1`, fingerprint, since, observation.At, observation.Health, active)
	return err
}

func alarmTransition(ctx context.Context, tx pgx.Tx, incident, transition, summary string, at time.Time, email bool) error {
	var eventID int64
	if err := tx.QueryRow(ctx, `INSERT INTO alarm_events(incident_id,transition,summary,created_at,email_requested) VALUES($1,$2,$3,$4,$5)
		ON CONFLICT(incident_id,transition) DO UPDATE SET incident_id=EXCLUDED.incident_id RETURNING id`, incident, transition, summary, at, email).Scan(&eventID); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, "UPDATE alarm_incidents SET last_event_id=$2 WHERE id=$1", incident, eventID)
	return err
}

type AlarmEvaluationLease struct {
	ID       string
	Cursor   string
	NodesDue bool
}

// A short persisted lease prevents every API replica from scanning Kubernetes.
// State row locks and observation timestamps also make a lease-expiry overlap safe.
func (s *Store) ClaimAlarmEvaluation(ctx context.Context) (*AlarmEvaluationLease, error) {
	lease := &AlarmEvaluationLease{ID: NewID()}
	err := s.Pool.QueryRow(ctx, `UPDATE alarm_evaluator SET lease_id=$1,lease_until=now()+interval '45 seconds'
		WHERE singleton AND (lease_until IS NULL OR lease_until<now()) RETURNING application_cursor,nodes_due_at<=now()`, lease.ID).Scan(&lease.Cursor, &lease.NodesDue)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return lease, err
}

func (s *Store) NextAlarmApplication(ctx context.Context, cursor string) (Application, error) {
	return scanApp(s.Pool.QueryRow(ctx, "SELECT "+appCols+" FROM applications WHERE id>$1 ORDER BY id LIMIT 1", cursor))
}

func (s *Store) AdvanceAlarmEvaluation(ctx context.Context, lease *AlarmEvaluationLease, nodesChecked bool, release bool) error {
	result, err := s.Pool.Exec(ctx, `UPDATE alarm_evaluator SET application_cursor=$2,
		nodes_due_at=CASE WHEN $3 THEN now()+interval '30 seconds' ELSE nodes_due_at END,
		lease_until=CASE WHEN $4 THEN NULL ELSE lease_until END WHERE singleton AND lease_id=$1 AND lease_until>now()`, lease.ID, lease.Cursor, nodesChecked, release)
	if err == nil && result.RowsAffected() != 1 {
		return ErrConflict
	}
	return err
}

// Retention is incremental; no transaction loads historical incidents or mail
// into memory. Active incidents are never removed by the retention sweep.
func (s *Store) PruneAlarms(ctx context.Context) error {
	_, err := s.Pool.Exec(ctx, `UPDATE alarm_email_deliveries SET status='failed',lease_id='',lease_until=NULL,last_error='Email delivery deadline or retry limit reached.'
		WHERE (event_id,identity_id) IN (SELECT d.event_id,d.identity_id FROM alarm_email_deliveries d JOIN alarm_events e ON e.id=d.event_id
		WHERE d.status IN ('pending','sending') AND (d.lease_until IS NULL OR d.lease_until<now()) AND (d.attempts>=6 OR e.created_at<now()-interval '24 hours') LIMIT 100)`)
	if err != nil {
		return err
	}
	_, err = s.Pool.Exec(ctx, `DELETE FROM alarm_incidents WHERE id IN (SELECT id FROM alarm_incidents WHERE status='recovered' AND recovered_at<now()-interval '30 days' ORDER BY recovered_at LIMIT 100)`)
	if err != nil {
		return err
	}
	_, err = s.Pool.Exec(ctx, `DELETE FROM alarm_states WHERE fingerprint IN (SELECT fingerprint FROM alarm_states WHERE active_incident_id IS NULL AND last_checked_at<now()-interval '30 days' ORDER BY last_checked_at LIMIT 100)`)
	return err
}
