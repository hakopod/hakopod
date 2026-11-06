package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/hakopod/hakopod/internal/agentaccess"
	"github.com/hakopod/hakopod/internal/operations"
	"github.com/hakopod/hakopod/internal/store"
	"github.com/jackc/pgx/v5"
)

const agentAccessBodyLimit = 64 << 10

func (s *Server) registerAgentAccessRoutes(routes *http.ServeMux) {
	routes.HandleFunc("GET /api/v1/agent-policies", s.listAgentPolicies)
	routes.HandleFunc("POST /api/v1/agent-policies", s.createAgentPolicy)
	routes.HandleFunc("POST /api/v1/agent-policies/preview", s.previewAgentPolicy)
	routes.HandleFunc("POST /api/v1/agent-policies/simulate", s.simulateAgentPolicy)
	routes.HandleFunc("GET /api/v1/agent-policies/{id}", s.getAgentPolicy)
	routes.HandleFunc("DELETE /api/v1/agent-policies/{id}", s.deleteAgentPolicy)
	routes.HandleFunc("PUT /api/v1/agent-policies/{id}/draft", s.saveAgentPolicyDraft)
	routes.HandleFunc("POST /api/v1/agent-policies/{id}/publish", s.publishAgentPolicy)
	routes.HandleFunc("GET /api/v1/agent-policies/{id}/versions", s.agentPolicyVersions)
	routes.HandleFunc("GET /api/v1/agent-approvals", s.listAgentApprovals)
	routes.HandleFunc("GET /api/v1/agent-approvals/{id}", s.getAgentApproval)
	routes.HandleFunc("POST /api/v1/agent-approvals/{id}/approve", func(w http.ResponseWriter, r *http.Request) { s.decideAgentApproval(w, r, true) })
	routes.HandleFunc("POST /api/v1/agent-approvals/{id}/deny", func(w http.ResponseWriter, r *http.Request) { s.decideAgentApproval(w, r, false) })
}

// agentFailure adds the license and MFA cases the shared failure helper lacks.
func agentFailure(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrLicenseRequired):
		problem(w, 402, "license_required", err.Error())
	case errors.Is(err, store.ErrMFARequired):
		problem(w, 401, "mfa_required", err.Error())
	default:
		failure(w, err)
	}
}

// validDocument writes 400 invalid_policy and returns false when doc is invalid.
func validDocument(w http.ResponseWriter, doc *agentaccess.Document) bool {
	if doc == nil {
		problem(w, 400, "invalid_policy", "A policy document is required.")
		return false
	}
	if err := doc.Validate(); err != nil {
		problem(w, 400, "invalid_policy", err.Error())
		return false
	}
	return true
}

func (s *Server) listAgentPolicies(w http.ResponseWriter, r *http.Request) {
	items, summary, err := s.Store.ListAgentPolicies(r.Context(), who(r))
	if err != nil {
		agentFailure(w, err)
		return
	}
	since, err := s.Store.AgentAccessEvaluatingSince(r.Context())
	if err != nil {
		agentFailure(w, err)
		return
	}
	licensed := s.Store.RequireFeatures(r.Context(), store.AgentPoliciesFeature) == nil
	write(w, 200, map[string]any{"items": items, "summary": summary, "licensed": licensed, "evaluating_since": since})
}

func (s *Server) createAgentPolicy(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name     string                `json:"name"`
		Document *agentaccess.Document `json:"document"`
	}
	if !decodeLimited(w, r, &in, agentAccessBodyLimit) || !validDocument(w, in.Document) {
		return
	}
	policy, err := s.Store.CreateAgentPolicy(r.Context(), who(r), in.Name, *in.Document)
	if err != nil {
		agentFailure(w, err)
		return
	}
	s.agentPolicies.invalidate()
	write(w, 201, policy)
}

func (s *Server) getAgentPolicy(w http.ResponseWriter, r *http.Request) {
	policy, err := s.Store.AgentPolicy(r.Context(), who(r), r.PathValue("id"))
	if err != nil {
		agentFailure(w, err)
		return
	}
	write(w, 200, policy)
}

func (s *Server) saveAgentPolicyDraft(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ExpectedRevision *int64                `json:"expected_revision"`
		Name             string                `json:"name"`
		Document         *agentaccess.Document `json:"document"`
	}
	if !decodeLimited(w, r, &in, agentAccessBodyLimit) {
		return
	}
	if in.ExpectedRevision == nil {
		failure(w, fmt.Errorf("%w: expected_revision is required", store.ErrInput))
		return
	}
	if !validDocument(w, in.Document) {
		return
	}
	policy, err := s.Store.SaveAgentPolicyDraft(r.Context(), who(r), r.PathValue("id"), *in.ExpectedRevision, in.Name, *in.Document)
	if err != nil {
		agentFailure(w, err)
		return
	}
	s.agentPolicies.invalidate()
	write(w, 200, policy)
}

func (s *Server) publishAgentPolicy(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ExpectedRevision *int64 `json:"expected_revision"`
		Status           string `json:"status"`
	}
	if !decodeLimited(w, r, &in, agentAccessBodyLimit) {
		return
	}
	if in.ExpectedRevision == nil || (in.Status != agentaccess.StatusEnforced && in.Status != agentaccess.StatusMonitor) {
		failure(w, fmt.Errorf("%w: expected_revision is required and status must be enforced or monitor", store.ErrInput))
		return
	}
	policy, err := s.Store.PublishAgentPolicy(r.Context(), who(r), r.PathValue("id"), *in.ExpectedRevision, in.Status)
	if err != nil {
		agentFailure(w, err)
		return
	}
	s.agentPolicies.invalidate()
	write(w, 200, policy)
}

func (s *Server) deleteAgentPolicy(w http.ResponseWriter, r *http.Request) {
	expected, err := strconv.ParseInt(r.URL.Query().Get("expected_revision"), 10, 64)
	if err != nil {
		failure(w, fmt.Errorf("%w: expected_revision must be a whole number", store.ErrInput))
		return
	}
	if err = s.Store.DeleteAgentPolicy(r.Context(), who(r), r.PathValue("id"), expected); err != nil {
		agentFailure(w, err)
		return
	}
	s.agentPolicies.invalidate()
	write(w, 200, map[string]string{"status": "deleted"})
}

func (s *Server) agentPolicyVersions(w http.ResponseWriter, r *http.Request) {
	versions, err := s.Store.AgentPolicyVersions(r.Context(), who(r), r.PathValue("id"))
	if err != nil {
		agentFailure(w, err)
		return
	}
	write(w, 200, map[string]any{"items": versions})
}

type agentPolicySample struct {
	ObservedAt  time.Time `json:"observed_at"`
	KeyID       string    `json:"key_id"`
	KeyName     string    `json:"key_name"`
	Operation   string    `json:"operation"`
	Category    string    `json:"category"`
	Project     string    `json:"project"`
	Environment string    `json:"environment"`
	Decision    string    `json:"decision"`
}

func (s *Server) previewAgentPolicy(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Document *agentaccess.Document `json:"document"`
		Days     int                   `json:"days"`
	}
	if !decodeLimited(w, r, &in, agentAccessBodyLimit) {
		return
	}
	if in.Days < 1 || in.Days > 30 {
		failure(w, fmt.Errorf("%w: days must be 1–30", store.ErrInput))
		return
	}
	if !validDocument(w, in.Document) {
		return
	}
	now := time.Now().UTC()
	rows, err := s.Store.AgentAccessEvaluations(r.Context(), who(r), now.Add(-time.Duration(in.Days)*24*time.Hour), 20000)
	if err != nil {
		agentFailure(w, err)
		return
	}
	since, err := s.Store.AgentAccessEvaluatingSince(r.Context())
	if err != nil {
		agentFailure(w, err)
		return
	}
	policies := []agentaccess.Published{{ID: "preview", Name: "Preview", Version: 1, Status: agentaccess.StatusEnforced, Document: *in.Document}}
	out := map[string]any{"days": in.Days, "evaluated": len(rows), "evaluating_since": since}
	matched, approval, blocked, exceptions := 0, 0, 0, 0
	samples := []agentPolicySample{}
	for _, row := range rows { // newest first
		d := agentaccess.Evaluate(agentaccess.Request{KeyID: row.KeyID, IdentityID: row.IdentityID, TeamIDs: row.TeamIDs, Operation: row.Operation, Category: row.Category, Project: row.Project, Environment: row.Environment, ActiveIncident: row.ActiveIncident, Now: now}, policies)
		if len(d.Matches) == 0 {
			continue
		}
		matched++
		switch d.Decision {
		case agentaccess.DecisionRequireApproval:
			approval++
		case agentaccess.DecisionBlock:
			blocked++
		case agentaccess.DecisionException:
			exceptions++
		}
		if len(samples) < 8 {
			samples = append(samples, agentPolicySample{row.ObservedAt, row.KeyID, row.KeyName, row.Operation, row.Category, row.Project, row.Environment, d.Decision})
		}
	}
	out["matched"], out["require_approval"], out["blocked"], out["exceptions"], out["samples"] = matched, approval, blocked, exceptions, samples
	write(w, 200, out)
}

func (s *Server) simulateAgentPolicy(w http.ResponseWriter, r *http.Request) {
	var in struct {
		KeyID          string                `json:"key_id"`
		Operation      string                `json:"operation"`
		Project        string                `json:"project"`
		Environment    string                `json:"environment"`
		ActiveIncident bool                  `json:"active_incident"`
		PolicyID       string                `json:"policy_id"`
		Document       *agentaccess.Document `json:"document"`
	}
	if !decodeLimited(w, r, &in, agentAccessBodyLimit) {
		return
	}
	if p := who(r); !p.IsAdmin() || !p.IsHuman() || p.CredentialType != "browser" {
		failure(w, store.ErrForbidden)
		return
	}
	if in.Document != nil && !validDocument(w, in.Document) {
		return
	}
	op, ok := operations.OperationByID(in.Operation)
	if !ok {
		failure(w, fmt.Errorf("%w: Unknown operation.", store.ErrInput))
		return
	}
	category := op.Policy.Category
	if category == "unclassified" {
		category = ""
	}
	if in.KeyID == "" || len(in.KeyID) > 64 {
		failure(w, fmt.Errorf("%w: Unknown key.", store.ErrInput))
		return
	}
	var identityID string
	if err := s.Store.Pool.QueryRow(r.Context(), "SELECT identity_id FROM api_keys WHERE id=$1 LIMIT 1", in.KeyID).Scan(&identityID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			err = fmt.Errorf("%w: Unknown key.", store.ErrInput)
		}
		failure(w, err)
		return
	}
	teams, _, err := s.Store.AgentAccessContext(r.Context(), identityID, "", "")
	if err != nil {
		failure(w, err)
		return
	}
	policies, err := s.Store.PublishedAgentPolicies(r.Context())
	if err != nil {
		failure(w, err)
		return
	}
	if in.Document != nil {
		id := in.PolicyID
		if id == "" {
			id = "draft"
		}
		entry := agentaccess.Published{ID: id, Name: "Draft", Version: 0, Status: agentaccess.StatusEnforced, Document: *in.Document}
		replaced := false
		for i := range policies {
			if policies[i].ID == id {
				entry.Name, entry.Version = policies[i].Name, policies[i].Version
				policies[i], replaced = entry, true
			}
		}
		if !replaced && in.PolicyID != "" {
			if existing, err := s.Store.AgentPolicy(r.Context(), who(r), id); err == nil {
				entry.Name = existing.Name
			}
		}
		if !replaced {
			policies = append(policies, entry)
		}
	}
	d := agentaccess.Evaluate(agentaccess.Request{KeyID: in.KeyID, IdentityID: identityID, TeamIDs: teams, Operation: in.Operation, Category: category, Project: in.Project, Environment: in.Environment, ActiveIncident: in.ActiveIncident, Now: time.Now().UTC()}, policies)
	matches := d.Matches
	if matches == nil {
		matches = []agentaccess.Match{}
	}
	write(w, 200, map[string]any{"decision": d.Decision, "policy": d.Policy, "matches": matches, "reason": d.Reason, "category": category})
}

func (s *Server) listAgentApprovals(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	state := q.Get("state")
	if state == "" {
		state = "pending"
	}
	limit := 25
	if raw := q.Get("limit"); raw != "" {
		var err error
		if limit, err = strconv.Atoi(raw); err != nil {
			limit = 0
		}
	}
	if (state != "pending" && state != "decided") || limit < 1 || limit > 50 || len(q.Get("cursor")) > 256 {
		failure(w, fmt.Errorf("%w: state must be pending or decided and limit must be 1–50", store.ErrInput))
		return
	}
	items, next, pending, err := s.Store.AgentApprovals(r.Context(), who(r), state, q.Get("cursor"), limit)
	if err != nil {
		agentFailure(w, err)
		return
	}
	write(w, 200, map[string]any{"items": items, "next_cursor": next, "pending": pending})
}

func (s *Server) getAgentApproval(w http.ResponseWriter, r *http.Request) {
	a, err := s.Store.AgentApproval(r.Context(), who(r), r.PathValue("id"))
	if err != nil {
		agentFailure(w, err)
		return
	}
	write(w, 200, a)
}

func (s *Server) decideAgentApproval(w http.ResponseWriter, r *http.Request, approve bool) {
	var in struct {
		Reason string `json:"reason"`
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, agentAccessBodyLimit))
	if err != nil {
		problem(w, 400, "invalid_request", "request body is too large or unreadable")
		return
	}
	if len(bytes.TrimSpace(body)) > 0 { // the body is optional
		d := json.NewDecoder(bytes.NewReader(body))
		d.DisallowUnknownFields()
		if err = d.Decode(&in); err != nil || d.More() {
			problem(w, 400, "invalid_request", "invalid JSON or unknown field")
			return
		}
	}
	if len([]rune(in.Reason)) > 500 {
		failure(w, fmt.Errorf("%w: reason must be 500 characters or fewer", store.ErrInput))
		return
	}
	a, err := s.Store.DecideAgentApproval(r.Context(), who(r), r.PathValue("id"), approve, in.Reason)
	if err != nil {
		agentFailure(w, err)
		return
	}
	write(w, 200, a)
}
