package api

import (
	"bytes"
	"context"
	"crypto"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/spec"
	"github.com/hakopod/hakopod/internal/store"
)

func gitConnectionCall(t *testing.T, h http.Handler, key, method, path string, body any, want int) map[string]any {
	t.Helper()
	r := httptest.NewRequest(method, "/api/v1"+path, bytes.NewReader(store.JSON(body)))
	r.Header.Set("Authorization", "Bearer "+key)
	r.Header.Set("Idempotency-Key", "named-git-connection-review")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != want {
		t.Fatalf("%s %s: got %d want %d: %s", method, path, w.Code, want, w.Body.String())
	}
	var out map[string]any
	if json.Unmarshal(w.Body.Bytes(), &out) != nil {
		t.Fatal("invalid JSON response")
	}
	return out
}
func TestNamedGitConnectionsReferencesAndIsolation(t *testing.T) {
	db := sourceDatabase(t)
	ctx := context.Background()
	raw, err := db.Bootstrap(ctx, "named-git")
	if err != nil {
		t.Fatal(err)
	}
	principal, err := db.Authenticate(ctx, raw)
	if err != nil {
		t.Fatal(err)
	}
	seen := []string{}
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Header.Get("Authorization"))
		write(w, 200, map[string]string{"sha": strings.Repeat("a", 40)})
	}))
	defer remote.Close()
	legacy := func(context.Context) (map[string][]byte, error) {
		return map[string][]byte{"token": []byte("legacy-token"), "webhook-secret": bytes.Repeat([]byte("l"), 32)}, nil
	}
	s := &Server{Store: db, Auth: AuthConfig{EncryptionKey: strings.Repeat("17", 32)}, githubAPIURL: remote.URL, githubHTTP: remote.Client(), githubTestCredentials: legacy, gitlabTestCredentials: legacy}
	h := s.Handler()
	ids := []string{}
	secrets := []string{}
	for _, name := range []string{"one", "two"} {
		out := gitConnectionCall(t, h, raw, "POST", "/git/connections", map[string]any{"name": name, "provider": "github", "auth_kind": "token", "token": "private-" + name}, 201)
		ids = append(ids, out["id"].(string))
		secrets = append(secrets, out["webhook_secret"].(string))
		if strings.Contains(string(store.JSON(out)), "private-"+name) {
			t.Fatal("credential in response")
		}
	}
	var cipher []byte
	if err = db.Pool.QueryRow(ctx, "SELECT credentials FROM git_connections WHERE id=$1", ids[0]).Scan(&cipher); err != nil || bytes.Contains(cipher, []byte("private-one")) {
		t.Fatal("credentials not encrypted")
	}
	list := gitConnectionCall(t, h, raw, "GET", "/git/connections", nil, 200)
	encoded := string(store.JSON(list))
	if strings.Contains(encoded, secrets[0]) || strings.Contains(encoded, "private-one") {
		t.Fatal("listing exposed a credential")
	}
	gitConnectionCall(t, h, raw, "PUT", "/git/connections/"+ids[0], map[string]any{"name": "renamed", "provider": "github", "auth_kind": "token", "expected_revision": 1}, 200)
	gitConnectionCall(t, h, raw, "PUT", "/git/connections/"+ids[0], map[string]any{"name": "stale", "provider": "github", "auth_kind": "token", "expected_revision": 1}, 409)
	for _, id := range append(ids, "github-default") {
		var out any
		if err = s.githubGET(ctx, "/repos/example/repo/commits/main", &out, id); err != nil {
			t.Fatal(err)
		}
	}
	if strings.Join(seen, ",") != "Bearer private-one,Bearer private-two,Bearer legacy-token" {
		t.Fatal("provider request selected the wrong connection")
	}
	apps := []string{}
	for i, id := range ids {
		a, _ := spec.Normalize(spec.Application{Name: "connection-" + strconv.Itoa(i), Services: map[string]spec.Service{"web": {Image: "python:3.13-alpine", Port: 8080}}})
		d, e := db.Accept(ctx, principal, "demo", "development", a, 0, "named-source-"+id)
		if e != nil {
			t.Fatal(e)
		}
		apps = append(apps, d.ApplicationID)
		gitConnectionCall(t, h, raw, "PUT", "/applications/"+d.ApplicationID+"/source", map[string]any{"provider": "github", "connection_id": id, "repository": "example/repo", "branch": "main", "path": "hakopod.toml", "auto_deploy": true, "expected_source_revision": 0}, 200)
	}
	body := store.JSON(map[string]any{"ref": "refs/heads/main", "after": strings.Repeat("a", 40), "repository": map[string]string{"full_name": "example/repo"}})
	send := func(id, secret string, want int) {
		t.Helper()
		r := httptest.NewRequest("POST", "/api/v1/webhooks/git/"+id, bytes.NewReader(body))
		r.Header.Set("X-GitHub-Event", "push")
		r.Header.Set("X-GitHub-Delivery", "same-delivery-123")
		mac := hmac.New(sha256.New, []byte(secret))
		mac.Write(body)
		r.Header.Set("X-Hub-Signature-256", "sha256="+hex.EncodeToString(mac.Sum(nil)))
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	send(ids[1], secrets[0], 401)
	send(ids[0], secrets[0], 202)
	send(ids[0], secrets[0], 202)
	var count int
	if err = db.Pool.QueryRow(ctx, "SELECT count(*) FROM source_jobs WHERE connection_id=$1", ids[0]).Scan(&count); err != nil || count != 1 {
		t.Fatal("duplicate or missing first connection work")
	}
	if err = db.Pool.QueryRow(ctx, "SELECT count(*) FROM source_jobs WHERE application_id=$1", apps[1]).Scan(&count); err != nil || count != 0 {
		t.Fatal("webhook escaped connection scope")
	}
	send(ids[1], secrets[1], 202)
	if err = db.Pool.QueryRow(ctx, "SELECT count(*) FROM source_jobs").Scan(&count); err != nil || count != 2 {
		t.Fatal("different connections incorrectly shared delivery deduplication")
	}
	gitConnectionCall(t, h, raw, "DELETE", "/git/connections/"+ids[0]+"?expected_revision=2", nil, 409)
	gitConnectionCall(t, h, raw, "DELETE", "/git/connections/github-default?expected_revision=1", nil, 409)
	build := gitConnectionCall(t, h, raw, "POST", "/builds", map[string]any{"project": "demo", "environment": "development", "name": "named-build", "repository": "example/repo", "architecture": "arm64", "connection_id": ids[1]}, 201)
	c, e := s.readBuild(ctx, build["id"].(string))
	if e != nil || c.ConnectionID != ids[1] {
		t.Fatal("build connection not persisted")
	}
	gitConnectionCall(t, h, raw, "PUT", "/git/connections/"+ids[1], map[string]any{"name": "two", "provider": "github", "auth_kind": "token", "expected_revision": 1, "enabled": false}, 200)
	var out any
	if err = s.githubGET(ctx, "/repos/example/repo/commits/main", &out, ids[1]); err == nil {
		t.Fatal("disabled credential remained usable")
	}
	t.Log("encrypted CRUD, stable legacy defaults, optimistic updates, used-reference deletion and per-connection provider/webhook isolation verified")
}

func TestGitHubAppInstallationAuthentication(t *testing.T) {
	db := sourceDatabase(t)
	ctx := context.Background()
	raw, e := db.Bootstrap(ctx, "github-app")
	if e != nil {
		t.Fatal(e)
	}
	key, e := rsa.GenerateKey(rand.Reader, 2048)
	if e != nil {
		t.Fatal(e)
	}
	pemKey := string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}))
	minted := 0
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/repos/") {
			if r.Header.Get("Authorization") != "Bearer installation-token" {
				t.Error("source request did not use installation token")
			}
			write(w, 200, map[string]string{"sha": strings.Repeat("c", 40)})
			return
		}
		parts := strings.Split(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "), ".")
		if len(parts) != 3 {
			t.Error("App API missing JWT")
			w.WriteHeader(401)
			return
		}
		sig, _ := base64.RawURLEncoding.DecodeString(parts[2])
		digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
		if rsa.VerifyPKCS1v15(&key.PublicKey, crypto.SHA256, digest[:], sig) != nil {
			t.Error("invalid App JWT")
		}
		claimsBytes, _ := base64.RawURLEncoding.DecodeString(parts[1])
		var claims map[string]any
		json.Unmarshal(claimsBytes, &claims)
		if claims["iss"] != "42" || claims["exp"].(float64) > float64(time.Now().Add(10*time.Minute).Unix()) {
			t.Error("invalid JWT claims")
		}
		switch r.URL.Path {
		case "/app":
			write(w, 200, map[string]any{"id": 42})
		case "/app/installations/71":
			write(w, 200, map[string]any{"id": 71, "app_id": 42, "account": map[string]any{"id": 9, "login": "example"}})
		case "/app/installations/72":
			write(w, 200, map[string]any{"id": 72, "app_id": 99, "account": map[string]any{"id": 9, "login": "example"}})
		case "/app/installations/71/access_tokens":
			var in struct {
				Repositories []string          `json:"repositories"`
				Permissions  map[string]string `json:"permissions"`
			}
			json.NewDecoder(r.Body).Decode(&in)
			if len(in.Repositories) != 1 || in.Repositories[0] != "repo" || in.Permissions["contents"] != "read" {
				t.Error("installation token not downscoped")
			}
			minted++
			write(w, 201, map[string]any{"token": "installation-token", "expires_at": time.Now().Add(time.Hour)})
		default:
			w.WriteHeader(404)
		}
	}))
	defer remote.Close()
	s := &Server{Store: db, Auth: AuthConfig{EncryptionKey: strings.Repeat("17", 32)}, githubAPIURL: remote.URL, githubHTTP: remote.Client()}
	h := s.Handler()
	input := map[string]any{"name": "app", "provider": "github", "auth_kind": "github_app", "github_app_id": "42", "github_private_key": pemKey, "github_installation_id": 71}
	created := gitConnectionCall(t, h, raw, "POST", "/git/connections", input, 201)
	id := created["id"].(string)
	if created["account"] != "example" || created["webhook_path"] != "/api/v1/webhooks/github-app/42" {
		t.Fatal("installation identity not observed")
	}
	if strings.Contains(string(store.JSON(created)), "PRIVATE KEY") {
		t.Fatal("private key exposed")
	}
	var out any
	if e = s.githubGET(ctx, "/repos/example/repo/commits/main", &out, id); e != nil {
		t.Fatal(e)
	}
	if minted != 1 {
		t.Fatal("did not mint installation token")
	}
	if e = s.githubGET(ctx, "/repos/other/repo/commits/main", &out, id); e == nil {
		t.Fatal("installation account boundary missing")
	}
	input["name"] = "wrong-app"
	input["github_installation_id"] = 72
	gitConnectionCall(t, h, raw, "POST", "/git/connections", input, 400)
	body := store.JSON(map[string]any{"installation": map[string]int{"id": 72}})
	r := httptest.NewRequest("POST", "/api/v1/webhooks/git/"+id, bytes.NewReader(body))
	mac := hmac.New(sha256.New, []byte(created["webhook_secret"].(string)))
	mac.Write(body)
	r.Header.Set("X-Hub-Signature-256", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	r.Header.Set("X-GitHub-Event", "push")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal("wrong installation accepted", w.Code)
	}
	gitConnectionCall(t, h, raw, "DELETE", "/git/connections/"+id+"?expected_revision=1", nil, 200)
}

func TestGitConnectionAdminBoundary(t *testing.T) {
	db := sourceDatabase(t)
	ctx := context.Background()
	owner, e := db.SetupOwner(ctx, "Owner", "git-owner@example.test", "owner password long enough", "")
	if e != nil {
		t.Fatal(e)
	}
	session, e := db.NewSession(ctx, owner.ID, "browser", "", "", nil)
	if e != nil {
		t.Fatal(e)
	}
	_, invite, e := db.CreateInvite(ctx, session.User, "git-developer@example.test", "", "demo", "developer")
	if e != nil {
		t.Fatal(e)
	}
	id, e := db.AcceptInvite(ctx, invite, "Developer", "developer password long enough", nil)
	if e != nil {
		t.Fatal(e)
	}
	developer, e := db.NewSession(ctx, id, "browser", "", "", nil)
	if e != nil {
		t.Fatal(e)
	}
	s := &Server{Store: db}
	h := s.Handler()
	for _, method := range []string{"GET", "POST"} {
		gitConnectionCall(t, h, developer.Token, method, "/git/connections", map[string]any{}, 403)
	}
	gitConnectionCall(t, h, developer.Token, "PUT", "/git/connections/github-default", map[string]any{}, 403)
	gitConnectionCall(t, h, developer.Token, "DELETE", "/git/connections/github-default?expected_revision=1", nil, 403)
}
