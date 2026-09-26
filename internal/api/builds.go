package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strings"
	"time"

	"github.com/hakopod/hakopod/internal/framework"
	"github.com/hakopod/hakopod/internal/spec"
	"github.com/hakopod/hakopod/internal/store"
	"github.com/jackc/pgx/v5"
)

type buildConfig struct {
	ReuseServices      []string                   `json:"reuse_services,omitempty"`
	Secrets            *map[string]spec.SecretRef `json:"secrets,omitempty"`
	ManagedRegistry    string                     `json:"managed_registry,omitempty"`
	Env                *map[string]string         `json:"env,omitempty"`
	Command            *[]string                  `json:"command,omitempty"`
	Args               *[]string                  `json:"args,omitempty"`
	Framework          *framework.Plan            `json:"framework,omitempty"`
	BuildSecrets       map[string]string          `json:"build_secrets,omitempty"`
	BuildArgs          map[string]string          `json:"build_args,omitempty"`
	Provider           string                     `json:"provider"`
	ConnectionID       string                     `json:"connection_id"`
	Architecture       string                     `json:"architecture"`
	AutoBuild          bool                       `json:"auto_build"`
	AutoDeploy         bool                       `json:"auto_deploy"`
	GrantID            string                     `json:"-"`
	ID                 string                     `json:"id"`
	ApplicationID      string                     `json:"application_id,omitempty"`
	Project            string                     `json:"project"`
	Environment        string                     `json:"environment"`
	Name               string                     `json:"name"`
	Service            string                     `json:"service"`
	Repository         string                     `json:"repository"`
	Branch             string                     `json:"branch"`
	Mode               string                     `json:"mode"`
	Preset             string                     `json:"preset"`
	ContextPath        string                     `json:"context_path"`
	Dockerfile         string                     `json:"dockerfile"`
	RegistryCredential string                     `json:"registry_credential,omitempty"`
	Port               int                        `json:"port"`
	Public             bool                       `json:"public"`
	Submodules         bool                       `json:"submodules,omitempty"`
	Size               string                     `json:"size"`
	Revision           int64                      `json:"revision"`
	InstalledRevision  int64                      `json:"installed_revision"`
	InstalledCommit    string                     `json:"installed_commit"`
}
type buildInput struct {
	ReuseServices          []string                   `json:"reuse_services,omitempty"`
	Secrets                *map[string]spec.SecretRef `json:"secrets,omitempty"`
	Env                    *map[string]string         `json:"env,omitempty"`
	Command                *[]string                  `json:"command,omitempty"`
	Args                   *[]string                  `json:"args,omitempty"`
	Framework              *framework.Plan            `json:"framework,omitempty"`
	Submodules             *bool                      `json:"submodules,omitempty"`
	BuildSecrets           map[string]string          `json:"build_secrets,omitempty"`
	BuildArgs              map[string]string          `json:"build_args,omitempty"`
	Provider               string                     `json:"provider"`
	ConnectionID           string                     `json:"connection_id"`
	Architecture           string                     `json:"architecture"`
	AutoBuild              bool                       `json:"auto_build"`
	AutoDeploy             bool                       `json:"auto_deploy"`
	ApplicationID          string                     `json:"application_id"`
	Project                string                     `json:"project"`
	Environment            string                     `json:"environment"`
	Name                   string                     `json:"name"`
	Service                string                     `json:"service"`
	Repository             string                     `json:"repository"`
	Branch                 string                     `json:"branch"`
	Mode                   string                     `json:"mode"`
	Preset                 string                     `json:"preset"`
	ContextPath            string                     `json:"context_path"`
	Dockerfile             string                     `json:"dockerfile"`
	RegistryCredential     string                     `json:"registry_credential"`
	Port                   int                        `json:"port"`
	Public                 bool                       `json:"public"`
	Size                   string                     `json:"size"`
	ExpectedConfigRevision *int64                     `json:"expected_config_revision"`
}
type buildRun struct {
	Provider       string      `json:"provider"`
	RemoteRunID    int64       `json:"remote_run_id"`
	Automatic      bool        `json:"automatic"`
	AutoStatus     string      `json:"auto_status"`
	ID             string      `json:"id"`
	BuildID        string      `json:"build_id"`
	ConfigRevision int64       `json:"config_revision"`
	CommitSHA      string      `json:"commit_sha"`
	Status         string      `json:"status"`
	GitHubRunID    int64       `json:"github_run_id"`
	Conclusion     string      `json:"conclusion"`
	Image          string      `json:"image"`
	RunURL         string      `json:"run_url"`
	Message        string      `json:"message"`
	DeploymentID   string      `json:"deployment_id"`
	CreatedAt      time.Time   `json:"created_at"`
	UpdatedAt      time.Time   `json:"updated_at"`
	Config         buildConfig `json:"-"`
}

const buildRunColumns = "id,build_id,config_revision,commit_sha,status,github_run_id,conclusion,image,run_url,message,deployment_id,created_at,updated_at,config,automatic,auto_status"

func scanBuildRun(row pgx.Row) (buildRun, error) {
	var b buildRun
	err := row.Scan(&b.ID, &b.BuildID, &b.ConfigRevision, &b.CommitSHA, &b.Status, &b.RemoteRunID, &b.Conclusion, &b.Image, &b.RunURL, &b.Message, &b.DeploymentID, &b.CreatedAt, &b.UpdatedAt, &b.Config, &b.Automatic, &b.AutoStatus)
	if b.Config.Provider == "" {
		b.Config.Provider = "github"
	}
	b.Config.ConnectionID = selectedGitConnection(b.Config.Provider, b.Config.ConnectionID)
	b.Provider = b.Config.Provider
	if b.Provider == "github" {
		b.GitHubRunID = b.RemoteRunID
	}
	return b, err
}
func (s *Server) registerBuildRoutes(routes *http.ServeMux) {
	routes.HandleFunc("POST /api/v1/builds/detect", s.detectBuild)
	routes.HandleFunc("GET /api/v1/builds", s.listBuilds)
	routes.HandleFunc("POST /api/v1/builds", s.createBuild)
	routes.HandleFunc("GET /api/v1/builds/{id}", s.getBuild)
	routes.HandleFunc("PUT /api/v1/builds/{id}", s.updateBuild)
	routes.HandleFunc("POST /api/v1/builds/{id}/preview", s.previewBuild)
	routes.HandleFunc("POST /api/v1/builds/{id}/install", s.installBuild)
	routes.HandleFunc("POST /api/v1/builds/{id}/run", s.runBuild)
	routes.HandleFunc("GET /api/v1/builds/{id}/runs", s.listBuildRuns)
	routes.HandleFunc("GET /api/v1/builds/{id}/runs/{run}", s.observeBuildRun)
	routes.HandleFunc("POST /api/v1/builds/{id}/runs/{run}/plan", s.planBuildRun)
	routes.HandleFunc("POST /api/v1/builds/{id}/runs/{run}/deploy", s.deployBuildRun)
	routes.HandleFunc("POST /api/v1/builds/{id}/runs/{run}/cancel", s.cancelBuildRun)
}

var buildIdentifier = regexp.MustCompile(`^[a-f0-9]{32}$`)
var buildPathPattern = regexp.MustCompile(`^[A-Za-z0-9_.\-/]+$`)

func validBuildPath(value string) bool {
	return len(value) > 0 && len(value) <= 200 && buildPathPattern.MatchString(value) && path.Clean(value) == value && !strings.HasPrefix(value, "/") && value != ".." && !strings.HasPrefix(value, "../")
}
func normalizeBuild(in buildInput) (buildConfig, error) {
	c := buildConfig{ReuseServices: append([]string(nil), in.ReuseServices...), Secrets: in.Secrets, Env: in.Env, Command: in.Command, Args: in.Args, Framework: in.Framework, BuildSecrets: in.BuildSecrets, BuildArgs: in.BuildArgs, ConnectionID: selectedGitConnection(in.Provider, in.ConnectionID), Architecture: in.Architecture, AutoBuild: in.AutoBuild, AutoDeploy: in.AutoDeploy, ApplicationID: in.ApplicationID, Project: in.Project, Environment: in.Environment, Name: in.Name, Service: in.Service, Repository: in.Repository, Branch: in.Branch, Mode: in.Mode, Preset: in.Preset, ContextPath: in.ContextPath, Dockerfile: in.Dockerfile, RegistryCredential: in.RegistryCredential, Port: in.Port, Public: in.Public, Size: in.Size}
	// Only an update carries an expected revision, so an absent field means a new
	// build (submodules on) rather than an existing build that never had it.
	if in.Submodules != nil {
		c.Submodules = *in.Submodules
	} else {
		c.Submodules = in.ExpectedConfigRevision == nil
	}
	if len(c.ReuseServices) > 19 || len(c.ReuseServices) > 0 && c.ApplicationID == "" {
		return c, fmt.Errorf("%w: reuse_services: image reuse needs an existing application and at most 19 additional services", store.ErrInput)
	}
	if c.Service == "" {
		c.Service = "web"
	}
	seen := map[string]bool{c.Service: true}
	for _, name := range c.ReuseServices {
		if !slug.MatchString(name) || seen[name] {
			return c, fmt.Errorf("%w: reuse_services: choose unique additional services in this application", store.ErrInput)
		}
		seen[name] = true
	}
	c.Repository = normalizeSourceRepository(c.Repository)
	c.Provider = in.Provider
	if c.Provider == "" {
		c.Provider = "github"
	}
	if c.Branch == "" {
		c.Branch = "main"
	}
	if c.Mode == "" {
		c.Mode = "dockerfile"
	}
	if c.Preset == "" {
		c.Preset = "auto"
	}
	if c.ContextPath == "" {
		c.ContextPath = "."
	}
	if c.Dockerfile == "" {
		c.Dockerfile = path.Join(c.ContextPath, "Dockerfile")
	}
	if c.Size == "" {
		c.Size = "small"
	}
	if c.Port == 0 {
		c.Port = 8080
	}
	checks := []struct {
		valid   bool
		message string
	}{
		{validScope(c.Project, c.Environment), "Select a valid project and environment"},
		{slug.MatchString(c.Name), "name: Application name must start with a lowercase letter, use only lowercase letters, numbers and hyphens, end with a letter or number, and contain at most 40 characters"},
		{slug.MatchString(c.Service), "service: Service name must use lowercase letters, numbers and hyphens, start with a letter, end with a letter or number, and contain at most 40 characters"},
		{validSourceRepository(c.Provider, c.Repository), "repository: Enter a valid owner/repository (GitLab also accepts nested groups)"},
		{len(c.Branch) <= 200 && !strings.ContainsAny(c.Branch, "\r\n\x00 ?#"), "branch: Enter a valid source branch without spaces or URL query characters"},
		{validBuildPath(c.ContextPath), "context_path: Build context must use a relative path inside the repository"},
		{validBuildPath(c.Dockerfile), "dockerfile: Dockerfile must use a relative path inside the repository"},
		{c.Port >= 1 && c.Port <= 65535, "port: Service port must be between 1 and 65535"},
		{len(c.RegistryCredential) <= 100, "registry_credential: Select a valid registry credential"},
	}
	for _, check := range checks {
		if !check.valid {
			return c, fmt.Errorf("%w: %s", store.ErrInput, check.message)
		}
	}

	var command, args []string
	if c.Command != nil {
		command = *c.Command
	}
	if c.Args != nil {
		args = *c.Args
	}
	if err := spec.ValidateCommand(command, args); err != nil {
		return c, fmt.Errorf("%w: %s", store.ErrInput, err)
	}
	if c.Env != nil || c.Secrets != nil {
		runtimeService := spec.Service{Image: "busybox:stable"}
		if c.Env != nil {
			runtimeService.Env = *c.Env
		}
		if c.Secrets != nil {
			runtimeService.Secrets = *c.Secrets
		}
		_, err := spec.Normalize(spec.Application{Name: "runtime", Services: map[string]spec.Service{c.Service: runtimeService}})
		if err != nil {
			return c, fmt.Errorf("%w: %s", store.ErrInput, err)
		}
	}
	if err := spec.ValidateBuildArguments(c.BuildArgs); err != nil {
		return c, fmt.Errorf("%w: %s", store.ErrInput, err)
	}
	if c.Architecture != "" && c.Architecture != "amd64" && c.Architecture != "arm64" {
		return c, fmt.Errorf("%w: architecture: architecture must be amd64 or arm64", store.ErrInput)
	}
	if c.AutoDeploy && !c.AutoBuild {
		return c, fmt.Errorf("%w: automatic deployment requires automatic builds", store.ErrInput)
	}
	if c.Mode != "dockerfile" && c.Mode != "buildpacks" && c.Mode != "framework" {
		return c, fmt.Errorf("%w: mode: mode must be dockerfile, buildpacks or framework", store.ErrInput)
	}
	if err := framework.ValidateSecrets(c.BuildSecrets); err != nil {
		return c, fmt.Errorf("%w: build_secrets: %s", store.ErrInput, err)
	}
	if c.Mode == "buildpacks" && len(c.BuildSecrets) > 0 {
		return c, fmt.Errorf("%w: build_secrets: BuildKit secrets require a Dockerfile or framework build", store.ErrInput)
	}
	if c.Mode == "framework" {
		if c.Framework == nil {
			return c, fmt.Errorf("%w: framework: review a framework build plan", store.ErrInput)
		}
		if err := framework.Validate(*c.Framework); err != nil {
			return c, fmt.Errorf("%w: %s", store.ErrInput, err)
		}
		c.Port = c.Framework.Port
	} else if c.Framework != nil {
		return c, fmt.Errorf("%w: framework: framework settings require framework mode", store.ErrInput)
	}
	switch c.Preset {
	case "auto", "nodejs", "python", "go", "java", "dotnet", "ruby", "static":
	default:
		return c, fmt.Errorf("%w: preset: unsupported buildpack preset", store.ErrInput)
	}
	if _, ok := spec.Profiles[c.Size]; !ok {
		return c, fmt.Errorf("%w: size: unsupported service size", store.ErrInput)
	}
	return c, nil
}
func (s *Server) readBuild(ctx context.Context, id string) (buildConfig, error) {
	var c buildConfig
	err := s.Store.Pool.QueryRow(ctx, "SELECT config,revision,installed_revision,installed_commit,COALESCE(application_id,''),grant_id FROM build_configs WHERE id=$1", id).Scan(&c, &c.Revision, &c.InstalledRevision, &c.InstalledCommit, &c.ApplicationID, &c.GrantID)
	if c.Provider == "" {
		c.Provider = "github"
	}
	c.ConnectionID = selectedGitConnection(c.Provider, c.ConnectionID)
	return c, err
}
func (s *Server) authorizedBuild(w http.ResponseWriter, r *http.Request, permission string) (buildConfig, bool) {
	c, err := s.readBuild(r.Context(), r.PathValue("id"))
	if err != nil {
		authFailure(w, err)
		return c, false
	}
	if !who(r).Allows(permission, c.Project, c.Environment, c.Name) {
		authFailure(w, store.ErrForbidden)
		return c, false
	}
	return c, true
}
func (s *Server) listBuilds(w http.ResponseWriter, r *http.Request) {
	project, environment := scope(r)
	if !validScope(project, environment) {
		authFailure(w, store.ErrInput)
		return
	}
	if !who(r).Allows("deployments:read", project, environment, who(r).Application) {
		authFailure(w, store.ErrForbidden)
		return
	}
	rows, err := s.Store.Pool.Query(r.Context(), "SELECT config,revision,installed_revision,installed_commit,COALESCE(application_id,''),grant_id FROM build_configs WHERE project=$1 AND environment=$2 ORDER BY created_at DESC LIMIT 100", project, environment)
	if err != nil {
		authFailure(w, err)
		return
	}
	defer rows.Close()
	out := []buildConfig{}
	for rows.Next() {
		var c buildConfig
		if err = rows.Scan(&c, &c.Revision, &c.InstalledRevision, &c.InstalledCommit, &c.ApplicationID, &c.GrantID); err != nil {
			authFailure(w, err)
			return
		}
		if c.Provider == "" {
			c.Provider = "github"
		}
		c.ConnectionID = selectedGitConnection(c.Provider, c.ConnectionID)
		if who(r).Allows("deployments:read", project, environment, c.Name) {
			out = append(out, c)
		}
	}
	if err = rows.Err(); err != nil {
		authFailure(w, err)
		return
	}
	write(w, 200, map[string]any{"items": out})
}
func (s *Server) getBuild(w http.ResponseWriter, r *http.Request) {
	c, ok := s.authorizedBuild(w, r, "deployments:read")
	if ok {
		write(w, 200, c)
	}
}
func (s *Server) validateBuildApplication(ctx context.Context, c buildConfig) error {
	if c.ApplicationID == "" {
		return nil
	}
	a, err := s.Store.Application(ctx, c.ApplicationID)
	if err != nil {
		return err
	}
	if a.Project != c.Project || a.Environment != c.Environment || a.Name != c.Name {
		return store.ErrForbidden
	}
	if _, ok := a.Spec.Services[c.Service]; !ok {
		return fmt.Errorf("%w: service does not exist in the linked application", store.ErrInput)
	}
	for _, name := range c.ReuseServices {
		if _, ok := a.Spec.Services[name]; !ok {
			return fmt.Errorf("%w: an image reuse service does not exist in the linked application", store.ErrInput)
		}
	}
	return nil
}
func (s *Server) createBuild(w http.ResponseWriter, r *http.Request) {
	var in buildInput
	if !decode(w, r, &in) {
		return
	}
	c, err := normalizeBuild(in)
	if err != nil {
		authFailure(w, err)
		return
	}
	if err = s.resolveBuildArchitecture(r.Context(), &c); err != nil {
		authFailure(w, err)
		return
	}
	if !who(r).Allows("deployments:write", c.Project, c.Environment, c.Name) {
		authFailure(w, store.ErrForbidden)
		return
	}
	if err = s.validateGitConnection(r.Context(), c.Provider, c.ConnectionID); err != nil {
		authFailure(w, err)
		return
	}
	if c.ConnectionID != defaultGitConnection(c.Provider) && !who(r).CanManageGit() {
		problem(w, 403, "repository_approval_required", "An administrator must approve a named connection repository for this build")
		return
	}
	if err = s.validateBuildApplication(r.Context(), c); err != nil {
		authFailure(w, err)
		return
	}
	c.ID = store.NewID()
	c.Revision = 1
	if err = s.assignBuildRegistry(r.Context(), &c); err != nil {
		authFailure(w, err)
		return
	}
	tx, err := s.Store.Pool.Begin(r.Context())
	if err != nil {
		authFailure(w, err)
		return
	}
	defer tx.Rollback(r.Context())
	var env string
	if err = tx.QueryRow(r.Context(), "SELECT name FROM environments WHERE project=$1 AND name=$2 FOR UPDATE", c.Project, c.Environment).Scan(&env); err != nil {
		authFailure(w, err)
		return
	}
	var count int
	if err = tx.QueryRow(r.Context(), "SELECT count(*) FROM build_configs WHERE project=$1 AND environment=$2", c.Project, c.Environment).Scan(&count); err != nil {
		authFailure(w, err)
		return
	}
	if count >= 100 {
		authFailure(w, store.ErrBusy)
		return
	}
	c.GrantID, err = s.Store.NewSourceGrant(r.Context(), tx, who(r), c.Project, c.Environment, c.Name)
	if err != nil {
		authFailure(w, err)
		return
	}
	if _, err = tx.Exec(r.Context(), "INSERT INTO build_configs(id,project,environment,name,service,application_id,config,created_by,grant_id) VALUES($1,$2,$3,$4,$5,NULLIF($6,''),$7,$8,$9)", c.ID, c.Project, c.Environment, c.Name, c.Service, c.ApplicationID, store.JSON(c), who(r).ID, c.GrantID); err != nil {
		problem(w, 409, "build_conflict", "a build already exists for this application and service")
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		authFailure(w, err)
		return
	}
	write(w, 201, c)
}
func (s *Server) updateBuild(w http.ResponseWriter, r *http.Request) {
	old, ok := s.authorizedBuild(w, r, "deployments:write")
	if !ok {
		return
	}
	var in buildInput
	if !decode(w, r, &in) {
		return
	}
	if in.ExpectedConfigRevision == nil || *in.ExpectedConfigRevision != old.Revision {
		authFailure(w, store.ErrConflict)
		return
	}
	c, err := normalizeBuild(in)
	if err != nil {
		authFailure(w, err)
		return
	}
	if err = s.resolveBuildArchitecture(r.Context(), &c); err != nil {
		authFailure(w, err)
		return
	}
	if c.Project != old.Project || c.Environment != old.Environment || c.Name != old.Name || c.Service != old.Service || c.ApplicationID != old.ApplicationID {
		problem(w, 400, "immutable_build_target", "project, environment, application name, service and linked application cannot change")
		return
	}
	if err = s.validateGitConnection(r.Context(), c.Provider, c.ConnectionID); err != nil {
		authFailure(w, err)
		return
	}
	if (c.ConnectionID != defaultGitConnection(c.Provider) || old.ConnectionID != defaultGitConnection(old.Provider)) && (c.ConnectionID != old.ConnectionID || c.Repository != old.Repository || c.Provider != old.Provider) && !who(r).CanManageGit() {
		problem(w, 403, "repository_approval_required", "An administrator must approve changing this build repository or connection")
		return
	}
	if err = s.validateBuildApplication(r.Context(), c); err != nil {
		authFailure(w, err)
		return
	}
	c.ID = old.ID
	c.Revision = old.Revision + 1
	if err = s.assignBuildRegistry(r.Context(), &c); err != nil {
		authFailure(w, err)
		return
	}
	tx, err := s.Store.Pool.Begin(r.Context())
	if err != nil {
		authFailure(w, err)
		return
	}
	defer tx.Rollback(r.Context())
	c.GrantID, err = s.Store.NewSourceGrant(r.Context(), tx, who(r), c.Project, c.Environment, c.Name)
	if err != nil {
		authFailure(w, err)
		return
	}
	result, err := tx.Exec(r.Context(), "UPDATE build_configs SET config=$2,revision=revision+1,installed_revision=0,installed_commit='',grant_id=$4,updated_at=now() WHERE id=$1 AND revision=$3", c.ID, store.JSON(c), old.Revision, c.GrantID)
	if err != nil {
		authFailure(w, err)
		return
	}
	if result.RowsAffected() != 1 {
		authFailure(w, store.ErrConflict)
		return
	}
	if _, err = tx.Exec(r.Context(), "UPDATE api_keys SET revoked_at=now() WHERE id=$1", old.GrantID); err != nil {
		authFailure(w, err)
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		authFailure(w, err)
		return
	}
	write(w, 200, c)
}

const submoduleRequirement = "Submodules are checked out recursively; a private submodule in another repository will fail at checkout because the CI token only reaches this repository"

func (s *Server) previewBuild(w http.ResponseWriter, r *http.Request) {
	c, ok := s.authorizedBuild(w, r, "deployments:write")
	if !ok {
		return
	}
	if c.Provider == "gitlab" {
		gitlab := []string{"GitLab.com CI/CD and Container Registry enabled for this project", "GitLab integration token with API access and repository commit/pipeline permissions", "One Hakopod-managed .gitlab-ci.yml per repository; existing unowned CI or another build's entrypoint will not be overwritten", "A platform administrator explicitly installs the reviewed CI file on the default branch", "Automatic builds also require this exact CI file on the selected source branch and authenticated Pipeline Hook events", "GitLab-hosted Linux runner capacity for the selected architecture and Docker-in-Docker", "Private registry.gitlab.com images require a persistent read_registry pull credential before deployment"}
		if c.Submodules {
			gitlab = append(gitlab, submoduleRequirement)
		}
		write(w, 200, map[string]any{"config": c, "workflow_path": c.workflowPath(), "workflow": buildWorkflow(c), "image_repository": c.imageName(), "requirements": gitlab})
		return
	}
	requirements := []string{"GitHub Actions enabled for this repository", "GitHub App access with repository contents/workflows write and Actions write permissions", "A repository manager explicitly installs this reviewed workflow on the repository default branch", "Automatic builds also require this exact workflow on the selected source branch when it differs from the repository default branch", "GitHub-hosted Linux runner capacity for the selected architecture"}
	if c.Submodules {
		requirements = append(requirements, submoduleRequirement)
	}
	if c.ManagedRegistry != "" {
		requirements = append(requirements, "Hakopod supplies private registry storage and scoped worker pull credentials automatically", "This workflow requests a short-lived GitHub Actions identity token to authorize image publishing")
	} else {
		requirements = append(requirements, "GHCR package publishing permission", "For private GHCR images, configure a persistent read:packages registry credential before deployment")
	}
	write(w, 200, map[string]any{"config": c, "workflow_path": c.workflowPath(), "workflow": buildWorkflow(c), "image_repository": c.imageName(), "requirements": requirements})
}
func (s *Server) githubBuildRequest(ctx context.Context, method, endpoint string, body any, connections ...string) (*http.Response, error) {
	level := "read"
	if method != "GET" {
		level = "write"
	}
	permissions := map[string]string{"contents": level}
	if strings.Contains(endpoint, "/actions/") {
		permissions = map[string]string{"actions": level}
	} else if method != "GET" && strings.Contains(endpoint, "/.github/workflows/") {
		permissions["workflows"] = "write"
	}
	data, err := s.connectionCredentials(ctx, "github", selectedGitConnection("github", connections...), githubEndpointRepository(endpoint), permissions)
	if err != nil {
		return nil, err
	}
	if len(data["token"]) == 0 {
		return nil, fmt.Errorf("configure a GitHub integration token before installing or running builds")
	}
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(store.JSON(body))
	}
	base := "https://api.github.com"
	if s.githubAPIURL != "" {
		base = s.githubAPIURL
	}
	request, err := http.NewRequestWithContext(ctx, method, base+endpoint, reader)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+string(data["token"]))
	request.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	request.Header.Set("User-Agent", "hakopod")
	client := s.githubHTTP
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("GitHub request did not complete; check its observed state before retrying")
	}
	return response, nil
}

type githubContents struct {
	SHA      string `json:"sha"`
	Content  string `json:"content"`
	Encoding string `json:"encoding"`
}

func (s *Server) workflowContents(ctx context.Context, c buildConfig, ref string) (githubContents, int, error) {
	response, err := s.githubBuildRequest(ctx, "GET", "/repos/"+c.Repository+"/contents/"+c.workflowPath()+"?ref="+url.QueryEscape(ref), nil, c.ConnectionID)
	if err != nil {
		return githubContents{}, 0, err
	}
	defer response.Body.Close()
	var content githubContents
	if response.StatusCode == 200 {
		err = json.NewDecoder(io.LimitReader(response.Body, 128<<10)).Decode(&content)
	}
	return content, response.StatusCode, err
}
func decodeWorkflow(c githubContents) (string, error) {
	if c.Encoding != "base64" {
		return "", errors.New("unsupported workflow content encoding")
	}
	data, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(c.Content, "\n", ""))
	if err != nil || len(data) > 64<<10 {
		return "", errors.New("invalid workflow content")
	}
	return string(data), nil
}
func (s *Server) installBuild(w http.ResponseWriter, r *http.Request) {
	c, ok := s.authorizedBuild(w, r, "deployments:write")
	if !ok {
		return
	}
	if !gitInteractive(r) && !(who(r).CredentialType == "cli" && who(r).CanManageGit()) {
		authFailure(w, store.ErrForbidden)
		return
	}
	var in struct {
		ExpectedConfigRevision *int64 `json:"expected_config_revision"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.ExpectedConfigRevision == nil || *in.ExpectedConfigRevision != c.Revision {
		authFailure(w, store.ErrConflict)
		return
	}
	if c.Provider == "gitlab" {
		s.installGitLabBuild(w, r, c)
		return
	}
	var repo struct {
		DefaultBranch string `json:"default_branch"`
	}
	if err := s.githubGET(r.Context(), "/repos/"+c.Repository, &repo, c.ConnectionID); err != nil {
		problem(w, 409, "github_unavailable", err.Error())
		return
	}
	current, status, err := s.workflowContents(r.Context(), c, repo.DefaultBranch)
	if err != nil {
		problem(w, 409, "github_unavailable", err.Error())
		return
	}
	if status != 200 && status != 404 {
		problem(w, 409, "workflow_access", "GitHub denied reading the workflow; verify repository permissions")
		return
	}
	if status == 200 {
		old, err := decodeWorkflow(current)
		if err != nil || !strings.HasPrefix(old, "# Managed by Hakopod build "+c.ID+".") {
			problem(w, 409, "unowned_workflow", "the target workflow exists without this build's ownership marker")
			return
		}
	}
	body := map[string]any{"message": "Configure Hakopod build " + c.Name + "/" + c.Service, "branch": repo.DefaultBranch, "content": base64.StdEncoding.EncodeToString([]byte(buildWorkflow(c)))}
	if current.SHA != "" {
		body["sha"] = current.SHA
	}
	response, err := s.githubBuildRequest(r.Context(), "PUT", "/repos/"+c.Repository+"/contents/"+c.workflowPath(), body, c.ConnectionID)
	if err != nil {
		problem(w, 503, "install_unknown", err.Error())
		return
	}
	defer response.Body.Close()
	if response.StatusCode != 200 && response.StatusCode != 201 {
		problem(w, 409, "install_failed", fmt.Sprintf("GitHub returned HTTP %d; verify contents/workflows write access and branch protection", response.StatusCode))
		return
	}
	var out struct {
		Commit struct {
			SHA string `json:"sha"`
		} `json:"commit"`
	}
	if err = json.NewDecoder(io.LimitReader(response.Body, 128<<10)).Decode(&out); err != nil || !commitPattern.MatchString(out.Commit.SHA) {
		problem(w, 503, "install_unknown", "GitHub accepted the write but its commit response was invalid; inspect the repository")
		return
	}
	result, err := s.Store.Pool.Exec(r.Context(), "UPDATE build_configs SET installed_revision=$2,installed_commit=$3,updated_at=now() WHERE id=$1 AND revision=$2", c.ID, c.Revision, out.Commit.SHA)
	if err != nil {
		authFailure(w, err)
		return
	}
	if result.RowsAffected() != 1 {
		problem(w, 409, "build_conflict", "configuration changed during installation; review and install the current version")
		return
	}
	_, _ = s.Store.Pool.Exec(r.Context(), "INSERT INTO audit_events(identity_id,key_id,action,resource) VALUES($1,$2,'build.workflow.install',$3)", who(r).ID, who(r).KeyID, c.ID)
	c.InstalledRevision = c.Revision
	c.InstalledCommit = out.Commit.SHA
	write(w, 200, map[string]any{"config": c, "commit_sha": out.Commit.SHA, "workflow_branch": repo.DefaultBranch, "workflow_path": c.workflowPath()})
}
func (s *Server) runBuild(w http.ResponseWriter, r *http.Request) {
	c, ok := s.authorizedBuild(w, r, "deployments:write")
	if !ok {
		return
	}
	var in struct {
		ExpectedConfigRevision *int64 `json:"expected_config_revision"`
		Commit                 string `json:"commit"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.ExpectedConfigRevision == nil || *in.ExpectedConfigRevision != c.Revision || c.InstalledRevision != c.Revision {
		problem(w, 409, "workflow_not_installed", "review and install the current build configuration before starting a build")
		return
	}
	idem := r.Header.Get("Idempotency-Key")
	if len(idem) < 8 || len(idem) > 128 {
		authFailure(w, fmt.Errorf("%w: Idempotency-Key must contain 8–128 characters", store.ErrInput))
		return
	}
	hash := sha256.Sum256(store.JSON(map[string]any{"revision": c.Revision, "commit": in.Commit}))
	var oldHash []byte
	existing, err := scanBuildRun(s.Store.Pool.QueryRow(r.Context(), "SELECT "+buildRunColumns+" FROM build_runs WHERE build_id=$1 AND identity_id=$2 AND idempotency_key=$3", c.ID, who(r).ID, idem))
	if err == nil {
		if err = s.Store.Pool.QueryRow(r.Context(), "SELECT request_hash FROM build_runs WHERE id=$1", existing.ID).Scan(&oldHash); err != nil {
			authFailure(w, err)
			return
		}
		if !bytes.Equal(hash[:], oldHash) {
			authFailure(w, store.ErrConflict)
			return
		}
		write(w, 202, existing)
		return
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		authFailure(w, err)
		return
	}
	ref := in.Commit
	if ref == "" {
		ref = c.Branch
	} else if !commitPattern.MatchString(ref) {
		authFailure(w, store.ErrInput)
		return
	}
	var commit struct {
		SHA string `json:"sha"`
	}
	var repo struct {
		DefaultBranch string `json:"default_branch"`
	}
	if c.Provider == "gitlab" {
		commit.SHA, repo.DefaultBranch, err = s.prepareGitLabBuildDispatch(r.Context(), c, ref)
		if err != nil {
			problem(w, 409, "source_unavailable", err.Error())
			return
		}
	} else {
		if err = s.githubGET(r.Context(), "/repos/"+c.Repository+"/commits/"+url.PathEscape(ref), &commit, c.ConnectionID); err != nil {
			problem(w, 409, "source_unavailable", err.Error())
			return
		}
		if !commitPattern.MatchString(commit.SHA) {
			problem(w, 503, "invalid_source", "GitHub returned an invalid commit identifier")
			return
		}
		if err = s.githubGET(r.Context(), "/repos/"+c.Repository, &repo, c.ConnectionID); err != nil {
			problem(w, 409, "source_unavailable", err.Error())
			return
		}
		content, status, err := s.workflowContents(r.Context(), c, repo.DefaultBranch)
		text, decodeErr := decodeWorkflow(content)
		if err != nil || status != 200 || decodeErr != nil || text != buildWorkflow(c) {
			problem(w, 409, "workflow_changed", "the installed workflow changed; review and install the current generated workflow")
			return
		}
	}
	tx, err := s.Store.Pool.Begin(r.Context())
	if err != nil {
		authFailure(w, err)
		return
	}
	defer tx.Rollback(r.Context())
	var current int64
	if err = tx.QueryRow(r.Context(), "SELECT revision FROM build_configs WHERE id=$1 FOR UPDATE", c.ID).Scan(&current); err != nil {
		authFailure(w, err)
		return
	}
	if current != c.Revision {
		authFailure(w, store.ErrConflict)
		return
	}
	var active int
	if err = tx.QueryRow(r.Context(), "SELECT count(*) FROM build_runs WHERE build_id=$1 AND status NOT IN ('completed','failed','cancelled') AND created_at>now()-interval '1 hour'", c.ID).Scan(&active); err != nil {
		authFailure(w, err)
		return
	}
	if active >= 3 {
		problem(w, 429, "build_limit", "at most three recent builds may be active for this service")
		return
	}
	id := store.NewID()
	_, err = tx.Exec(r.Context(), "INSERT INTO build_runs(id,build_id,identity_id,key_id,idempotency_key,request_hash,config,config_revision,commit_sha) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9) ON CONFLICT(build_id,identity_id,idempotency_key) DO NOTHING", id, c.ID, who(r).ID, who(r).KeyID, idem, hash[:], store.JSON(c), c.Revision, commit.SHA)
	if err != nil {
		authFailure(w, err)
		return
	}
	var actualID string
	if err = tx.QueryRow(r.Context(), "SELECT id FROM build_runs WHERE build_id=$1 AND identity_id=$2 AND idempotency_key=$3", c.ID, who(r).ID, idem).Scan(&actualID); err != nil {
		authFailure(w, err)
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		authFailure(w, err)
		return
	}
	if actualID != id {
		if err = s.Store.Pool.QueryRow(r.Context(), "SELECT request_hash FROM build_runs WHERE id=$1", actualID).Scan(&oldHash); err != nil {
			authFailure(w, err)
			return
		}
		if !bytes.Equal(oldHash, hash[:]) {
			authFailure(w, store.ErrConflict)
			return
		}
		existing, err = scanBuildRun(s.Store.Pool.QueryRow(r.Context(), "SELECT "+buildRunColumns+" FROM build_runs WHERE id=$1", actualID))
		if err != nil {
			authFailure(w, err)
			return
		}
		write(w, 202, existing)
		return
	}
	state, message := "queued", "Waiting for GitHub Actions to create the run"
	var remoteID int64
	if c.Provider == "gitlab" {
		state, message, remoteID = s.dispatchGitLabBuild(r.Context(), c, repo.DefaultBranch, commit.SHA, id)
	} else {
		response, err := s.githubBuildRequest(r.Context(), "POST", "/repos/"+c.Repository+"/actions/workflows/"+url.PathEscape(path.Base(c.workflowPath()))+"/dispatches", map[string]any{"ref": repo.DefaultBranch, "inputs": map[string]string{"commit": commit.SHA, "request_id": id}}, c.ConnectionID)
		if err != nil {
			state = "dispatch_unknown"
			message = "Dispatch response was interrupted. Refresh to locate the remote run before retrying."
		} else {
			response.Body.Close()
			if response.StatusCode != 204 {
				state = "failed"
				message = fmt.Sprintf("GitHub denied workflow dispatch (HTTP %d); check Actions permissions and workflow installation", response.StatusCode)
			}
		}
	}
	_, err = s.Store.Pool.Exec(r.Context(), "UPDATE build_runs SET status=$2,message=$3,github_run_id=$4,updated_at=now() WHERE id=$1", id, state, message, remoteID)
	if err != nil {
		authFailure(w, err)
		return
	}
	run, err := scanBuildRun(s.Store.Pool.QueryRow(r.Context(), "SELECT "+buildRunColumns+" FROM build_runs WHERE id=$1", id))
	if err != nil {
		authFailure(w, err)
		return
	}
	write(w, 202, run)
}
func (s *Server) listBuildRuns(w http.ResponseWriter, r *http.Request) {
	c, ok := s.authorizedBuild(w, r, "deployments:read")
	if !ok {
		return
	}
	rows, err := s.Store.Pool.Query(r.Context(), "SELECT "+buildRunColumns+" FROM build_runs WHERE build_id=$1 ORDER BY created_at DESC LIMIT 20", c.ID)
	if err != nil {
		authFailure(w, err)
		return
	}
	defer rows.Close()
	out := []buildRun{}
	for rows.Next() {
		v, err := scanBuildRun(rows)
		if err != nil {
			authFailure(w, err)
			return
		}
		out = append(out, v)
	}
	if err = rows.Err(); err != nil {
		authFailure(w, err)
		return
	}
	write(w, 200, map[string]any{"items": out})
}

func (s *Server) resolveBuildArchitecture(ctx context.Context, c *buildConfig) error {
	if c.Architecture != "" {
		return nil
	}
	if s.Cluster == nil {
		return fmt.Errorf("%w: select amd64 or arm64 because cluster architecture is unavailable", store.ErrInput)
	}
	nodes, err := s.Cluster.Nodes(ctx)
	if err != nil {
		return fmt.Errorf("%w: select an explicit build architecture; node inspection is unavailable", store.ErrInput)
	}
	architecture := ""
	for _, node := range nodes {
		if node.Architecture == "" {
			continue
		}
		if architecture != "" && architecture != node.Architecture {
			return fmt.Errorf("%w: this cluster has mixed architectures; select amd64 or arm64", store.ErrInput)
		}
		architecture = node.Architecture
	}
	if architecture != "amd64" && architecture != "arm64" {
		return fmt.Errorf("%w: select a supported build architecture", store.ErrInput)
	}
	c.Architecture = architecture
	return nil
}
