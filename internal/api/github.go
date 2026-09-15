package api

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strings"
	"time"

	"github.com/hakopod/hakopod/internal/spec"
	"github.com/hakopod/hakopod/internal/store"
	"github.com/jackc/pgx/v5"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
)

type sourceBinding struct {
	Provider       string    `json:"provider"`
	ConnectionID   string    `json:"connection_id"`
	ApplicationID  string    `json:"application_id"`
	Repository     string    `json:"repository"`
	Branch         string    `json:"branch"`
	Path           string    `json:"path"`
	AutoDeploy     bool      `json:"auto_deploy"`
	GrantID        string    `json:"-"`
	Revision       int64     `json:"revision"`
	LastCommit     string    `json:"last_commit"`
	LastDeployment string    `json:"last_deployment"`
	LastError      string    `json:"last_error"`
	UpdatedAt      time.Time `json:"updated_at"`
}

var repositoryPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,99}/[A-Za-z0-9][A-Za-z0-9_.-]{0,99}$`)
var commitPattern = regexp.MustCompile(`^[a-f0-9]{40,64}$`)

const sourceColumns = "application_id,provider,repository,branch,path,auto_deploy,grant_id,revision,last_commit,last_deployment,last_error,updated_at,connection_id"

func (s *Server) registerSourceRoutes(public, protected *http.ServeMux) {
	s.registerSourceImportRoutes(protected)
	s.registerGitConnectionRoutes(public, protected)
	s.registerGitLabRoutes(public, protected)
	protected.HandleFunc("GET /api/v1/integrations/github", s.githubStatus)
	protected.HandleFunc("PUT /api/v1/integrations/github", s.configureGitHub)
	protected.HandleFunc("GET /api/v1/applications/{id}/source", s.getSource)
	protected.HandleFunc("PUT /api/v1/applications/{id}/source", s.setSource)
	protected.HandleFunc("POST /api/v1/applications/{id}/source/plan", s.planSource)
	protected.HandleFunc("POST /api/v1/applications/{id}/source/deploy", s.deploySource)
	public.HandleFunc("POST /api/v1/webhooks/github", s.githubWebhook)
}

func (s *Server) githubCredentials(ctx context.Context) (map[string][]byte, error) {
	if s.githubTestCredentials != nil {
		return s.githubTestCredentials(ctx)
	}
	if s.Cluster == nil {
		return nil, fmt.Errorf("Kubernetes credential storage is unavailable")
	}
	secret, err := s.Cluster.GetPlatformSecret(ctx, "github-connection")
	if apierrors.IsNotFound(err) {
		return map[string][]byte{}, nil
	}
	if err != nil {
		return nil, err
	}
	return secret.Data, nil
}
func (s *Server) githubStatus(w http.ResponseWriter, r *http.Request) {
	if !admin(w, r) {
		return
	}
	data, err := s.githubCredentials(r.Context())
	if err != nil {
		failure(w, err)
		return
	}
	write(w, 200, map[string]any{"configured": len(data["webhook-secret"]) > 0, "token_configured": len(data["token"]) > 0, "webhook_path": "/api/v1/webhooks/github", "private_repositories": len(data["token"]) > 0})
}
func (s *Server) configureGitHub(w http.ResponseWriter, r *http.Request) {
	if !admin(w, r) {
		return
	}
	var in struct {
		Token         string `json:"token"`
		WebhookSecret string `json:"webhook_secret"`
	}
	if !decode(w, r, &in) {
		return
	}
	if len(in.Token) > 1024 || len(in.WebhookSecret) > 256 || strings.ContainsAny(in.Token, "\r\n") {
		problem(w, 400, "invalid_credentials", "GitHub credentials exceed supported bounds")
		return
	}
	data, err := s.githubCredentials(r.Context())
	if err != nil {
		failure(w, err)
		return
	}
	if in.Token != "" {
		data["token"] = []byte(in.Token)
	}
	once := ""
	if in.WebhookSecret != "" {
		if len(in.WebhookSecret) < 32 {
			problem(w, 400, "weak_webhook_secret", "webhook secret must contain at least 32 characters")
			return
		}
		data["webhook-secret"] = []byte(in.WebhookSecret)
	} else if len(data["webhook-secret"]) == 0 {
		b := make([]byte, 32)
		if _, err = rand.Read(b); err != nil {
			failure(w, err)
			return
		}
		once = hex.EncodeToString(b)
		data["webhook-secret"] = []byte(once)
	}
	if err = s.Cluster.PutPlatformSecret(r.Context(), "github-connection", corev1.SecretTypeOpaque, data, map[string]string{"hakopod.io/integration": "github"}); err != nil {
		failure(w, err)
		return
	}
	_, err = s.Store.Pool.Exec(r.Context(), "INSERT INTO audit_events(identity_id,key_id,action,resource) VALUES($1,$2,'github.configure','installation')", who(r).ID, who(r).KeyID)
	if err != nil {
		failure(w, err)
		return
	}
	write(w, 200, map[string]any{"configured": true, "token_configured": len(data["token"]) > 0, "webhook_path": "/api/v1/webhooks/github", "webhook_secret": once})
}
func (s *Server) readSource(ctx context.Context, id string) (sourceBinding, error) {
	var b sourceBinding
	err := s.Store.Pool.QueryRow(ctx, "SELECT "+sourceColumns+" FROM application_sources WHERE application_id=$1", id).Scan(&b.ApplicationID, &b.Provider, &b.Repository, &b.Branch, &b.Path, &b.AutoDeploy, &b.GrantID, &b.Revision, &b.LastCommit, &b.LastDeployment, &b.LastError, &b.UpdatedAt, &b.ConnectionID)
	return b, err
}
func (s *Server) getSource(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.authorizedApp(w, r, r.PathValue("id"), "deployments:read"); !ok {
		return
	}
	b, err := s.readSource(r.Context(), r.PathValue("id"))
	if errors.Is(err, pgx.ErrNoRows) {
		write(w, 200, map[string]any{"connected": false})
		return
	}
	if err != nil {
		failure(w, err)
		return
	}
	write(w, 200, map[string]any{"connected": true, "source": b})
}
func validSource(b sourceBinding) bool {
	return validSourceRepository(b.Provider, b.Repository) && len(b.Branch) > 0 && len(b.Branch) <= 200 && !strings.ContainsAny(b.Branch, "\r\n\x00 ?#") && b.Path != "" && len(b.Path) <= 256 && !strings.HasPrefix(b.Path, "/") && path.Clean(b.Path) == b.Path && !strings.HasPrefix(b.Path, "../") && !strings.ContainsAny(b.Path, "\\\x00\r\n") && strings.HasSuffix(b.Path, ".toml")
}
func (s *Server) setSource(w http.ResponseWriter, r *http.Request) {
	a, ok := s.authorizedApp(w, r, r.PathValue("id"), "deployments:write")
	if !ok {
		return
	}
	var in struct {
		Provider               string `json:"provider"`
		ConnectionID           string `json:"connection_id"`
		Repository             string `json:"repository"`
		Branch                 string `json:"branch"`
		Path                   string `json:"path"`
		AutoDeploy             bool   `json:"auto_deploy"`
		ExpectedSourceRevision *int64 `json:"expected_source_revision"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.Provider == "" {
		in.Provider = "github"
	}
	b := sourceBinding{ConnectionID: selectedGitConnection(in.Provider, in.ConnectionID), Provider: in.Provider, Repository: strings.ToLower(in.Repository), Branch: in.Branch, Path: in.Path, AutoDeploy: in.AutoDeploy}
	if !validSource(b) {
		problem(w, 400, "invalid_source", "use owner/repository, a branch, and a relative .toml path without traversal")
		return
	}
	if in.ExpectedSourceRevision == nil || *in.ExpectedSourceRevision < 0 {
		problem(w, 400, "source_revision_required", "provide the expected_source_revision returned by the current source binding, or zero for the first binding")
		return
	}
	if err := s.validateGitConnection(r.Context(), b.Provider, b.ConnectionID); err != nil {
		authFailure(w, err)
		return
	}
	tx, err := s.Store.Pool.Begin(r.Context())
	if err != nil {
		failure(w, err)
		return
	}
	defer tx.Rollback(r.Context())
	// Lock the application even when the first binding does not exist yet.
	var locked string
	if err = tx.QueryRow(r.Context(), "SELECT id FROM applications WHERE id=$1 FOR UPDATE", a.ID).Scan(&locked); err != nil {
		failure(w, err)
		return
	}
	var previous, approvedRepository, approvedProvider, approvedConnection string
	var revision int64
	err = tx.QueryRow(r.Context(), "SELECT grant_id,revision,repository,provider,connection_id FROM application_sources WHERE application_id=$1 FOR UPDATE", a.ID).Scan(&previous, &revision, &approvedRepository, &approvedProvider, &approvedConnection)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		failure(w, err)
		return
	}
	if revision != *in.ExpectedSourceRevision {
		problem(w, 409, "source_conflict", "source binding changed; refresh before saving")
		return
	}
	// A shared installation token may read private repositories outside this
	// project's authority. Only a global admin can approve the repository;
	// project deployers may choose branch/path within that locked approval.
	repositoryChanged := revision == 0 || approvedConnection != b.ConnectionID || approvedProvider != b.Provider || !strings.EqualFold(approvedRepository, b.Repository)
	if repositoryChanged && !who(r).CanManageGit() {
		problem(w, 403, "repository_approval_required", "a platform administrator must approve the first repository binding or a repository change")
		return
	}
	if in.AutoDeploy {
		data, err := s.gitWebhookCredentials(r.Context(), b.Provider, b.ConnectionID)
		if err != nil || len(data["webhook-secret"]) < 32 {
			problem(w, 400, "source_not_configured", "configure this source provider’s webhook authentication before enabling automatic deployments")
			return
		}
	}
	grant, err := s.Store.NewSourceGrant(r.Context(), tx, who(r), a.Project, a.Environment, a.Name)
	if err != nil {
		failure(w, err)
		return
	}
	_, err = tx.Exec(r.Context(), `INSERT INTO application_sources(application_id,repository,branch,path,auto_deploy,grant_id,provider,connection_id) VALUES($1,$2,$3,$4,$5,$6,$7,$8) ON CONFLICT(application_id) DO UPDATE SET connection_id=EXCLUDED.connection_id,provider=EXCLUDED.provider,repository=EXCLUDED.repository,branch=EXCLUDED.branch,path=EXCLUDED.path,auto_deploy=EXCLUDED.auto_deploy,grant_id=EXCLUDED.grant_id,revision=application_sources.revision+1,updated_at=now()`, a.ID, b.Repository, b.Branch, b.Path, b.AutoDeploy, grant, b.Provider, b.ConnectionID)
	if err == nil && previous != "" {
		_, err = tx.Exec(r.Context(), "UPDATE api_keys SET revoked_at=now() WHERE id=$1", previous)
	}
	if err == nil {
		_, err = tx.Exec(r.Context(), "INSERT INTO audit_events(identity_id,key_id,action,resource) VALUES($1,$2,'source.configure',$3)", who(r).ID, who(r).KeyID, a.ID)
	}
	if err == nil && repositoryChanged {
		_, err = tx.Exec(r.Context(), "INSERT INTO audit_events(identity_id,key_id,action,resource) VALUES($1,$2,'source.repository.approve',$3)", who(r).ID, who(r).KeyID, a.ID+":"+b.Repository)
	}
	if err == nil {
		err = tx.Commit(r.Context())
	}
	if err != nil {
		failure(w, err)
		return
	}
	b, err = s.readSource(r.Context(), a.ID)
	if err != nil {
		failure(w, err)
		return
	}
	write(w, 200, map[string]any{"connected": true, "source": b})
}

func (s *Server) githubGET(ctx context.Context, endpoint string, output any, connections ...string) error {
	permissions := map[string]string{"contents": "read"}
	if strings.Contains(endpoint, "/actions/") {
		permissions = map[string]string{"actions": "read"}
	}
	data, err := s.connectionCredentials(ctx, "github", selectedGitConnection("github", connections...), githubEndpointRepository(endpoint), permissions)
	if err != nil {
		return err
	}
	base := "https://api.github.com"
	if s.githubAPIURL != "" {
		base = s.githubAPIURL
	}
	request, err := http.NewRequestWithContext(ctx, "GET", base+endpoint, nil)
	if err != nil {
		return err
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	request.Header.Set("User-Agent", "hakopod")
	if len(data["token"]) > 0 {
		request.Header.Set("Authorization", "Bearer "+string(data["token"]))
	}
	client := &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	if s.githubHTTP != nil {
		client = s.githubHTTP
	}
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("GitHub is unreachable; retry after checking outbound HTTPS")
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return fmt.Errorf("GitHub returned HTTP %d; verify repository access, branch, path and token permissions", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 512<<10+1))
	if err != nil {
		return err
	}
	if len(body) > 512<<10 {
		return fmt.Errorf("GitHub response exceeds 512 KiB")
	}
	return json.Unmarshal(body, output)
}
func (s *Server) sourceSpec(ctx context.Context, b sourceBinding, commit string) (spec.Application, string, error) {
	if b.Provider == "gitlab" {
		return s.gitlabSourceSpec(ctx, b, commit)
	}
	if commit == "" {
		var ref struct {
			SHA string `json:"sha"`
		}
		err := s.githubGET(ctx, "/repos/"+b.Repository+"/commits/"+url.PathEscape(b.Branch), &ref, b.ConnectionID)
		if err != nil {
			return spec.Application{}, "", err
		}
		commit = ref.SHA
	}
	if !commitPattern.MatchString(commit) {
		return spec.Application{}, "", fmt.Errorf("GitHub did not return a valid commit SHA")
	}
	var file struct {
		Type     string `json:"type"`
		Encoding string `json:"encoding"`
		Content  string `json:"content"`
		Size     int    `json:"size"`
	}
	err := s.githubGET(ctx, "/repos/"+b.Repository+"/contents/"+url.PathEscape(b.Path)+"?ref="+url.QueryEscape(commit), &file, b.ConnectionID)
	if err != nil {
		return spec.Application{}, commit, err
	}
	if file.Type != "file" || file.Encoding != "base64" || file.Size > 256<<10 {
		return spec.Application{}, commit, fmt.Errorf("source must be a regular TOML file under 256 KiB")
	}
	bytes, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(file.Content, "\n", ""))
	if err != nil {
		return spec.Application{}, commit, fmt.Errorf("invalid GitHub file encoding")
	}
	if len(bytes) > spec.MaxBytes {
		return spec.Application{}, commit, fmt.Errorf("source TOML exceeds 256 KiB")
	}
	application, err := spec.Parse(bytes)
	return application, commit, err
}
func (s *Server) planSource(w http.ResponseWriter, r *http.Request) {
	a, ok := s.authorizedApp(w, r, r.PathValue("id"), "deployments:write")
	if !ok {
		return
	}
	b, err := s.readSource(r.Context(), a.ID)
	if err != nil {
		failure(w, err)
		return
	}
	next, commit, err := s.sourceSpec(r.Context(), b, "")
	if err != nil {
		problem(w, 400, "source_unavailable", err.Error())
		return
	}
	if next.Name != a.Name {
		problem(w, 400, "application_mismatch", "Source TOML application name must match this application")
		return
	}
	if !s.validateDeliveryPlan(w, r, a.Project, a.Environment, next, &a) {
		return
	}
	write(w, 200, map[string]any{"application_id": a.ID, "expected_revision": a.Revision, "spec": next, "changes": spec.Diff(&a.Spec, next), "warnings": deliveryWarnings(r, next), "commit_sha": commit, "expected_source_revision": b.Revision})
}
func (s *Server) deploySource(w http.ResponseWriter, r *http.Request) {
	a, ok := s.authorizedApp(w, r, r.PathValue("id"), "deployments:write")
	if !ok {
		return
	}
	var in struct {
		ExpectedRevision       *int64 `json:"expected_revision"`
		ExpectedSourceRevision *int64 `json:"expected_source_revision"`
		CommitSHA              string `json:"commit_sha"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.ExpectedRevision == nil || in.ExpectedSourceRevision == nil || !commitPattern.MatchString(in.CommitSHA) {
		problem(w, 400, "review_required", "provide expected_revision, expected_source_revision and the commit_sha returned by source plan")
		return
	}
	b, err := s.readSource(r.Context(), a.ID)
	if err != nil {
		failure(w, err)
		return
	}
	if b.Revision != *in.ExpectedSourceRevision {
		problem(w, 409, "source_conflict", "source binding changed; review it again")
		return
	}
	next, commit, err := s.sourceSpec(r.Context(), b, in.CommitSHA)
	if err != nil {
		problem(w, 400, "source_unavailable", err.Error())
		return
	}
	if next.Name != a.Name {
		problem(w, 400, "application_mismatch", "Source TOML application name must match this application")
		return
	}
	principal, err := s.Store.KeyPrincipal(r.Context(), who(r).KeyID)
	if err != nil {
		failure(w, err)
		return
	}
	if err = s.validateGitConnection(r.Context(), b.Provider, b.ConnectionID); err != nil {
		authFailure(w, err)
		return
	}
	d, err := s.Store.Accept(r.Context(), principal, a.Project, a.Environment, next, *in.ExpectedRevision, r.Header.Get("Idempotency-Key"))
	if err != nil {
		failure(w, err)
		return
	}
	_, _ = s.Store.Pool.Exec(r.Context(), "UPDATE application_sources SET last_commit=$2,last_deployment=$3,last_error='' WHERE application_id=$1", a.ID, commit, d.ID)
	write(w, 202, d)
}

func (s *Server) githubWebhook(w http.ResponseWriter, r *http.Request) {
	ip, _, _ := net.SplitHostPort(r.RemoteAddr)
	if !s.rate("webhook:" + ip) {
		w.Header().Set("Retry-After", "5")
		problem(w, 429, "rate_limit", "too many webhook deliveries; retry with backoff")
		return
	}
	connectionID := selectedGitConnection("github", r.PathValue("connection"))
	data, err := s.gitWebhookCredentials(r.Context(), "github", connectionID)
	if err != nil || len(data["webhook-secret"]) < 32 {
		problem(w, 503, "github_not_configured", "GitHub webhook authentication is not configured")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 512<<10)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		problem(w, 413, "body_limit", "webhook body exceeds 512 KiB")
		return
	}
	provided, err := hex.DecodeString(strings.TrimPrefix(r.Header.Get("X-Hub-Signature-256"), "sha256="))
	mac := hmac.New(sha256.New, data["webhook-secret"])
	mac.Write(body)
	if err != nil || !strings.HasPrefix(r.Header.Get("X-Hub-Signature-256"), "sha256=") || !hmac.Equal(provided, mac.Sum(nil)) {
		problem(w, 401, "invalid_signature", "GitHub webhook signature is invalid")
		return
	}
	// App-level pings have no installation ID. The connection must already be
	// enabled and its HMAC verified above; a ping never enqueues application work.
	if r.Header.Get("X-GitHub-Event") == "ping" {
		write(w, 200, map[string]bool{"ok": true})
		return
	}
	if !s.verifyWebhookConnection(w, r, connectionID, "github", body) {
		return
	}
	if r.Header.Get("X-GitHub-Event") == "installation" && connectionID != defaultGitConnection("github") {
		var event struct {
			Action string `json:"action"`
		}
		if json.Unmarshal(body, &event) == nil && (event.Action == "deleted" || event.Action == "suspend") {
			if _, err = s.Store.Pool.Exec(r.Context(), "UPDATE git_connections SET enabled=false,revision=revision+1,updated_at=now() WHERE id=$1 AND auth_kind='github_app' AND enabled", connectionID); err != nil {
				failure(w, err)
				return
			}
		}
		write(w, 202, map[string]bool{"accepted": true})
		return
	}
	if r.Header.Get("X-GitHub-Event") == "workflow_run" {
		delivery := r.Header.Get("X-GitHub-Delivery")
		if len(delivery) < 8 || len(delivery) > 128 {
			problem(w, 400, "delivery_required", "valid X-GitHub-Delivery is required")
			return
		}
		if err := s.enqueueBuildWebhook(r.Context(), "workflow_run", body, delivery, connectionID); err != nil {
			if errors.Is(err, errBuildQueueFull) {
				w.Header().Set("Retry-After", "30")
				problem(w, 503, "queue_full", "build inbox is full; GitHub should retry this delivery")
				return
			}
			failure(w, err)
			return
		}
		write(w, 202, map[string]bool{"accepted": true})
		return
	}
	if r.Header.Get("X-GitHub-Event") != "push" {
		write(w, 202, map[string]bool{"ignored": true})
		return
	}
	var payload struct {
		Ref        string `json:"ref"`
		After      string `json:"after"`
		Deleted    bool   `json:"deleted"`
		Repository struct {
			FullName string `json:"full_name"`
		} `json:"repository"`
	}
	if json.Unmarshal(body, &payload) != nil || !repositoryPattern.MatchString(payload.Repository.FullName) || !commitPattern.MatchString(payload.After) {
		problem(w, 400, "invalid_push", "invalid GitHub push payload")
		return
	}
	if payload.Deleted {
		write(w, 202, map[string]bool{"ignored": true})
		return
	}
	delivery := r.Header.Get("X-GitHub-Delivery")
	if len(delivery) < 8 || len(delivery) > 128 {
		problem(w, 400, "delivery_required", "valid X-GitHub-Delivery is required")
		return
	}
	if err = s.enqueueProviderSources(r.Context(), "github", delivery, payload.After, payload.Repository.FullName, payload.Ref, connectionID); err != nil {
		if errors.Is(err, errSourceQueueFull) {
			w.Header().Set("Retry-After", "30")
			problem(w, 503, "queue_full", "source inbox is full; GitHub should retry this delivery")
			return
		}
		failure(w, err)
		return
	}
	write(w, 202, map[string]bool{"accepted": true})
}

var errSourceQueueFull = errors.New("source inbox is full")

func (s *Server) enqueueSources(ctx context.Context, delivery, commit, repository, ref string) error {
	return s.enqueueProviderSources(ctx, "github", delivery, commit, repository, ref)
}
func (s *Server) enqueueProviderSources(ctx context.Context, provider, delivery, commit, repository, ref string, connections ...string) error {
	connectionID := selectedGitConnection(provider, connections...)
	if connectionID != defaultGitConnection(provider) {
		delivery = connectionID + ":" + delivery
	}
	if provider != "github" {
		delivery = provider + ":" + delivery
	}
	repository = strings.ToLower(repository)
	tx, err := s.Store.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(724891004)"); err != nil {
		return err
	}
	var pending, added int
	if err = tx.QueryRow(ctx, "SELECT count(*) FROM source_jobs WHERE status IN ('queued','running')").Scan(&pending); err != nil {
		return err
	}
	if err = tx.QueryRow(ctx, `SELECT count(*) FROM application_sources s WHERE connection_id=$5 AND provider=$4 AND repository=$1 AND 'refs/heads/'||branch=$2 AND auto_deploy AND NOT EXISTS(SELECT 1 FROM source_jobs j WHERE j.application_id=s.application_id AND j.delivery_id=$3)`, repository, ref, delivery, provider, connectionID).Scan(&added); err != nil {
		return err
	}
	if pending+added > 1000 {
		return errSourceQueueFull
	}
	_, err = tx.Exec(ctx, `INSERT INTO source_jobs(id,application_id,source_revision,commit_sha,delivery_id,connection_id) SELECT md5(application_id||':'||$1),application_id,revision,$2,$1,connection_id FROM application_sources WHERE connection_id=$6 AND provider=$5 AND repository=$3 AND 'refs/heads/'||branch=$4 AND auto_deploy ON CONFLICT(application_id,delivery_id) DO NOTHING`, delivery, commit, repository, ref, provider, connectionID)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// RunSources keeps one bounded worker and a small durable inbox, without an
// additional queue service. Terminal delivery records expire after 30 days.
func (s *Server) RunSources(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	cycles := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.runSource(ctx)
			cycles++
			if cycles%12 == 0 {
				cleanup, cancel := context.WithTimeout(ctx, 5*time.Second)
				_, _ = s.Store.Pool.Exec(cleanup, `DELETE FROM source_jobs WHERE id IN (SELECT id FROM source_jobs WHERE status NOT IN ('queued','running') ORDER BY created_at DESC OFFSET 5000 LIMIT 500) OR id IN (SELECT id FROM source_jobs WHERE status NOT IN ('queued','running') AND created_at<now()-interval '30 days' LIMIT 500)`)
				cancel()
			}
		}
	}
}

func (s *Server) runSource(parent context.Context) {
	ctx, cancel := context.WithTimeout(parent, 50*time.Second)
	defer cancel()
	var id, appID, commit, connectionID string
	var sourceRevision int64
	var attempts int
	err := s.Store.Pool.QueryRow(ctx, `UPDATE source_jobs SET status='running',claimed_at=now(),attempts=attempts+1 WHERE id=(SELECT id FROM source_jobs WHERE (status='queued' AND next_attempt_at<=now()) OR (status='running' AND claimed_at<now()-interval '2 minutes') ORDER BY created_at FOR UPDATE SKIP LOCKED LIMIT 1) RETURNING id,application_id,source_revision,commit_sha,attempts,connection_id`).Scan(&id, &appID, &sourceRevision, &commit, &attempts, &connectionID)
	if err != nil {
		return
	}
	// Recover the exact accepted operation before fetching a newer revision.
	var deploymentID string
	err = s.Store.Pool.QueryRow(ctx, "SELECT id FROM deployments WHERE application_id=$1 AND idempotency_key=$2", appID, "github-"+id).Scan(&deploymentID)
	terminal := false
	if errors.Is(err, pgx.ErrNoRows) {
		var b sourceBinding
		b, err = s.readSource(ctx, appID)
		if err == nil && (!b.AutoDeploy || b.Revision != sourceRevision || b.ConnectionID != connectionID) {
			err = errors.New("source binding changed or automatic deployment disabled")
			terminal = true
		}
		var principal store.Principal
		var app store.Application
		var next spec.Application
		if err == nil {
			principal, err = s.Store.KeyPrincipal(ctx, b.GrantID)
		}
		if err == nil {
			app, err = s.Store.Application(ctx, appID)
		}
		if err == nil && !principal.Allows("deployments:write", app.Project, app.Environment, app.Name) {
			err = store.ErrForbidden
		}
		if err == nil {
			next, _, err = s.sourceSpec(ctx, b, commit)
		}
		if err == nil && next.Name != app.Name {
			err = errors.New("source application name does not match binding")
			terminal = true
		}
		if err == nil {
			principal, err = s.Store.KeyPrincipal(ctx, b.GrantID)
		}
		if err == nil {
			err = s.validateGitConnection(ctx, b.Provider, b.ConnectionID)
		}
		if err == nil {
			var d store.Deployment
			d, err = s.Store.Accept(ctx, principal, app.Project, app.Environment, next, app.Revision, "github-"+id)
			deploymentID = d.ID
		}
	}
	if parent.Err() != nil {
		return
	}
	status, message := "accepted", ""
	if err != nil {
		message = err.Error()
		if len(message) > 1024 {
			message = message[:1024]
		}
		terminal = terminal || errors.Is(err, store.ErrUnauthorized) || errors.Is(err, store.ErrForbidden) || errors.Is(err, pgx.ErrNoRows)
		status = "failed"
		if !terminal && attempts < 5 {
			status = "queued"
		}
	}
	finish, done := context.WithTimeout(parent, 5*time.Second)
	defer done()
	tx, err := s.Store.Pool.Begin(finish)
	if err != nil {
		return
	}
	defer tx.Rollback(finish)
	// The claim age prevents concurrent attempts; compare attempts as an extra fence.
	tag, err := tx.Exec(finish, `UPDATE source_jobs SET status=$2,error=$3,finished_at=CASE WHEN $2='queued' THEN NULL ELSE now() END,next_attempt_at=now()+$4*interval '1 second' WHERE id=$1 AND status='running' AND attempts=$5`, id, status, message, min(300, 5*(1<<min(attempts, 6))), attempts)
	if err != nil || tag.RowsAffected() != 1 {
		return
	}
	_, err = tx.Exec(finish, "UPDATE application_sources SET last_commit=$2,last_deployment=$3,last_error=$4 WHERE application_id=$1 AND revision=$5", appID, commit, deploymentID, message, sourceRevision)
	if err == nil {
		_ = tx.Commit(finish)
	}
}
