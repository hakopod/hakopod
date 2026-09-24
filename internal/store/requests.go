package store

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/hakopod/hakopod/internal/requestlog"
	"github.com/jackc/pgx/v5"
)

type RequestQuery struct {
	Project       string `json:"project"`
	Environment   string `json:"environment"`
	ApplicationID string `json:"application_id"`
	Service       string `json:"service"`
	Method        string `json:"method"`
	Status        int    `json:"status"` // 2–5 selects a status class; 100–599 selects an exact status.
	Search        string `json:"search"`
	SinceSeconds  int    `json:"since_seconds"`
	Cursor        string `json:"cursor"`
	Limit         int    `json:"limit"`
}
type RequestCollection struct {
	State         string     `json:"state"`
	Message       string     `json:"message"`
	CheckedAt     time.Time  `json:"checked_at"`
	LastSuccessAt *time.Time `json:"last_success_at"`
	GapCount      int64      `json:"gap_count"`
}
type RequestList struct {
	Items          []requestlog.Entry `json:"items"`
	NextCursor     string             `json:"next_cursor"`
	Collection     RequestCollection  `json:"collection"`
	RetentionHours int                `json:"retention_hours"`
	MaxEntries     int                `json:"max_entries"`
}

func (s *Store) Requests(ctx context.Context, p Principal, q RequestQuery) (RequestList, error) {
	result := RequestList{Items: []requestlog.Entry{}, RetentionHours: 24, MaxEntries: 100000}
	if q.Limit == 0 {
		q.Limit = 50
	}
	if q.SinceSeconds == 0 {
		q.SinceSeconds = 3600
	}
	if q.Limit < 1 || q.Limit > 100 || q.SinceSeconds < 1 || q.SinceSeconds > 86400 || len(q.Search) > 200 || len(q.Method) > 24 || len(q.Service) > 63 || len(q.Project) > 63 || len(q.Environment) > 63 || len(q.ApplicationID) > 64 || len(q.Cursor) > 512 || (q.Status != 0 && (q.Status < 2 || q.Status > 5) && (q.Status < 100 || q.Status > 599)) {
		return result, fmt.Errorf("%w: invalid request filters", ErrInput)
	}
	args := []any{time.Now().Add(-time.Duration(q.SinceSeconds) * time.Second)}
	where := []string{"r.observed_at >= $1"}
	bind := func(value any) string { args = append(args, value); return fmt.Sprintf("$%d", len(args)) }
	eq := func(column, value string) {
		if value != "" {
			where = append(where, column+"="+bind(value))
		}
	}
	// Mirror Principal.Allows(logs:read) in SQL BEFORE pagination. Ingress records
	// without an owned application are visible only to an unrestricted administrator.
	if !p.IsAdmin() {
		where = append(where, "a.id IS NOT NULL")
		if !contains(p.Permissions, "admin") && !contains(p.Permissions, "logs:read") {
			where = append(where, "FALSE")
		}
		if !p.Admin {
			if p.Email != "" {
				projects := []string{}
				for _, r := range p.ProjectRoles {
					if contains(r.permissions(), "logs:read") {
						projects = append(projects, r.Project)
					}
				}
				where = append(where, "a.project=ANY("+bind(projects)+"::text[])")
			} else if !contains(p.IdentityPermissions, "logs:read") {
				where = append(where, "FALSE")
			}
		}
		eq("a.project", p.Project)
		eq("a.environment", p.Environment)
		eq("a.name", p.Application)
		eq("a.project", p.IdentityProject)
		eq("a.environment", p.IdentityEnvironment)
	}
	eq("a.project", q.Project)
	eq("a.environment", q.Environment)
	eq("a.id", q.ApplicationID)
	eq("r.service", q.Service)
	eq("r.method", q.Method)
	if q.Status >= 2 && q.Status <= 5 {
		where = append(where, "r.status/100="+bind(q.Status))
	} else if q.Status != 0 {
		where = append(where, "r.status="+bind(q.Status))
	}
	if q.Search != "" {
		term := bind(q.Search)
		where = append(where, "(strpos(lower(r.host||r.path),lower("+term+"))>0)")
	}
	if q.Cursor != "" {
		var cursor struct {
			At time.Time
			ID string
		}
		data, err := base64.RawURLEncoding.DecodeString(q.Cursor)
		if err != nil || json.Unmarshal(data, &cursor) != nil || cursor.At.IsZero() || len(cursor.ID) != 64 {
			return result, fmt.Errorf("%w: invalid request cursor", ErrInput)
		}
		where = append(where, "(r.observed_at,r.id)<("+bind(cursor.At)+","+bind(cursor.ID)+")")
	}
	rows, err := s.Pool.Query(ctx, "SELECT r.payload,COALESCE(a.id,''),COALESCE(a.name,''),COALESCE(a.project,''),COALESCE(a.environment,'') FROM request_entries r LEFT JOIN applications a ON a.id=r.application_id WHERE "+strings.Join(where, " AND ")+" ORDER BY r.observed_at DESC,r.id DESC LIMIT "+bind(q.Limit+1), args...)
	if err != nil {
		return result, err
	}
	defer rows.Close()
	for rows.Next() {
		var e requestlog.Entry
		var data []byte
		var app, name, project, env string
		if err = rows.Scan(&data, &app, &name, &project, &env); err != nil {
			return result, err
		}
		if err = json.Unmarshal(data, &e); err != nil {
			return result, err
		}
		e.ApplicationID, e.Application, e.Project, e.Environment = app, name, project, env
		if len(result.Items) == q.Limit {
			last := result.Items[len(result.Items)-1]
			result.NextCursor = base64.RawURLEncoding.EncodeToString(JSON(struct {
				At time.Time
				ID string
			}{last.Timestamp, last.ID}))
			break
		}
		result.Items = append(result.Items, e)
	}
	if err = rows.Err(); err != nil {
		return result, err
	}
	rows.Close()
	err = s.Pool.QueryRow(ctx, "SELECT state,message,checked_at,last_success_at,gap_count FROM request_collection WHERE singleton").Scan(&result.Collection.State, &result.Collection.Message, &result.Collection.CheckedAt, &result.Collection.LastSuccessAt, &result.Collection.GapCount)
	if time.Since(result.Collection.CheckedAt) > time.Minute {
		result.Collection.State = "stale"
		result.Collection.Message = "Request collector has not checked in recently. New requests may be missing."
	}
	return result, err
}
func (s *Store) RequestCursors(ctx context.Context) (map[string]time.Time, error) {
	rows, err := s.Pool.Query(ctx, "SELECT id,cursor_at FROM request_sources WHERE updated_at>now()-interval '24 hours' LIMIT 64")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]time.Time{}
	for rows.Next() {
		var id string
		var at time.Time
		if err = rows.Scan(&id, &at); err != nil {
			return nil, err
		}
		out[id] = at
	}
	return out, rows.Err()
}
func (s *Store) SaveRequests(ctx context.Context, entries []requestlog.Entry, cursors map[string]time.Time, state, message string, gaps int) error {
	if len(entries) > 5000 || len(cursors) > 8 {
		return ErrInput
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	// One collector per database holds the session advisory lock. All data and
	// cursor updates commit together; a crash replays overlapping lines safely.
	// Pipeline inserts in bounded batches to avoid one network round trip per request.
	for offset := 0; offset < len(entries); offset += 250 {
		batch := &pgx.Batch{}
		end := min(offset+250, len(entries))
		for _, e := range entries[offset:end] {
			var app any
			if e.ApplicationID != "" {
				app = e.ApplicationID
			}
			batch.Queue("INSERT INTO request_entries(id,observed_at,application_id,service,method,host,path,status,duration_ms,payload) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10) ON CONFLICT(id) DO NOTHING", e.ID, e.Timestamp, app, e.Service, e.Method, e.Host, e.Path, e.Status, e.DurationMS, e.JSON())
		}
		results := tx.SendBatch(ctx, batch)
		if err = results.Close(); err != nil {
			return err
		}
	}
	for id, at := range cursors {
		if _, err = tx.Exec(ctx, "INSERT INTO request_sources(id,cursor_at) VALUES($1,$2) ON CONFLICT(id) DO UPDATE SET cursor_at=GREATEST(request_sources.cursor_at,$2),updated_at=now()", id, at); err != nil {
			return err
		}
	}
	if _, err = tx.Exec(ctx, "DELETE FROM request_entries WHERE observed_at<now()-interval '24 hours' OR id IN (SELECT id FROM request_entries ORDER BY observed_at DESC,id DESC OFFSET 100000)"); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, "DELETE FROM request_sources WHERE updated_at<now()-interval '24 hours'"); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, "UPDATE request_collection SET state=$1,message=$2,checked_at=now(),last_success_at=CASE WHEN $1 IN ('collecting','limited') THEN now() ELSE last_success_at END,gap_count=gap_count+$3 WHERE singleton", state, message, gaps)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}
