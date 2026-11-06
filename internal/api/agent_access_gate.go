package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hakopod/hakopod/internal/agentaccess"
	"github.com/hakopod/hakopod/internal/operations"
	"github.com/hakopod/hakopod/internal/store"
	"github.com/jackc/pgx/v5"
)

const (
	agentPolicyTTL        = 5 * time.Second
	agentPolicyStaleLimit = 60 * time.Second
	agentGateBodyLimit    = 1 << 20
	agentEvaluationQueue  = 2048
	agentEvaluationBatch  = 500
)

// agentPolicyCache holds the published policies and the license answer for
// the machine-key gate. The zero value is ready to use.
type agentPolicyCache struct {
	mu       sync.Mutex
	policies []agentaccess.Published
	licensed bool
	loaded   bool
	loadedAt time.Time
	loading  chan struct{} // non-nil while one request reloads
	failedAt time.Time
	lastErr  error

	queueOnce sync.Once
	queue     chan store.AgentAccessEvaluation
	dropped   atomic.Int64
}

// invalidate makes the next request reload policies.
func (c *agentPolicyCache) invalidate() {
	c.mu.Lock()
	c.loadedAt = time.Time{}
	c.mu.Unlock()
}

func (c *agentPolicyCache) evaluations() chan store.AgentAccessEvaluation {
	c.queueOnce.Do(func() { c.queue = make(chan store.AgentAccessEvaluation, agentEvaluationQueue) })
	return c.queue
}

// record never blocks the request; a full queue drops the row and counts it.
func (c *agentPolicyCache) record(e store.AgentAccessEvaluation) {
	select {
	case c.evaluations() <- e:
	default:
		c.dropped.Add(1)
	}
}

// load returns the cached policies, reloading them after the TTL. One caller
// reloads at a time, outside the lock; the others keep using the current value
// or, with none, wait for that load. A failed reload is retried after a 1s
// back-off, and meanwhile a value younger than the stale limit is served; with
// nothing usable it returns the error so the caller can fail closed.
func (c *agentPolicyCache) load(ctx context.Context, s *Server) ([]agentaccess.Published, bool, error) {
	c.mu.Lock()
	now := time.Now()
	if c.loaded && now.Sub(c.loadedAt) < agentPolicyTTL {
		defer c.mu.Unlock()
		return c.policies, c.licensed, nil
	}
	if c.loading != nil || now.Sub(c.failedAt) < time.Second {
		wait := c.loading
		if c.loaded && now.Sub(c.loadedAt) < agentPolicyStaleLimit {
			defer c.mu.Unlock()
			return c.policies, c.licensed, nil
		}
		c.mu.Unlock()
		if wait != nil {
			select {
			case <-wait:
			case <-ctx.Done():
				return nil, false, ctx.Err()
			}
		}
		return c.current()
	}
	done := make(chan struct{})
	c.loading = done
	c.mu.Unlock()

	lctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	licensed, policies, err := true, []agentaccess.Published(nil), s.Store.RequireFeatures(lctx, store.AgentPoliciesFeature)
	if errors.Is(err, store.ErrLicenseRequired) {
		licensed, err = false, nil
	}
	if err == nil && licensed {
		policies, err = s.Store.PublishedAgentPolicies(lctx)
	}
	cancel()

	c.mu.Lock()
	c.loading = nil
	close(done)
	if err != nil {
		c.failedAt, c.lastErr = time.Now(), err
	} else {
		c.policies, c.licensed, c.loaded, c.loadedAt = policies, licensed, true, now
	}
	c.mu.Unlock()
	return c.current()
}

// current returns the cached value while it is within the stale limit.
func (c *agentPolicyCache) current() ([]agentaccess.Published, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.loaded && time.Since(c.loadedAt) < agentPolicyStaleLimit {
		return c.policies, c.licensed, nil
	}
	if c.lastErr != nil {
		return nil, false, c.lastErr
	}
	return nil, false, errors.New("agent policies are not loaded")
}

// gateBody reads at most 1 MiB of the request body and puts it back so the
// handler still sees every byte. tooLarge means the body went past the limit;
// it is restored in full but must not be parsed or approved.
func gateBody(r *http.Request) (body []byte, tooLarge bool, err error) {
	if r.Body == nil || r.Body == http.NoBody {
		return nil, false, nil
	}
	buf, err := io.ReadAll(io.LimitReader(r.Body, agentGateBodyLimit+1))
	if err != nil {
		return nil, false, err
	}
	if len(buf) > agentGateBodyLimit {
		r.Body = struct {
			io.Reader
			io.Closer
		}{io.MultiReader(bytes.NewReader(buf), r.Body), r.Body}
		return nil, true, nil
	}
	r.Body.Close()
	r.Body = io.NopCloser(bytes.NewReader(buf))
	r.ContentLength = int64(len(buf))
	return buf, false, nil
}

// gateFields is the part of a JSON body the gate reads. Unknown fields and
// non-object bodies are ignored.
type gateFields struct {
	Project       string `json:"project"`
	Environment   string `json:"environment"`
	ApplicationID string `json:"application_id"`
	ExpiresAt     string `json:"expires_at"`
	NeverExpires  bool   `json:"never_expires"`
}

func parseGateFields(body []byte) gateFields {
	var f gateFields
	_ = json.Unmarshal(body, &f)
	return f
}

// gateScopeResource names the row the request acts on: the {id} of a family
// in gateScopeQueries, else a body application_id. segments is the unescaped
// path below /api/v1.
func gateScopeResource(segments []string, f gateFields) (table, id string) {
	if len(segments) >= 2 && segments[1] != "" {
		if _, ok := gateScopeQueries[segments[0]]; ok {
			return segments[0], segments[1]
		}
	}
	if f.ApplicationID != "" {
		return "applications", f.ApplicationID
	}
	return "", ""
}

// gateScopeQueries maps a path family to one bounded lookup of the owning
// project and environment. A family that only names a project returns an
// empty environment.
var gateScopeQueries = map[string]string{
	"applications":       "SELECT project,environment FROM applications WHERE id=$1",
	"deployments":        "SELECT a.project,a.environment FROM deployments d JOIN applications a ON a.id=d.application_id WHERE d.id=$1",
	"databases":          "SELECT project,environment FROM managed_databases WHERE id=$1",
	"builds":             "SELECT project,environment FROM build_configs WHERE id=$1",
	"backups":            "SELECT COALESCE(authority->>'project',''),COALESCE(authority->>'environment','') FROM backup_jobs WHERE id=$1",
	"backup-artifacts":   "SELECT COALESCE(j.authority->>'project',''),COALESCE(j.authority->>'environment','') FROM backup_artifacts b JOIN backup_jobs j ON j.id=b.job_id WHERE b.id=$1",
	"external-databases": "SELECT project,environment FROM external_databases WHERE id=$1",
	"keys":               "SELECT project,environment FROM api_keys WHERE id=$1",
	"previews":           "SELECT project,environment FROM previews WHERE id=$1",
	"projects":           "SELECT name,'' FROM projects WHERE name=$1",
}

type gateCandidate struct{ project, environment string }

// gateScope returns the scopes to evaluate. A resolved resource wins, so query
// or body fields cannot move a request on a production application into
// another environment. Otherwise every distinct scope the request names (query,
// body, then the key's own scope, each filled from the key scope) is a
// candidate, and the caller keeps the most restrictive decision. A lookup
// failure other than a missing row is returned so the caller fails closed.
func (s *Server) gateScope(ctx context.Context, r *http.Request, segments []string, f gateFields, p store.Principal) ([]gateCandidate, error) {
	if table, id := gateScopeResource(segments, f); table != "" && s.Store != nil && s.Store.Pool != nil {
		ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		var c gateCandidate
		err := s.Store.Pool.QueryRow(ctx, gateScopeQueries[table], id).Scan(&c.project, &c.environment)
		if err == nil {
			if table == "projects" && len(segments) >= 4 && segments[2] == "environments" {
				c.environment = segments[3]
			}
			return []gateCandidate{c}, nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return nil, err
		}
	}
	q := r.URL.Query()
	var out []gateCandidate
	for _, c := range []gateCandidate{{q.Get("project"), q.Get("environment")}, {f.Project, f.Environment}, {}} {
		if c.project == "" {
			c.project = p.Project
		}
		if c.environment == "" {
			c.environment = p.Environment
		}
		if !slices.Contains(out, c) {
			out = append(out, c)
		}
	}
	return out, nil
}

// credentialTTLViolation returns the enforced policy whose key lifetime cap
// this createKey or rotateKey request breaks, if any.
func credentialTTLViolation(operation string, d agentaccess.Decision, policies []agentaccess.Published, f gateFields, now time.Time) (string, time.Duration, bool) {
	if operation != "createKey" && operation != "rotateKey" {
		return "", 0, false
	}
	for _, m := range d.Matches {
		if m.Status != agentaccess.StatusEnforced || m.Exception {
			continue
		}
		for _, p := range policies {
			if p.ID != m.PolicyID {
				continue
			}
			limit, caps := agentaccess.CapsCredential(p.Document)
			if !caps {
				continue
			}
			expires, err := time.Parse(time.RFC3339, f.ExpiresAt)
			if f.NeverExpires || err != nil || expires.After(now.Add(limit)) {
				return p.Name, limit, true
			}
		}
	}
	return "", 0, false
}

func writePolicyBlocked(w http.ResponseWriter, m *agentaccess.Match) {
	problem(w, 403, "policy_blocked", fmt.Sprintf("Blocked by agent policy %q v%d.", m.Name, m.Version))
}

func writeApprovalRequired(w http.ResponseWriter, policyName string, a store.AgentApproval) {
	expires := a.ExpiresAt.UTC().Format(time.RFC3339)
	w.Header().Set("Hakopod-Approval-Id", a.ID)
	w.Header().Set("Hakopod-Approval-Expires", expires)
	problem(w, 403, "approval_required", fmt.Sprintf("Agent policy %q requires approval. Approval %s expires at %s. Retry the identical request with the header Hakopod-Approval: %s after it is approved.", policyName, a.ID, expires, a.ID))
}

func writePolicyUnavailable(w http.ResponseWriter) {
	problem(w, 503, "agent_policy_unavailable", "Agent policies could not be loaded. Try again.")
}

// gateSegments splits the escaped request path below /api/v1 on "/" and
// unescapes each segment, as http.ServeMux does, so an escaped slash cannot
// shift a segment. ok is false when a segment does not unescape.
func gateSegments(escaped string) ([]string, bool) {
	parts := strings.Split(strings.TrimPrefix(strings.TrimPrefix(escaped, "/api/v1"), "/"), "/")
	for i, part := range parts {
		u, err := url.PathUnescape(part)
		if err != nil {
			return nil, false
		}
		parts[i] = u
	}
	return parts, true
}

// gateDigest binds an approval to the key, the workspace header (folded into
// the key argument as keyID NUL workspace), method, escaped path, query and
// body.
func gateDigest(r *http.Request, p store.Principal, body []byte) [32]byte {
	return agentaccess.RequestDigest(p.KeyID+"\x00"+r.Header.Get("X-Hakopod-Workspace"), r.Method, r.URL.EscapedPath(), r.URL.RawQuery, body)
}

// agentAccessContext is the store lookup; a server without a store (tests)
// has no teams and no incidents.
func (s *Server) agentAccessContext(ctx context.Context, identityID, project, env string) ([]string, bool, error) {
	if s.Store == nil {
		return nil, false, nil
	}
	return s.Store.AgentAccessContext(ctx, identityID, project, env)
}

// gateAgentAccess applies published agent policies to a machine-key request.
// It returns false after writing the response when the request must stop.
func (s *Server) gateAgentAccess(w http.ResponseWriter, r *http.Request, p store.Principal) bool {
	if p.CredentialType != "machine" {
		return true
	}
	c := &s.agentPolicies
	method := r.Method
	if method == http.MethodHead {
		method = http.MethodGet
	}
	var operation, category string
	segments, ok := gateSegments(r.URL.EscapedPath())
	if ok {
		operation, category, _, _ = operations.ResolveSegments(method, segments)
	}
	// The approval status route is how a held agent learns the outcome; it is
	// never gated.
	if operation == "getAgentApproval" {
		c.record(store.AgentAccessEvaluation{ObservedAt: time.Now().UTC(), KeyID: p.KeyID, IdentityID: p.ID, Operation: operation, Category: category, Decision: agentaccess.DecisionAllow})
		return true
	}
	policies, licensed, err := c.load(r.Context(), s)
	if err != nil {
		writePolicyUnavailable(w)
		return false
	}
	if !licensed {
		return true
	}
	body, tooLarge, err := gateBody(r)
	if err != nil {
		problem(w, 400, "invalid_body", "The request body could not be read.")
		return false
	}
	var fields gateFields
	if !tooLarge {
		fields = parseGateFields(body)
	}
	candidates, err := s.gateScope(r.Context(), r, segments, fields, p)
	if err != nil {
		writePolicyUnavailable(w)
		return false
	}
	row := store.AgentAccessEvaluation{ObservedAt: time.Now().UTC(), KeyID: p.KeyID, IdentityID: p.ID, Operation: operation, Category: category, Project: candidates[0].project, Environment: candidates[0].environment, Decision: agentaccess.DecisionAllow}
	now := time.Now()
	d := agentaccess.Decision{Decision: agentaccess.DecisionAllow}
	if len(policies) > 0 {
		best := -1
		for _, cand := range candidates {
			ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
			teams, incident, err := s.agentAccessContext(ctx, p.ID, cand.project, cand.environment)
			cancel()
			if err != nil {
				writePolicyUnavailable(w)
				return false
			}
			cd := agentaccess.Evaluate(agentaccess.Request{KeyID: p.KeyID, IdentityID: p.ID, TeamIDs: teams, Operation: operation, Category: category, Project: cand.project, Environment: cand.environment, ActiveIncident: incident, Now: now}, policies)
			if rank := agentaccess.Restrictiveness(cd.Decision); rank > best {
				best, d = rank, cd
				row.Project, row.Environment, row.TeamIDs, row.ActiveIncident = cand.project, cand.environment, teams, incident
			}
		}
	}
	row.Decision = d.Decision
	if d.Policy != nil {
		row.PolicyID, row.PolicyVersion = d.Policy.PolicyID, d.Policy.Version
	}

	if id := r.Header.Get("Hakopod-Approval"); id != "" {
		// A block published after the approval was granted still wins, and
		// the approval is left unspent.
		if d.Decision == agentaccess.DecisionBlock {
			c.record(row)
			writePolicyBlocked(w, d.Policy)
			return false
		}
		if tooLarge {
			problem(w, 413, "approval_body_too_large", "Requests larger than 1 MiB cannot be approved.")
			return false
		}
		// The approval is consumed before the handler runs, so a request that
		// then fails in the handler has still used its approval.
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		a, err := s.Store.ConsumeAgentApproval(ctx, id, p.KeyID, gateDigest(r, p, body))
		cancel()
		if err != nil {
			problem(w, 403, "approval_invalid", "This approval does not exist, is not approved yet, has expired, was already used, or does not match this request.")
			return false
		}
		row.Decision, row.PolicyID, row.PolicyVersion = agentaccess.DecisionApproved, a.PolicyID, a.PolicyVersion
		c.record(row)
		return true
	}

	if name, limit, bad := credentialTTLViolation(operation, d, policies, fields, now); bad {
		row.Decision = agentaccess.DecisionBlock
		c.record(row)
		problem(w, 403, "policy_credential_ttl", fmt.Sprintf("Agent policy %q limits new keys to %s.", name, limit))
		return false
	}

	switch d.Decision {
	case agentaccess.DecisionBlock:
		c.record(row)
		writePolicyBlocked(w, d.Policy)
		return false
	case agentaccess.DecisionRequireApproval:
		c.record(row)
		if tooLarge {
			problem(w, 413, "approval_body_too_large", "Requests larger than 1 MiB cannot be approved.")
			return false
		}
		if len(r.URL.EscapedPath()) > 4096 {
			problem(w, 414, "uri_too_long", "Request path is too long to approve.")
			return false
		}
		var doc agentaccess.Document
		for _, pol := range policies {
			if pol.ID == d.Policy.PolicyID {
				doc = pol.Document
			}
		}
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		a, _, err := s.Store.RequestAgentApproval(ctx, store.AgentApprovalRequest{
			PolicyID: d.Policy.PolicyID, PolicyVersion: d.Policy.Version, KeyID: p.KeyID, IdentityID: p.ID,
			Operation: operation, Category: category, Method: r.Method, Path: r.URL.EscapedPath(), Project: row.Project, Environment: row.Environment,
			Digest:    gateDigest(r, p, body),
			Approvers: doc.Then.Approvers, TTL: time.Duration(doc.Then.ApprovalTTLSeconds) * time.Second,
		})
		cancel()
		if errors.Is(err, store.ErrApprovalLimit) {
			problem(w, 429, "approval_limit", "Too many requests made with this key are waiting for approval. Wait for them to be decided or expire.")
			return false
		}
		if err != nil {
			writePolicyUnavailable(w)
			return false
		}
		writeApprovalRequired(w, d.Policy.Name, a)
		return false
	case agentaccess.DecisionException:
		c.record(row)
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		if err := s.Store.NotifyAgentPolicyException(ctx, row, d.Policy.Name); err != nil {
			slog.Warn("agent policy exception notice could not be stored")
		}
		cancel()
		return true
	}
	c.record(row)
	return true
}

// RunAgentAccess writes queued gate decisions in batches and prunes old rows
// every hour.
func (s *Server) RunAgentAccess(ctx context.Context) {
	c := &s.agentPolicies
	queue := c.evaluations()
	flushTick, pruneTick := time.NewTicker(2*time.Second), time.NewTicker(time.Hour)
	defer flushTick.Stop()
	defer pruneTick.Stop()
	batch := make([]store.AgentAccessEvaluation, 0, agentEvaluationBatch)
	flush := func() {
		if dropped := c.dropped.Swap(0); dropped > 0 {
			slog.Warn("agent access decisions dropped because the queue was full", "count", dropped)
		}
		if len(batch) == 0 {
			return
		}
		fctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		if err := s.Store.RecordAgentAccessEvaluations(fctx, batch); err != nil {
			slog.Warn("agent access decisions could not be stored", "count", len(batch))
		}
		cancel()
		batch = batch[:0]
	}
	for {
		select {
		case <-ctx.Done():
			flush()
			return
		case e := <-queue:
			batch = append(batch, e)
			if len(batch) >= agentEvaluationBatch {
				flush()
			}
		case <-flushTick.C:
			flush()
		case <-pruneTick.C:
			pctx, cancel := context.WithTimeout(ctx, 30*time.Second)
			if err := s.Store.PruneAgentAccess(pctx, time.Now()); err != nil {
				slog.Warn("agent access history could not be pruned")
			}
			cancel()
		}
	}
}
