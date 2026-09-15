package api

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/hakopod/hakopod/internal/cluster"
	"github.com/hakopod/hakopod/internal/store"
	"github.com/jackc/pgx/v5"
	corev1 "k8s.io/api/core/v1"
)

var registryPrefixPattern = regexp.MustCompile(`^[a-z0-9.-]+/(internal|[a-f0-9]{32})$`)
var registryBuildID = regexp.MustCompile(`^[a-f0-9]{32}$`)
var registryOIDCKeys = oidc.NewRemoteKeySet(oidc.ClientContext(context.Background(), &http.Client{Timeout: 10 * time.Second}), "https://token.actions.githubusercontent.com/.well-known/jwks")

// ConfigureBuildRegistry runs before serving requests. Existing builds without
// explicit registry credentials adopt managed storage and require workflow review.
// Running jobs and already deployed applications are left alone.
func (s *Server) ConfigureBuildRegistry(ctx context.Context, prefix string) error {
	if prefix == "" {
		return nil
	}
	key, keyErr := hex.DecodeString(s.Auth.EncryptionKey)
	if keyErr != nil || len(key) != 32 || s.Auth.DeploymentMode != cluster.DeploymentManagedCloud || !registryPrefixPattern.MatchString(prefix) || len(s.Auth.EncryptionKey) != 64 {
		return errors.New("managed build registry requires Cloud mode, a valid registry namespace, and an encryption key")
	}
	s.BuildRegistry = prefix
	return s.backfillBuildRegistry(ctx)
}
func (s *Server) backfillBuildRegistry(ctx context.Context) error {
	if s.BuildRegistry == "" {
		return nil
	}
	_, err := s.Store.Pool.Exec(ctx, `WITH candidates AS (
 SELECT b.id FROM build_configs b JOIN git_connections g ON b.config->>'connection_id'=g.id
 WHERE g.auth_kind='github_app' AND g.enabled AND COALESCE(b.config->>'provider','github')='github'
 AND COALESCE(b.config->>'registry_credential','')='' AND COALESCE(b.config->>'managed_registry','')=''
 AND NOT EXISTS (SELECT 1 FROM build_runs r WHERE r.build_id=b.id AND r.status NOT IN ('completed','cancelled','failed'))
 ORDER BY b.id LIMIT 100 FOR UPDATE OF b SKIP LOCKED)
 UPDATE build_configs b SET config=jsonb_set(b.config,'{managed_registry}',to_jsonb($1::text || '/' || b.id)),revision=b.revision+1,installed_revision=0,installed_commit='',updated_at=now()
 FROM candidates WHERE b.id=candidates.id`, s.BuildRegistry)
	return err
}
func (s *Server) assignBuildRegistry(ctx context.Context, c *buildConfig) error {
	if s.BuildRegistry == "" || c.Provider != "github" || c.RegistryCredential != "" {
		return nil
	}
	connection, err := s.readGitConnection(ctx, c.ConnectionID)
	if err != nil {
		return err
	}
	if connection.AuthKind == "github_app" {
		c.ManagedRegistry = s.BuildRegistry + "/" + c.ID
	}
	return nil
}
func (s *Server) buildPullPassword(c buildConfig) string {
	key, _ := hex.DecodeString(s.Auth.EncryptionKey)
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte("hakopod-build-pull-v1\x00" + c.ManagedRegistry + "\x00" + c.Project + "\x00" + c.Environment))
	return hex.EncodeToString(mac.Sum(nil))
}
func (c buildConfig) registryUsername(action string) string {
	parts := strings.Split(c.ManagedRegistry, "/")
	if len(parts) != 3 {
		return ""
	}
	return action + "-" + parts[1] + "-" + c.ID
}
func (s *Server) ensureBuildRegistry(ctx context.Context, c buildConfig) (string, error) {
	if c.ManagedRegistry == "" {
		return c.RegistryCredential, nil
	}
	if c.ManagedRegistry != s.BuildRegistry+"/"+c.ID || s.Cluster == nil {
		return "", errors.New("managed registry is unavailable on this runtime")
	}
	name := "build-" + c.ID
	host := strings.Split(c.ManagedRegistry, "/")[0]
	old, err := s.Store.RuntimeResource(ctx, "registry", c.Project, c.Environment, name)
	if err == nil {
		var meta registryMetadata
		if json.Unmarshal(old.Metadata, &meta) != nil || meta.Registry != host || meta.SecretName != "hp-build-registry-"+c.ID {
			return "", store.ErrConflict
		}
		return name, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return "", err
	}
	credential := cluster.RegistryCredential{Registry: host, Username: c.registryUsername("pull"), Password: s.buildPullPassword(c), Revision: 1}
	meta := registryMetadata{Registry: host, SecretName: "hp-build-registry-" + c.ID}
	if err = s.Cluster.PutPlatformSecret(ctx, meta.SecretName, corev1.SecretTypeDockerConfigJson, cluster.RegistrySecretData(credential), map[string]string{"hakopod.io/registry-credential": cluster.RegistryScope(c.Project, c.Environment, name)}); err != nil {
		return "", err
	}
	p, ok := ctx.Value(principalKey{}).(store.Principal)
	if !ok {
		return "", store.ErrForbidden
	}
	_, err = s.Store.PutRuntimeResource(ctx, p, "registry", c.Project, c.Environment, name, 0, meta)
	if err != nil {
		return "", err
	}
	return name, nil
}

// The registry gateway passes only credentials and the exact repository action.
// It receives no API key, registry password, or user identity in the response.
func (s *Server) authorizeBuildRegistry(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	var in struct {
		BuildID  string `json:"build_id"`
		Username string `json:"username"`
		Password string `json:"password"`
		Action   string `json:"action"`
	}
	if !decode(w, r, &in) {
		return
	}
	if s.BuildRegistry == "" || !registryBuildID.MatchString(in.BuildID) || len(in.Password) > 16384 || (in.Action != "pull" && in.Action != "push" && in.Action != "exchange") {
		failure(w, store.ErrForbidden)
		return
	}
	c, err := s.readBuild(ctx, in.BuildID)
	if err != nil || c.ManagedRegistry != s.BuildRegistry+"/"+c.ID {
		failure(w, store.ErrForbidden)
		return
	}
	if in.Username == c.registryUsername("pull") && in.Action == "pull" && hmac.Equal([]byte(in.Password), []byte(s.buildPullPassword(c))) {
		write(w, 200, map[string]bool{"authorized": true})
		return
	}
	if in.Username != c.registryUsername("push") || c.Provider != "github" || c.InstalledRevision != c.Revision {
		failure(w, store.ErrForbidden)
		return
	}
	if err = s.Store.Reauthorize(ctx, c.GrantID, c.Project, c.Environment, c.Name); err != nil {
		failure(w, store.ErrForbidden)
		return
	}
	if in.Action != "exchange" {
		parts := strings.Split(in.Password, ".")
		if len(parts) != 2 {
			failure(w, store.ErrForbidden)
			return
		}
		expiry, e := strconv.ParseInt(parts[0], 10, 64)
		if e != nil || expiry < time.Now().Unix() || expiry > time.Now().Add(46*time.Minute).Unix() || !hmac.Equal([]byte(in.Password), []byte(s.buildPushPassword(c, expiry))) {
			failure(w, store.ErrForbidden)
			return
		}
		write(w, 200, map[string]bool{"authorized": true})
		return
	}
	verifier := oidc.NewVerifier("https://token.actions.githubusercontent.com", registryOIDCKeys, &oidc.Config{ClientID: "https://" + strings.Split(c.ManagedRegistry, "/")[0], SupportedSigningAlgs: []string{"RS256"}})
	token, err := verifier.Verify(ctx, in.Password)
	if err != nil {
		failure(w, store.ErrForbidden)
		return
	}
	var claims struct {
		Repository   string `json:"repository"`
		RepositoryID string `json:"repository_id"`
		WorkflowRef  string `json:"workflow_ref"`
		Event        string `json:"event_name"`
	}
	if token.Claims(&claims) != nil || !strings.EqualFold(claims.Repository, c.Repository) || (claims.Event != "push" && claims.Event != "workflow_dispatch") {
		failure(w, store.ErrForbidden)
		return
	}
	var repo struct {
		ID            json.Number `json:"id"`
		DefaultBranch string      `json:"default_branch"`
	}
	if err = s.githubGET(ctx, "/repos/"+c.Repository, &repo, c.ConnectionID); err != nil {
		failure(w, store.ErrForbidden)
		return
	}
	branch := repo.DefaultBranch
	if claims.Event == "push" {
		if !c.AutoBuild {
			failure(w, store.ErrForbidden)
			return
		}
		branch = c.Branch
	}
	workflow, ref, ok := strings.Cut(claims.WorkflowRef, "@")
	if claims.RepositoryID == "" || claims.RepositoryID != repo.ID.String() || !ok || !strings.EqualFold(workflow, c.Repository+"/"+c.workflowPath()) || ref != "refs/heads/"+branch {
		failure(w, store.ErrForbidden)
		return
	}
	write(w, 200, map[string]string{"password": s.buildPushPassword(c, time.Now().Add(45*time.Minute).Unix())})
}

func (s *Server) buildPushPassword(c buildConfig, expiry int64) string {
	key, _ := hex.DecodeString(s.Auth.EncryptionKey)
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(fmt.Sprintf("hakopod-build-push-v1\x00%s\x00%d\x00%d", c.ManagedRegistry, c.Revision, expiry)))
	return strconv.FormatInt(expiry, 10) + "." + hex.EncodeToString(mac.Sum(nil))
}

func managedRegistryLogin(c buildConfig) string {
	host := strings.Split(c.ManagedRegistry, "/")[0]
	return fmt.Sprintf(`      - name: Authorize this build with Hakopod Registry
        env:
          REGISTRY: %q
          REGISTRY_USER: %q
        run: |
          set -euo pipefail
          TOKEN="$(curl --fail --silent --show-error -H "Authorization: Bearer $ACTIONS_ID_TOKEN_REQUEST_TOKEN" "$ACTIONS_ID_TOKEN_REQUEST_URL&audience=https%%3A%%2F%%2F$REGISTRY" | jq -er .value)"
          echo "::add-mask::$TOKEN"
          TOKEN="$(jq -n --arg username "$REGISTRY_USER" --arg password "$TOKEN" '{username:$username,password:$password}' | curl --fail --silent --show-error -H 'Content-Type: application/json' --data-binary @- "https://$REGISTRY/api/registry/exchange" | jq -er .password)"
          echo "::add-mask::$TOKEN"
          printf '%%s' "$TOKEN" | docker login "$REGISTRY" --username "$REGISTRY_USER" --password-stdin
`, host, c.registryUsername("push"))
}
