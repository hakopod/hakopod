package api

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"k8s.io/client-go/tools/clientcmd"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/golang-jwt/jwt/v5"
	"github.com/hakopod/hakopod/internal/cluster"
	"github.com/hakopod/hakopod/internal/store"
)

func TestManagedRegistryCredentialsAndOIDC(t *testing.T) {
	db := sourceDatabase(t)
	ctx := context.Background()
	raw, err := db.Bootstrap(ctx, "registry-test")
	if err != nil {
		t.Fatal(err)
	}
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/keys" {
			write(w, 200, map[string]any{"keys": []map[string]string{{"kty": "RSA", "kid": "test", "alg": "RS256", "use": "sig", "n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()), "e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes())}}})
			return
		}
		write(w, 200, map[string]any{"id": 123, "default_branch": "main"})
	}))
	defer remote.Close()
	oldKeys := registryOIDCKeys
	registryOIDCKeys = oidc.NewRemoteKeySet(ctx, remote.URL+"/keys")
	defer func() { registryOIDCKeys = oldKeys }()
	s := &Server{Store: db, Auth: AuthConfig{DeploymentMode: cluster.DeploymentManagedCloud, EncryptionKey: strings.Repeat("12", 32)}, githubAPIURL: remote.URL, githubTestCredentials: func(context.Context) (map[string][]byte, error) {
		return map[string][]byte{"token": []byte("fixture")}, nil
	}}
	h := s.Handler()
	created := gitConnectionCall(t, h, raw, "POST", "/builds", map[string]any{"project": "demo", "environment": "development", "name": "registry-app", "service": "web", "repository": "example/source", "architecture": "amd64"}, 201)
	id := created["id"].(string)
	c, err := s.readBuild(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	s.BuildRegistry = "cloud.example.test/internal"
	c.ManagedRegistry = s.BuildRegistry + "/" + id
	_, err = db.Pool.Exec(ctx, "UPDATE build_configs SET config=$2,installed_revision=revision WHERE id=$1", id, store.JSON(c))
	if err != nil {
		t.Fatal(err)
	}
	c.InstalledRevision = c.Revision
	call := func(user, password, action string, want int) map[string]any {
		t.Helper()
		req := httptest.NewRequest("POST", "/api/v1/build-registry/authorize", bytes.NewReader(store.JSON(map[string]string{"build_id": id, "username": user, "password": password, "action": action})))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != want {
			t.Fatalf("authorization got %d want %d", rec.Code, want)
		}
		var result map[string]any
		json.Unmarshal(rec.Body.Bytes(), &result)
		return result
	}
	pull := s.buildPullPassword(c)
	call(c.registryUsername("pull"), pull, "pull", 200)
	call(c.registryUsername("pull"), pull, "push", 403)
	call(c.registryUsername("pull"), strings.Repeat("0", 64), "pull", 403)
	claims := jwt.MapClaims{"iss": "https://token.actions.githubusercontent.com", "aud": "https://cloud.example.test", "exp": time.Now().Add(5 * time.Minute).Unix(), "iat": time.Now().Unix(), "sub": "repo:example/source:ref:refs/heads/main", "repository": "example/source", "repository_id": "123", "workflow_ref": "example/source/" + c.workflowPath() + "@refs/heads/main", "event_name": "workflow_dispatch"}
	sign := func() string {
		token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
		token.Header["kid"] = "test"
		signed, e := token.SignedString(key)
		if e != nil {
			t.Fatal(e)
		}
		return signed
	}
	exchanged := call(c.registryUsername("push"), sign(), "exchange", 200)
	password := exchanged["password"].(string)
	call(c.registryUsername("push"), password, "push", 200)
	call(c.registryUsername("push"), password, "pull", 200)
	for _, field := range []string{"aud", "repository_id", "workflow_ref", "event_name", "repository"} {
		old := claims[field]
		claims[field] = "wrong"
		call(c.registryUsername("push"), sign(), "exchange", 403)
		claims[field] = old
	}
	call(c.registryUsername("push"), s.buildPushPassword(c, time.Now().Add(-time.Minute).Unix()), "push", 403)
	c.Revision++
	call(c.registryUsername("push"), s.buildPushPassword(c, time.Now().Add(time.Minute).Unix()), "push", 403)
	_, err = db.Pool.Exec(ctx, "UPDATE api_keys SET revoked_at=now() WHERE id=$1", c.GrantID)
	if err != nil {
		t.Fatal(err)
	}
	call(c.registryUsername("push"), password, "push", 403)
}
func TestManagedRegistryWorkflowHasNoSavedPushSecret(t *testing.T) {
	c := buildConfig{ID: strings.Repeat("a", 32), Provider: "github", Repository: "example/app", ManagedRegistry: "cloud.example.test/internal/" + strings.Repeat("a", 32), Mode: "dockerfile", ContextPath: ".", Dockerfile: "Dockerfile"}
	workflow := buildWorkflow(c)
	for _, want := range []string{"id-token: write", "/api/registry/exchange", "--password-stdin", c.ManagedRegistry, "::add-mask::"} {
		if !strings.Contains(workflow, want) {
			t.Fatal("missing", want)
		}
	}
	for _, bad := range []string{"packages: write", "secrets.GITHUB_TOKEN", "{{"} {
		if strings.Contains(workflow, bad) && bad != "{{" {
			t.Fatal("unexpected", bad)
		}
	}
}

func TestManagedRegistryBackfillIsIdempotentAndPreservesExplicitCredentials(t *testing.T) {
	db := sourceDatabase(t)
	ctx := context.Background()
	raw, err := db.Bootstrap(ctx, "backfill-test")
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{Store: db, Auth: AuthConfig{DeploymentMode: cluster.DeploymentManagedCloud, EncryptionKey: strings.Repeat("ab", 32)}}
	h := s.Handler()
	conn := strings.Repeat("f", 32)
	_, err = db.Pool.Exec(ctx, "INSERT INTO git_connections(id,name,provider,auth_kind,credentials) VALUES($1,'Existing App','github','github_app',$2)", conn, []byte("fixture"))
	if err != nil {
		t.Fatal(err)
	}
	ids := []string{}
	for i, name := range []string{"automatic", "explicit"} {
		input := map[string]any{"project": "demo", "environment": "development", "name": name, "repository": "example/source", "architecture": "amd64", "connection_id": conn}
		if i == 1 {
			input["registry_credential"] = "my-registry"
		}
		created := gitConnectionCall(t, h, raw, "POST", "/builds", input, 201)
		ids = append(ids, created["id"].(string))
	}
	if err = s.ConfigureBuildRegistry(ctx, "cloud.example.test/internal"); err != nil {
		t.Fatal(err)
	}
	if err = s.backfillBuildRegistry(ctx); err != nil {
		t.Fatal(err)
	}
	migrated, err := s.readBuild(ctx, ids[0])
	if err != nil {
		t.Fatal(err)
	}
	if migrated.ManagedRegistry != "cloud.example.test/internal/"+ids[0] || migrated.Revision != 2 || migrated.InstalledRevision != 0 {
		t.Fatalf("backfill not idempotent: %+v", migrated)
	}
	explicit, err := s.readBuild(ctx, ids[1])
	if err != nil {
		t.Fatal(err)
	}
	if explicit.ManagedRegistry != "" || explicit.RegistryCredential != "my-registry" || explicit.Revision != 1 {
		t.Fatal("explicit registry changed")
	}
}

func TestManagedBuildRegistrySecretLive(t *testing.T) {
	path := os.Getenv("HAKOPOD_TEST_KUBECONFIG")
	if path == "" {
		t.Skip("named development Kubernetes fixture required")
	}
	config, err := clientcmd.LoadFromFile(path)
	if err != nil || config.CurrentContext != "k3d-hakopod-dev" {
		t.Fatal("refusing unnamed Kubernetes fixture")
	}
	db := sourceDatabase(t)
	kube, err := cluster.New(path, cluster.Options{RegistrySecretName: db.RegistrySecretName})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	raw, err := db.Bootstrap(ctx, "registry-live-fixture")
	if err != nil {
		t.Fatal(err)
	}
	principal, err := db.Authenticate(ctx, raw)
	if err != nil {
		t.Fatal(err)
	}
	ctx = context.WithValue(ctx, principalKey{}, principal)
	s := &Server{Store: db, Cluster: kube, BuildRegistry: "cloud.example.test/internal", Auth: AuthConfig{EncryptionKey: strings.Repeat("32", 32)}}
	c := buildConfig{ID: store.NewID(), Project: "demo", Environment: "development"}
	c.ManagedRegistry = s.BuildRegistry + "/" + c.ID
	t.Cleanup(func() { _ = kube.DeletePlatformSecret(context.Background(), "hp-build-registry-"+c.ID) })
	name, err := s.ensureBuildRegistry(ctx, c)
	if err != nil {
		t.Fatal(err)
	}
	again, err := s.ensureBuildRegistry(ctx, c)
	if err != nil || name != again {
		t.Fatal("credential was not reused", err)
	}
	credential, err := kube.RegistryCredential(ctx, c.Project, c.Environment, name, c.ManagedRegistry+":latest")
	if err != nil {
		t.Fatal(err)
	}
	if credential.Username != c.registryUsername("pull") || credential.Password != s.buildPullPassword(c) {
		t.Fatal("wrong pull credential")
	}
	if _, err = kube.RegistryCredential(ctx, "other", c.Environment, name, c.ManagedRegistry+":latest"); err == nil {
		t.Fatal("cross-project credential exposed")
	}
}
