package store

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/hakopod/hakopod/internal/agentaccess"
)

const AgentPoliciesFeature = "agent_policies"

// maxAgentPolicies bounds every policy row, drafts included.
const maxAgentPolicies = 200

type AgentPolicy struct {
	ID             string                `json:"id"`
	Name           string                `json:"name"`
	Status         string                `json:"status"`
	CurrentVersion int                   `json:"current_version"`
	Revision       int64                 `json:"revision"`
	Document       *agentaccess.Document `json:"document"`
	Draft          *agentaccess.Document `json:"draft"`
	CreatedAt      time.Time             `json:"created_at"`
	UpdatedAt      time.Time             `json:"updated_at"`
	UpdatedBy      string                `json:"updated_by"`
	UpdatedByName  string                `json:"updated_by_name"`
}

type AgentPolicySummary struct {
	Total            int64 `json:"total"`
	Enforced         int64 `json:"enforced"`
	Monitor          int64 `json:"monitor"`
	Draft            int64 `json:"draft"`
	PendingApprovals int64 `json:"pending_approvals"`
}

type AgentPolicyVersion struct {
	Version         int                  `json:"version"`
	Name            string               `json:"name"`
	Status          string               `json:"status"`
	Document        agentaccess.Document `json:"document"`
	PublishedAt     time.Time            `json:"published_at"`
	PublishedBy     string               `json:"published_by"`
	PublishedByName string               `json:"published_by_name"`
}

type AgentAccessEvaluation struct {
	ObservedAt     time.Time `json:"observed_at"`
	KeyID          string    `json:"key_id"`
	KeyName        string    `json:"key_name"`
	IdentityID     string    `json:"identity_id"`
	TeamIDs        []string  `json:"team_ids"`
	Operation      string    `json:"operation"`
	Category       string    `json:"category"`
	Project        string    `json:"project"`
	Environment    string    `json:"environment"`
	ActiveIncident bool      `json:"active_incident"`
	Decision       string    `json:"decision"`
	PolicyID       string    `json:"policy_id"`
	PolicyVersion  int       `json:"policy_version"`
}

type AgentApprovalRequest struct {
	PolicyID      string
	PolicyVersion int
	KeyID         string
	IdentityID    string
	Operation     string
	Category      string
	Method        string
	Path          string
	Project       string
	Environment   string
	Digest        [32]byte
	Approvers     []agentaccess.Approver
	TTL           time.Duration
}

type AgentApproval struct {
	ID            string                 `json:"id"`
	PolicyID      string                 `json:"policy_id"`
	PolicyName    string                 `json:"policy_name"`
	PolicyVersion int                    `json:"policy_version"`
	KeyID         string                 `json:"key_id"`
	KeyName       string                 `json:"key_name"`
	IdentityID    string                 `json:"identity_id"`
	IdentityName  string                 `json:"identity_name"`
	Operation     string                 `json:"operation"`
	Category      string                 `json:"category"`
	Method        string                 `json:"method"`
	Path          string                 `json:"path"`
	Project       string                 `json:"project"`
	Environment   string                 `json:"environment"`
	Digest        string                 `json:"digest"`
	Approvers     []agentaccess.Approver `json:"approvers"`
	State         string                 `json:"state"`
	DecidedBy     *string                `json:"decided_by"`
	DecidedByName *string                `json:"decided_by_name"`
	DecidedAt     *time.Time             `json:"decided_at"`
	Reason        string                 `json:"reason"`
	ExpiresAt     time.Time              `json:"expires_at"`
	ConsumedAt    *time.Time             `json:"consumed_at"`
	CreatedAt     time.Time              `json:"created_at"`
	CanDecide     bool                   `json:"can_decide"`
}

// Policy management is limited to installation admins signed in with a browser.
func agentPolicyAdmin(p Principal) bool {
	return p.IsAdmin() && p.IsHuman() && p.CredentialType == "browser"
}

func validateAgentPolicy(name string, doc agentaccess.Document) error {
	if n := utf8.RuneCountInString(strings.TrimSpace(name)); n < 1 || n > 100 {
		return fmt.Errorf("%w: name must be between 1 and 100 characters", ErrInput)
	}
	if err := doc.Validate(); err != nil {
		return fmt.Errorf("%w: %v", ErrInput, err)
	}
	return nil
}

const agentPolicySelect = `SELECT p.id,p.name,p.status,p.current_version,p.revision,v.document,p.draft,p.created_at,p.updated_at,p.updated_by,i.name
 FROM agent_access_policies p JOIN identities i ON i.id=p.updated_by
 LEFT JOIN agent_access_policy_versions v ON v.policy_id=p.id AND v.version=p.current_version`

func decodeAgentDocument(raw []byte) (*agentaccess.Document, error) {
	if raw == nil {
		return nil, nil
	}
	var d agentaccess.Document
	if err := json.Unmarshal(raw, &d); err != nil {
		return nil, err
	}
	return &d, nil
}

func scanAgentPolicy(row pgx.Row) (AgentPolicy, error) {
	var a AgentPolicy
	var doc, draft []byte
	if err := row.Scan(&a.ID, &a.Name, &a.Status, &a.CurrentVersion, &a.Revision, &doc, &draft, &a.CreatedAt, &a.UpdatedAt, &a.UpdatedBy, &a.UpdatedByName); err != nil {
		return a, err
	}
	var err error
	if a.Document, err = decodeAgentDocument(doc); err != nil {
		return a, err
	}
	a.Draft, err = decodeAgentDocument(draft)
	return a, err
}

func (s *Store) ListAgentPolicies(ctx context.Context, p Principal) ([]AgentPolicy, AgentPolicySummary, error) {
	var sum AgentPolicySummary
	if !agentPolicyAdmin(p) {
		return nil, sum, ErrForbidden
	}
	rows, err := s.Pool.Query(ctx, agentPolicySelect+` ORDER BY CASE p.status WHEN 'enforced' THEN 0 WHEN 'monitor' THEN 1 ELSE 2 END,lower(p.name),p.id LIMIT 200`)
	if err != nil {
		return nil, sum, err
	}
	defer rows.Close()
	out := []AgentPolicy{}
	for rows.Next() {
		a, err := scanAgentPolicy(rows)
		if err != nil {
			return nil, sum, err
		}
		out = append(out, a)
	}
	if err = rows.Err(); err != nil {
		return nil, sum, err
	}
	err = s.Pool.QueryRow(ctx, `SELECT count(*),count(*) FILTER (WHERE status='enforced'),count(*) FILTER (WHERE status='monitor'),count(*) FILTER (WHERE status='draft'),
 (SELECT count(*) FROM (SELECT 1 FROM agent_access_approvals WHERE state='pending' AND expires_at>now() LIMIT 10000) q)
 FROM (SELECT status FROM agent_access_policies LIMIT 1000) p`).Scan(&sum.Total, &sum.Enforced, &sum.Monitor, &sum.Draft, &sum.PendingApprovals)
	return out, sum, err
}

func (s *Store) AgentPolicy(ctx context.Context, p Principal, id string) (AgentPolicy, error) {
	if !agentPolicyAdmin(p) {
		return AgentPolicy{}, ErrForbidden
	}
	return scanAgentPolicy(s.Pool.QueryRow(ctx, agentPolicySelect+` WHERE p.id=$1 LIMIT 1`, id))
}

// beginAgentPolicyMutation opens a transaction holding the license and a lock
// that serialises the policy count checks.
func (s *Store) beginAgentPolicyMutation(ctx context.Context, p Principal, licensed bool) (pgx.Tx, error) {
	if !agentPolicyAdmin(p) {
		return nil, ErrForbidden
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	if licensed {
		if err = s.RequireFeaturesTx(ctx, tx, AgentPoliciesFeature); err != nil {
			tx.Rollback(ctx)
			return nil, err
		}
	}
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended('hakopod.agent_policies',0))"); err != nil {
		tx.Rollback(ctx)
		return nil, err
	}
	return tx, nil
}

func lockAgentPolicy(ctx context.Context, tx pgx.Tx, id string, expected int64) (status string, version int, draft []byte, err error) {
	var revision int64
	if err = tx.QueryRow(ctx, "SELECT revision,status,current_version,draft FROM agent_access_policies WHERE id=$1 FOR UPDATE", id).Scan(&revision, &status, &version, &draft); err != nil {
		return
	}
	if revision != expected {
		err = ErrConflict
	}
	return
}

func (s *Store) CreateAgentPolicy(ctx context.Context, p Principal, name string, doc agentaccess.Document) (AgentPolicy, error) {
	if err := validateAgentPolicy(name, doc); err != nil {
		return AgentPolicy{}, err
	}
	tx, err := s.beginAgentPolicyMutation(ctx, p, true)
	if err != nil {
		return AgentPolicy{}, err
	}
	defer tx.Rollback(ctx)
	var count int
	if err = tx.QueryRow(ctx, "SELECT count(*) FROM (SELECT 1 FROM agent_access_policies LIMIT $1) q", maxAgentPolicies).Scan(&count); err != nil {
		return AgentPolicy{}, err
	}
	if count >= maxAgentPolicies {
		return AgentPolicy{}, fmt.Errorf("%w: at most %d agent policies can exist", ErrInput, maxAgentPolicies)
	}
	id := NewID()
	if _, err = tx.Exec(ctx, "INSERT INTO agent_access_policies(id,name,draft,created_by,updated_by) VALUES($1,$2,$3,$4,$4)", id, strings.TrimSpace(name), JSON(doc), p.ID); err != nil {
		return AgentPolicy{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return AgentPolicy{}, err
	}
	return s.AgentPolicy(ctx, p, id)
}

func (s *Store) SaveAgentPolicyDraft(ctx context.Context, p Principal, id string, expected int64, name string, doc agentaccess.Document) (AgentPolicy, error) {
	if err := validateAgentPolicy(name, doc); err != nil {
		return AgentPolicy{}, err
	}
	tx, err := s.beginAgentPolicyMutation(ctx, p, true)
	if err != nil {
		return AgentPolicy{}, err
	}
	defer tx.Rollback(ctx)
	if _, _, _, err = lockAgentPolicy(ctx, tx, id, expected); err != nil {
		return AgentPolicy{}, err
	}
	if _, err = tx.Exec(ctx, "UPDATE agent_access_policies SET name=$2,draft=$3,revision=revision+1,updated_by=$4,updated_at=now() WHERE id=$1", id, strings.TrimSpace(name), JSON(doc), p.ID); err != nil {
		return AgentPolicy{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return AgentPolicy{}, err
	}
	return s.AgentPolicy(ctx, p, id)
}

// PublishAgentPolicy publishes the draft, or republishes the current document
// with a new status when there is no draft.
func (s *Store) PublishAgentPolicy(ctx context.Context, p Principal, id string, expected int64, status string) (AgentPolicy, error) {
	if status != agentaccess.StatusEnforced && status != agentaccess.StatusMonitor {
		return AgentPolicy{}, fmt.Errorf("%w: status must be enforced or monitor", ErrInput)
	}
	tx, err := s.beginAgentPolicyMutation(ctx, p, true)
	if err != nil {
		return AgentPolicy{}, err
	}
	defer tx.Rollback(ctx)
	current, version, draft, err := lockAgentPolicy(ctx, tx, id, expected)
	if err != nil {
		return AgentPolicy{}, err
	}
	doc := draft
	if doc == nil {
		if version == 0 {
			return AgentPolicy{}, fmt.Errorf("%w: the policy has no draft to publish", ErrInput)
		}
		if err = tx.QueryRow(ctx, "SELECT document FROM agent_access_policy_versions WHERE policy_id=$1 AND version=$2", id, version).Scan(&doc); err != nil {
			return AgentPolicy{}, err
		}
	}
	var parsed agentaccess.Document
	if err = json.Unmarshal(doc, &parsed); err != nil {
		return AgentPolicy{}, err
	}
	if err = parsed.Validate(); err != nil {
		return AgentPolicy{}, fmt.Errorf("%w: %v", ErrInput, err)
	}
	if current == agentaccess.StatusDraft {
		var published int
		if err = tx.QueryRow(ctx, "SELECT count(*) FROM (SELECT 1 FROM agent_access_policies WHERE status<>'draft' LIMIT $1) q", agentaccess.MaxPublishedPolicies).Scan(&published); err != nil {
			return AgentPolicy{}, err
		}
		if published >= agentaccess.MaxPublishedPolicies {
			return AgentPolicy{}, fmt.Errorf("%w: at most %d agent policies can be published", ErrInput, agentaccess.MaxPublishedPolicies)
		}
	}
	next := version + 1
	if _, err = tx.Exec(ctx, "INSERT INTO agent_access_policy_versions(policy_id,version,name,status,document,published_by) SELECT id,$2,name,$3,$4,$5 FROM agent_access_policies WHERE id=$1", id, next, status, JSON(parsed), p.ID); err != nil {
		return AgentPolicy{}, err
	}
	if _, err = tx.Exec(ctx, "UPDATE agent_access_policies SET status=$2,current_version=$3,draft=NULL,revision=revision+1,updated_by=$4,updated_at=now() WHERE id=$1", id, status, next, p.ID); err != nil {
		return AgentPolicy{}, err
	}
	if _, err = tx.Exec(ctx, "INSERT INTO audit_events(identity_id,key_id,action,resource,metadata) VALUES($1,$2,'agent_policy.publish',$3,$4)", p.ID, p.KeyID, id, JSON(map[string]any{"version": next, "status": status})); err != nil {
		return AgentPolicy{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return AgentPolicy{}, err
	}
	return s.AgentPolicy(ctx, p, id)
}

// DeleteAgentPolicy works without a license, like removing a custom role, so a
// lapsed license never leaves an admin unable to remove a policy.
func (s *Store) DeleteAgentPolicy(ctx context.Context, p Principal, id string, expected int64) error {
	tx, err := s.beginAgentPolicyMutation(ctx, p, false)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, _, _, err = lockAgentPolicy(ctx, tx, id, expected); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, "DELETE FROM agent_access_policies WHERE id=$1", id); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, "INSERT INTO audit_events(identity_id,key_id,action,resource) VALUES($1,$2,'agent_policy.delete',$3)", p.ID, p.KeyID, id); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) AgentPolicyVersions(ctx context.Context, p Principal, id string) ([]AgentPolicyVersion, error) {
	if !agentPolicyAdmin(p) {
		return nil, ErrForbidden
	}
	var exists bool
	if err := s.Pool.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM agent_access_policies WHERE id=$1 LIMIT 1)", id).Scan(&exists); err != nil {
		return nil, err
	}
	if !exists {
		return nil, pgx.ErrNoRows
	}
	rows, err := s.Pool.Query(ctx, `SELECT v.version,v.name,v.status,v.document,v.published_at,v.published_by,i.name
 FROM agent_access_policy_versions v JOIN identities i ON i.id=v.published_by WHERE v.policy_id=$1 ORDER BY v.version DESC LIMIT 100`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AgentPolicyVersion{}
	for rows.Next() {
		var v AgentPolicyVersion
		var doc []byte
		if err = rows.Scan(&v.Version, &v.Name, &v.Status, &doc, &v.PublishedAt, &v.PublishedBy, &v.PublishedByName); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(doc, &v.Document); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func (s *Store) PublishedAgentPolicies(ctx context.Context) ([]agentaccess.Published, error) {
	rows, err := s.Pool.Query(ctx, `SELECT p.id,v.name,v.version,v.status,v.document FROM agent_access_policies p
 JOIN agent_access_policy_versions v ON v.policy_id=p.id AND v.version=p.current_version
 WHERE p.status IN ('enforced','monitor') ORDER BY p.id LIMIT $1`, agentaccess.MaxPublishedPolicies)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []agentaccess.Published{}
	for rows.Next() {
		var v agentaccess.Published
		var doc []byte
		if err = rows.Scan(&v.ID, &v.Name, &v.Version, &v.Status, &doc); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(doc, &v.Document); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func agentTeamIDs(ctx context.Context, q licenseQuerier, identityID string) ([]string, error) {
	var ids []string
	err := q.QueryRow(ctx, "SELECT COALESCE(array_agg(team_id ORDER BY team_id),'{}') FROM (SELECT team_id FROM team_members WHERE identity_id=$1 ORDER BY team_id LIMIT 256) t", identityID).Scan(&ids)
	return ids, err
}

func (s *Store) AgentAccessContext(ctx context.Context, identityID, project, environment string) ([]string, bool, error) {
	teams, err := agentTeamIDs(ctx, s.Pool, identityID)
	if err != nil || project == "" {
		return teams, false, err
	}
	var active bool
	err = s.Pool.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM alarm_incidents WHERE status='active' AND project=$1 AND (environment=$2 OR environment='') LIMIT 1)", project, environment).Scan(&active)
	return teams, active, err
}

func (s *Store) RecordAgentAccessEvaluations(ctx context.Context, rows []AgentAccessEvaluation) error {
	if len(rows) > 500 {
		rows = rows[:500]
	}
	if len(rows) == 0 {
		return nil
	}
	_, err := s.Pool.CopyFrom(ctx, pgx.Identifier{"agent_access_evaluations"},
		[]string{"observed_at", "key_id", "identity_id", "team_ids", "operation", "category", "project", "environment", "active_incident", "decision", "policy_id", "policy_version"},
		pgx.CopyFromSlice(len(rows), func(i int) ([]any, error) {
			e := rows[i]
			teams := e.TeamIDs
			if teams == nil {
				teams = []string{}
			}
			at := e.ObservedAt
			if at.IsZero() {
				at = time.Now().UTC()
			}
			return []any{at, e.KeyID, e.IdentityID, teams, e.Operation, e.Category, e.Project, e.Environment, e.ActiveIncident, e.Decision, e.PolicyID, e.PolicyVersion}, nil
		}))
	return err
}

func (s *Store) AgentAccessEvaluations(ctx context.Context, p Principal, since time.Time, limit int) ([]AgentAccessEvaluation, error) {
	if !agentPolicyAdmin(p) {
		return nil, ErrForbidden
	}
	limit = min(max(limit, 1), 20000)
	rows, err := s.Pool.Query(ctx, `SELECT e.observed_at,e.key_id,COALESCE(k.name,''),e.identity_id,e.team_ids,e.operation,e.category,e.project,e.environment,e.active_incident,e.decision,e.policy_id,e.policy_version
 FROM agent_access_evaluations e LEFT JOIN api_keys k ON k.id=e.key_id WHERE e.observed_at>=$1 ORDER BY e.observed_at DESC,e.id DESC LIMIT $2`, since, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AgentAccessEvaluation{}
	for rows.Next() {
		var e AgentAccessEvaluation
		if err = rows.Scan(&e.ObservedAt, &e.KeyID, &e.KeyName, &e.IdentityID, &e.TeamIDs, &e.Operation, &e.Category, &e.Project, &e.Environment, &e.ActiveIncident, &e.Decision, &e.PolicyID, &e.PolicyVersion); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (s *Store) AgentAccessEvaluatingSince(ctx context.Context) (*time.Time, error) {
	var at *time.Time
	err := s.Pool.QueryRow(ctx, "SELECT min(observed_at) FROM agent_access_evaluations LIMIT 1").Scan(&at)
	return at, err
}

func (s *Store) PruneAgentAccess(ctx context.Context, now time.Time) error {
	cutoff := now.Add(-30 * 24 * time.Hour)
	for _, q := range []struct {
		sql string
		arg time.Time
	}{
		{"DELETE FROM agent_access_evaluations WHERE observed_at<$1", cutoff},
		{"UPDATE agent_access_approvals SET state='expired' WHERE state='pending' AND expires_at<=$1", now},
		{"DELETE FROM agent_access_approvals WHERE state<>'pending' AND created_at<$1", cutoff},
	} {
		if _, err := s.Pool.Exec(ctx, q.sql, q.arg); err != nil {
			return err
		}
	}
	return nil
}

// agentApprovalSelect reads approvals from src, which is the table or a CTE
// returning its rows.
func agentApprovalSelect(src string) string {
	return `SELECT a.id,a.policy_id,p.name,a.policy_version,a.key_id,k.name,a.identity_id,i.name,a.operation,a.category,a.method,a.path,a.project,a.environment,a.digest,a.approvers,a.state,a.decided_by,d.name,a.decided_at,a.reason,a.expires_at,a.consumed_at,a.created_at
 FROM ` + src + ` a JOIN agent_access_policies p ON p.id=a.policy_id JOIN api_keys k ON k.id=a.key_id JOIN identities i ON i.id=a.identity_id LEFT JOIN identities d ON d.id=a.decided_by`
}

func scanAgentApproval(row pgx.Row) (AgentApproval, error) {
	var a AgentApproval
	var digest, approvers []byte
	if err := row.Scan(&a.ID, &a.PolicyID, &a.PolicyName, &a.PolicyVersion, &a.KeyID, &a.KeyName, &a.IdentityID, &a.IdentityName, &a.Operation, &a.Category, &a.Method, &a.Path, &a.Project, &a.Environment, &digest, &approvers, &a.State, &a.DecidedBy, &a.DecidedByName, &a.DecidedAt, &a.Reason, &a.ExpiresAt, &a.ConsumedAt, &a.CreatedAt); err != nil {
		return a, err
	}
	a.Digest = hex.EncodeToString(digest)
	// A pending row past its expiry is expired; pruning only catches up later.
	if a.State == "pending" && !a.ExpiresAt.After(time.Now()) {
		a.State = "expired"
	}
	a.Approvers = []agentaccess.Approver{}
	return a, json.Unmarshal(approvers, &a.Approvers)
}

// agentDecider reports whether a principal may decide approvals at all.
func agentDecider(p Principal) bool {
	return p.IsHuman() && p.CredentialType == "browser" && !p.MFARequired
}

func canDecideAgentApproval(p Principal, teams []string, a AgentApproval) bool {
	if !agentDecider(p) || p.ID == a.IdentityID {
		return false
	}
	if len(a.Approvers) == 0 {
		return p.IsAdmin()
	}
	for _, ap := range a.Approvers {
		switch ap.Kind {
		case agentaccess.ApproverProjectAdmins:
			if p.IsAdmin() || p.CanManageProject(a.Project) {
				return true
			}
		case agentaccess.ApproverTeam:
			if contains(teams, ap.ID) {
				return true
			}
		case agentaccess.ApproverIdentity:
			if p.ID == ap.ID {
				return true
			}
		}
	}
	return false
}

// agentApproverTeams returns the approver teams of these approvals that the
// identity belongs to, so approver checks never depend on a team-count cap.
func agentApproverTeams(ctx context.Context, q licenseQuerier, identityID string, approvals ...AgentApproval) ([]string, error) {
	wanted := []string{}
	for _, a := range approvals {
		for _, ap := range a.Approvers {
			if ap.Kind == agentaccess.ApproverTeam {
				wanted = append(wanted, ap.ID)
			}
		}
	}
	ids := []string{}
	if len(wanted) == 0 {
		return ids, nil
	}
	err := q.QueryRow(ctx, "SELECT COALESCE(array_agg(DISTINCT team_id),'{}') FROM team_members WHERE identity_id=$1 AND team_id=ANY($2::text[])", identityID, wanted).Scan(&ids)
	return ids, err
}

func agentApprovalTTL(d time.Duration) time.Duration {
	return min(max(d, agentaccess.MinApprovalTTLSeconds*time.Second), agentaccess.MaxApprovalTTLSeconds*time.Second)
}

// enqueueAgentSlackEvent adds one Slack outbox row per configured destination,
// as security notifications do. sourceKey dedupes the event per destination.
func enqueueAgentSlackEvent(ctx context.Context, tx pgx.Tx, kind, sourceKey string, payload map[string]any, project, environment string) error {
	payload["schema_version"] = 1
	payload["event_id"] = sourceKey
	payload["project"] = project
	payload["environment"] = environment
	if _, err := tx.Exec(ctx, `INSERT INTO slack_event_outbox(delivery_target,integration_revision,event_kind,source_id,payload)
 SELECT 'self_hosted',i.revision,$1,hashtextextended($2,0),$3::jsonb FROM slack_integration i WHERE $1=ANY(i.events)
 ON CONFLICT DO NOTHING`, kind, sourceKey, JSON(payload)); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `INSERT INTO slack_event_outbox(delivery_target,runtime_source_id,event_kind,source_id,payload)
 SELECT 'cloud',source.source_id,$1,hashtextextended($2,0),$3::jsonb FROM slack_cloud_event_source source
 WHERE source.project='' OR (source.project=$4 AND source.environment=$5)
 ON CONFLICT DO NOTHING`, kind, sourceKey, JSON(payload), project, environment)
	return err
}

// agentApprovalPendingLimit caps the open approvals one key may hold.
const agentApprovalPendingLimit = 20

// ErrApprovalLimit means the key already has too many pending approvals.
var ErrApprovalLimit = errors.New("too many pending approvals for this key")

// RequestAgentApproval reuses the pending approval for the same key and
// request digest, or creates one and announces it.
func (s *Store) RequestAgentApproval(ctx context.Context, in AgentApprovalRequest) (AgentApproval, bool, error) {
	approvers := in.Approvers
	if approvers == nil {
		approvers = []agentaccess.Approver{}
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return AgentApproval{}, false, err
	}
	defer tx.Rollback(ctx)
	// An expired pending row would otherwise hold the unique index.
	if _, err = tx.Exec(ctx, "UPDATE agent_access_approvals SET state='expired' WHERE key_id=$1 AND digest=$2 AND state='pending' AND expires_at<=now()", in.KeyID, in.Digest[:]); err != nil {
		return AgentApproval{}, false, err
	}
	var reuse bool
	if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM agent_access_approvals WHERE key_id=$1 AND digest=$2 AND state='pending' LIMIT 1)", in.KeyID, in.Digest[:]).Scan(&reuse); err != nil {
		return AgentApproval{}, false, err
	}
	if !reuse {
		var pending int
		if err = tx.QueryRow(ctx, "SELECT count(*) FROM (SELECT 1 FROM agent_access_approvals WHERE key_id=$1 AND state='pending' AND expires_at>now() LIMIT 21) x", in.KeyID).Scan(&pending); err != nil {
			return AgentApproval{}, false, err
		}
		if pending >= agentApprovalPendingLimit {
			return AgentApproval{}, false, ErrApprovalLimit
		}
	}
	id := NewID()
	created := true
	err = tx.QueryRow(ctx, `INSERT INTO agent_access_approvals(id,policy_id,policy_version,key_id,identity_id,operation,category,method,path,project,environment,digest,approvers,expires_at)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,now()+$14::interval) ON CONFLICT (key_id,digest) WHERE state='pending' DO NOTHING RETURNING id`,
		id, in.PolicyID, in.PolicyVersion, in.KeyID, in.IdentityID, in.Operation, in.Category, in.Method, in.Path, in.Project, in.Environment, in.Digest[:], JSON(approvers), agentApprovalTTL(in.TTL)).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		created = false
		err = tx.QueryRow(ctx, "SELECT id FROM agent_access_approvals WHERE key_id=$1 AND digest=$2 AND state='pending' LIMIT 1", in.KeyID, in.Digest[:]).Scan(&id)
	}
	// The policy was deleted after the gate evaluated it; the caller retries
	// and evaluates again.
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23503" {
		return AgentApproval{}, false, ErrConflict
	}
	if err != nil {
		return AgentApproval{}, false, err
	}
	a, err := scanAgentApproval(tx.QueryRow(ctx, agentApprovalSelect("agent_access_approvals")+" WHERE a.id=$1 LIMIT 1", id))
	if err != nil {
		return a, false, err
	}
	if created {
		if err = enqueueAgentSlackEvent(ctx, tx, "agent.approval.requested", "agent-approval-"+a.ID, map[string]any{
			"occurred_at": a.CreatedAt.UTC(), "approval_id": a.ID, "policy_name": a.PolicyName, "key_name": a.KeyName,
			"operation": a.Operation, "expires_at": a.ExpiresAt.UTC(),
		}, a.Project, a.Environment); err != nil {
			return a, false, err
		}
	}
	return a, created, tx.Commit(ctx)
}

// ConsumeAgentApproval spends an approved approval once. Every failure looks
// the same so a caller cannot learn why.
func (s *Store) ConsumeAgentApproval(ctx context.Context, id, keyID string, digest [32]byte) (AgentApproval, error) {
	a, err := scanAgentApproval(s.Pool.QueryRow(ctx, `WITH u AS (UPDATE agent_access_approvals SET state='consumed',consumed_at=now()
 WHERE id=$1 AND key_id=$2 AND digest=$3 AND state='approved' AND expires_at>now() RETURNING *) `+agentApprovalSelect("u")+" LIMIT 1", id, keyID, digest[:]))
	if errors.Is(err, pgx.ErrNoRows) {
		return a, ErrForbidden
	}
	return a, err
}

// agentApprovalVisibility limits non-admin humans to rows they can decide.
// $1 is the principal id, $2 the projects it manages and $3
// whether it is an installation admin, who sees every row.
const agentApprovalVisibility = ` AND ($3::boolean OR (a.identity_id<>$1::text AND EXISTS(SELECT 1 FROM jsonb_array_elements(a.approvers) x WHERE
 (x->>'kind'='project_admins' AND a.project=ANY($2::text[])) OR (x->>'kind'='team' AND EXISTS(SELECT 1 FROM team_members tm WHERE tm.identity_id=$1::text AND tm.team_id=x->>'id')) OR (x->>'kind'='identity' AND x->>'id'=$1::text) LIMIT 1)))`

func managedProjects(p Principal) []string {
	out := []string{}
	for _, r := range p.ProjectRoles {
		if p.CanManageProject(r.Project) {
			out = append(out, r.Project)
		}
	}
	return out
}

func (s *Store) AgentApprovals(ctx context.Context, p Principal, state, cursor string, limit int) ([]AgentApproval, string, int64, error) {
	if !agentDecider(p) {
		return nil, "", 0, ErrForbidden
	}
	limit = min(max(limit, 1), 50)
	args := []any{p.ID, managedProjects(p), p.IsAdmin()}
	where := " WHERE true"
	switch state {
	case "", "pending":
		where += " AND a.state='pending' AND a.expires_at>now()"
	case "decided":
		where += " AND NOT (a.state='pending' AND a.expires_at>now())"
	default:
		return nil, "", 0, fmt.Errorf("%w: state must be pending or decided", ErrInput)
	}
	if cursor != "" {
		at, id, ok := strings.Cut(cursor, "|")
		t, perr := time.Parse(time.RFC3339Nano, at)
		if !ok || perr != nil || id == "" {
			return nil, "", 0, fmt.Errorf("%w: invalid cursor", ErrInput)
		}
		args = append(args, t, id)
		where += " AND (a.created_at,a.id)<($4,$5)"
	}
	args = append(args, limit+1)
	rows, err := s.Pool.Query(ctx, agentApprovalSelect("agent_access_approvals")+where+agentApprovalVisibility+fmt.Sprintf(" ORDER BY a.created_at DESC,a.id DESC LIMIT $%d", len(args)), args...)
	if err != nil {
		return nil, "", 0, err
	}
	defer rows.Close()
	out := []AgentApproval{}
	for rows.Next() {
		a, err := scanAgentApproval(rows)
		if err != nil {
			return nil, "", 0, err
		}
		out = append(out, a)
	}
	if err = rows.Err(); err != nil {
		return nil, "", 0, err
	}
	teams, err := agentApproverTeams(ctx, s.Pool, p.ID, out...)
	if err != nil {
		return nil, "", 0, err
	}
	now := time.Now()
	for i := range out {
		out[i].CanDecide = out[i].State == "pending" && out[i].ExpiresAt.After(now) && canDecideAgentApproval(p, teams, out[i])
	}
	next := ""
	if len(out) > limit {
		out = out[:limit]
		last := out[limit-1]
		next = last.CreatedAt.UTC().Format(time.RFC3339Nano) + "|" + last.ID
	}
	var pending int64
	err = s.Pool.QueryRow(ctx, "SELECT count(*) FROM (SELECT 1 FROM agent_access_approvals a WHERE a.state='pending' AND a.expires_at>now()"+agentApprovalVisibility+" LIMIT 10000) q", args[:3]...).Scan(&pending)
	return out, next, pending, err
}

// agentApprovalFor loads an approval the principal may see. Rows it may not
// see are reported as not found so their existence does not leak.
func (s *Store) agentApprovalFor(ctx context.Context, q licenseQuerier, p Principal, id, suffix string) (AgentApproval, error) {
	a, err := scanAgentApproval(q.QueryRow(ctx, agentApprovalSelect("agent_access_approvals")+" WHERE a.id=$1 LIMIT 1"+suffix, id))
	if err != nil {
		return a, err
	}
	if p.CredentialType == "machine" && p.KeyID != "" && p.KeyID == a.KeyID {
		return a, nil
	}
	if !agentDecider(p) {
		return AgentApproval{}, pgx.ErrNoRows
	}
	teams, err := agentApproverTeams(ctx, q, p.ID, a)
	if err != nil {
		return AgentApproval{}, err
	}
	a.CanDecide = canDecideAgentApproval(p, teams, a)
	// The person whose key made the request may see it but never decide it.
	if !p.IsAdmin() && !a.CanDecide && p.ID != a.IdentityID {
		return AgentApproval{}, pgx.ErrNoRows
	}
	a.CanDecide = a.CanDecide && a.State == "pending" && a.ExpiresAt.After(time.Now())
	return a, nil
}

func (s *Store) AgentApproval(ctx context.Context, p Principal, id string) (AgentApproval, error) {
	return s.agentApprovalFor(ctx, s.Pool, p, id, "")
}

func (s *Store) DecideAgentApproval(ctx context.Context, p Principal, id string, approve bool, reason string) (AgentApproval, error) {
	if p.MFARequired {
		return AgentApproval{}, ErrMFARequired
	}
	if !agentDecider(p) {
		return AgentApproval{}, ErrForbidden
	}
	if utf8.RuneCountInString(reason) > 500 || len(reason) > 500 {
		return AgentApproval{}, fmt.Errorf("%w: reason must be at most 500 characters", ErrInput)
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return AgentApproval{}, err
	}
	defer tx.Rollback(ctx)
	a, err := s.agentApprovalFor(ctx, tx, p, id, " FOR UPDATE OF a")
	if err != nil {
		return a, err
	}
	if p.ID == a.IdentityID {
		return AgentApproval{}, ErrForbidden
	}
	teams, err := agentApproverTeams(ctx, tx, p.ID, a)
	if err != nil {
		return AgentApproval{}, err
	}
	if !canDecideAgentApproval(p, teams, a) {
		return AgentApproval{}, ErrForbidden
	}
	state, action := "denied", "agent_approval.deny"
	if approve {
		state, action = "approved", "agent_approval.approve"
	}
	tag, err := tx.Exec(ctx, "UPDATE agent_access_approvals SET state=$2,decided_by=$3,decided_at=now(),reason=$4 WHERE id=$1 AND state='pending' AND expires_at>now()", id, state, p.ID, reason)
	if err != nil {
		return AgentApproval{}, err
	}
	if tag.RowsAffected() != 1 {
		return AgentApproval{}, ErrConflict
	}
	if _, err = tx.Exec(ctx, "INSERT INTO audit_events(identity_id,key_id,action,resource,metadata) VALUES($1,$2,$3,$4,$5)", p.ID, p.KeyID, action, id, JSON(map[string]any{"policy_id": a.PolicyID, "policy_version": a.PolicyVersion})); err != nil {
		return AgentApproval{}, err
	}
	a, err = scanAgentApproval(tx.QueryRow(ctx, agentApprovalSelect("agent_access_approvals")+" WHERE a.id=$1 LIMIT 1", id))
	if err != nil {
		return a, err
	}
	return a, tx.Commit(ctx)
}

// NotifyAgentPolicyException announces a request allowed by an incident
// exception. At most one event per policy and key is queued per ten minutes.
func (s *Store) NotifyAgentPolicyException(ctx context.Context, e AgentAccessEvaluation, policyName string) error {
	at := e.ObservedAt
	if at.IsZero() {
		at = time.Now().UTC()
	}
	key := fmt.Sprintf("agent-exception-%s-%s-%d", e.PolicyID, e.KeyID, at.Unix()/600)
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	keyName := e.KeyName
	if keyName == "" {
		if err = tx.QueryRow(ctx, "SELECT COALESCE((SELECT name FROM api_keys WHERE id=$1 LIMIT 1),'')", e.KeyID).Scan(&keyName); err != nil {
			return err
		}
	}
	if err = enqueueAgentSlackEvent(ctx, tx, "agent.policy.exception", key, map[string]any{
		"occurred_at": at.UTC(), "policy_id": e.PolicyID, "policy_name": policyName, "key_name": keyName, "operation": e.Operation,
	}, e.Project, e.Environment); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
