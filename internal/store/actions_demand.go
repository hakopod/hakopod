package store

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/hakopod/hakopod/internal/spec"
)

// ActionsJobEvent is one verified GitHub workflow_job delivery. Project and
// Environment carry the delivering connection's scope; empty means the
// installation-wide connection.
type ActionsJobEvent struct {
	Action      string
	JobID       int64
	Repository  string
	Labels      []string
	Project     string
	Environment string
}

// RecordActionsJobEvent turns a queued job into demand for each matching
// scale-to-zero pool and releases it once the job leaves the queue. A scoped
// connection can only wake pools in its own project and environment.
func (s *Store) RecordActionsJobEvent(ctx context.Context, e ActionsJobEvent) error {
	owner, _, _ := strings.Cut(e.Repository, "/")
	rows, err := s.Pool.Query(ctx, `SELECT application_id,service,config FROM actions_pools
 WHERE NOT removed AND COALESCE(config->'actions'->>'provider','') IN ('','github')
 AND COALESCE((config->'actions'->>'idle_seconds')::bigint,0)>0
 AND (lower(config->'actions'->>'repository')=lower($1) OR lower(config->'actions'->>'organization')=lower($2))
 AND ($3='' OR (project=$3 AND environment=$4))
 ORDER BY application_id,service LIMIT 50`, e.Repository, owner, e.Project, e.Environment)
	if err != nil {
		return err
	}
	type pool struct{ application, service string }
	matched := []pool{}
	for rows.Next() {
		var p pool
		var data []byte
		var config spec.Service
		if err = rows.Scan(&p.application, &p.service, &data); err != nil {
			rows.Close()
			return err
		}
		if json.Unmarshal(data, &config) == nil && ActionsJobMatches(config, e.Labels) {
			matched = append(matched, p)
		}
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return err
	}
	for _, p := range matched {
		if e.Action == "queued" {
			// Bounded per pool; GitHub's own 24-hour limit expires the rest.
			_, err = s.Pool.Exec(ctx, `INSERT INTO actions_demand(application_id,service,job_id) SELECT $1,$2,$3
 WHERE (SELECT count(*) FROM actions_demand WHERE application_id=$1 AND service=$2)<256 ON CONFLICT DO NOTHING`, p.application, p.service, e.JobID)
		} else {
			_, err = s.Pool.Exec(ctx, `DELETE FROM actions_demand WHERE application_id=$1 AND service=$2 AND job_id=$3`, p.application, p.service, e.JobID)
		}
		if err != nil {
			return err
		}
		if _, err = s.Pool.Exec(ctx, `UPDATE actions_pools SET active_at=now(),next_reconcile_at=LEAST(next_reconcile_at,now()) WHERE application_id=$1 AND service=$2`, p.application, p.service); err != nil {
			return err
		}
	}
	return nil
}

// ActionsDemand reports queued jobs for a pool and whether it is still inside
// its idle window. Expired demand is pruned on read.
func (s *Store) ActionsDemand(ctx context.Context, application, service string, idleSeconds int64) (queued int, warm bool, err error) {
	err = s.Pool.QueryRow(ctx, `WITH expired AS (
 DELETE FROM actions_demand WHERE application_id=$1 AND service=$2 AND queued_at<now()-interval '24 hours'
) SELECT (SELECT count(*) FROM actions_demand WHERE application_id=$1 AND service=$2 AND queued_at>=now()-interval '24 hours'),
 active_at>now()-make_interval(secs=>$3::bigint) FROM actions_pools WHERE application_id=$1 AND service=$2`, application, service, idleSeconds).Scan(&queued, &warm)
	return queued, warm, err
}

// ActionsJobMatches follows GitHub's rule: every runs-on label must be on the
// runner. Runners also carry GitHub's default self-hosted, OS and arch labels.
func ActionsJobMatches(config spec.Service, labels []string) bool {
	if config.Actions == nil || len(labels) == 0 {
		return false
	}
	have := map[string]bool{"self-hosted": true, "linux": true}
	switch config.Architecture {
	case "amd64":
		have["x64"] = true
	case "arm64":
		have["arm64"] = true
	default:
		have["x64"], have["arm64"] = true, true
	}
	for _, label := range config.Actions.Labels {
		have[strings.ToLower(label)] = true
	}
	for _, label := range labels {
		if !have[strings.ToLower(label)] {
			return false
		}
	}
	return true
}
