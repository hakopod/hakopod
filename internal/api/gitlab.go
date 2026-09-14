package api

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/hakopod/hakopod/internal/spec"
	"github.com/hakopod/hakopod/internal/store"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
)

func validSourceRepository(provider, repository string) bool {
	if provider == "" || provider == "github" {
		return repositoryPattern.MatchString(repository)
	}
	if provider != "gitlab" || len(repository) > 512 {
		return false
	}
	parts := strings.Split(repository, "/")
	if len(parts) < 2 || len(parts) > 12 {
		return false
	}
	for _, part := range parts {
		if part == "." || part == ".." || !repositoryPattern.MatchString("owner/"+part) {
			return false
		}
	}
	return true
}
func (s *Server) registerGitLabRoutes(public, protected *http.ServeMux) {
	protected.HandleFunc("GET /api/v1/integrations/gitlab", s.gitlabStatus)
	protected.HandleFunc("PUT /api/v1/integrations/gitlab", s.configureGitLab)
	public.HandleFunc("POST /api/v1/webhooks/gitlab", s.gitlabWebhook)
}
func (s *Server) sourceCredentials(ctx context.Context, provider string) (map[string][]byte, error) {
	if provider == "gitlab" {
		return s.gitlabCredentials(ctx)
	}
	return s.githubCredentials(ctx)
}
func (s *Server) gitlabCredentials(ctx context.Context) (map[string][]byte, error) {
	if s.gitlabTestCredentials != nil {
		return s.gitlabTestCredentials(ctx)
	}
	if s.Cluster == nil {
		return nil, fmt.Errorf("Kubernetes credential storage is unavailable")
	}
	secret, err := s.Cluster.GetPlatformSecret(ctx, "gitlab-connection")
	if apierrors.IsNotFound(err) {
		return map[string][]byte{}, nil
	}
	if err != nil {
		return nil, err
	}
	return secret.Data, nil
}
func (s *Server) gitlabStatus(w http.ResponseWriter, r *http.Request) {
	if !admin(w, r) {
		return
	}
	data, err := s.gitlabCredentials(r.Context())
	if err != nil {
		failure(w, err)
		return
	}
	write(w, 200, map[string]any{"configured": len(data["webhook-secret"]) >= 32, "token_configured": len(data["token"]) > 0, "webhook_path": "/api/v1/webhooks/gitlab", "private_repositories": len(data["token"]) > 0})
}
func (s *Server) configureGitLab(w http.ResponseWriter, r *http.Request) {
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
	if len(in.Token) > 1024 || len(in.WebhookSecret) > 256 || strings.ContainsAny(in.Token+in.WebhookSecret, "\r\n\x00") {
		problem(w, 400, "invalid_credentials", "GitLab credentials exceed supported bounds")
		return
	}
	data, err := s.gitlabCredentials(r.Context())
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
			problem(w, 400, "weak_webhook_secret", "Webhook secret must contain at least 32 characters")
			return
		}
		data["webhook-secret"] = []byte(in.WebhookSecret)
	} else if len(data["webhook-secret"]) < 32 {
		raw := make([]byte, 32)
		if _, err = rand.Read(raw); err != nil {
			failure(w, err)
			return
		}
		once = hex.EncodeToString(raw)
		data["webhook-secret"] = []byte(once)
	}
	if err = s.Cluster.PutPlatformSecret(r.Context(), "gitlab-connection", corev1.SecretTypeOpaque, data, map[string]string{"hakopod.io/integration": "gitlab"}); err != nil {
		failure(w, err)
		return
	}
	_, err = s.Store.Pool.Exec(r.Context(), "INSERT INTO audit_events(identity_id,key_id,action,resource) VALUES($1,$2,'gitlab.configure','installation')", who(r).ID, who(r).KeyID)
	if err != nil {
		failure(w, err)
		return
	}
	write(w, 200, map[string]any{"configured": true, "token_configured": len(data["token"]) > 0, "webhook_path": "/api/v1/webhooks/gitlab", "webhook_secret": once})
}
func (s *Server) gitlabGET(ctx context.Context, endpoint string, output any, connections ...string) error {
	data, err := s.connectionCredentials(ctx, "gitlab", selectedGitConnection("gitlab", connections...), "", nil)
	if err != nil {
		return err
	}
	base := "https://gitlab.com/api/v4"
	if s.gitlabAPIURL != "" {
		base = s.gitlabAPIURL
	}
	request, err := http.NewRequestWithContext(ctx, "GET", base+endpoint, nil)
	if err != nil {
		return err
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", "hakopod")
	if len(data["token"]) > 0 {
		request.Header.Set("PRIVATE-TOKEN", string(data["token"]))
	}
	client := &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	if s.gitlabHTTP != nil {
		client = s.gitlabHTTP
	}
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("GitLab is unreachable; check outbound HTTPS")
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return fmt.Errorf("GitLab returned HTTP %d; verify repository, branch, path and token permissions", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, (512<<10)+1))
	if err != nil {
		return err
	}
	if len(body) > 512<<10 {
		return fmt.Errorf("GitLab response exceeds 512 KiB")
	}
	return json.Unmarshal(body, output)
}
func (s *Server) gitlabSourceSpec(ctx context.Context, b sourceBinding, commit string) (spec.Application, string, error) {
	base := "/projects/" + url.PathEscape(b.Repository)
	if commit == "" {
		var ref struct {
			ID string `json:"id"`
		}
		err := s.gitlabGET(ctx, base+"/repository/commits/"+url.PathEscape(b.Branch), &ref, b.ConnectionID)
		if err != nil {
			return spec.Application{}, "", err
		}
		commit = ref.ID
	}
	if !commitPattern.MatchString(commit) {
		return spec.Application{}, "", fmt.Errorf("GitLab returned an invalid commit SHA")
	}
	var file struct {
		Encoding string `json:"encoding"`
		Content  string `json:"content"`
		Size     int    `json:"size"`
		Commit   string `json:"commit_id"`
	}
	err := s.gitlabGET(ctx, base+"/repository/files/"+url.PathEscape(b.Path)+"?ref="+url.QueryEscape(commit), &file, b.ConnectionID)
	if err != nil {
		return spec.Application{}, commit, err
	}
	if file.Encoding != "base64" || file.Size < 0 || file.Size > spec.MaxBytes || file.Commit != commit {
		return spec.Application{}, commit, fmt.Errorf("source must be a TOML file under 256 KiB at the reviewed commit")
	}
	body, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(file.Content, "\n", ""))
	if err != nil || len(body) > spec.MaxBytes {
		return spec.Application{}, commit, fmt.Errorf("invalid or oversized GitLab TOML file")
	}
	application, err := spec.Parse(body)
	return application, commit, err
}
func (s *Server) gitlabWebhook(w http.ResponseWriter, r *http.Request) {
	ip, _, _ := net.SplitHostPort(r.RemoteAddr)
	if !s.rate("webhook:" + ip) {
		w.Header().Set("Retry-After", "5")
		problem(w, 429, "rate_limit", "Too many webhook deliveries; retry with backoff")
		return
	}
	connectionID := selectedGitConnection("gitlab", r.PathValue("connection"))
	credentials, err := s.connectionCredentials(r.Context(), "gitlab", connectionID, "", nil)
	if err != nil || len(credentials["webhook-secret"]) < 32 {
		problem(w, 503, "gitlab_not_configured", "GitLab webhook authentication is not configured")
		return
	}
	if subtle.ConstantTimeCompare([]byte(r.Header.Get("X-Gitlab-Token")), credentials["webhook-secret"]) != 1 {
		problem(w, 401, "invalid_signature", "GitLab webhook token is invalid")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 512<<10)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		problem(w, 413, "body_limit", "Webhook body exceeds 512 KiB")
		return
	}
	if r.Header.Get("X-Gitlab-Event") == "Pipeline Hook" {
		delivery := r.Header.Get("X-Gitlab-Event-UUID")
		if delivery == "" {
			delivery = r.Header.Get("X-Gitlab-Webhook-UUID")
		}
		if len(delivery) < 8 || len(delivery) > 128 || strings.ContainsAny(delivery, "\r\n\x00") {
			problem(w, 400, "delivery_required", "A valid X-Gitlab-Event-UUID is required")
			return
		}
		if err = s.enqueueGitLabBuildWebhook(r.Context(), body, delivery, connectionID); err != nil {
			if errors.Is(err, errBuildQueueFull) {
				w.Header().Set("Retry-After", "30")
				problem(w, 503, "queue_full", "Build inbox is full; GitLab should retry")
				return
			}
			if errors.Is(err, store.ErrInput) {
				problem(w, 400, "invalid_pipeline", "Invalid GitLab pipeline payload")
				return
			}
			failure(w, err)
			return
		}
		write(w, 202, map[string]bool{"accepted": true})
		return
	}
	if r.Header.Get("X-Gitlab-Event") != "Push Hook" {
		write(w, 202, map[string]bool{"ignored": true})
		return
	}
	var payload struct {
		Kind    string `json:"object_kind"`
		Ref     string `json:"ref"`
		After   string `json:"after"`
		Project struct {
			Path string `json:"path_with_namespace"`
		} `json:"project"`
	}
	if json.Unmarshal(body, &payload) != nil || payload.Kind != "push" || !validSourceRepository("gitlab", payload.Project.Path) || !commitPattern.MatchString(payload.After) || !strings.HasPrefix(payload.Ref, "refs/heads/") {
		problem(w, 400, "invalid_push", "Invalid GitLab push payload")
		return
	}
	if strings.Trim(payload.After, "0") == "" {
		write(w, 202, map[string]bool{"ignored": true})
		return
	}
	delivery := r.Header.Get("X-Gitlab-Event-UUID")
	if delivery == "" {
		delivery = r.Header.Get("X-Gitlab-Webhook-UUID")
	}
	if len(delivery) < 8 || len(delivery) > 128 || strings.ContainsAny(delivery, "\r\n\x00") {
		problem(w, 400, "delivery_required", "A valid X-Gitlab-Event-UUID is required")
		return
	}
	if err = s.enqueueProviderSources(r.Context(), "gitlab", delivery, payload.After, payload.Project.Path, payload.Ref, connectionID); err != nil {
		if errors.Is(err, errSourceQueueFull) {
			w.Header().Set("Retry-After", "30")
			problem(w, 503, "queue_full", "Source inbox is full; GitLab should retry")
			return
		}
		failure(w, err)
		return
	}
	write(w, 202, map[string]bool{"accepted": true})
}
