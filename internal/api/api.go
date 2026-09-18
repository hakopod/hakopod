package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	contract "github.com/hakopod/hakopod/api"

	"github.com/hakopod/hakopod/internal/backup"
	"github.com/hakopod/hakopod/internal/cluster"
	"github.com/hakopod/hakopod/internal/serverlogs"
	"github.com/hakopod/hakopod/internal/spec"
	"github.com/hakopod/hakopod/internal/store"
	"github.com/jackc/pgx/v5"
)

type Server struct {
	// CloudControlPlane enables shared auth settings only in the trusted Cloud embedding.
	CloudControlPlane bool
	// OperatorRuntime is set only by the trusted Cloud embedding, never an HTTP request.
	// Customer runtimes keep installation administration disabled.
	BuildRegistry   string
	OperatorRuntime bool
	Store           *store.Store
	Cluster         *cluster.Client
	Auth            AuthConfig
	Backups         *backup.Service
	ProcessLogs     *serverlogs.Buffer
	// Overrides are only set by in-process tests, never by an API request.
	maintenanceHTTP       *http.Client
	githubHTTP            *http.Client
	gitlabHTTP            *http.Client
	gitlabAPIURL          string
	gitlabTestCredentials func(context.Context) (map[string][]byte, error)
	githubAPIURL          string
	githubTestCredentials func(context.Context) (map[string][]byte, error)
	domainLookupTXT       func(context.Context, string) ([]string, error)
	mu                    sync.Mutex
	buckets               map[string]bucket
	concurrent            chan struct{}
	streams               chan struct{}
	terminalMu            sync.Mutex
	terminals             map[string]*terminalSession
}
type bucket struct {
	at     time.Time
	tokens float64
}
type principalKey struct{}

func (s *Server) Handler() http.Handler {
	if s.Store != nil {
		s.Store.ManagedCloud = s.Auth.DeploymentMode == cluster.DeploymentManagedCloud
	}
	s.buckets = map[string]bucket{}
	s.concurrent = make(chan struct{}, 64)
	s.streams = make(chan struct{}, 16)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/build-registry/authorize", s.authorizeBuildRegistry)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { write(w, 200, map[string]string{"status": "ok"}) })
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if err := s.Store.Pool.Ping(ctx); err != nil {
			problem(w, 503, "unavailable", "PostgreSQL is unavailable")
			return
		}
		write(w, 200, map[string]string{"status": "ready"})
	})
	routes := http.NewServeMux()
	s.registerAuthRoutes(mux, routes)
	s.registerLicenseRoutes(routes)
	s.registerLoginProviderRoutes(routes)
	s.registerProfileRoutes(routes)
	s.registerDomainRoutes(routes)
	s.registerBackupRoutes(routes)
	s.registerAlarmRoutes(routes)
	s.registerSourceRoutes(mux, routes)
	s.registerSettingsRoutes(routes)
	s.registerInstallationRoutes(routes)
	s.registerWorkloadSecretRoutes(routes)
	s.registerSecretProviderRoutes(routes)
	s.registerTemplateRoutes(routes)
	s.registerProxyRoutes(routes)
	s.registerDeletionRoutes(routes)
	s.registerVirtualNetworkRoutes(routes)
	s.registerBuildRoutes(routes)
	s.registerRuntimeRoutes(routes)
	routes.HandleFunc("GET /api/v1/applications/{id}/services/{service}/certificates", s.backendCertificates)
	routes.HandleFunc("POST /api/v1/applications/{id}/services/{service}/certificates", s.uploadBackendCertificate)
	routes.HandleFunc("GET /api/v1/applications/{id}/services/{service}/delivery", s.serviceDelivery)
	s.registerTerminalRoutes(routes)
	routes.HandleFunc("PUT /api/v1/applications/{id}/name", s.renameApplication)
	routes.HandleFunc("PUT /api/v1/applications/{id}/services/{service}/name", s.renameApplication)
	routes.HandleFunc("PUT /api/v1/projects/{id}/name", s.renameProject)
	routes.HandleFunc("GET /api/v1/me", func(w http.ResponseWriter, r *http.Request) {
		p := who(r)
		p.CanManageGitConnections = p.CanManageGit()
		p.CanManageApplications = p.IsAdmin() || p.CanManageApplication(p.Project, p.Environment, "")
		// Compute capabilities before redacting the backing identity's admin
		// authority from a scoped key's public representation.
		p.Admin = p.IsAdmin()
		write(w, 200, p)
	})
	routes.HandleFunc("GET /api/v1/projects", s.projects)
	routes.HandleFunc("GET /api/v1/cloud/capabilities", s.cloudCapabilities)
	routes.HandleFunc("POST /api/v1/projects", s.createProject)
	routes.HandleFunc("POST /api/v1/projects/{project}/environments", s.createEnvironment)
	routes.HandleFunc("GET /api/v1/applications", s.applications)
	routes.HandleFunc("GET /api/v1/applications/{id}", s.application)
	s.registerPreviewRoutes(routes)
	routes.HandleFunc("POST /api/v1/plan", s.plan)
	routes.HandleFunc("POST /api/v1/compose/convert", s.convertCompose)
	routes.HandleFunc("POST /api/v1/deployments", s.deploy)
	routes.HandleFunc("GET /api/v1/deployments/{id}", s.deployment)
	routes.HandleFunc("GET /api/v1/idempotency/{key}", s.idempotentDeployment)
	routes.HandleFunc("GET /api/v1/deployments/{id}/events", s.events)
	routes.HandleFunc("POST /api/v1/deployments/{id}/cancel", s.cancel)
	routes.HandleFunc("POST /api/v1/applications/{id}/rollback", s.rollback)
	routes.HandleFunc("GET /api/v1/applications/{id}/logs", s.logs)
	routes.HandleFunc("POST /api/v1/applications/{id}/logs/query", s.queryLogs)
	routes.HandleFunc("GET /api/v1/nodes", s.nodes)
	routes.HandleFunc("GET /api/v1/keys", s.keys)
	routes.HandleFunc("POST /api/v1/keys", s.createKey)
	routes.HandleFunc("DELETE /api/v1/keys/{id}", s.revokeKey)
	routes.HandleFunc("POST /api/v1/keys/{id}/rotate", s.rotateKey)
	routes.HandleFunc("GET /api/v1/audit", s.audit)
	routes.HandleFunc("GET /api/v1/audit/history", s.auditHistory)
	routes.HandleFunc("GET /api/v1/audit/export", s.auditHistory)
	routes.HandleFunc("GET /api/v1/openapi.json", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write(contract.OpenAPI)
	})
	mux.Handle("/api/v1/", s.authenticate(routes))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Referrer-Policy", "no-referrer")
		select {
		case s.concurrent <- struct{}{}:
			defer func() { <-s.concurrent }()
		default:
			problem(w, 503, "busy", "request concurrency limit reached; retry with backoff")
			return
		}
		mux.ServeHTTP(w, r)
	})
}
func write(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func problem(w http.ResponseWriter, status int, code, message string) {
	write(w, status, map[string]any{"error": map[string]string{"code": code, "message": message}})
}
func failure(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrUnauthorized):
		problem(w, 401, "unauthorized", err.Error())
	case errors.Is(err, store.ErrForbidden):
		problem(w, 403, "forbidden", err.Error())
	case errors.Is(err, store.ErrConflict):
		problem(w, 409, "conflict", err.Error())
	case errors.Is(err, cluster.ErrPublicTCPDisabled):
		problem(w, 409, "public_tcp_disabled", err.Error())
	case errors.Is(err, store.ErrInput):
		problem(w, 400, "invalid_request", err.Error())
	case errors.Is(err, pgx.ErrNoRows):
		problem(w, 404, "not_found", "resource not found")
	default:
		problem(w, 503, "unavailable", "platform state is temporarily unavailable; retry with backoff")
	}
}
func who(r *http.Request) store.Principal { return r.Context().Value(principalKey{}).(store.Principal) }
func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 512<<10)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		problem(w, 400, "invalid_request", "invalid JSON or unknown field: "+err.Error())
		return false
	}
	if err := d.Decode(&struct{}{}); err != io.EOF {
		problem(w, 400, "invalid_request", "request must contain one JSON object")
		return false
	}
	return true
}
func (s *Server) rate(ip string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	b, ok := s.buckets[ip]
	if !ok {
		if len(s.buckets) >= 1024 {
			for k, v := range s.buckets {
				if now.Sub(v.at) > time.Minute {
					delete(s.buckets, k)
				}
			}
			if len(s.buckets) >= 1024 {
				return false
			}
		}
		b = bucket{at: now, tokens: 59}
		s.buckets[ip] = b
		return true
	}
	b.tokens = min(60, b.tokens+now.Sub(b.at).Seconds()*3)
	b.at = now
	allowed := b.tokens >= 1
	if allowed {
		b.tokens--
	}
	s.buckets[ip] = b
	return allowed
}
func (s *Server) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip, _, _ := net.SplitHostPort(r.RemoteAddr)
		if !s.rate(ip) {
			w.Header().Set("Retry-After", "2")
			problem(w, 429, "rate_limit", "request rate exceeded")
			return
		}
		raw, err := s.authenticateToken(r)
		if err != nil {
			failure(w, err)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		p, err := s.Store.Authenticate(ctx, raw)
		cancel()
		if err != nil {
			failure(w, err)
			return
		}
		if scope, ok := r.Context().Value(runtimeScopeKey{}).(RuntimeScope); ok {
			p, err = scopedRuntimePrincipal(p, scope)
			if err != nil {
				failure(w, err)
				return
			}
		}
		if s.Auth.DeploymentMode == cluster.DeploymentManagedCloud && (cloudInstallationPath(r.URL.Path) || (r.URL.Path == "/api/v1/license" && r.Method != "GET")) && !s.cloudOperator(p) {
			failure(w, store.ErrForbidden)
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), principalKey{}, p)))
	})
}
func admin(w http.ResponseWriter, r *http.Request) bool {
	if !who(r).IsAdmin() {
		failure(w, store.ErrForbidden)
		return false
	}
	return true
}
func scope(r *http.Request) (string, string) {
	p := who(r)
	project, env := r.URL.Query().Get("project"), r.URL.Query().Get("environment")
	if project == "" {
		project = p.Project
	}
	if env == "" {
		env = p.Environment
	}
	return project, env
}

var slug = regexp.MustCompile(`^[a-z][a-z0-9-]{0,38}[a-z0-9]$|^[a-z]$`)

func validScope(p, e string) bool { return slug.MatchString(p) && slug.MatchString(e) }
func (s *Server) projects(w http.ResponseWriter, r *http.Request) {
	p := who(r)
	visible := []string{}
	for _, candidate := range []string{p.Project, p.IdentityProject} {
		if candidate != "" {
			visible = append(visible, candidate)
		}
	}
	for _, role := range p.ProjectRoles {
		visible = append(visible, role.Project)
	}
	rows, err := s.Store.Pool.Query(r.Context(), "SELECT e.project,e.name,p.display_name,p.description,p.metadata_revision,EXISTS(SELECT 1 FROM personal_workspaces w WHERE w.project=e.project) FROM environments e JOIN projects p ON p.name=e.project WHERE ($1 OR e.project=ANY($2::text[])) AND ($3='' OR e.name=$3) ORDER BY e.project,e.name LIMIT 200", p.IsAdmin(), visible, p.Environment)
	if err != nil {
		failure(w, err)
		return
	}
	defer rows.Close()
	type env struct {
		Name string `json:"name"`
	}
	type project struct {
		MetadataRevision int64  `json:"metadata_revision"`
		ID               string `json:"id"`
		Name             string `json:"name"`
		DisplayName      string `json:"display_name"`
		Description      string `json:"description"`
		Personal         bool   `json:"personal"`
		Environments     []env  `json:"environments"`
	}
	out := []project{}
	for rows.Next() {
		var a, b, displayName, description string
		var personal bool
		var metadataRevision int64
		if err = rows.Scan(&a, &b, &displayName, &description, &metadataRevision, &personal); err != nil {
			failure(w, err)
			return
		}
		candidate := p
		candidate.Application = ""
		if !candidate.Allows("deployments:read", a, b, "") && !candidate.Allows("deployments:write", a, b, "") {
			continue
		}
		if len(out) == 0 || out[len(out)-1].Name != a {
			if displayName == "" {
				displayName = a
			}
			out = append(out, project{MetadataRevision: metadataRevision, ID: a, Name: a, DisplayName: displayName, Description: description, Personal: personal, Environments: []env{}})
		}
		out[len(out)-1].Environments = append(out[len(out)-1].Environments, env{Name: b})
	}
	if err = rows.Err(); err != nil {
		failure(w, err)
		return
	}
	write(w, 200, map[string]any{"items": out})
}
func (s *Server) createProject(w http.ResponseWriter, r *http.Request) {
	if !admin(w, r) {
		return
	}
	var in struct {
		Name        string  `json:"name"`
		Environment string  `json:"environment"`
		DisplayName *string `json:"display_name"`
		Description *string `json:"description"`
	}
	if !decode(w, r, &in) {
		return
	}
	if !validScope(in.Name, in.Environment) {
		problem(w, 400, "invalid_scope", "project/environment use 1–40 lowercase letters, digits and hyphens")
		return
	}
	retired, err := s.Store.RetiredName(r.Context(), "project", in.Name, "", in.Name)
	if err != nil {
		failure(w, err)
		return
	}
	if retired {
		problem(w, 409, "retired_project", "This project ID was deleted. Choose a new ID.")
		return
	}
	displayName, description, err := projectMetadata(in.Name, in.DisplayName, in.Description)
	if err != nil {
		problem(w, 400, "invalid_project", err.Error())
		return
	}
	tx, err := s.Store.Pool.Begin(r.Context())
	if err != nil {
		failure(w, err)
		return
	}
	defer tx.Rollback(r.Context())
	created, err := tx.Exec(r.Context(), "INSERT INTO projects(name,display_name,description) VALUES($1,$2,$3) ON CONFLICT DO NOTHING", in.Name, displayName, description)
	if err == nil && created.RowsAffected() == 0 && (in.DisplayName != nil || in.Description != nil) {
		problem(w, 409, "project_exists", "This project ID is already in use. Choose another ID.")
		return
	}
	if err == nil && created.RowsAffected() == 0 {
		err = tx.QueryRow(r.Context(), "SELECT COALESCE(NULLIF(display_name,''),name),description FROM projects WHERE name=$1", in.Name).Scan(&displayName, &description)
	}
	if err == nil {
		var locked string
		err = tx.QueryRow(r.Context(), "SELECT name FROM projects WHERE name=$1 FOR UPDATE", in.Name).Scan(&locked)
	}
	if err == nil {
		var count int
		var exists bool
		err = tx.QueryRow(r.Context(), "SELECT count(*),COALESCE(bool_or(name=$2),false) FROM environments WHERE project=$1", in.Name, in.Environment).Scan(&count, &exists)
		if err == nil && count >= 32 && !exists {
			problem(w, 400, "environment_limit", "A project supports at most 32 environments.")
			return
		}
	}
	if err == nil {
		_, err = tx.Exec(r.Context(), "INSERT INTO environments(project,name) VALUES($1,$2) ON CONFLICT DO NOTHING", in.Name, in.Environment)
	}
	if err == nil {
		_, err = tx.Exec(r.Context(), "INSERT INTO audit_events(identity_id,key_id,action,resource) VALUES($1,$2,'environment.create',$3)", who(r).ID, who(r).KeyID, in.Name+"/"+in.Environment)
	}
	if err == nil {
		err = tx.Commit(r.Context())
	}
	if err != nil {
		failure(w, err)
		return
	}
	write(w, 201, map[string]string{"name": in.Name, "environment": in.Environment, "display_name": displayName, "description": description})
}
func (s *Server) applications(w http.ResponseWriter, r *http.Request) {
	project, env := scope(r)
	if !validScope(project, env) {
		problem(w, 400, "missing_context", "specify project and environment")
		return
	}
	p := who(r)
	candidate := p
	candidate.Application = ""
	if !candidate.Allows("deployments:read", project, env, "") {
		failure(w, store.ErrForbidden)
		return
	}
	as, nextCursor, err := s.Store.ApplicationPage(r.Context(), project, env, r.URL.Query().Get("cursor"))
	if err != nil {
		failure(w, err)
		return
	}
	out := []store.Application{}
	for _, a := range as {
		if p.Allows("deployments:read", a.Project, a.Environment, a.Name) {
			out = append(out, a)
		}
	}
	write(w, 200, map[string]any{"items": out, "next_cursor": nextCursor})
}
func (s *Server) authorizedApp(w http.ResponseWriter, r *http.Request, id, permission string) (store.Application, bool) {
	a, err := s.Store.Application(r.Context(), id)
	if err != nil {
		failure(w, err)
		return a, false
	}
	if !who(r).Allows(permission, a.Project, a.Environment, a.Name) {
		failure(w, store.ErrForbidden)
		return a, false
	}
	return a, true
}
func (s *Server) application(w http.ResponseWriter, r *http.Request) {
	a, ok := s.authorizedApp(w, r, r.PathValue("id"), "deployments:read")
	if !ok {
		return
	}
	h, err := s.Store.History(r.Context(), a.ID)
	if err != nil {
		failure(w, err)
		return
	}
	a.Deployments = h
	write(w, 200, a)
}

type input struct {
	EnvFiles         map[string]string `json:"env_files,omitempty"`
	Project          string            `json:"project"`
	Environment      string            `json:"environment"`
	Spec             *spec.Application `json:"spec,omitempty"`
	TOML             string            `json:"toml,omitempty"`
	Service          string            `json:"service,omitempty"`
	Services         []string          `json:"services,omitempty"`
	ExpectedRevision *int64            `json:"expected_revision,omitempty"`
}

func (s *Server) prepare(w http.ResponseWriter, r *http.Request, in input, permission string) (spec.Application, *store.Application, bool) {
	if in.Services != nil && (in.Service != "" || len(in.Services) == 0 || len(in.Services) > 20) {
		problem(w, 400, "invalid_service", "provide either service or services; services must contain 1 to 20 unique service names")
		return spec.Application{}, nil, false
	}
	targets := make(map[string]bool)
	for _, name := range in.Services {
		if strings.TrimSpace(name) == "" || targets[name] {
			problem(w, 400, "invalid_service", "services must contain nonempty, unique service names")
			return spec.Application{}, nil, false
		}
		targets[name] = true
	}
	if in.Service != "" {
		targets[in.Service] = true
	}
	if !validScope(in.Project, in.Environment) {
		problem(w, 400, "missing_context", "project and environment are required")
		return spec.Application{}, nil, false
	}
	if (in.Spec == nil) == (in.TOML == "") {
		problem(w, 400, "invalid_spec", "provide exactly one of spec or toml")
		return spec.Application{}, nil, false
	}
	var next spec.Application
	var err error
	if in.Spec != nil {
		if len(in.EnvFiles) != 0 {
			problem(w, 400, "invalid_spec", "env_files require TOML with env_file references")
			return next, nil, false
		}
		next, err = spec.Normalize(*in.Spec)
	} else {
		next, err = s.importEnvironment(r.Context(), who(r), in.Project, in.Environment, "", []byte(in.TOML), in.EnvFiles)
	}
	if err != nil {
		if errors.Is(err, store.ErrForbidden) {
			failure(w, err)
			return next, nil, false
		}
		problem(w, 400, "invalid_spec", err.Error())
		return next, nil, false
	}
	if !who(r).Allows(permission, in.Project, in.Environment, next.Name) {
		failure(w, store.ErrForbidden)
		return next, nil, false
	}
	var existing *store.Application
	a, err := s.Store.FindApplication(r.Context(), in.Project, in.Environment, next.Name)
	if err == nil {
		existing = &a
	} else if !errors.Is(err, pgx.ErrNoRows) {
		failure(w, err)
		return next, nil, false
	}
	if existing == nil {
		retired, e := s.Store.RetiredName(r.Context(), "application", in.Project, in.Environment, next.Name)
		if e != nil {
			failure(w, e)
			return next, nil, false
		}
		if retired {
			problem(w, 409, "retired_application", "This application name was deleted. Choose a new name.")
			return next, nil, false
		}
		if len(next.Services) == 0 {
			problem(w, 400, "invalid_spec", "A new application needs at least one service.")
			return next, nil, false
		}
	}
	if len(targets) != 0 {
		if existing == nil {
			problem(w, 400, "invalid_service", "targeted deployment requires an existing application and a service present in the input")
			return next, nil, false
		}
		for name := range targets {
			if _, ok := next.Services[name]; !ok {
				problem(w, 400, "invalid_service", "every targeted service must be present in the input")
				return next, nil, false
			}
		}
		merged, cloneErr := spec.Normalize(existing.Spec)
		if cloneErr != nil {
			failure(w, cloneErr)
			return next, nil, false
		}
		if merged.InjectEnv != next.InjectEnv || !maps.Equal(merged.Env, next.Env) || !maps.Equal(merged.Secrets, next.Secrets) {
			problem(w, 400, "shared_configuration", "Application variables and secret defaults must be changed in an application-wide plan. Clear the service target and review the full configuration.")
			return next, nil, false
		}
		var previousResolved *spec.Application
		err = s.Store.Pool.QueryRow(r.Context(), "SELECT resolved_spec FROM deployments WHERE application_id=$1 AND revision=$2", existing.ID, existing.Revision).Scan(&previousResolved)
		if err != nil {
			failure(w, err)
			return next, nil, false
		}
		if previousResolved == nil {
			problem(w, 409, "unresolved_base", "wait for the current accepted release to resolve before a service-targeted update")
			return next, nil, false
		}
		for n, unchanged := range merged.Services {
			if !targets[n] {
				if prior, exists := previousResolved.Services[n]; exists {
					unchanged.Image = prior.Image
					unchanged.RegistryCredential = prior.RegistryCredential
					merged.Services[n] = unchanged
				}
			}
		}
		for name := range targets {
			svc := next.Services[name]
			merged.Services[name] = svc
			for _, network := range svc.Networks {
				if old, exists := merged.Networks[network]; !exists || old != next.Networks[network] {
					problem(w, 400, "shared_configuration", "Shared networks must be changed in an application-wide plan. Clear the service target and review the full TOML configuration.")
					return next, nil, false
				}
			}
			for _, mount := range svc.Mounts {
				if old, exists := merged.Volumes[mount.Volume]; !exists || old != next.Volumes[mount.Volume] {
					problem(w, 400, "shared_configuration", "Shared volumes must be changed in an application-wide plan. Clear the service target and review the full TOML configuration.")
					return next, nil, false
				}
			}
		}
		next, err = spec.Normalize(merged)
		if err != nil {
			problem(w, 400, "invalid_spec", err.Error())
			return next, nil, false
		}
	}
	if _, err := s.Store.ResolveVirtualNetworks(r.Context(), in.Project, in.Environment, next); err != nil {
		problem(w, 400, "invalid_network", err.Error())
		return next, existing, false
	}
	if !s.validateDeliveryPlan(w, r, in.Project, in.Environment, next, existing) {
		return next, existing, false
	}
	return next, existing, true
}
func (s *Server) plan(w http.ResponseWriter, r *http.Request) {
	var in input
	if !decode(w, r, &in) {
		return
	}
	next, current, ok := s.prepare(w, r, in, "deployments:write")
	if !ok {
		return
	}
	var previous *spec.Application
	var rev int64
	id := ""
	if current != nil {
		previous = &current.Spec
		rev = current.Revision
		id = current.ID
	}
	warnings := deliveryWarnings(r, next)
	if warnings == nil {
		warnings = []string{}
	}
	write(w, 200, map[string]any{"application_id": id, "expected_revision": rev, "spec": next, "changes": spec.Diff(previous, next), "warnings": warnings, "resource_profiles": spec.Profiles})
}
func (s *Server) deploy(w http.ResponseWriter, r *http.Request) {
	var in input
	if !decode(w, r, &in) {
		return
	}
	if in.ExpectedRevision == nil {
		problem(w, 400, "missing_revision", "expected_revision is required; review a plan first")
		return
	}
	next, _, ok := s.prepare(w, r, in, "deployments:write")
	if !ok {
		return
	}
	d, err := s.Store.Accept(r.Context(), who(r), in.Project, in.Environment, next, *in.ExpectedRevision, r.Header.Get("Idempotency-Key"))
	if err != nil {
		if errors.Is(err, store.ErrConflict) || errors.Is(err, store.ErrForbidden) {
			failure(w, err)
		} else {
			problem(w, 400, "invalid_deployment", err.Error())
		}
		return
	}
	write(w, 202, d)
}
func (s *Server) authorizedDeployment(w http.ResponseWriter, r *http.Request, permission string) (store.Deployment, store.Application, bool) {
	d, err := s.Store.Deployment(r.Context(), r.PathValue("id"))
	if err != nil {
		failure(w, err)
		return d, store.Application{}, false
	}
	a, ok := s.authorizedApp(w, r, d.ApplicationID, permission)
	return d, a, ok
}
func (s *Server) deployment(w http.ResponseWriter, r *http.Request) {
	d, _, ok := s.authorizedDeployment(w, r, "deployments:read")
	if !ok {
		return
	}
	events, err := s.Store.Events(r.Context(), d.ID)
	if err != nil {
		failure(w, err)
		return
	}
	d.Events = events
	write(w, 200, d)
}
func (s *Server) idempotentDeployment(w http.ResponseWriter, r *http.Request) {
	var id string
	if err := s.Store.Pool.QueryRow(r.Context(), "SELECT id FROM deployments WHERE identity_id=$1 AND idempotency_key=$2", who(r).ID, r.PathValue("key")).Scan(&id); err != nil {
		failure(w, err)
		return
	}
	d, err := s.Store.Deployment(r.Context(), id)
	if err != nil {
		failure(w, err)
		return
	}
	if _, ok := s.authorizedApp(w, r, d.ApplicationID, "deployments:write"); !ok {
		return
	}
	write(w, 200, d)
}
func (s *Server) rollback(w http.ResponseWriter, r *http.Request) {
	a, ok := s.authorizedApp(w, r, r.PathValue("id"), "deployments:write")
	if !ok {
		return
	}
	var in struct {
		Revision         int64  `json:"revision"`
		ExpectedRevision *int64 `json:"expected_revision"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.ExpectedRevision == nil {
		problem(w, 400, "missing_revision", "expected_revision is required")
		return
	}
	var resolved spec.Application
	err := s.Store.Pool.QueryRow(r.Context(), "SELECT resolved_spec FROM deployments WHERE application_id=$1 AND revision=$2 AND status='succeeded' AND resolved_spec IS NOT NULL", a.ID, in.Revision).Scan(&resolved)
	if err != nil {
		failure(w, err)
		return
	}
	d, err := s.Store.Accept(r.Context(), who(r), a.Project, a.Environment, resolved, *in.ExpectedRevision, r.Header.Get("Idempotency-Key"), resolved)
	if err != nil {
		failure(w, err)
		return
	}
	_ = s.Store.Event(r.Context(), d.ID, "rollback", fmt.Sprintf("Auditable rollback to successful revision %d; external data is not restored", in.Revision), "")
	write(w, 202, d)
}
func (s *Server) cancel(w http.ResponseWriter, r *http.Request) {
	d, _, ok := s.authorizedDeployment(w, r, "deployments:write")
	if !ok {
		return
	}
	if d.Status != "queued" && d.Status != "running" {
		problem(w, 409, "terminal_operation", "deployment has already completed")
		return
	}
	_, err := s.Store.Pool.Exec(r.Context(), "UPDATE deployments SET cancel_requested=true WHERE id=$1 AND status IN ('queued','running')", d.ID)
	if err != nil {
		failure(w, err)
		return
	}
	_ = s.Store.Event(r.Context(), d.ID, "cancellation_requested", "Cancellation stops new steps; already applied resources remain and can be rolled back", "")
	_, _ = s.Store.Pool.Exec(r.Context(), "INSERT INTO audit_events(identity_id,key_id,action,resource) VALUES($1,$2,'deployment.cancel',$3)", who(r).ID, who(r).KeyID, d.ID)
	write(w, 202, map[string]string{"id": d.ID, "status": "cancellation_requested"})
}
func (s *Server) nodes(w http.ResponseWriter, r *http.Request) {
	if !admin(w, r) {
		return
	}
	if s.Cluster == nil {
		problem(w, 503, "cluster_unavailable", "configure HAKOPOD_KUBECONFIG to inspect nodes")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	nodes, err := s.Cluster.Nodes(ctx)
	if err != nil {
		problem(w, 503, "cluster_unavailable", "Kubernetes node state is unavailable; verify cluster connectivity")
		return
	}
	write(w, 200, map[string]any{"items": nodes, "observed_at": time.Now().UTC()})
}
func (s *Server) keys(w http.ResponseWriter, r *http.Request) {
	if !admin(w, r) {
		return
	}
	keys, err := s.Store.Keys(r.Context())
	if err != nil {
		failure(w, err)
		return
	}
	write(w, 200, map[string]any{"items": keys})
}
func (s *Server) createKey(w http.ResponseWriter, r *http.Request) {
	if !admin(w, r) {
		return
	}
	var in store.KeyInput
	if !decode(w, r, &in) {
		return
	}
	key, raw, err := s.Store.CreateKey(r.Context(), who(r), in)
	if err != nil {
		problem(w, 400, "invalid_key", err.Error())
		return
	}
	write(w, 201, map[string]any{"key": raw, "metadata": key})
}
func (s *Server) revokeKey(w http.ResponseWriter, r *http.Request) {
	if !admin(w, r) {
		return
	}
	tx, err := s.Store.Pool.Begin(r.Context())
	if err != nil {
		failure(w, err)
		return
	}
	defer tx.Rollback(r.Context())
	tag, err := tx.Exec(r.Context(), "UPDATE api_keys SET revoked_at=COALESCE(revoked_at,now()) WHERE id=$1", r.PathValue("id"))
	if err != nil {
		failure(w, err)
		return
	}
	if tag.RowsAffected() == 0 {
		failure(w, pgx.ErrNoRows)
		return
	}
	_, err = tx.Exec(r.Context(), "INSERT INTO audit_events(identity_id,key_id,action,resource) VALUES($1,$2,'key.revoke',$3)", who(r).ID, who(r).KeyID, r.PathValue("id"))
	if err == nil {
		err = tx.Commit(r.Context())
	}
	if err != nil {
		failure(w, err)
		return
	}
	write(w, 200, map[string]string{"status": "revoked"})
}
func (s *Server) rotateKey(w http.ResponseWriter, r *http.Request) {
	if !admin(w, r) {
		return
	}
	var in struct {
		ExpiresAt time.Time `json:"expires_at"`
	}
	if !decode(w, r, &in) {
		return
	}
	key, raw, err := s.Store.RotateKey(r.Context(), who(r), r.PathValue("id"), in.ExpiresAt)
	if err != nil {
		problem(w, 400, "invalid_key", err.Error())
		return
	}
	write(w, 201, map[string]any{"key": raw, "metadata": key, "previous_key_expires_within_seconds": 900})
}
func (s *Server) audit(w http.ResponseWriter, r *http.Request) {
	if !admin(w, r) {
		return
	}
	rows, err := s.Store.Pool.Query(r.Context(), "SELECT id,identity_id,key_id,action,resource,time,metadata FROM audit_events ORDER BY id DESC LIMIT 100")
	if err != nil {
		failure(w, err)
		return
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var id int64
		var ident, key, action, res string
		var when time.Time
		var data json.RawMessage
		if err = rows.Scan(&id, &ident, &key, &action, &res, &when, &data); err != nil {
			failure(w, err)
			return
		}
		out = append(out, map[string]any{"id": id, "identity_id": ident, "key_id": key, "action": action, "resource": res, "time": when, "metadata": data})
	}
	write(w, 200, map[string]any{"items": out})
}
func (s *Server) streamSlot(w http.ResponseWriter) bool {
	select {
	case s.streams <- struct{}{}:
		return true
	default:
		problem(w, 429, "stream_limit", "maximum 16 concurrent streams; retry later")
		return false
	}
}
func (s *Server) logs(w http.ResponseWriter, r *http.Request) {
	a, ok := s.authorizedApp(w, r, r.PathValue("id"), "logs:read")
	if !ok {
		return
	}
	name := r.URL.Query().Get("service")
	if _, ok = a.Spec.Services[name]; !ok {
		problem(w, 400, "invalid_service", "select an application service")
		return
	}
	if s.Cluster == nil {
		problem(w, 503, "cluster_unavailable", "Kubernetes is unavailable")
		return
	}
	tail := int64(100)
	if value := r.URL.Query().Get("tail"); value != "" {
		n, err := strconv.ParseInt(value, 10, 64)
		if err != nil || n < 1 || n > 1000 {
			problem(w, 400, "invalid_tail", "tail must be between 1 and 1000")
			return
		}
		tail = n
	}
	if !s.streamSlot(w) {
		return
	}
	defer func() { <-s.streams }()
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Minute)
	defer cancel()
	go s.guardStream(ctx, cancel, who(r).KeyID, a, "logs:read")
	stream, err := s.Cluster.Logs(ctx, cluster.Namespace(a.ID), name, tail, r.URL.Query().Get("follow") == "true")
	if err != nil {
		problem(w, 503, "logs_unavailable", "No readable pod logs yet; inspect deployment events and retry")
		return
	}
	defer stream.Close()
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(200)
	controller := http.NewResponseController(w)
	defer controller.SetWriteDeadline(time.Time{})
	_ = controller.SetWriteDeadline(time.Now().Add(15 * time.Second))
	_ = controller.Flush()
	_ = controller.SetWriteDeadline(time.Time{})
	buf := make([]byte, 16<<10)
	for {
		n, err := stream.Read(buf)
		if n > 0 {
			_ = controller.SetWriteDeadline(time.Now().Add(15 * time.Second))
			if _, e := w.Write(buf[:n]); e != nil {
				return
			}
			_ = controller.Flush()
			_ = controller.SetWriteDeadline(time.Time{})
		}
		if err != nil {
			return
		}
	}
}
func (s *Server) guardStream(ctx context.Context, cancel context.CancelFunc, key string, a store.Application, permission string) {
	// A blocked pool acquisition must not stretch the revocation window. Poll
	// every three seconds and allow at most two seconds for each authority read.
	timer := time.NewTicker(3 * time.Second)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			check, done := context.WithTimeout(ctx, 2*time.Second)
			p, err := s.Store.KeyPrincipal(check, key)
			done()
			if err != nil || !p.Allows(permission, a.Project, a.Environment, a.Name) {
				cancel()
				return
			}
		}
	}
}
func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	d, a, ok := s.authorizedDeployment(w, r, "deployments:read")
	if !ok {
		return
	}
	if !s.streamSlot(w) {
		return
	}
	defer func() { <-s.streams }()
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Minute)
	defer cancel()
	go s.guardStream(ctx, cancel, who(r).KeyID, a, "deployments:read")
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("X-Accel-Buffering", "no")
	controller := http.NewResponseController(w)
	var last int64
	if v := r.Header.Get("Last-Event-ID"); v != "" {
		last, _ = strconv.ParseInt(v, 10, 64)
	}
	timer := time.NewTicker(2 * time.Second)
	defer timer.Stop()
	for {
		events, err := s.Store.Events(ctx, d.ID)
		if err != nil {
			return
		}
		for _, e := range events {
			if e.ID <= last {
				continue
			}
			_ = controller.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if _, err = fmt.Fprintf(w, "id: %d\nevent: deployment\ndata: %s\n\n", e.ID, store.JSON(e)); err != nil {
				return
			}
			last = e.ID
		}
		if _, err = fmt.Fprint(w, ": heartbeat\n\n"); err != nil {
			return
		}
		_ = controller.Flush()
		state, err := s.Store.Deployment(ctx, d.ID)
		if err != nil {
			return
		}
		if state.Status != "queued" && state.Status != "running" {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
	}
}
