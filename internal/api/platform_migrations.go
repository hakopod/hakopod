package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/hakopod/hakopod/internal/cluster"
	"github.com/hakopod/hakopod/internal/database"
	"github.com/hakopod/hakopod/internal/migrate"
	"github.com/hakopod/hakopod/internal/migrate/railway"
	"github.com/hakopod/hakopod/internal/migrate/vercel"
	"github.com/hakopod/hakopod/internal/store"
	"github.com/jackc/pgx/v5"
)

// Moving a Railway or Vercel project into a Hakopod application. The handlers
// read the source and save a reviewable proposal; RunPlatformMigrations does
// the work. The source token is sealed in the migration record and never
// returned, logged or audited.

func (s *Server) registerPlatformMigrationRoutes(routes *http.ServeMux) {
	routes.HandleFunc("POST /api/v1/migrations/projects", s.migrationSourceProjects)
	routes.HandleFunc("POST /api/v1/migrations", s.createPlatformMigration)
	routes.HandleFunc("GET /api/v1/migrations", s.platformMigrations)
	routes.HandleFunc("GET /api/v1/migrations/{id}", s.platformMigration)
	routes.HandleFunc("PUT /api/v1/migrations/{id}/variables", s.setPlatformMigrationVariables)
	routes.HandleFunc("POST /api/v1/migrations/{id}/start", s.startPlatformMigration)
	routes.HandleFunc("POST /api/v1/migrations/{id}/cancel", s.cancelPlatformMigration)
}

// migrationSource is what a provider reader offers.
type migrationSource interface {
	ListProjects(context.Context) ([]migrate.Project, error)
	Read(ctx context.Context, projectID, environmentID string) (migrate.Inventory, error)
}

func (s *Server) migrationSource(provider, token, teamID string) migrationSource {
	base := s.migrationSourceURLs[provider]
	switch provider {
	case "railway":
		var opts []railway.Option
		if base != "" {
			opts = append(opts, railway.WithBaseURL(base))
		}
		return railway.New(token, opts...)
	case "vercel":
		var opts []vercel.Option
		if base != "" {
			opts = append(opts, vercel.WithBaseURL(base))
		}
		if teamID != "" {
			opts = append(opts, vercel.WithTeam(teamID))
		}
		return vercel.New(token, opts...)
	}
	return nil
}

func providerTitle(provider string) string {
	if provider == "vercel" {
		return "Vercel"
	}
	return "Railway"
}

// sourceProblem reports a provider error without its text, which can quote
// source configuration.
func sourceProblem(w http.ResponseWriter, provider string, err error) {
	if errors.Is(err, railway.ErrUnauthorized) || errors.Is(err, vercel.ErrUnauthorized) {
		problem(w, 400, "source_unauthorized", providerTitle(provider)+" refused the token, or it cannot read this project. Check the token and its team or workspace.")
		return
	}
	problem(w, 502, "source_unavailable", providerTitle(provider)+" could not be read. Retry in a moment.")
}

func validMigrationToken(provider, token string) bool {
	return (provider == "railway" || provider == "vercel") && len(token) >= 8 && len(token) <= 4096 && !strings.ContainsAny(token, "\r\n\x00")
}

// canDeploySomewhere reports whether p may deploy in at least one project,
// the bar for reading a source token's project list.
func canDeploySomewhere(p store.Principal) bool {
	projects := []string{p.Project}
	if p.Project == "" {
		for _, role := range p.ProjectRoles {
			projects = append(projects, role.Project)
		}
	}
	for _, project := range projects {
		if p.Allows("deployments:write", project, p.Environment, p.Application) {
			return true
		}
	}
	return false
}

func (s *Server) migrationSourceProjects(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Provider string `json:"provider"`
		Token    string `json:"token"`
		TeamID   string `json:"team_id"`
	}
	if !decode(w, r, &in) {
		return
	}
	if !canDeploySomewhere(who(r)) {
		failure(w, store.ErrForbidden)
		return
	}
	if !s.rate("migration:" + who(r).ID) {
		w.Header().Set("Retry-After", "2")
		problem(w, 429, "rate_limit", "request rate exceeded")
		return
	}
	if !validMigrationToken(in.Provider, in.Token) || len(in.TeamID) > 128 {
		problem(w, 400, "invalid_request", "choose railway or vercel and paste a valid API token")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), time.Minute)
	defer cancel()
	projects, err := s.migrationSource(in.Provider, in.Token, in.TeamID).ListProjects(ctx)
	if err != nil {
		sourceProblem(w, in.Provider, err)
		return
	}
	if projects == nil {
		projects = []migrate.Project{}
	}
	write(w, 200, map[string]any{"projects": projects})
}

func (s *Server) createPlatformMigration(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Provider            string `json:"provider"`
		Token               string `json:"token"`
		SourceProjectID     string `json:"source_project_id"`
		SourceEnvironmentID string `json:"source_environment_id"`
		SourceTeamID        string `json:"source_team_id"`
		Project             string `json:"project"`
		Environment         string `json:"environment"`
		ApplicationName     string `json:"application_name"`
		GitConnectionID     string `json:"git_connection_id"`
	}
	if !decode(w, r, &in) {
		return
	}
	p := who(r)
	if !validScope(in.Project, in.Environment) {
		problem(w, 400, "missing_context", "Choose a project and environment")
		return
	}
	if !slug.MatchString(in.ApplicationName) {
		problem(w, 400, "invalid_request", "application_name: use lowercase letters, numbers and hyphens, starting with a letter")
		return
	}
	if !p.Allows("deployments:write", in.Project, in.Environment, in.ApplicationName) {
		failure(w, store.ErrForbidden)
		return
	}
	if !validMigrationToken(in.Provider, in.Token) || in.SourceProjectID == "" || len(in.SourceProjectID) > 128 || len(in.SourceEnvironmentID) > 128 || len(in.SourceTeamID) > 128 || len(in.GitConnectionID) > 128 {
		problem(w, 400, "invalid_request", "choose railway or vercel, paste a valid API token and pick a source project")
		return
	}
	key := s.authEncryptionKey()
	if len(key) != 32 {
		problem(w, 503, "unavailable", "Configure the persistent authentication encryption key before migrating a project.")
		return
	}
	if !s.rate("migration:" + p.ID) {
		w.Header().Set("Retry-After", "2")
		problem(w, 429, "rate_limit", "request rate exceeded")
		return
	}
	// The name check compose import uses, plus retired names as POST /plan.
	if _, err := s.Store.FindApplication(r.Context(), in.Project, in.Environment, in.ApplicationName); err == nil {
		problem(w, 409, "application_exists", "An application with this name already exists. Choose another name.")
		return
	} else if !errors.Is(err, pgx.ErrNoRows) {
		failure(w, err)
		return
	}
	if retired, err := s.Store.RetiredName(r.Context(), "application", in.Project, in.Environment, in.ApplicationName); err != nil {
		failure(w, err)
		return
	} else if retired {
		problem(w, 409, "retired_application", "This application name was deleted. Choose a new name.")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	inv, err := s.migrationSource(in.Provider, in.Token, in.SourceTeamID).Read(ctx, in.SourceProjectID, in.SourceEnvironmentID)
	if err != nil {
		sourceProblem(w, in.Provider, err)
		return
	}
	proposal, err := migrate.Convert(inv, migrate.ConvertOptions{Project: in.Project, Environment: in.Environment, Application: in.ApplicationName, GitConnectionID: in.GitConnectionID, SecretKey: key})
	if err != nil {
		problem(w, 400, "invalid_source", err.Error())
		return
	}
	// The worker creates databases and builds as this caller; refuse now what
	// it would be refused later.
	if len(proposal.Databases) > 0 && !p.AllowsDatabase(in.Project, in.Environment, true) {
		problem(w, 403, "forbidden", "Creating this project's databases needs deploy permission for the whole environment.")
		return
	}
	if len(proposal.Builds) > 0 {
		if err = s.validateGitConnection(r.Context(), "github", in.GitConnectionID); err != nil {
			failure(w, err)
			return
		}
		if in.GitConnectionID != "" && in.GitConnectionID != defaultGitConnection("github") && !p.CanManageGit() {
			problem(w, 403, "repository_approval_required", "An administrator must approve a named connection repository for these builds")
			return
		}
	}
	m := store.PlatformMigration{ID: store.NewID(), IdentityID: p.ID, Project: in.Project, Environment: in.Environment, ApplicationName: in.ApplicationName, Provider: in.Provider,
		SourceProjectID: in.SourceProjectID, SourceProjectName: inv.ProjectName, SourceEnvironmentID: in.SourceEnvironmentID, SourceTeamID: in.SourceTeamID, Proposal: store.JSON(proposal)}
	sealed, err := migrate.Seal(key, m.ID, "token", []byte(in.Token))
	if err != nil {
		failure(w, err)
		return
	}
	m, err = s.Store.CreatePlatformMigration(r.Context(), m, sealed, p.Application)
	if err != nil {
		failure(w, err)
		return
	}
	_, _ = s.Store.Pool.Exec(r.Context(), "INSERT INTO audit_events(identity_id,key_id,action,resource,metadata) VALUES($1,$2,'migration.plan',$3,$4)", p.ID, p.KeyID, m.ID,
		store.JSON(map[string]string{"provider": in.Provider, "source_project_id": in.SourceProjectID, "source_project_name": inv.ProjectName, "application": in.ApplicationName}))
	write(w, 201, m)
}

func (s *Server) platformMigrations(w http.ResponseWriter, r *http.Request) {
	project, environment := scope(r)
	if !validScope(project, environment) {
		problem(w, 400, "missing_context", "Choose a project and environment")
		return
	}
	items, err := s.Store.PlatformMigrations(r.Context(), project, environment)
	if err != nil {
		failure(w, err)
		return
	}
	out := []store.PlatformMigration{}
	for _, m := range items {
		if who(r).Allows("deployments:read", m.Project, m.Environment, m.ApplicationName) {
			out = append(out, m)
		}
	}
	write(w, 200, map[string]any{"items": out})
}

func (s *Server) authorizedPlatformMigration(w http.ResponseWriter, r *http.Request, permission string) (store.PlatformMigration, bool) {
	m, err := s.Store.PlatformMigration(r.Context(), r.PathValue("id"))
	if err != nil {
		failure(w, err)
		return m, false
	}
	if !who(r).Allows(permission, m.Project, m.Environment, m.ApplicationName) {
		// Like a missing record, so IDs in other scopes are not confirmed.
		failure(w, pgx.ErrNoRows)
		return m, false
	}
	return m, true
}

func (s *Server) platformMigration(w http.ResponseWriter, r *http.Request) {
	if m, ok := s.authorizedPlatformMigration(w, r, "deployments:read"); ok {
		write(w, 200, m)
	}
}

// migrationValues opens the values the user supplied for unreadable variables.
func migrationValues(key []byte, id string, sealed []byte) (map[string]map[string]string, error) {
	values := map[string]map[string]string{}
	if len(sealed) == 0 {
		return values, nil
	}
	raw, err := migrate.Open(key, id, "variables", sealed)
	if err != nil {
		return nil, err
	}
	return values, json.Unmarshal(raw, &values)
}

func (s *Server) setPlatformMigrationVariables(w http.ResponseWriter, r *http.Request) {
	m, ok := s.authorizedPlatformMigration(w, r, "deployments:write")
	if !ok {
		return
	}
	var in struct {
		Values map[string]map[string]string `json:"values"`
	}
	if !decode(w, r, &in) {
		return
	}
	key := s.authEncryptionKey()
	if len(key) != 32 {
		problem(w, 503, "unavailable", "Configure the persistent authentication encryption key before migrating a project.")
		return
	}
	if m.Status != "planned" || !m.HasToken {
		problem(w, 409, "conflict", "Variables can only be supplied while the migration is planned.")
		return
	}
	tokenSealed, variablesSealed, err := s.Store.PlatformMigrationSecrets(r.Context(), m.ID)
	if err != nil {
		failure(w, err)
		return
	}
	values, err := migrationValues(key, m.ID, variablesSealed)
	if err != nil {
		problem(w, 503, "unavailable", err.Error())
		return
	}
	for service, vars := range in.Values {
		for name, value := range vars {
			if len(value) > 65536 || strings.ContainsRune(value, 0) {
				problem(w, 400, "invalid_request", "values must be at most 64 KiB without NUL bytes")
				return
			}
			if values[service] == nil {
				values[service] = map[string]string{}
			}
			values[service][name] = value
		}
	}
	token, err := migrate.Open(key, m.ID, "token", tokenSealed)
	if err != nil {
		problem(w, 503, "unavailable", err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	inv, err := s.migrationSource(m.Provider, string(token), m.SourceTeamID).Read(ctx, m.SourceProjectID, m.SourceEnvironmentID)
	if err != nil {
		sourceProblem(w, m.Provider, err)
		return
	}
	var stored migrate.Proposal
	if err = json.Unmarshal(m.Proposal, &stored); err != nil {
		failure(w, err)
		return
	}
	proposal, err := migrate.Convert(inv, migrate.ConvertOptions{Project: m.Project, Environment: m.Environment, Application: m.ApplicationName, GitConnectionID: migrationGitConnection(stored), Values: values, SecretKey: key})
	if err != nil {
		problem(w, 400, "invalid_source", err.Error())
		return
	}
	sealed, err := migrate.Seal(key, m.ID, "variables", store.JSON(values))
	if err != nil {
		failure(w, err)
		return
	}
	m, err = s.Store.UpdatePlatformMigrationPlan(r.Context(), m.ID, store.JSON(proposal), sealed)
	if err != nil {
		failure(w, err)
		return
	}
	write(w, 200, m)
}

// migrationGitConnection is the connection the plan's builds were made with.
func migrationGitConnection(p migrate.Proposal) string {
	if len(p.Builds) > 0 {
		return p.Builds[0].ConnectionID
	}
	return ""
}

func (s *Server) startPlatformMigration(w http.ResponseWriter, r *http.Request) {
	m, ok := s.authorizedPlatformMigration(w, r, "deployments:write")
	if !ok {
		return
	}
	idem := r.Header.Get("Idempotency-Key")
	if len(idem) < 8 || len(idem) > 128 {
		problem(w, 400, "invalid_request", "an Idempotency-Key of 8 to 128 characters is required")
		return
	}
	var proposal migrate.Proposal
	if err := json.Unmarshal(m.Proposal, &proposal); err != nil {
		failure(w, err)
		return
	}
	if len(proposal.Missing) > 0 {
		names := make([]string, 0, len(proposal.Missing))
		for _, v := range proposal.Missing {
			names = append(names, v.Service+"/"+v.Name)
		}
		problem(w, 409, "missing_variables", providerTitle(m.Provider)+" does not return these variables, so supply their values first: "+strings.Join(names, ", "))
		return
	}
	queued, err := s.Store.QueuePlatformMigration(r.Context(), m.ID, idem)
	if errors.Is(err, store.ErrConflict) {
		problem(w, 409, "conflict", "This migration was already started with another Idempotency-Key, or it has ended.")
		return
	}
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			problem(w, 409, "conflict", "This migration can no longer be started. Plan it again.")
			return
		}
		failure(w, err)
		return
	}
	if m.Status == "planned" {
		_, _ = s.Store.Pool.Exec(r.Context(), "INSERT INTO audit_events(identity_id,key_id,action,resource,metadata) VALUES($1,$2,'migration.start',$3,$4)", who(r).ID, who(r).KeyID, m.ID,
			store.JSON(map[string]string{"provider": m.Provider, "source_project_id": m.SourceProjectID, "application": m.ApplicationName}))
	}
	write(w, 200, queued)
}

func (s *Server) cancelPlatformMigration(w http.ResponseWriter, r *http.Request) {
	m, ok := s.authorizedPlatformMigration(w, r, "deployments:write")
	if !ok {
		return
	}
	m, err := s.Store.CancelPlatformMigration(r.Context(), m.ID)
	if errors.Is(err, store.ErrConflict) {
		problem(w, 409, "conflict", "This migration has already ended.")
		return
	}
	if err != nil {
		failure(w, err)
		return
	}
	// A running copy holds the source credentials; stop it now rather than at
	// the worker's next heartbeat. The worker then finishes as cancelled.
	if rt := s.migrationRuntime(); rt != nil && m.Status == "running" {
		for _, id := range runningMigrationCopies(m.Steps) {
			ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
			_ = rt.CancelDatabaseCopy(ctx, cluster.CopyID(id))
			cancel()
		}
	}
	_, _ = s.Store.Pool.Exec(r.Context(), "INSERT INTO audit_events(identity_id,key_id,action,resource) VALUES($1,$2,'migration.cancel',$3)", who(r).ID, who(r).KeyID, m.ID)
	write(w, 200, m)
}

// migrationRuntime is the part of the cluster the migration worker uses.
// Tests replace it; in production it is the Kubernetes client.
type migrationRuntime interface {
	PutWorkloadSecret(ctx context.Context, project, environment, application, name, value string) error
	DatabaseControllerAvailable(context.Context, database.Spec) error
	ValidateDatabasePlacement(context.Context, database.Resource) error
	ObserveDatabase(context.Context, database.Resource) (database.Observation, error)
	DatabaseEmpty(context.Context, database.Resource, database.Observation) error
	StartDatabaseCopy(context.Context, cluster.CopyRequest) (cluster.CopyID, error)
	DatabaseCopyStatus(context.Context, cluster.CopyID) (cluster.CopyStatus, error)
	CancelDatabaseCopy(context.Context, cluster.CopyID) error
	CleanupDatabaseCopies(ctx context.Context, databaseID string) error
	Nodes(context.Context) ([]cluster.Node, error)
}

func (s *Server) migrationRuntime() migrationRuntime {
	if s.migrationTestRuntime != nil {
		return s.migrationTestRuntime
	}
	if s.Cluster == nil {
		return nil
	}
	return s.Cluster
}
