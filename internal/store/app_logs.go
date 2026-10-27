package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/hakopod/hakopod/internal/applogs"
	"github.com/jackc/pgx/v5"
)

var _ applogs.Index = (*Store)(nil)

const segCols = "id,application_id,service,pod_uid,pod_name,container,restart,revision,node,path,first_time,last_time,lines,bytes,sealed"

func scanSegment(r scanner) (applogs.Segment, error) {
	var s applogs.Segment
	i := &s.Instance
	err := r.Scan(&s.ID, &i.ApplicationID, &i.Service, &i.PodUID, &i.PodName, &i.Container, &i.Restart, &i.Revision, &i.Node, &s.Path, &s.FirstTime, &s.LastTime, &s.Lines, &s.Bytes, &s.Sealed)
	return s, err
}

func clampLimit(n, max int) int {
	if n < 1 {
		return 1
	}
	if n > max {
		return max
	}
	return n
}

func (s *Store) querySegments(ctx context.Context, sql string, args ...any) ([]applogs.Segment, error) {
	rows, err := s.Pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []applogs.Segment{}
	for rows.Next() {
		seg, err := scanSegment(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, seg)
	}
	return out, rows.Err()
}

func (s *Store) SaveSegment(ctx context.Context, g applogs.Segment) error {
	i := g.Instance
	_, err := s.Pool.Exec(ctx, `INSERT INTO app_log_segments(`+segCols+`) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)
		ON CONFLICT (id) DO UPDATE SET service=EXCLUDED.service,pod_name=EXCLUDED.pod_name,revision=EXCLUDED.revision,node=EXCLUDED.node,
		path=EXCLUDED.path,first_time=EXCLUDED.first_time,last_time=EXCLUDED.last_time,lines=EXCLUDED.lines,bytes=EXCLUDED.bytes,
		sealed=EXCLUDED.sealed,updated_at=now()`,
		g.ID, i.ApplicationID, i.Service, i.PodUID, i.PodName, i.Container, i.Restart, i.Revision, i.Node, g.Path, g.FirstTime, g.LastTime, g.Lines, g.Bytes, g.Sealed)
	return err
}

func (s *Store) Segments(ctx context.Context, q applogs.SegmentQuery) ([]applogs.Segment, error) {
	// Empty filter arrays mean "any"; a zero From or To leaves that side open.
	return s.querySegments(ctx, `SELECT `+segCols+` FROM app_log_segments WHERE application_id=$1
		AND ($2::timestamptz IS NULL OR last_time>=$2) AND ($3::timestamptz IS NULL OR first_time<=$3)
		AND (cardinality($4::text[])=0 OR pod_uid=ANY($4)) AND (cardinality($5::text[])=0 OR service=ANY($5))
		ORDER BY first_time,id LIMIT $6`,
		q.ApplicationID, nullTime(q.From), nullTime(q.To), nonNil(q.PodUIDs), nonNil(q.Services), clampLimit(q.Limit, 500))
}

func nullTime(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

func nonNil(v []string) []string {
	if v == nil {
		return []string{}
	}
	return v
}

func (s *Store) DeleteSegments(ctx context.Context, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	_, err := s.Pool.Exec(ctx, "DELETE FROM app_log_segments WHERE id=ANY($1)", ids)
	return err
}

func (s *Store) ExpiredSegments(ctx context.Context, before time.Time, limit int) ([]applogs.Segment, error) {
	return s.querySegments(ctx, "SELECT "+segCols+" FROM app_log_segments WHERE last_time<$1 ORDER BY last_time,id LIMIT $2", before, clampLimit(limit, 500))
}

func (s *Store) ApplicationsOverBytes(ctx context.Context, max int64, limit int) ([]string, error) {
	rows, err := s.Pool.Query(ctx, "SELECT application_id FROM app_log_segments GROUP BY application_id HAVING sum(bytes)>$1 ORDER BY application_id LIMIT $2", max, clampLimit(limit, 500))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

func (s *Store) OldestSegments(ctx context.Context, applicationID string, limit int) ([]applogs.Segment, error) {
	return s.querySegments(ctx, "SELECT "+segCols+" FROM app_log_segments WHERE application_id=$1 ORDER BY last_time,id LIMIT $2", applicationID, clampLimit(limit, 500))
}

// ClaimAppLogCollector takes or renews the singleton lease. The same owner
// may renew; another owner must wait for expiry.
func (s *Store) ClaimAppLogCollector(ctx context.Context, owner string, lease time.Duration) (bool, error) {
	tag, err := s.Pool.Exec(ctx, `UPDATE app_log_collector SET lease_owner=$1,lease_until=now()+$2::interval
		WHERE id=1 AND (lease_owner=$1 OR lease_until IS NULL OR lease_until<now())`, owner, fmt.Sprintf("%d milliseconds", lease.Milliseconds()))
	return tag.RowsAffected() == 1, err
}

func (s *Store) UpsertLogPod(ctx context.Context, applicationID string, p applogs.PodRecord) error {
	// An end reason only gets more specific: '' -> any, replaced/deleted -> specific.
	_, err := s.Pool.Exec(ctx, `INSERT INTO app_log_pods(pod_uid,application_id,service,pod_name,revision,node,source,started_at,ended_at,end_reason,restarts,last_seen)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,now())
		ON CONFLICT (pod_uid) DO UPDATE SET last_seen=now(),
		 revision=CASE WHEN EXCLUDED.revision>0 THEN EXCLUDED.revision ELSE app_log_pods.revision END,
		 node=CASE WHEN EXCLUDED.node<>'' THEN EXCLUDED.node ELSE app_log_pods.node END,
		 restarts=GREATEST(app_log_pods.restarts,EXCLUDED.restarts),
		 ended_at=COALESCE(app_log_pods.ended_at,EXCLUDED.ended_at),
		 end_reason=CASE WHEN app_log_pods.end_reason='' AND EXCLUDED.end_reason<>'' THEN EXCLUDED.end_reason
		  WHEN app_log_pods.end_reason IN ('replaced','deleted') AND EXCLUDED.end_reason IN ('oom_killed','evicted','error','completed') THEN EXCLUDED.end_reason
		  ELSE app_log_pods.end_reason END`,
		p.PodUID, applicationID, p.Service, p.PodName, p.Revision, p.Node, string(p.Source), p.StartedAt, p.EndedAt, string(p.EndReason), p.Restarts)
	return err
}

func (s *Store) EndOpenLogPods(ctx context.Context, applicationID string, seen []string, at time.Time, reason applogs.EndReason) error {
	_, err := s.Pool.Exec(ctx, "UPDATE app_log_pods SET ended_at=$3,end_reason=$4 WHERE application_id=$1 AND ended_at IS NULL AND pod_uid<>ALL($2::text[])", applicationID, nonNil(seen), at, string(reason))
	return err
}

func (s *Store) AddLogGap(ctx context.Context, podUID string, seconds int, note string) error {
	if seconds < 0 {
		seconds = 0
	}
	if seconds > 86400 {
		seconds = 86400
	}
	if r := []rune(note); len(r) > 200 {
		note = string(r[:200])
	}
	_, err := s.Pool.Exec(ctx, "UPDATE app_log_pods SET gap_seconds=LEAST(gap_seconds+$2,2000000000),gap_note=$3 WHERE pod_uid=$1", podUID, seconds, note)
	return err
}

func (s *Store) LogPods(ctx context.Context, applicationID string, from, to time.Time, limit int) ([]applogs.PodRecord, error) {
	rows, err := s.Pool.Query(ctx, `SELECT pod_uid,pod_name,service,revision,node,source,started_at,ended_at,end_reason,restarts,gap_seconds,gap_note
		FROM app_log_pods WHERE application_id=$1 AND ($3::timestamptz IS NULL OR started_at<=$3) AND ($2::timestamptz IS NULL OR ended_at IS NULL OR ended_at>=$2)
		ORDER BY started_at DESC,pod_uid LIMIT $4`, applicationID, nullTime(from), nullTime(to), clampLimit(limit, 500))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []applogs.PodRecord{}
	for rows.Next() {
		var p applogs.PodRecord
		var src, reason string
		if err := rows.Scan(&p.PodUID, &p.PodName, &p.Service, &p.Revision, &p.Node, &src, &p.StartedAt, &p.EndedAt, &reason, &p.Restarts, &p.GapSeconds, &p.GapNote); err != nil {
			return nil, err
		}
		p.Source, p.EndReason = applogs.Source(src), applogs.EndReason(reason)
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) LogCursor(ctx context.Context, key string) (json.RawMessage, bool, error) {
	var c []byte
	err := s.Pool.QueryRow(ctx, "SELECT cursor FROM app_log_cursors WHERE key=$1", key).Scan(&c)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, false, nil
	}
	return c, err == nil, err
}

func (s *Store) SaveLogCursor(ctx context.Context, key, podUID string, cursor json.RawMessage) error {
	_, err := s.Pool.Exec(ctx, `INSERT INTO app_log_cursors(key,pod_uid,cursor) VALUES($1,$2,$3)
		ON CONFLICT (key) DO UPDATE SET pod_uid=EXCLUDED.pod_uid,cursor=EXCLUDED.cursor,updated_at=now()`, key, podUID, []byte(cursor))
	return err
}

func (s *Store) DeleteLogCursorsBefore(ctx context.Context, before time.Time) (int64, error) {
	tag, err := s.Pool.Exec(ctx, "DELETE FROM app_log_cursors WHERE updated_at<$1", before)
	return tag.RowsAffected(), err
}

// PruneLogPods forgets ended pods that ended before the cutoff.
func (s *Store) PruneLogPods(ctx context.Context, before time.Time) (int64, error) {
	tag, err := s.Pool.Exec(ctx, "DELETE FROM app_log_pods WHERE ended_at IS NOT NULL AND ended_at<$1", before)
	return tag.RowsAffected(), err
}

type AppLogSettings struct {
	Enabled       bool `json:"enabled"`
	RetentionDays int  `json:"retention_days"`
	MaxMiBPerApp  int  `json:"max_mib_per_app"`
}

func (s *Store) AppLogSettings(ctx context.Context) (AppLogSettings, int64, error) {
	v := AppLogSettings{Enabled: true, RetentionDays: 7, MaxMiBPerApp: 512}
	var raw []byte
	var rev int64
	err := s.Pool.QueryRow(ctx, "SELECT value,revision FROM installation_settings WHERE name='app_logs'").Scan(&raw, &rev)
	if errors.Is(err, pgx.ErrNoRows) {
		return v, 0, nil
	}
	if err != nil {
		return v, 0, err
	}
	return v, rev, json.Unmarshal(raw, &v)
}

func (s *Store) PutAppLogSettings(ctx context.Context, v AppLogSettings, expectedRevision int64) (int64, error) {
	if v.RetentionDays < 1 || v.RetentionDays > 30 {
		return 0, fmt.Errorf("%w: keep logs for between 1 and 30 days", ErrInput)
	}
	if v.MaxMiBPerApp < 64 || v.MaxMiBPerApp > 4096 {
		return 0, fmt.Errorf("%w: allow between 64 and 4096 MiB of logs per application", ErrInput)
	}
	var rev int64
	var err error
	if expectedRevision == 0 {
		err = s.Pool.QueryRow(ctx, "INSERT INTO installation_settings(name,value) VALUES('app_logs',$1) ON CONFLICT(name) DO NOTHING RETURNING revision", JSON(v)).Scan(&rev)
	} else {
		err = s.Pool.QueryRow(ctx, "UPDATE installation_settings SET value=$1,revision=revision+1,updated_at=now() WHERE name='app_logs' AND revision=$2 RETURNING revision", JSON(v), expectedRevision).Scan(&rev)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, fmt.Errorf("%w: log settings changed; reload them and try again", ErrConflict)
	}
	return rev, err
}

// DeleteLogCursorsForEndedPods drops cursors of pods that ended before the
// cutoff, and orphan cursors (no pod row) not touched since. Cursors of live
// pods are kept however quiet they are.
func (s *Store) DeleteLogCursorsForEndedPods(ctx context.Context, before time.Time) (int64, error) {
	tag, err := s.Pool.Exec(ctx, `DELETE FROM app_log_cursors c WHERE
		EXISTS (SELECT 1 FROM app_log_pods p WHERE p.pod_uid=c.pod_uid AND p.ended_at<$1)
		OR (NOT EXISTS (SELECT 1 FROM app_log_pods p WHERE p.pod_uid=c.pod_uid) AND c.updated_at<$1)`, before)
	return tag.RowsAffected(), err
}

// ReleaseAppLogCollector gives up the lease early; a non-owner call does nothing.
func (s *Store) ReleaseAppLogCollector(ctx context.Context, owner string) error {
	_, err := s.Pool.Exec(ctx, "UPDATE app_log_collector SET lease_until=NULL WHERE id=1 AND lease_owner=$1", owner)
	return err
}

// OpenLogPodCursorTimes maps each open pod outside notIn to its newest cursor
// write, or its last_seen when it has no cursor. At most 500 pods.
func (s *Store) OpenLogPodCursorTimes(ctx context.Context, applicationID string, notIn []string) (map[string]time.Time, error) {
	rows, err := s.Pool.Query(ctx, `SELECT p.pod_uid,COALESCE((SELECT max(c.updated_at) FROM app_log_cursors c WHERE c.pod_uid=p.pod_uid),p.last_seen)
		FROM app_log_pods p WHERE p.application_id=$1 AND p.ended_at IS NULL AND p.pod_uid<>ALL($2::text[]) ORDER BY p.pod_uid LIMIT 500`, applicationID, nonNil(notIn))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]time.Time{}
	for rows.Next() {
		var uid string
		var at time.Time
		if err := rows.Scan(&uid, &at); err != nil {
			return nil, err
		}
		out[uid] = at
	}
	return out, rows.Err()
}
