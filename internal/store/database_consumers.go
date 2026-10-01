package store

import (
	"context"
	"time"
)

const MaxDatabaseConnections = 250

// DatabaseConnectionReference describes configuration evidence, not a live
// socket. A failed rollout can leave the last successful connection in use.
type DatabaseConnectionReference struct {
	ApplicationID          string `json:"application_id"`
	ApplicationName        string `json:"application_name"`
	ApplicationDisplayName string `json:"application_display_name"`
	Project                string `json:"project"`
	Environment            string `json:"environment"`
	Service                string `json:"service"`
	Variable               string `json:"variable"`
	Endpoint               string `json:"endpoint"`
	SavedRevision          int64  `json:"saved_revision"`
	LastSuccessfulRevision int64  `json:"last_successful_revision"`
	LatestAttemptRevision  int64  `json:"latest_attempt_revision"`
	LatestAttemptStatus    string `json:"latest_attempt_status"`
}

type DatabaseConnections struct {
	Items     []DatabaseConnectionReference `json:"items"`
	Truncated bool                          `json:"truncated"`
	Limit     int                           `json:"limit"`
}

// DatabaseConnections projects only binding metadata. Configuration bodies,
// environment values and credentials never leave the store through this API.
func (s *Store) DatabaseConnections(ctx context.Context, p Principal, id string) (DatabaseConnections, error) {
	out := DatabaseConnections{Items: []DatabaseConnectionReference{}, Limit: MaxDatabaseConnections}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	d, err := s.Database(ctx, p, id, false)
	if err != nil {
		return out, err
	}
	rows, err := s.Pool.Query(ctx, `
SELECT a.id,a.name,a.display_name,svc.key,b.key,b.value->>'endpoint',
 COALESCE(max(v.revision) FILTER (WHERE v.kind='saved'),0),
 COALESCE(max(v.revision) FILTER (WHERE v.kind='last_successful'),0),
 COALESCE(max(v.revision) FILTER (WHERE v.kind='latest_attempt'),0),
 COALESCE(max(v.status) FILTER (WHERE v.kind='latest_attempt'),'')
FROM applications a
CROSS JOIN LATERAL (
 SELECT 'saved' AS kind,a.revision,'' AS status,a.spec AS definition
 UNION ALL
 SELECT history.kind,history.revision,history.status,body.definition
 FROM (
  (SELECT 'last_successful' AS kind,revision,status,spec,resolved_spec,recovery_spec
   FROM deployments WHERE application_id=a.id AND status='succeeded' ORDER BY revision DESC LIMIT 1)
  UNION ALL
  (SELECT 'latest_attempt',revision,status,spec,resolved_spec,recovery_spec
   FROM deployments WHERE application_id=a.id ORDER BY revision DESC LIMIT 1)
 ) history
 CROSS JOIN LATERAL (VALUES(history.spec),(history.resolved_spec),(history.recovery_spec)) body(definition)
 WHERE history.kind='last_successful' OR history.status<>'succeeded'
) v
CROSS JOIN LATERAL jsonb_each(v.definition->'services') svc
CROSS JOIN LATERAL jsonb_each(svc.value->'bindings') b
WHERE a.project=$1 AND a.environment=$2 AND b.value->>'managed_database'=$3
GROUP BY a.id,a.name,a.display_name,svc.key,b.key,b.value->>'endpoint'
ORDER BY a.name,a.id,svc.key,b.key,b.value->>'endpoint'
LIMIT $4`, d.Project, d.Environment, id, MaxDatabaseConnections+1)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var item DatabaseConnectionReference
		if err := rows.Scan(&item.ApplicationID, &item.ApplicationName, &item.ApplicationDisplayName, &item.Service, &item.Variable, &item.Endpoint, &item.SavedRevision, &item.LastSuccessfulRevision, &item.LatestAttemptRevision, &item.LatestAttemptStatus); err != nil {
			return out, err
		}
		if len(out.Items) == MaxDatabaseConnections {
			out.Truncated = true
			break
		}
		item.Project, item.Environment = d.Project, d.Environment
		out.Items = append(out.Items, item)
	}
	return out, rows.Err()
}
