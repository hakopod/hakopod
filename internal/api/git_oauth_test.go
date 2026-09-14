package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/spec"
	"github.com/hakopod/hakopod/internal/store"
)

func TestGitLabOAuthPKCEAndRefresh(t *testing.T) {
	db := sourceDatabase(t)
	ctx := context.Background()
	owner, err := db.SetupOwner(ctx, "GitLab Operator", "oauth-source@example.test", "long enough operator password", "")
	if err != nil {
		t.Fatal(err)
	}
	session, err := db.NewSession(ctx, owner.ID, "browser", "", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	var refreshes atomic.Int32
	var failRefresh atomic.Bool
	challenge := ""
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/oauth/token" {
			if r.ParseForm() != nil || r.Form.Get("client_id") != "local-client" || r.Form.Get("client_secret") != "local-client-secret" {
				t.Error("missing OAuth client authentication")
				w.WriteHeader(400)
				return
			}
			if r.Form.Get("grant_type") == "authorization_code" {
				sum := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
				if base64.RawURLEncoding.EncodeToString(sum[:]) != challenge || r.Form.Get("redirect_uri") != "https://hako.example.test/settings/git/callback" {
					t.Error("PKCE or callback mismatch")
				}
				write(w, 200, map[string]any{"access_token": "access-one", "refresh_token": "refresh-one", "expires_in": 7200, "token_type": "Bearer", "scope": "api"})
				return
			}
			refreshes.Add(1)
			if failRefresh.Load() {
				w.WriteHeader(503)
				w.Write([]byte("private-token-provider-error"))
				return
			}
			if r.Form.Get("refresh_token") != "refresh-one" {
				t.Error("refresh token raced or was replayed")
			}
			write(w, 200, map[string]any{"access_token": "access-two", "refresh_token": "refresh-two", "expires_in": 7200, "token_type": "Bearer", "scope": "api"})
			return
		}
		if r.URL.Path == "/user" {
			if r.Header.Get("Authorization") != "Bearer access-one" {
				t.Error("identity request used wrong token")
			}
			write(w, 200, map[string]any{"id": 23, "username": "verified-gitlab-user"})
			return
		}
		if r.Header.Get("Authorization") != "Bearer access-two" || r.Header.Get("PRIVATE-TOKEN") != "" {
			t.Error("OAuth request did not use refreshed bearer token")
		}
		write(w, 200, map[string]any{"id": "observed"})
	}))
	defer remote.Close()
	s := &Server{Store: db, Auth: AuthConfig{EncryptionKey: strings.Repeat("17", 32), PublicURL: "https://hako.example.test"}, gitlabAPIURL: remote.URL, gitlabHTTP: remote.Client()}
	h := s.Handler()
	created := gitConnectionCall(t, h, session.Token, "POST", "/git/connections", map[string]any{"name": "GitLab OAuth", "provider": "gitlab", "auth_kind": "gitlab_oauth", "oauth_client_id": "local-client", "oauth_client_secret": "local-client-secret"}, 201)
	id := created["id"].(string)
	if created["configured"] != false || created["status"] != "reauthorize" {
		t.Fatal("pending connection presented as configured")
	}
	start := gitConnectionCall(t, h, session.Token, "POST", "/git/connections/"+id+"/authorize", map[string]any{}, 200)
	u, err := url.Parse(start["authorization_url"].(string))
	if err != nil {
		t.Fatal(err)
	}
	challenge = u.Query().Get("code_challenge")
	state := u.Query().Get("state")
	if u.Query().Get("code_challenge_method") != "S256" || challenge == "" || u.Query().Get("scope") != "api" {
		t.Fatal("authorization request missing scopes or PKCE")
	}
	done := gitConnectionCall(t, h, session.Token, "POST", "/git/oauth/complete", map[string]string{"code": "local-authorization-code", "state": state}, 200)
	if done["account"] != "verified-gitlab-user" || done["configured"] != true {
		t.Fatal("authorized connection identity missing")
	}
	if strings.Contains(string(store.JSON(done)), "access-one") || strings.Contains(string(store.JSON(done)), "refresh-one") {
		t.Fatal("OAuth token exposed")
	}
	gitConnectionCall(t, h, session.Token, "POST", "/git/oauth/complete", map[string]string{"code": "local-authorization-code", "state": state}, 401)
	expire := func() {
		t.Helper()
		c, e := s.readGitConnection(ctx, id)
		if e != nil {
			t.Fatal(e)
		}
		v, e := s.decodeGitCredentials(c)
		if e != nil {
			t.Fatal(e)
		}
		v.ExpiresAt = time.Now().Add(-time.Minute)
		encrypted, e := s.encodeGitCredentials(id, v)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = db.Pool.Exec(ctx, "UPDATE git_connections SET credentials=$2 WHERE id=$1", id, encrypted); e != nil {
			t.Fatal(e)
		}
	}
	expire()
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); var out any; errs <- s.gitlabGET(ctx, "/projects/example", &out, id) }()
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	if refreshes.Load() != 1 {
		t.Fatal("concurrent reads rotated tokens more than once")
	}
	c, e := s.readGitConnection(ctx, id)
	if e != nil {
		t.Fatal(e)
	}
	v, e := s.decodeGitCredentials(c)
	if e != nil || v.Token != "access-two" || v.RefreshToken != "refresh-two" || c.Revision != 2 {
		t.Fatal("token pair was not persisted atomically or refresh invalidated reviews")
	}
	expire()
	failRefresh.Store(true)
	_, e = s.gitOAuthToken(ctx, id)
	if e == nil || strings.Contains(e.Error(), "private-token") {
		t.Fatal("failed refresh did not fail safely")
	}
	_, e = s.gitOAuthToken(ctx, id)
	if e == nil || refreshes.Load() != 2 {
		t.Fatal("possibly consumed refresh token replayed")
	}
	status := gitConnectionCall(t, h, session.Token, "GET", "/git/connections/"+id, nil, 200)
	if status["status"] != "reauthorize" {
		t.Fatal("refresh failure not visible")
	}
	// Decoder failures must not reflect a malformed credential field name.
	req := httptest.NewRequest("POST", "/api/v1/git/connections", strings.NewReader(`{"secret-field-contents":"value"}`))
	req.Header.Set("Authorization", "Bearer "+session.Token)
	res := httptest.NewRecorder()
	h.ServeHTTP(res, req)
	if res.Code != 400 || strings.Contains(res.Body.String(), "secret-field-contents") {
		t.Fatal("credential decoder reflected user input")
	}
	t.Log("source-specific PKCE, same-session completion, token redaction, single refresh across concurrent calls, atomic rotation and fail-closed interruption verified")
}

func TestNamedSourceImportBindsConnectionRevision(t *testing.T) {
	db := sourceDatabase(t)
	ctx := context.Background()
	raw, e := db.Bootstrap(ctx, "named-import")
	if e != nil {
		t.Fatal(e)
	}
	content := "schema_version=1\nname='named-import'\n[services.web]\nimage='python:3.13-alpine'\nport=8080\n"
	sha := strings.Repeat("a", 40)
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer named-import-token" {
			t.Error("import selected wrong connection")
		}
		if strings.Contains(r.URL.Path, "/commits/") {
			write(w, 200, map[string]string{"sha": sha})
			return
		}
		write(w, 200, map[string]any{"type": "file", "size": len(content), "encoding": "base64", "content": base64.StdEncoding.EncodeToString([]byte(content))})
	}))
	defer remote.Close()
	s := &Server{Store: db, Auth: AuthConfig{EncryptionKey: strings.Repeat("17", 32)}, githubAPIURL: remote.URL, githubHTTP: remote.Client()}
	h := s.Handler()
	created := gitConnectionCall(t, h, raw, "POST", "/git/connections", map[string]any{"name": "Import", "provider": "github", "auth_kind": "token", "token": "named-import-token"}, 201)
	id := created["id"].(string)
	input := map[string]any{"project": "demo", "environment": "development", "provider": "github", "connection_id": id, "repository": "example/repo", "branch": "main", "path": "hakopod.toml", "auto_deploy": true}
	review := gitConnectionCall(t, h, raw, "POST", "/sources/plan", input, 200)
	gitConnectionCall(t, h, raw, "PUT", "/git/connections/"+id, map[string]any{"name": "Renamed", "provider": "github", "auth_kind": "token", "expected_revision": 1}, 200)
	gitConnectionCall(t, h, raw, "POST", "/sources/deploy", map[string]any{"review_token": review["review_token"]}, 409)
	review = gitConnectionCall(t, h, raw, "POST", "/sources/plan", input, 200)
	deployment := gitConnectionCall(t, h, raw, "POST", "/sources/deploy", map[string]any{"review_token": review["review_token"]}, 202)
	binding, e := s.readSource(ctx, deployment["application_id"].(string))
	if e != nil || binding.ConnectionID != id {
		t.Fatal("accepted import lost explicit connection")
	}
	gitConnectionCall(t, h, raw, "PUT", "/git/connections/"+id, map[string]any{"name": "Renamed", "provider": "github", "auth_kind": "token", "expected_revision": 2, "enabled": false}, 200)
	replay := gitConnectionCall(t, h, raw, "POST", "/sources/deploy", map[string]any{"review_token": review["review_token"]}, 202)
	if replay["id"] != deployment["id"] {
		t.Fatal("accepted import replay lost durable result")
	}
	var audit []byte
	if e = db.Pool.QueryRow(ctx, "SELECT metadata FROM audit_events WHERE action='source.import' ORDER BY id DESC LIMIT 1").Scan(&audit); e != nil || bytes.Contains(audit, []byte("named-import-token")) {
		t.Fatal("import audit contains credentials")
	}
}

func TestGitOAuthCompletionRequiresInitiatingSession(t *testing.T) {
	db := sourceDatabase(t)
	ctx := context.Background()
	owner, e := db.SetupOwner(ctx, "Operator", "bound-oauth@example.test", "operator password long enough", "")
	if e != nil {
		t.Fatal(e)
	}
	first, e := db.NewSession(ctx, owner.ID, "browser", "", "", nil)
	if e != nil {
		t.Fatal(e)
	}
	other, e := db.NewSession(ctx, owner.ID, "browser", "", "", nil)
	if e != nil {
		t.Fatal(e)
	}
	s := &Server{Store: db, Auth: AuthConfig{EncryptionKey: strings.Repeat("17", 32), PublicURL: "https://hako.example.test"}}
	h := s.Handler()
	c := gitConnectionCall(t, h, first.Token, "POST", "/git/connections", map[string]any{"name": "Bound", "provider": "gitlab", "auth_kind": "gitlab_oauth", "oauth_client_id": "client", "oauth_client_secret": "secret"}, 201)
	start := gitConnectionCall(t, h, first.Token, "POST", "/git/connections/"+c["id"].(string)+"/authorize", map[string]any{}, 200)
	u, _ := url.Parse(start["authorization_url"].(string))
	gitConnectionCall(t, h, other.Token, "POST", "/git/oauth/complete", map[string]string{"code": "code", "state": u.Query().Get("state")}, 403)
}

func TestSourceImportConnectionRevocationDuringFetch(t *testing.T) {
	db := sourceDatabase(t)
	ctx := context.Background()
	raw, e := db.Bootstrap(ctx, "source-race")
	if e != nil {
		t.Fatal(e)
	}
	var revoke atomic.Bool
	connectionID := ""
	content := "schema_version=1\nname='raced-source'\n[services.web]\nimage='python:3.13-alpine'\nport=8080\n"
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/commits/") {
			write(w, 200, map[string]string{"sha": strings.Repeat("a", 40)})
			return
		}
		if revoke.Load() {
			if _, e := db.Pool.Exec(ctx, "UPDATE git_connections SET enabled=false,revision=revision+1 WHERE id=$1", connectionID); e != nil {
				t.Error(e)
			}
		}
		write(w, 200, map[string]any{"type": "file", "size": len(content), "encoding": "base64", "content": base64.StdEncoding.EncodeToString([]byte(content))})
	}))
	defer remote.Close()
	s := &Server{Store: db, Auth: AuthConfig{EncryptionKey: strings.Repeat("17", 32)}, githubAPIURL: remote.URL, githubHTTP: remote.Client()}
	h := s.Handler()
	c := gitConnectionCall(t, h, raw, "POST", "/git/connections", map[string]any{"name": "Race", "provider": "github", "auth_kind": "token", "token": "local-race-token"}, 201)
	connectionID = c["id"].(string)
	review := gitConnectionCall(t, h, raw, "POST", "/sources/plan", map[string]any{"project": "demo", "environment": "development", "provider": "github", "connection_id": connectionID, "repository": "example/repo", "branch": "main", "path": "hakopod.toml", "auto_deploy": false}, 200)
	revoke.Store(true)
	gitConnectionCall(t, h, raw, "POST", "/sources/deploy", map[string]any{"review_token": review["review_token"]}, 409)
	principal, e := db.Authenticate(ctx, raw)
	if e != nil {
		t.Fatal(e)
	}
	application, e := spec.Parse([]byte(content))
	if e != nil {
		t.Fatal(e)
	}
	source := store.InitialSource{Provider: "github", ConnectionID: connectionID, ExpectedConnectionRevision: 1, Repository: "example/repo", Branch: "main", Path: "hakopod.toml", CommitSHA: strings.Repeat("a", 40)}
	if _, e = db.AcceptSourceImport(ctx, principal, "demo", "development", application, source, "atomic-connection-check"); !errors.Is(e, store.ErrForbidden) {
		t.Fatal("store accepted a disabled connection", e)
	}
	if _, e = db.Pool.Exec(ctx, "UPDATE git_connections SET enabled=true WHERE id=$1", connectionID); e != nil {
		t.Fatal(e)
	}
	if _, e = db.AcceptSourceImport(ctx, principal, "demo", "development", application, source, "atomic-connection-check"); !errors.Is(e, store.ErrConflict) {
		t.Fatal("store accepted a stale connection review", e)
	}
	var count int
	if e = db.Pool.QueryRow(ctx, "SELECT count(*) FROM applications WHERE name='raced-source'").Scan(&count); e != nil || count != 0 {
		t.Fatal("rejected import leaked a partial application")
	}
}
