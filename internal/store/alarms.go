package store

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

type AlarmScope struct {
	Project       string `json:"project"`
	Environment   string `json:"environment"`
	ApplicationID string `json:"application_id"`
}

type AlarmSettings struct {
	AlarmScope
	Enabled        bool   `json:"enabled"`
	HoldSeconds    int    `json:"hold_seconds"`
	EmailEnabled   bool   `json:"email_enabled"`
	Revision       int64  `json:"revision"`
	Inherited      bool   `json:"inherited"`
	Source         string `json:"source"`
	EmailAvailable bool   `json:"email_available"`
	CanManage      bool   `json:"can_manage"`
}

type AlarmSettingsInput struct {
	Enabled          bool  `json:"enabled"`
	HoldSeconds      int   `json:"hold_seconds"`
	EmailEnabled     bool  `json:"email_enabled"`
	ExpectedRevision int64 `json:"expected_revision"`
}

type Alarm struct {
	AlarmScope
	ID                string     `json:"id"`
	Rule              string     `json:"rule"`
	ResourceType      string     `json:"resource_type"`
	ResourceID        string     `json:"resource_id"`
	ResourceName      string     `json:"resource_name"`
	Service           string     `json:"service"`
	Status            string     `json:"status"`
	Summary           string     `json:"summary"`
	FirstObservedAt   time.Time  `json:"first_observed_at"`
	FiredAt           time.Time  `json:"fired_at"`
	RecoveredAt       *time.Time `json:"recovered_at"`
	LastObservedAt    time.Time  `json:"last_observed_at"`
	UpdatedAt         time.Time  `json:"updated_at"`
	LastEventID       int64      `json:"last_event_id"`
	Read              bool       `json:"read"`
	Acknowledged      bool       `json:"acknowledged"`
	ObservationStatus string     `json:"observation_status"`
}

type AlarmList struct {
	Items      []Alarm `json:"items"`
	NextCursor string  `json:"next_cursor"`
	Summary    struct {
		Active int64 `json:"active"`
		Unread int64 `json:"unread"`
	} `json:"summary"`
}

func CanManageAlarmSettings(p Principal, scope AlarmScope) bool {
	if scope.Project == "" {
		return p.IsAdmin()
	}
	if scope.ApplicationID != "" {
		return p.Allows("deployments:write", scope.Project, scope.Environment, scope.ApplicationID)
	}
	return p.CanManageProject(scope.Project) && p.Allows("deployments:write", scope.Project, scope.Environment, "")
}

func (s *Store) ValidateAlarmSettingsScope(ctx context.Context, p Principal, scope AlarmScope, write bool) error {
	if scope.Project == "" {
		if scope.Environment != "" || scope.ApplicationID != "" {
			return fmt.Errorf("%w: project is required for an environment or application", ErrInput)
		}
		if !p.IsAdmin() {
			return ErrForbidden
		}
		return nil
	}
	if !p.Allows("deployments:read", scope.Project, scope.Environment, scope.ApplicationID) || (write && !CanManageAlarmSettings(p, scope)) {
		return ErrForbidden
	}
	var exists bool
	var err error
	switch {
	case scope.ApplicationID != "":
		if scope.Environment == "" {
			return fmt.Errorf("%w: environment is required for application settings", ErrInput)
		}
		err = s.Pool.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM applications WHERE id=$1 AND project=$2 AND environment=$3)", scope.ApplicationID, scope.Project, scope.Environment).Scan(&exists)
	case scope.Environment != "":
		err = s.Pool.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM environments WHERE project=$1 AND name=$2)", scope.Project, scope.Environment).Scan(&exists)
	default:
		err = s.Pool.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM projects WHERE name=$1)", scope.Project).Scan(&exists)
	}
	if err == nil && !exists {
		return pgx.ErrNoRows
	}
	return err
}

type alarmQueryer interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func effectiveAlarmSettings(ctx context.Context, db alarmQueryer, scope AlarmScope) (AlarmSettings, error) {
	result := AlarmSettings{AlarmScope: scope, Enabled: true, HoldSeconds: 120, Inherited: true, Source: "default"}
	var stored AlarmScope
	var revision int64
	err := db.QueryRow(ctx, `SELECT project,environment,application_id,enabled,hold_seconds,email_enabled,revision FROM alarm_settings
		WHERE project=$1 AND (environment='' OR environment=$2) AND (application_id='' OR application_id=$3)
		ORDER BY (application_id<>'') DESC,(environment<>'') DESC LIMIT 1`, scope.Project, scope.Environment, scope.ApplicationID).
		Scan(&stored.Project, &stored.Environment, &stored.ApplicationID, &result.Enabled, &result.HoldSeconds, &result.EmailEnabled, &revision)
	if errors.Is(err, pgx.ErrNoRows) {
		return result, nil
	}
	if err != nil {
		return result, err
	}
	result.Source = "project"
	if stored.Project == "" {
		result.Source = "installation"
	} else if stored.ApplicationID != "" {
		result.Source = "application"
	} else if stored.Environment != "" {
		result.Source = "environment"
	}
	result.Inherited = stored != scope
	if !result.Inherited {
		result.Revision = revision
	}
	return result, nil
}

func (s *Store) AlarmSettings(ctx context.Context, p Principal, scope AlarmScope) (AlarmSettings, error) {
	if err := s.ValidateAlarmSettingsScope(ctx, p, scope, false); err != nil {
		return AlarmSettings{}, err
	}
	result, err := effectiveAlarmSettings(ctx, s.Pool, scope)
	result.CanManage = CanManageAlarmSettings(p, scope)
	return result, err
}

func (s *Store) PutAlarmSettings(ctx context.Context, p Principal, scope AlarmScope, input AlarmSettingsInput) (AlarmSettings, error) {
	if input.HoldSeconds < 0 || input.HoldSeconds > 3600 || input.ExpectedRevision < 0 {
		return AlarmSettings{}, fmt.Errorf("%w: hold_seconds must be 0–3600 and expected_revision must be nonnegative", ErrInput)
	}
	if err := s.ValidateAlarmSettingsScope(ctx, p, scope, true); err != nil {
		return AlarmSettings{}, err
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return AlarmSettings{}, err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `INSERT INTO alarm_settings(project,environment,application_id,updated_by,revision) VALUES($1,$2,$3,$4,0) ON CONFLICT DO NOTHING`, scope.Project, scope.Environment, scope.ApplicationID, p.ID); err != nil {
		return AlarmSettings{}, err
	}
	row, err := tx.Exec(ctx, `UPDATE alarm_settings SET enabled=$4,hold_seconds=$5,email_enabled=$6,revision=revision+1,updated_by=$7,updated_at=now()
		WHERE project=$1 AND environment=$2 AND application_id=$3 AND revision=$8`, scope.Project, scope.Environment, scope.ApplicationID, input.Enabled, input.HoldSeconds, input.EmailEnabled, p.ID, input.ExpectedRevision)
	if err != nil {
		return AlarmSettings{}, err
	}
	if row.RowsAffected() != 1 {
		return AlarmSettings{}, ErrConflict
	}
	if _, err = tx.Exec(ctx, "INSERT INTO audit_events(identity_id,key_id,action,resource,metadata) VALUES($1,$2,'alarms.settings.updated',$3,$4)", p.ID, p.KeyID, scope.Project+"/"+scope.Environment+"/"+scope.ApplicationID, JSON(input)); err != nil {
		return AlarmSettings{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return AlarmSettings{}, err
	}
	return s.AlarmSettings(ctx, p, scope)
}

const alarmColumns = `a.id,a.rule,a.resource_type,a.resource_id,a.resource_name,a.project,a.environment,a.application_id,a.service,a.status,a.summary,a.first_observed_at,a.fired_at,a.recovered_at,a.last_observed_at,a.updated_at,a.last_event_id,
	COALESCE(r.read_event_id,0)>=a.last_event_id,COALESCE(r.acknowledged_event_id,0)>=a.last_event_id,
 CASE WHEN a.status='active' AND a.last_observed_at<now()-interval '5 minutes' THEN 'unknown' ELSE a.observation_status END`
const alarmJoins = ` FROM alarm_incidents a LEFT JOIN alarm_reads r ON r.incident_id=a.id AND r.identity_id=$1`

func scanAlarm(row scanner) (Alarm, error) {
	var a Alarm
	err := row.Scan(&a.ID, &a.Rule, &a.ResourceType, &a.ResourceID, &a.ResourceName, &a.Project, &a.Environment, &a.ApplicationID, &a.Service, &a.Status, &a.Summary, &a.FirstObservedAt, &a.FiredAt, &a.RecoveredAt, &a.LastObservedAt, &a.UpdatedAt, &a.LastEventID, &a.Read, &a.Acknowledged, &a.ObservationStatus)
	return a, err
}

type alarmFilter struct {
	where []string
	args  []any
}

func (f *alarmFilter) equal(column, value string) {
	if value != "" {
		f.args = append(f.args, value)
		f.where = append(f.where, fmt.Sprintf("%s=$%d", column, len(f.args)))
	}
}

// Apply both credential and current identity restrictions in SQL, before
// pagination and aggregates. Installation incidents never leak into project keys.
func alarmVisibility(p Principal, scope AlarmScope, status string) alarmFilter {
	f := alarmFilter{args: []any{p.ID}, where: []string{"TRUE"}}
	if !p.IsAdmin() {
		f.where = append(f.where, "a.project<>''")
		if !contains(p.Permissions, "admin") && !contains(p.Permissions, "deployments:read") {
			f.where = append(f.where, "FALSE")
		}
		if !p.Admin {
			if p.Email != "" {
				projects := []string{}
				for _, role := range p.ProjectRoles {
					if contains(rolePermissions(role.Role), "deployments:read") {
						projects = append(projects, role.Project)
					}
				}
				f.args = append(f.args, projects)
				f.where = append(f.where, fmt.Sprintf("a.project=ANY($%d::text[])", len(f.args)))
			} else if !contains(p.IdentityPermissions, "deployments:read") {
				f.where = append(f.where, "FALSE")
			}
		}
		f.equal("a.project", p.Project)
		f.equal("a.environment", p.Environment)
		f.equal("a.application_id", p.Application)
		f.equal("a.project", p.IdentityProject)
		f.equal("a.environment", p.IdentityEnvironment)
	}
	f.equal("a.project", scope.Project)
	f.equal("a.environment", scope.Environment)
	f.equal("a.application_id", scope.ApplicationID)
	f.equal("a.status", status)
	return f
}

func alarmReadable(p Principal, a Alarm) bool {
	if a.Project == "" {
		return p.IsAdmin()
	}
	return p.Allows("deployments:read", a.Project, a.Environment, a.ApplicationID)
}

type alarmCursor struct {
	At time.Time `json:"at"`
	ID string    `json:"id"`
}

func (s *Store) Alarms(ctx context.Context, p Principal, scope AlarmScope, status, cursor string, limit int) (AlarmList, error) {
	result := AlarmList{Items: []Alarm{}}
	if limit < 1 || limit > 50 || (status != "" && status != "active" && status != "recovered") || len(cursor) > 256 {
		return result, fmt.Errorf("%w: invalid alarm page or status", ErrInput)
	}
	f := alarmVisibility(p, scope, status)
	baseWhere := strings.Join(f.where, " AND ")
	if err := s.Pool.QueryRow(ctx, `SELECT count(*) FILTER(WHERE a.status='active'),count(*) FILTER(WHERE COALESCE(r.read_event_id,0)<a.last_event_id)`+alarmJoins+" WHERE "+baseWhere, f.args...).Scan(&result.Summary.Active, &result.Summary.Unread); err != nil {
		return result, err
	}
	if cursor != "" {
		var c alarmCursor
		data, err := base64.RawURLEncoding.DecodeString(cursor)
		if err != nil || json.Unmarshal(data, &c) != nil || c.ID == "" || c.At.IsZero() {
			return result, fmt.Errorf("%w: invalid alarm cursor", ErrInput)
		}
		f.args = append(f.args, c.At, c.ID)
		f.where = append(f.where, fmt.Sprintf("(a.updated_at,a.id)<($%d,$%d)", len(f.args)-1, len(f.args)))
	}
	f.args = append(f.args, limit+1)
	rows, err := s.Pool.Query(ctx, "SELECT "+alarmColumns+alarmJoins+" WHERE "+strings.Join(f.where, " AND ")+fmt.Sprintf(" ORDER BY a.updated_at DESC,a.id DESC LIMIT $%d", len(f.args)), f.args...)
	if err != nil {
		return result, err
	}
	defer rows.Close()
	for rows.Next() {
		a, err := scanAlarm(rows)
		if err != nil {
			return result, err
		}
		if len(result.Items) == limit {
			last := result.Items[len(result.Items)-1]
			result.NextCursor = base64.RawURLEncoding.EncodeToString(JSON(alarmCursor{At: last.UpdatedAt, ID: last.ID}))
			break
		}
		result.Items = append(result.Items, a)
	}
	return result, rows.Err()
}

func (s *Store) ReadAlarm(ctx context.Context, p Principal, id string, acknowledge bool, expectedEvent ...int64) (Alarm, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return Alarm{}, err
	}
	defer tx.Rollback(ctx)
	a, err := scanAlarm(tx.QueryRow(ctx, "SELECT "+alarmColumns+alarmJoins+" WHERE a.id=$2 FOR UPDATE OF a", p.ID, id))
	if err != nil {
		return Alarm{}, err
	}
	if !alarmReadable(p, a) {
		return Alarm{}, ErrForbidden
	}
	if len(expectedEvent) > 0 && expectedEvent[0] != a.LastEventID {
		return Alarm{}, ErrConflict
	}
	ack := int64(0)
	if acknowledge {
		ack = a.LastEventID
	}
	_, err = tx.Exec(ctx, `INSERT INTO alarm_reads(incident_id,identity_id,read_event_id,acknowledged_event_id) VALUES($1,$2,$3,$4)
		ON CONFLICT(incident_id,identity_id) DO UPDATE SET read_event_id=GREATEST(alarm_reads.read_event_id,$3),acknowledged_event_id=GREATEST(alarm_reads.acknowledged_event_id,$4),updated_at=now()`, id, p.ID, a.LastEventID, ack)
	if err != nil {
		return Alarm{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return Alarm{}, err
	}
	a.Read = true
	a.Acknowledged = a.Acknowledged || acknowledge
	return a, nil
}
