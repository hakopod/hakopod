package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"time"

	"github.com/hakopod/hakopod/internal/advisory"
	"github.com/hakopod/hakopod/internal/store"
)

func (s *Server) registerSecurityRoutes(routes *http.ServeMux) {
	routes.HandleFunc("GET /api/v1/security/advisories", s.securityOverview)
	routes.HandleFunc("GET /api/v1/security/settings", s.securitySettings)
	routes.HandleFunc("PUT /api/v1/security/settings", s.putSecuritySettings)
	routes.HandleFunc("GET /api/v1/applications/{id}/security", s.applicationSecurity)
	routes.HandleFunc("POST /api/v1/applications/{id}/security/check", s.checkApplicationSecurity)
	routes.HandleFunc("POST /api/v1/applications/{id}/security/apply", s.applySecurityFix)
	routes.HandleFunc("PUT /api/v1/applications/{id}/security/policy", s.putSecurityPolicy)
}

type securityAttempt struct {
	ID           string    `json:"id"`
	AdvisoryID   string    `json:"advisory_id"`
	Service      string    `json:"service"`
	Status       string    `json:"status"`
	DeploymentID string    `json:"deployment_id"`
	Error        string    `json:"error"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

type securityApplication struct {
	ApplicationID     string             `json:"application_id"`
	ApplicationName   string             `json:"application_name"`
	Project           string             `json:"project"`
	Environment       string             `json:"environment"`
	DeploymentID      string             `json:"deployment_id"`
	Revision          int64              `json:"revision"`
	CheckedAt         *time.Time         `json:"checked_at"`
	EvaluatedAt       *time.Time         `json:"evaluated_at"`
	Findings          []advisory.Finding `json:"findings"`
	UnknownServices   []string           `json:"unknown_services"`
	Incomplete        []string           `json:"incomplete"`
	AutoPatch         bool               `json:"auto_patch"`
	ApprovedBy        string             `json:"approved_by"`
	ApprovalExpiresAt *time.Time         `json:"approval_expires_at"`
	PolicyRevision    int64              `json:"policy_revision"`
	Attempts          []securityAttempt  `json:"attempts"`
	CanWrite          bool               `json:"can_write"`
}

type securityFeedStatus struct {
	FeedEnabled bool       `json:"feed_enabled"`
	OSVEnabled  bool       `json:"osv_enabled"`
	Revision    int64      `json:"revision"`
	IssuedAt    *time.Time `json:"issued_at"`
	ExpiresAt   *time.Time `json:"expires_at"`
	CheckedAt   *time.Time `json:"checked_at"`
	Stale       bool       `json:"stale"`
	Error       string     `json:"error"`
}

type securitySummary struct {
	AffectedApplications int `json:"affected_applications"`
	Critical             int `json:"critical"`
	High                 int `json:"high"`
}

func feedStale(st store.FeedState, now time.Time) bool {
	return len(st.Body) == 0 || st.ExpiresAt == nil || st.ExpiresAt.Before(now)
}

func feedStatus(in securityInputs) securityFeedStatus {
	return securityFeedStatus{FeedEnabled: in.settings.FeedEnabled, OSVEnabled: in.settings.OSVEnabled, Revision: in.state.Revision,
		IssuedAt: in.state.IssuedAt, ExpiresAt: in.state.ExpiresAt, CheckedAt: in.state.CheckedAt, Stale: feedStale(in.state, time.Now()), Error: in.state.Error}
}

// summarize counts affected applications; critical and high count applications
// by their most severe finding.
func summarize(apps []securityApplication) securitySummary {
	var out securitySummary
	for _, a := range apps {
		if len(a.Findings) == 0 {
			continue
		}
		out.AffectedApplications++
		severities := map[advisory.Severity]bool{}
		for _, f := range a.Findings {
			severities[f.Severity] = true
		}
		if severities[advisory.Critical] {
			out.Critical++
		} else if severities[advisory.High] {
			out.High++
		}
	}
	return out
}

func (s *Server) securityView(ctx context.Context, p store.Principal, row store.ApplicationSecurity) (securityApplication, error) {
	attempts, err := s.Store.PatchAttempts(ctx, row.ApplicationID, 10)
	if err != nil {
		return securityApplication{}, err
	}
	view := securityApplication{ApplicationID: row.ApplicationID, ApplicationName: row.ApplicationName, Project: row.Project, Environment: row.Environment,
		DeploymentID: row.DeploymentID, Revision: row.Revision, CheckedAt: row.CheckedAt, EvaluatedAt: row.EvaluatedAt, Findings: row.Findings,
		UnknownServices: []string{}, Incomplete: []string{}, AutoPatch: row.AutoPatch, ApprovedBy: row.ApprovedBy, ApprovalExpiresAt: row.ApprovalExpiresAt, PolicyRevision: row.PolicyRevision, Attempts: []securityAttempt{},
		CanWrite: p.Allows("deployments:write", row.Project, row.Environment, row.ApplicationName)}
	if view.Findings == nil {
		view.Findings = []advisory.Finding{}
	}
	if row.Inventory != nil {
		view.Incomplete = append(view.Incomplete, row.Inventory.Incomplete...)
		for _, svc := range row.Inventory.Services {
			if svc.Unknown {
				view.UnknownServices = append(view.UnknownServices, svc.Service)
			}
		}
	}
	for _, a := range attempts {
		view.Attempts = append(view.Attempts, securityAttempt{a.ID, a.AdvisoryID, a.Service, a.Status, a.DeploymentID, a.Error, a.CreatedAt, a.UpdatedAt})
	}
	return view, nil
}

func (s *Server) writeSecurityView(w http.ResponseWriter, r *http.Request, row store.ApplicationSecurity) {
	view, err := s.securityView(r.Context(), who(r), row)
	if err != nil {
		failure(w, err)
		return
	}
	write(w, 200, view)
}

func (s *Server) securityOverview(w http.ResponseWriter, r *http.Request) {
	in, err := s.loadSecurityInputs(r.Context())
	if err != nil {
		failure(w, err)
		return
	}
	rows, err := s.Store.SecurityOverview(r.Context(), who(r))
	if err != nil {
		failure(w, err)
		return
	}
	apps := []securityApplication{}
	// ponytail: one attempts query per visible row (at most 500); batch it if the overview gets slow.
	for _, row := range rows {
		view, err := s.securityView(r.Context(), who(r), row)
		if err != nil {
			failure(w, err)
			return
		}
		apps = append(apps, view)
	}
	write(w, 200, map[string]any{"feed": feedStatus(in), "applications": apps, "summary": summarize(apps)})
}

func (s *Server) applicationSecurity(w http.ResponseWriter, r *http.Request) {
	a, ok := s.authorizedApp(w, r, r.PathValue("id"), "deployments:read")
	if !ok {
		return
	}
	row, err := s.Store.ApplicationSecurity(r.Context(), a.ID)
	if err != nil {
		failure(w, err)
		return
	}
	s.writeSecurityView(w, r, row)
}

// evaluateNow re-inspects the current deployment synchronously.
func (s *Server) evaluateNow(ctx context.Context, a store.Application) (store.ApplicationSecurity, securityInputs, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	in, err := s.loadSecurityInputs(ctx)
	if err != nil {
		return store.ApplicationSecurity{}, in, err
	}
	c, err := s.candidateFor(ctx, a)
	if err != nil {
		return store.ApplicationSecurity{}, in, err
	}
	row, err := s.evaluateSecurity(ctx, c, in)
	return row, in, err
}

// recentOrEvaluate reuses a row checked under 30 seconds ago against the
// current deployment, the same guard check uses, and otherwise evaluates now.
func (s *Server) recentOrEvaluate(ctx context.Context, a store.Application, deploymentID string) (store.ApplicationSecurity, securityInputs, error) {
	row, err := s.Store.ApplicationSecurity(ctx, a.ID)
	if err == nil && row.DeploymentID == deploymentID && row.CheckedAt != nil && time.Since(*row.CheckedAt) < 30*time.Second {
		in, err := s.loadSecurityInputs(ctx)
		return row, in, err
	}
	return s.evaluateNow(ctx, a)
}

// appliedFix finds a fix this principal already started with the same
// Idempotency-Key, the way Store.Accept and runBuild recognise replays.
func (s *Server) appliedFix(ctx context.Context, p store.Principal, idem, applicationID, service string) (map[string]string, bool) {
	var id string
	if s.Store.Pool.QueryRow(ctx, "SELECT id FROM deployments WHERE identity_id=$1 AND idempotency_key=$2 AND application_id=$3", p.ID, idem, applicationID).Scan(&id) == nil {
		return map[string]string{"kind": string(advisory.FixReresolve), "deployment_id": id, "build_id": ""}, true
	}
	builds, err := s.applicationBuilds(ctx, applicationID)
	config, ok := builds[service]
	if err != nil || !ok {
		return nil, false
	}
	if s.Store.Pool.QueryRow(ctx, "SELECT id FROM build_runs WHERE build_id=$1 AND identity_id=$2 AND idempotency_key=$3", config.ID, p.ID, idem).Scan(&id) == nil {
		return map[string]string{"kind": string(advisory.FixRebuild), "deployment_id": "", "build_id": id}, true
	}
	return nil, false
}

func securityFailure(w http.ResponseWriter, err error) {
	if errors.Is(err, errRevisionNotReady) {
		problem(w, 409, "revision_not_ready", "Wait for the current deployment to finish, then check again.")
		return
	}
	failure(w, err)
}

func (s *Server) checkApplicationSecurity(w http.ResponseWriter, r *http.Request) {
	a, ok := s.authorizedApp(w, r, r.PathValue("id"), "deployments:write")
	if !ok {
		return
	}
	current, err := s.Store.ApplicationSecurity(r.Context(), a.ID)
	if err != nil {
		failure(w, err)
		return
	}
	if current.CheckedAt != nil && time.Since(*current.CheckedAt) < 30*time.Second {
		problem(w, 429, "rate_limited", "This app was checked less than 30 seconds ago. Try again shortly.")
		return
	}
	row, _, err := s.evaluateNow(r.Context(), a)
	if err != nil {
		securityFailure(w, err)
		return
	}
	s.writeSecurityView(w, r, row)
}

type securityApplyInput struct {
	AdvisoryID       string `json:"advisory_id"`
	Service          string `json:"service"`
	ExpectedRevision *int64 `json:"expected_revision"`
}

// applyRefusal explains why a finding cannot be fixed with one click.
func applyRefusal(row store.ApplicationSecurity, advisoryID, service string, stale bool) (advisory.Finding, string) {
	for _, f := range row.Findings {
		if f.AdvisoryID != advisoryID || f.Service != service {
			continue
		}
		switch {
		case f.Fix == advisory.FixManual:
			return f, "This finding needs a manual change."
		case f.Source == advisory.SourceHakopod && stale:
			return f, "The advisory feed is out of date. Wait for it to refresh, then try again."
		}
		return f, ""
	}
	return advisory.Finding{}, "This finding no longer applies to the running app."
}

func (s *Server) applySecurityFix(w http.ResponseWriter, r *http.Request) {
	a, ok := s.authorizedApp(w, r, r.PathValue("id"), "deployments:write")
	if !ok {
		return
	}
	var in securityApplyInput
	if !decode(w, r, &in) {
		return
	}
	if in.ExpectedRevision == nil || in.AdvisoryID == "" || in.Service == "" {
		problem(w, 400, "invalid_request", "advisory_id, service and expected_revision are required")
		return
	}
	idem := r.Header.Get("Idempotency-Key")
	if len(idem) < 8 || len(idem) > 128 {
		problem(w, 400, "invalid_request", "Idempotency-Key must contain 8–128 characters")
		return
	}
	if replay, ok := s.appliedFix(r.Context(), who(r), idem, a.ID, in.Service); ok {
		write(w, 202, replay)
		return
	}
	if *in.ExpectedRevision != a.Revision {
		problem(w, 409, "revision_conflict", "The app changed since you reviewed it. Reload and review the update again.")
		return
	}
	// Never build a fix on an older spec: that would silently revert the
	// configuration of a newer revision that did not deploy.
	current, err := s.Store.RuntimeBase(r.Context(), a.ID, a.Revision)
	if err != nil {
		failure(w, err)
		return
	}
	if current.Status != "succeeded" {
		if current.Status == "queued" || current.Status == "running" {
			securityFailure(w, errRevisionNotReady)
		} else {
			problem(w, 409, "revision_failed", "The latest revision did not deploy. Roll back or fix it, then update.")
		}
		return
	}
	row, inputs, err := s.recentOrEvaluate(r.Context(), a, current.ID)
	if err != nil {
		securityFailure(w, err)
		return
	}
	f, refusal := applyRefusal(row, in.AdvisoryID, in.Service, feedStale(inputs.state, time.Now()))
	if refusal != "" {
		problem(w, 422, "fix_unavailable", refusal)
		return
	}
	result := map[string]string{"kind": string(f.Fix), "deployment_id": "", "build_id": ""}
	if f.Fix == advisory.FixRebuild {
		result["build_id"], ok = s.rebuildService(w, r, a.ID, row.DeploymentID, in.Service, idem)
		if !ok {
			return
		}
	} else {
		d, err := s.Store.Deployment(r.Context(), row.DeploymentID)
		if err != nil {
			failure(w, err)
			return
		}
		next, err := s.Store.Accept(r.Context(), who(r), a.Project, a.Environment, d.Spec, a.Revision, idem)
		if err != nil {
			failure(w, err)
			return
		}
		result["deployment_id"] = next.ID
	}
	_ = s.Store.RuntimeAudit(r.Context(), who(r), "security.fix.apply", a.ID, map[string]any{"advisory_id": f.AdvisoryID, "service": f.Service, "kind": f.Fix, "deployment_id": result["deployment_id"], "build_id": result["build_id"]})
	write(w, 202, result)
}

// rebuildService starts the service's build at the running commit through the
// same handler a person uses, so every build check and limit still applies.
func (s *Server) rebuildService(w http.ResponseWriter, r *http.Request, applicationID, deploymentID, service, idem string) (string, bool) {
	builds, err := s.applicationBuilds(r.Context(), applicationID)
	if err != nil {
		failure(w, err)
		return "", false
	}
	c, ok := builds[service]
	d, err := s.Store.Deployment(r.Context(), deploymentID)
	if err != nil {
		failure(w, err)
		return "", false
	}
	commit := d.Provenance[service].CommitSHA
	if !ok || commit == "" {
		problem(w, 422, "fix_unavailable", "This service has no build configuration to rebuild from.")
		return "", false
	}
	body, _ := json.Marshal(map[string]any{"expected_config_revision": c.Revision, "commit": commit})
	inner := httptest.NewRequestWithContext(r.Context(), "POST", "/api/v1/builds/"+c.ID+"/run", strings.NewReader(string(body)))
	inner.SetPathValue("id", c.ID)
	inner.Header.Set("Idempotency-Key", idem)
	rec := httptest.NewRecorder()
	s.runBuild(rec, inner)
	if rec.Code != 202 {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(rec.Code)
		_, _ = w.Write(rec.Body.Bytes())
		return "", false
	}
	var run buildRun
	if err := json.Unmarshal(rec.Body.Bytes(), &run); err != nil {
		failure(w, err)
		return "", false
	}
	return run.ID, true
}

func (s *Server) putSecurityPolicy(w http.ResponseWriter, r *http.Request) {
	a, ok := s.authorizedApp(w, r, r.PathValue("id"), "deployments:write")
	if !ok {
		return
	}
	var in struct {
		AutoPatch              *bool  `json:"auto_patch"`
		ExpectedPolicyRevision *int64 `json:"expected_policy_revision"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.AutoPatch == nil || in.ExpectedPolicyRevision == nil {
		problem(w, 400, "invalid_request", "auto_patch and expected_policy_revision are required")
		return
	}
	row, err := s.Store.SetSecurityPolicy(r.Context(), who(r), a.ID, *in.AutoPatch, *in.ExpectedPolicyRevision)
	if err != nil {
		failure(w, err)
		return
	}
	_ = s.Store.RuntimeAudit(r.Context(), who(r), "security.policy.update", a.ID, map[string]any{"auto_patch": *in.AutoPatch, "policy_revision": row.PolicyRevision})
	s.writeSecurityView(w, r, row)
}

func (s *Server) writeSecuritySettings(w http.ResponseWriter, r *http.Request) {
	v, rev, err := s.Store.SecuritySettings(r.Context())
	if err != nil {
		failure(w, err)
		return
	}
	write(w, 200, map[string]any{"feed_enabled": v.FeedEnabled, "osv_enabled": v.OSVEnabled, "revision": rev, "can_manage": installationAdministrator(who(r))})
}

func (s *Server) securitySettings(w http.ResponseWriter, r *http.Request) {
	s.writeSecuritySettings(w, r)
}

func (s *Server) putSecuritySettings(w http.ResponseWriter, r *http.Request) {
	if !installationAdministrator(who(r)) {
		failure(w, store.ErrForbidden)
		return
	}
	var in struct {
		FeedEnabled      *bool  `json:"feed_enabled"`
		OSVEnabled       *bool  `json:"osv_enabled"`
		ExpectedRevision *int64 `json:"expected_revision"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.FeedEnabled == nil || in.OSVEnabled == nil || in.ExpectedRevision == nil {
		problem(w, 400, "invalid_request", "feed_enabled, osv_enabled and expected_revision are required")
		return
	}
	rev, err := s.Store.PutSecuritySettings(r.Context(), store.SecuritySettings{FeedEnabled: *in.FeedEnabled, OSVEnabled: *in.OSVEnabled}, *in.ExpectedRevision)
	if err != nil {
		failure(w, err)
		return
	}
	_ = s.Store.RuntimeAudit(r.Context(), who(r), "security.settings.update", "security_advisories", map[string]any{"feed_enabled": *in.FeedEnabled, "osv_enabled": *in.OSVEnabled, "revision": rev})
	s.writeSecuritySettings(w, r)
}
