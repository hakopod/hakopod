package api

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/hakopod/hakopod/internal/cluster"
	"github.com/hakopod/hakopod/internal/store"
)

// hostnameCreatesPerMinute bounds hostname creation per application, so a
// runaway SaaS backend cannot flood DNS checks and certificate orders.
const hostnameCreatesPerMinute = 120

type hostnameWindow struct {
	start time.Time
	count int
}

type customHostnameView struct {
	store.CustomHostname
	Target string `json:"target"`
}

func (s *Server) registerCustomHostnameRoutes(m *http.ServeMux) {
	m.HandleFunc("GET /api/v1/applications/{id}/services/{service}/custom-hostnames", s.listCustomHostnames)
	m.HandleFunc("POST /api/v1/applications/{id}/services/{service}/custom-hostnames", s.createCustomHostname)
	m.HandleFunc("GET /api/v1/applications/{id}/services/{service}/custom-hostnames/{hostname}", s.getCustomHostname)
	m.HandleFunc("DELETE /api/v1/applications/{id}/services/{service}/custom-hostnames/{hostname}", s.deleteCustomHostname)
	m.HandleFunc("GET /api/v1/settings/custom-hostnames", s.getCustomHostnameSettings)
	m.HandleFunc("PUT /api/v1/settings/custom-hostnames", s.putCustomHostnameSettings)
}

// hostnameApp authorizes the caller and resolves the service and the CNAME
// target customers point at.
func (s *Server) hostnameApp(w http.ResponseWriter, r *http.Request, permission string) (store.Application, string, string, bool) {
	a, ok := s.authorizedApp(w, r, r.PathValue("id"), permission)
	if !ok {
		return a, "", "", false
	}
	service := r.PathValue("service")
	svc, exists := a.Spec.Services[service]
	if !exists {
		problem(w, 404, "not_found", "unknown service")
		return a, "", "", false
	}
	target := ""
	if svc.CustomHostnames != nil && svc.CustomHostnames.Target != "" {
		target = svc.CustomHostnames.Target
	} else if s.Cluster != nil {
		target = s.Cluster.ServiceHostname(cluster.Target{ApplicationID: a.ID, Spec: a.Spec}, service)
	}
	return a, service, target, true
}

// allowHostnameCreate is a fixed one-minute window per application. It
// returns how long to wait when the window is full.
func (s *Server) allowHostnameCreate(app string, now time.Time) (bool, time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.hostnameCreates == nil {
		s.hostnameCreates = map[string]hostnameWindow{}
	}
	if len(s.hostnameCreates) >= 1024 {
		for key, window := range s.hostnameCreates {
			if now.Sub(window.start) >= time.Minute {
				delete(s.hostnameCreates, key)
			}
		}
	}
	window := s.hostnameCreates[app]
	if now.Sub(window.start) >= time.Minute {
		window = hostnameWindow{start: now}
	}
	if window.count >= hostnameCreatesPerMinute {
		return false, window.start.Add(time.Minute).Sub(now)
	}
	window.count++
	s.hostnameCreates[app] = window
	return true, 0
}

func (s *Server) createCustomHostname(w http.ResponseWriter, r *http.Request) {
	a, service, target, ok := s.hostnameApp(w, r, "hostnames:manage")
	if !ok {
		return
	}
	var in struct {
		Hostname string `json:"hostname"`
	}
	if !decode(w, r, &in) {
		return
	}
	if allowed, wait := s.allowHostnameCreate(a.ID, time.Now()); !allowed {
		w.Header().Set("Retry-After", strconv.Itoa(max(1, int(wait.Seconds()+0.999))))
		problem(w, 429, "rate_limit", "This application adds at most 120 custom hostnames per minute; retry after the indicated delay.")
		return
	}
	h, created, err := s.Store.CreateCustomHostname(r.Context(), who(r), a, service, strings.ToLower(strings.TrimSpace(in.Hostname)))
	if err != nil {
		failure(w, err)
		return
	}
	status := 200
	if created {
		status = 201
	}
	write(w, status, customHostnameView{h, target})
}

func (s *Server) listCustomHostnames(w http.ResponseWriter, r *http.Request) {
	a, service, target, ok := s.hostnameApp(w, r, "deployments:read")
	if !ok {
		return
	}
	q := r.URL.Query()
	status := q.Get("status")
	switch status {
	case "", store.HostnamePendingDNS, store.HostnameIssuing, store.HostnameActive, store.HostnameFailed, store.HostnameDeleting:
	default:
		problem(w, 400, "invalid_request", "status must be pending_dns, issuing, active, failed or deleting")
		return
	}
	limit := 100
	if raw := q.Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > 500 {
			problem(w, 400, "invalid_request", "limit must be between 1 and 500")
			return
		}
		limit = n
	}
	items, next, err := s.Store.CustomHostnames(r.Context(), a.ID, service, status, q.Get("cursor"), limit)
	if err != nil {
		failure(w, err)
		return
	}
	views := make([]customHostnameView, 0, len(items))
	for _, h := range items {
		views = append(views, customHostnameView{h, target})
	}
	write(w, 200, map[string]any{"items": views, "next_cursor": next, "target": target})
}

func (s *Server) getCustomHostname(w http.ResponseWriter, r *http.Request) {
	a, service, target, ok := s.hostnameApp(w, r, "deployments:read")
	if !ok {
		return
	}
	h, err := s.Store.CustomHostname(r.Context(), a.ID, service, r.PathValue("hostname"))
	if err != nil {
		failure(w, err)
		return
	}
	write(w, 200, customHostnameView{h, target})
}

func (s *Server) deleteCustomHostname(w http.ResponseWriter, r *http.Request) {
	a, service, target, ok := s.hostnameApp(w, r, "hostnames:manage")
	if !ok {
		return
	}
	h, err := s.Store.DeleteCustomHostname(r.Context(), who(r), a, service, r.PathValue("hostname"))
	if err != nil {
		failure(w, err)
		return
	}
	write(w, 202, customHostnameView{h, target})
}

type customHostnameSettingsView struct {
	store.CustomHostnameSettings
	Revision int64 `json:"revision"`
}

type customHostnameSettingsInput struct {
	store.CustomHostnameSettings
	ExpectedRevision int64 `json:"expected_revision"`
}

func (s *Server) getCustomHostnameSettings(w http.ResponseWriter, r *http.Request) {
	if !admin(w, r) {
		return
	}
	v, rev, err := s.Store.CustomHostnameSettings(r.Context())
	if err != nil {
		failure(w, err)
		return
	}
	write(w, 200, customHostnameSettingsView{v, rev})
}

func (s *Server) putCustomHostnameSettings(w http.ResponseWriter, r *http.Request) {
	if !admin(w, r) {
		return
	}
	var in customHostnameSettingsInput
	if !decode(w, r, &in) {
		return
	}
	if in.ExpectedRevision < 0 {
		problem(w, 400, "invalid_request", "expected_revision must be zero for first-time settings or the revision you loaded")
		return
	}
	rev, err := s.Store.PutCustomHostnameSettings(r.Context(), in.CustomHostnameSettings, in.ExpectedRevision)
	if err != nil {
		failure(w, err)
		return
	}
	// The settings are saved; an audit failure must not report them as lost.
	_, _ = s.Store.Pool.Exec(r.Context(), "INSERT INTO audit_events(identity_id,key_id,action,resource) VALUES($1,$2,'settings.custom_hostnames','installation')", who(r).ID, who(r).KeyID)
	write(w, 200, customHostnameSettingsView{in.CustomHostnameSettings, rev})
}
