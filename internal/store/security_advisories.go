package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/hakopod/hakopod/internal/advisory"
	"github.com/jackc/pgx/v5"
)

type FeedState struct {
	Body        []byte     `json:"body"`
	ETag        string     `json:"etag"`
	Revision    int64      `json:"revision"`
	IssuedAt    *time.Time `json:"issued_at"`
	ExpiresAt   *time.Time `json:"expires_at"`
	CheckedAt   *time.Time `json:"checked_at"`
	NextCheckAt time.Time  `json:"next_check_at"`
	Failures    int        `json:"failures"`
	Error       string     `json:"error"`
}

func (s *Store) FeedState(ctx context.Context) (FeedState, error) {
	var f FeedState
	err := s.Pool.QueryRow(ctx, `SELECT body,etag,revision,issued_at,expires_at,checked_at,next_check_at,failures,error
		FROM advisory_feed_state WHERE source='hakopod'`).
		Scan(&f.Body, &f.ETag, &f.Revision, &f.IssuedAt, &f.ExpiresAt, &f.CheckedAt, &f.NextCheckAt, &f.Failures, &f.Error)
	return f, err
}

// ClaimFeed takes the fetch lease only when a check is due and no live lease exists.
func (s *Store) ClaimFeed(ctx context.Context, owner string, lease time.Duration) (bool, error) {
	tag, err := s.Pool.Exec(ctx, `UPDATE advisory_feed_state SET lease_owner=$1,lease_until=now()+$2*interval '1 millisecond'
		WHERE source='hakopod' AND next_check_at<=now() AND (lease_until IS NULL OR lease_until<now())`, owner, lease.Milliseconds())
	return err == nil && tag.RowsAffected() == 1, err
}

// SaveFeed records a fetch outcome and releases the lease; only the lease owner may save.
func (s *Store) SaveFeed(ctx context.Context, owner string, st FeedState) error {
	tag, err := s.Pool.Exec(ctx, `UPDATE advisory_feed_state SET body=$2,etag=$3,revision=$4,issued_at=$5,expires_at=$6,checked_at=$7,
		next_check_at=$8,failures=$9,error=$10,lease_owner='',lease_until=NULL
		WHERE source='hakopod' AND lease_owner=$1 AND $1<>''`,
		owner, st.Body, st.ETag, st.Revision, st.IssuedAt, st.ExpiresAt, st.CheckedAt, st.NextCheckAt, st.Failures, st.Error)
	if err == nil && tag.RowsAffected() != 1 {
		return ErrConflict
	}
	return err
}

func (s *Store) OSVCached(ctx context.Context, runtime, version string, maxAge time.Duration) (json.RawMessage, bool, error) {
	var body json.RawMessage
	err := s.Pool.QueryRow(ctx, `SELECT body FROM osv_cache WHERE runtime=$1 AND version=$2 AND checked_at>=now()-$3*interval '1 millisecond'`,
		runtime, version, maxAge.Milliseconds()).Scan(&body)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, false, nil
	}
	return body, err == nil, err
}

func (s *Store) SaveOSV(ctx context.Context, runtime, version string, body json.RawMessage) error {
	_, err := s.Pool.Exec(ctx, `INSERT INTO osv_cache(runtime,version,body,checked_at) VALUES($1,$2,$3,now())
		ON CONFLICT(runtime,version) DO UPDATE SET body=excluded.body,checked_at=excluded.checked_at`, runtime, version, body)
	return err
}

func (s *Store) ClaimAdvisoryScan(ctx context.Context, owner string, lease time.Duration) (bool, error) {
	tag, err := s.Pool.Exec(ctx, `UPDATE advisory_scanner SET lease_owner=$1,lease_until=now()+$2*interval '1 millisecond'
		WHERE id=1 AND (lease_until IS NULL OR lease_until<now())`, owner, lease.Milliseconds())
	return err == nil && tag.RowsAffected() == 1, err
}

type SecurityCandidate struct {
	ApplicationID string `json:"application_id"`
	Project       string `json:"project"`
	Environment   string `json:"environment"`
	Name          string `json:"name"`
	Revision      int64  `json:"revision"`
	DeploymentID  string `json:"deployment_id"`
}

// latestSucceeded picks the newest succeeded deployment with a resolved
// specification; a later failed rollout does not hide what is running.
const latestSucceeded = `SELECT id,revision FROM deployments WHERE application_id=a.id AND status='succeeded' AND resolved_spec IS NOT NULL ORDER BY revision DESC LIMIT 1`

// SecurityCandidates lists running applications whose evaluation is missing or
// stale. Revision is the application revision; DeploymentID is the newest
// succeeded deployment. A NULL osv_checked_at makes an app due only when OSV
// is enabled. Apps backing off after a failed evaluation wait for
// next_evaluation_at unless a newer deployment succeeded since.
func (s *Store) SecurityCandidates(ctx context.Context, feedRevision int64, osvMaxAge time.Duration, osvEnabled bool, limit int) ([]SecurityCandidate, error) {
	limit = min(max(limit, 1), 100)
	rows, err := s.Pool.Query(ctx, `SELECT a.id,a.project,a.environment,a.name,a.revision,d.id
		FROM applications a
		JOIN LATERAL (`+latestSucceeded+`) d ON true
		LEFT JOIN application_security s ON s.application_id=a.id
		WHERE (s.next_evaluation_at IS NULL OR s.next_evaluation_at<=now() OR s.deployment_id<>d.id) AND (s.application_id IS NULL OR s.evaluated_at IS NULL
			OR s.deployment_id<>d.id OR s.feed_revision<$1
			OR ($4 AND s.osv_checked_at IS NULL)
			OR ($2>0 AND s.osv_checked_at<now()-$2*interval '1 millisecond'))
		ORDER BY s.evaluated_at NULLS FIRST, a.id LIMIT $3`, feedRevision, osvMaxAge.Milliseconds(), limit, osvEnabled)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SecurityCandidate{}
	for rows.Next() {
		var c SecurityCandidate
		if err := rows.Scan(&c.ApplicationID, &c.Project, &c.Environment, &c.Name, &c.Revision, &c.DeploymentID); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// LatestSucceededDeployment is the newest succeeded deployment of an app
// that has a resolved specification.
func (s *Store) LatestSucceededDeployment(ctx context.Context, applicationID string) (Deployment, error) {
	return scanDep(s.Pool.QueryRow(ctx, "SELECT "+depCols+" FROM deployments WHERE application_id=$1 AND status='succeeded' AND resolved_spec IS NOT NULL ORDER BY revision DESC LIMIT 1", applicationID))
}

// SecurityEvaluationFailed stamps a failed evaluation so the app rotates to
// the back of the queue with backoff (1 minute doubling to 6 hours). note
// replaces the inventory's incomplete list.
func (s *Store) SecurityEvaluationFailed(ctx context.Context, applicationID, note string) error {
	_, err := s.Pool.Exec(ctx, `INSERT INTO application_security(application_id,evaluated_at,next_evaluation_at,evaluation_failures,inventory)
		VALUES($1,now(),now()+interval '1 minute',1,jsonb_build_object('incomplete',jsonb_build_array($2::text)))
		ON CONFLICT(application_id) DO UPDATE SET evaluated_at=now(),evaluation_failures=application_security.evaluation_failures+1,
		next_evaluation_at=now()+LEAST(interval '6 hours',interval '1 minute'*power(2,LEAST(application_security.evaluation_failures+1,9))),
		inventory=jsonb_set(COALESCE(application_security.inventory,'{}'::jsonb),'{incomplete}',jsonb_build_array($2::text)),updated_at=now()`, applicationID, note)
	return err
}

type ApplicationSecurity struct {
	ApplicationID   string              `json:"application_id"`
	ApplicationName string              `json:"application_name"`
	Project         string              `json:"project"`
	Environment     string              `json:"environment"`
	DeploymentID    string              `json:"deployment_id"`
	Revision        int64               `json:"revision"`
	Inventory       *advisory.Inventory `json:"inventory"`
	Findings        []advisory.Finding  `json:"findings"`
	FeedRevision    int64               `json:"feed_revision"`
	OSVCheckedAt    *time.Time          `json:"osv_checked_at"`
	CheckedAt       *time.Time          `json:"checked_at"`
	EvaluatedAt     *time.Time          `json:"evaluated_at"`
	AutoPatch       bool                `json:"auto_patch"`
	ApprovedBy      string              `json:"approved_by"`
	PolicyRevision  int64               `json:"policy_revision"`
	// GrantID is the non-login deployment grant automatic patches run under.
	GrantID string `json:"-"`
	// ApprovalExpiresAt is when the grant expires; nil while automatic patching is off.
	ApprovalExpiresAt *time.Time `json:"approval_expires_at"`
}

// Missing application_security rows read as defaults (policy revision 1).
const appSecuritySelect = `SELECT a.id,a.name,a.project,a.environment,COALESCE(s.deployment_id,''),COALESCE(s.revision,0),s.inventory,
	COALESCE(s.findings,'[]'),COALESCE(s.feed_revision,0),s.osv_checked_at,s.checked_at,s.evaluated_at,
	COALESCE(s.auto_patch,false),COALESCE(s.approved_by,''),COALESCE(s.policy_revision,1),COALESCE(s.grant_id,''),
	CASE WHEN s.auto_patch THEN (SELECT k.expires_at FROM api_keys k WHERE k.id=s.grant_id) END
	FROM applications a LEFT JOIN application_security s ON s.application_id=a.id `

func scanAppSecurity(row scanner) (ApplicationSecurity, error) {
	var a ApplicationSecurity
	var inventory, findings []byte
	err := row.Scan(&a.ApplicationID, &a.ApplicationName, &a.Project, &a.Environment, &a.DeploymentID, &a.Revision, &inventory,
		&findings, &a.FeedRevision, &a.OSVCheckedAt, &a.CheckedAt, &a.EvaluatedAt, &a.AutoPatch, &a.ApprovedBy, &a.PolicyRevision, &a.GrantID, &a.ApprovalExpiresAt)
	if err != nil {
		return a, err
	}
	if len(inventory) > 0 && string(inventory) != "null" {
		a.Inventory = &advisory.Inventory{}
		if err = json.Unmarshal(inventory, a.Inventory); err != nil {
			return a, err
		}
	}
	if err = json.Unmarshal(findings, &a.Findings); err != nil {
		return a, err
	}
	if a.Findings == nil {
		a.Findings = []advisory.Finding{}
	}
	return a, nil
}

func (s *Store) ApplicationSecurity(ctx context.Context, applicationID string) (ApplicationSecurity, error) {
	return scanAppSecurity(s.Pool.QueryRow(ctx, appSecuritySelect+"WHERE a.id=$1", applicationID))
}

// SaveApplicationSecurity stores evaluation results; policy columns are never touched.
func (s *Store) SaveApplicationSecurity(ctx context.Context, a ApplicationSecurity) error {
	var inventory []byte
	if a.Inventory != nil {
		var err error
		if inventory, err = json.Marshal(a.Inventory); err != nil {
			return err
		}
	}
	findings := a.Findings
	if findings == nil {
		findings = []advisory.Finding{}
	}
	findingsJSON, err := json.Marshal(findings)
	if err != nil {
		return err
	}
	_, err = s.Pool.Exec(ctx, `INSERT INTO application_security(application_id,deployment_id,revision,inventory,findings,feed_revision,osv_checked_at,checked_at,evaluated_at)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9) ON CONFLICT(application_id) DO UPDATE SET deployment_id=excluded.deployment_id,revision=excluded.revision,
		inventory=excluded.inventory,findings=excluded.findings,feed_revision=excluded.feed_revision,osv_checked_at=excluded.osv_checked_at,
		checked_at=excluded.checked_at,evaluated_at=excluded.evaluated_at,next_evaluation_at=NULL,evaluation_failures=0,updated_at=now()`,
		a.ApplicationID, a.DeploymentID, a.Revision, inventory, findingsJSON, a.FeedRevision, a.OSVCheckedAt, a.CheckedAt, a.EvaluatedAt)
	return err
}

// SecurityOverview returns at most 500 rows with findings, automatic patching
// or an incomplete check,
// filtered to what the principal may read.
// ponytail: filter runs after the 500 cap; scope the SQL if installations exceed it.
func (s *Store) SecurityOverview(ctx context.Context, p Principal) ([]ApplicationSecurity, error) {
	rows, err := s.Pool.Query(ctx, appSecuritySelect+`WHERE s.findings<>'[]'::jsonb OR s.auto_patch
		OR jsonb_array_length(COALESCE(s.inventory->'incomplete','[]'))>0
		ORDER BY a.project,a.environment,a.name LIMIT 500`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ApplicationSecurity{}
	for rows.Next() {
		a, err := scanAppSecurity(rows)
		if err != nil {
			return nil, err
		}
		if p.Allows("deployments:read", a.Project, a.Environment, a.ApplicationName) {
			out = append(out, a)
		}
	}
	return out, rows.Err()
}

// SetSecurityPolicy changes automatic patching under optimistic concurrency.
// A missing row has policy revision 1. Turning it on mints a non-login
// deployment grant for p (the pattern sources and builds use); turning it off
// revokes that grant. Automatic patches run under the grant, so the worker
// reauthorizes them against the approver's current roles.
func (s *Store) SetSecurityPolicy(ctx context.Context, p Principal, applicationID string, autoPatch bool, expectedPolicyRevision int64) (ApplicationSecurity, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return ApplicationSecurity{}, err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, "INSERT INTO application_security(application_id) VALUES($1) ON CONFLICT DO NOTHING", applicationID); err != nil {
		return ApplicationSecurity{}, err
	}
	if _, err = tx.Exec(ctx, "SELECT 1 FROM application_security WHERE application_id=$1 FOR UPDATE", applicationID); err != nil {
		return ApplicationSecurity{}, err
	}
	current, err := scanAppSecurity(tx.QueryRow(ctx, appSecuritySelect+"WHERE a.id=$1", applicationID))
	if err != nil {
		return current, err
	}
	if current.PolicyRevision != expectedPolicyRevision {
		return ApplicationSecurity{}, ErrConflict
	}
	grant, approver := current.GrantID, current.ApprovedBy
	switch {
	case autoPatch && !current.AutoPatch:
		if grant, err = s.NewSourceGrant(ctx, tx, p, current.Project, current.Environment, current.ApplicationName); err != nil {
			return ApplicationSecurity{}, err
		}
		approver = p.ID
	case !autoPatch:
		if grant != "" {
			if _, err = tx.Exec(ctx, "UPDATE api_keys SET revoked_at=COALESCE(revoked_at,now()) WHERE id=$1", grant); err != nil {
				return ApplicationSecurity{}, err
			}
		}
		grant, approver = "", ""
	}
	if _, err = tx.Exec(ctx, `UPDATE application_security SET auto_patch=$2,approved_by=$3,grant_id=$4,
		policy_revision=policy_revision+1,updated_at=now() WHERE application_id=$1`, applicationID, autoPatch, approver, grant); err != nil {
		return ApplicationSecurity{}, err
	}
	a, err := scanAppSecurity(tx.QueryRow(ctx, appSecuritySelect+"WHERE a.id=$1", applicationID))
	if err != nil {
		return a, err
	}
	return a, tx.Commit(ctx)
}

type PatchAttempt struct {
	ID               string    `json:"id"`
	ApplicationID    string    `json:"application_id"`
	AdvisoryID       string    `json:"advisory_id"`
	AdvisoryRevision int64     `json:"advisory_revision"`
	Service          string    `json:"service"`
	FromDeploymentID string    `json:"from_deployment_id"`
	DeploymentID     string    `json:"deployment_id"`
	Status           string    `json:"status"`
	Error            string    `json:"error"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
}

const patchAttemptCols = "id,application_id,advisory_id,advisory_revision,service,from_deployment_id,deployment_id,status,error,created_at,updated_at"

// ClaimPatchAttempt inserts the attempt; false means one with this id already exists.
func (s *Store) ClaimPatchAttempt(ctx context.Context, a PatchAttempt) (bool, error) {
	if a.Status == "" {
		a.Status = "pending"
	}
	tag, err := s.Pool.Exec(ctx, `INSERT INTO security_patch_attempts(id,application_id,advisory_id,advisory_revision,service,from_deployment_id,deployment_id,status,error)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9) ON CONFLICT DO NOTHING`,
		a.ID, a.ApplicationID, a.AdvisoryID, a.AdvisoryRevision, a.Service, a.FromDeploymentID, a.DeploymentID, a.Status, a.Error)
	return err == nil && tag.RowsAffected() == 1, err
}

func (s *Store) UpdatePatchAttempt(ctx context.Context, id, status, deploymentID, errText string) error {
	tag, err := s.Pool.Exec(ctx, `UPDATE security_patch_attempts SET status=$2,deployment_id=CASE WHEN $3='' THEN deployment_id ELSE $3 END,
		error=$4,updated_at=now() WHERE id=$1`, id, status, deploymentID, errText)
	if err == nil && tag.RowsAffected() != 1 {
		return pgx.ErrNoRows
	}
	return err
}

func (s *Store) PatchAttempts(ctx context.Context, applicationID string, limit int) ([]PatchAttempt, error) {
	return s.queryPatchAttempts(ctx, "WHERE application_id=$1 ORDER BY created_at DESC,id LIMIT $2", applicationID, min(max(limit, 1), 100))
}

func (s *Store) OpenPatchAttempts(ctx context.Context, limit int) ([]PatchAttempt, error) {
	return s.queryPatchAttempts(ctx, "WHERE status IN ('pending','accepted') ORDER BY created_at,id LIMIT $1", min(max(limit, 1), 100))
}

func (s *Store) queryPatchAttempts(ctx context.Context, where string, args ...any) ([]PatchAttempt, error) {
	rows, err := s.Pool.Query(ctx, "SELECT "+patchAttemptCols+" FROM security_patch_attempts "+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []PatchAttempt{}
	for rows.Next() {
		var a PatchAttempt
		if err := rows.Scan(&a.ID, &a.ApplicationID, &a.AdvisoryID, &a.AdvisoryRevision, &a.Service, &a.FromDeploymentID,
			&a.DeploymentID, &a.Status, &a.Error, &a.CreatedAt, &a.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

type SecuritySettings struct {
	FeedEnabled bool `json:"feed_enabled"`
	OSVEnabled  bool `json:"osv_enabled"`
}

// SecuritySettings returns defaults with revision 0 when no row exists.
func (s *Store) SecuritySettings(ctx context.Context) (SecuritySettings, int64, error) {
	v := SecuritySettings{FeedEnabled: true, OSVEnabled: true}
	var rev int64
	err := s.Pool.QueryRow(ctx, "SELECT value,revision FROM installation_settings WHERE name='security_advisories'").Scan(&v, &rev)
	if errors.Is(err, pgx.ErrNoRows) {
		return v, 0, nil
	}
	return v, rev, err
}

func (s *Store) PutSecuritySettings(ctx context.Context, v SecuritySettings, expectedRevision int64) (int64, error) {
	var rev int64
	var err error
	if expectedRevision == 0 {
		err = s.Pool.QueryRow(ctx, `INSERT INTO installation_settings(name,value,revision) VALUES('security_advisories',$1,1)
			ON CONFLICT DO NOTHING RETURNING revision`, v).Scan(&rev)
	} else {
		err = s.Pool.QueryRow(ctx, `UPDATE installation_settings SET value=$1,revision=revision+1,updated_at=now()
			WHERE name='security_advisories' AND revision=$2 RETURNING revision`, v, expectedRevision).Scan(&rev)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrConflict
	}
	return rev, err
}
