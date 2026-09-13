package store

import (
	"context"
	"fmt"
	"time"
)

func (s *Store) UnknownNodeAlarms(ctx context.Context, at time.Time) error {
	at = at.UTC().Truncate(time.Microsecond)
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	_, err = tx.Exec(ctx, `UPDATE alarm_states SET observation_status='unknown',last_checked_at=$1,
		unhealthy_since=CASE WHEN active_incident_id IS NULL THEN NULL ELSE unhealthy_since END
		WHERE fingerprint IN (SELECT fingerprint FROM alarm_states WHERE application_id='' AND (active_incident_id IS NOT NULL OR unhealthy_since IS NOT NULL) AND last_checked_at<$1 ORDER BY last_checked_at LIMIT 800)`, at)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE alarm_incidents SET observation_status='unknown' WHERE id IN
		(SELECT active_incident_id FROM alarm_states WHERE application_id='' AND last_checked_at=$1 AND observation_status='unknown' AND active_incident_id IS NOT NULL LIMIT 800)`, at)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// RemovedAlarmResources is called only after an authoritative, complete
// inventory read. Removal resolves the rule with an explicit explanation; it
// does not assert that an absent resource is healthy.
func (s *Store) RemovedAlarmResources(ctx context.Context, kind, applicationID string, revision int64, current []string, at time.Time, emailAvailable bool) error {
	at = at.UTC().Truncate(time.Microsecond)
	if (kind != "service" && kind != "node" && kind != "application") || len(current) > 200 {
		return fmt.Errorf("%w: invalid removed-resource inventory", ErrInput)
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if kind == "service" {
		var latest int64
		if err := tx.QueryRow(ctx, "SELECT revision FROM applications WHERE id=$1 FOR SHARE", applicationID).Scan(&latest); err != nil {
			return err
		}
		if latest != revision {
			return nil // The observation predates a new application configuration.
		}
	}
	known := make([]string, 0, len(current)*4)
	for _, id := range current {
		if kind == "service" {
			known = append(known, "service-not-ready:"+id)
		}
		if kind == "node" {
			for _, rule := range []string{"node-not-ready", "node-disk-pressure", "node-memory-pressure", "node-pid-pressure"} {
				known = append(known, rule+":"+id)
			}
		}
	}
	pendingWhere := "application_id<>'' AND NOT EXISTS(SELECT 1 FROM applications a WHERE a.id=alarm_states.application_id)"
	pendingArgs := []any{at}
	if kind != "application" {
		pendingWhere = "application_id=$2 AND NOT(fingerprint=ANY($3::text[]))"
		pendingArgs = append(pendingArgs, applicationID, known)
		if kind == "service" {
			pendingWhere += " AND fingerprint LIKE 'service-not-ready:%'"
		}
	}
	if _, err = tx.Exec(ctx, "UPDATE alarm_states SET unhealthy_since=NULL,observation_status='unknown',last_checked_at=$1 WHERE fingerprint IN (SELECT fingerprint FROM alarm_states WHERE active_incident_id IS NULL AND unhealthy_since IS NOT NULL AND last_checked_at<$1 AND "+pendingWhere+" ORDER BY fingerprint LIMIT 800)", pendingArgs...); err != nil {
		return err
	}
	query := `SELECT st.fingerprint,a.id,a.resource_type,a.resource_name,a.project,a.environment,a.application_id
		FROM alarm_states st JOIN alarm_incidents a ON a.id=st.active_incident_id WHERE a.status='active' AND st.last_checked_at<$1`
	args := []any{at}
	switch kind {
	case "service":
		query += " AND a.resource_type='service' AND a.application_id=$2 AND NOT(a.resource_id=ANY($3::text[]))"
		args = append(args, applicationID, current)
	case "node":
		query += " AND a.resource_type='node' AND NOT(a.resource_id=ANY($2::text[]))"
		args = append(args, current)
	case "application":
		query += " AND a.application_id<>'' AND NOT EXISTS(SELECT 1 FROM applications app WHERE app.id=a.application_id)"
	}
	query += " ORDER BY a.id LIMIT 100 FOR UPDATE OF st,a SKIP LOCKED"
	rows, err := tx.Query(ctx, query, args...)
	if err != nil {
		return err
	}
	type removed struct {
		AlarmScope
		fingerprint, id, kind, name string
	}
	items := make([]removed, 0, 100)
	for rows.Next() {
		var item removed
		if err = rows.Scan(&item.fingerprint, &item.id, &item.kind, &item.name, &item.Project, &item.Environment, &item.ApplicationID); err != nil {
			rows.Close()
			return err
		}
		items = append(items, item)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, item := range items {
		settings, err := effectiveAlarmSettings(ctx, tx, item.AlarmScope)
		if err != nil {
			return err
		}
		summary := fmt.Sprintf("The %s %s was removed; this alarm no longer applies.", item.kind, item.name)
		if _, err = tx.Exec(ctx, `UPDATE alarm_incidents SET status='recovered',observation_status='unknown',summary=$2,recovered_at=$3,updated_at=$3 WHERE id=$1`, item.id, summary, at); err != nil {
			return err
		}
		if err = alarmTransition(ctx, tx, item.id, "recovered", summary, at, settings.Enabled && settings.EmailEnabled && emailAvailable && item.kind != "service"); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `UPDATE alarm_states SET active_incident_id=NULL,unhealthy_since=NULL,observation_status='unknown',last_checked_at=$2 WHERE fingerprint=$1`, item.fingerprint, at); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}
