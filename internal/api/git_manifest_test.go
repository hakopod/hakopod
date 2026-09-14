package api

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/hakopod/hakopod/internal/store"
)

func TestGitAppPublicOrigin(t *testing.T) {
	for _, raw := range []string{"", "http://hako.example.com", "https://localhost", "https://127.0.0.1", "https://10.0.0.1", "https://[::1]", "https://hako.local", "https://user@hako.example.com", "https://hako.example.com/path", "https://hako.example.com?x=1"} {
		s := Server{Auth: AuthConfig{PublicURL: raw}}
		if _, err := s.gitAppOrigin(); err == nil {
			t.Errorf("accepted invalid public URL %q", raw)
		}
	}
	s := Server{Auth: AuthConfig{PublicURL: "https://hako.example.com/"}}
	if origin, err := s.gitAppOrigin(); err != nil || origin != "https://hako.example.com" {
		t.Fatal(origin, err)
	}
}
func TestGitHubManifestBrowserSetup(t *testing.T) {
	db := sourceDatabase(t)
	ctx := context.Background()
	owner, err := db.SetupOwner(ctx, "App Owner", "app-owner@example.test", "long enough owner password", "")
	if err != nil {
		t.Fatal(err)
	}
	session, err := db.NewSession(ctx, owner.ID, "browser", "", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	other, err := db.NewSession(ctx, owner.ID, "browser", "", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	keyPEM := string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}))
	secret := strings.Repeat("provider-webhook-secret", 3)
	var connectionID string
	conversions := 0
	bad := ""
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/app-manifests/provider-code/conversions":
			conversions++
			if r.Method != "POST" || r.Header.Get("Authorization") != "" {
				t.Error("manifest conversion should use the one-time code, not user credentials")
			}
			write(w, 201, map[string]any{"id": 42, "slug": "hakopod-test-app", "pem": keyPEM, "webhook_secret": secret, "owner": map[string]string{"login": "engineering"}})
		case "/app":
			if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ey") {
				t.Error("missing App JWT")
			}
			write(w, 200, map[string]any{"id": 42})
		case "/app/installations/71":
			appID := 42
			account := "engineering"
			if bad == "app" {
				appID = 99
			}
			if bad == "owner" {
				account = "other-team"
			}
			permissions := map[string]string{"contents": "write", "metadata": "read", "actions": "write", "workflows": "write"}
			if bad == "permissions" {
				permissions["actions"] = "read"
			}
			out := map[string]any{"id": 71, "app_id": appID, "account": map[string]any{"id": 9, "login": account}, "permissions": permissions}
			if bad == "suspended" {
				out["suspended_at"] = "2026-01-01T00:00:00Z"
			}
			write(w, 200, out)
		case "/app/hook/config":
			hookURL := "https://hako.example.test/api/v1/webhooks/git/" + connectionID
			if bad == "webhook" {
				hookURL = "https://other.example.test/hook"
			}
			write(w, 200, map[string]string{"url": hookURL, "insecure_ssl": "0", "content_type": "json"})
		default:
			t.Errorf("unexpected provider request %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer remote.Close()
	s := &Server{Store: db, Auth: AuthConfig{EncryptionKey: strings.Repeat("17", 32), PublicURL: "https://hako.example.test"}, githubAPIURL: remote.URL, githubHTTP: remote.Client()}
	h := s.Handler()
	cli, err := db.NewSession(ctx, owner.ID, "cli", "demo", "development", []string{"admin"})
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/git/github/start", "/git/github/complete", "/git/github/install/complete", "/git/connections/fixture/github/setup"} {
		gitConnectionCall(t, h, cli.Token, "POST", path, map[string]any{}, 403)
	}
	setup := gitConnectionCall(t, h, session.Token, "GET", "/git/setup", nil, 200)
	if setup["github_available"] != true || setup["gitlab_callback_url"] != "https://hako.example.test/settings/git/callback" {
		t.Fatal(setup)
	}
	startBody := map[string]any{"name": "Engineering", "organization": "engineering", "builds": true}
	gitConnectionCall(t, h, "", "POST", "/git/github/start", startBody, 401)
	gitConnectionCall(t, h, session.Token, "POST", "/git/github/start", map[string]any{"name": "bad", "organization": "../escape"}, 400)
	s.Auth.PublicURL = "http://localhost:4173"
	gitConnectionCall(t, h, session.Token, "POST", "/git/github/start", startBody, 409)
	s.Auth.PublicURL = "https://hako.example.test"
	start := gitConnectionCall(t, h, session.Token, "POST", "/git/github/start", startBody, 200)
	connectionID = start["connection_id"].(string)
	gitConnectionCall(t, h, session.Token, "POST", "/git/github/start", startBody, 409)
	// Multiple unregistered App drafts do not collide on the absent provider ID.
	personal := gitConnectionCall(t, h, session.Token, "POST", "/git/github/start", map[string]any{"name": "Personal", "builds": false}, 200)
	if !strings.HasPrefix(personal["action_url"].(string), "https://github.com/settings/apps/new?") {
		t.Fatal("personal registration destination")
	}
	var personalManifest map[string]any
	json.Unmarshal([]byte(personal["manifest"].(string)), &personalManifest)
	if len(personalManifest["default_permissions"].(map[string]any)) != 2 {
		t.Fatal("source-only app overprivileged")
	}
	stateOf := func(out map[string]any) string {
		t.Helper()
		u, e := url.Parse(out["action_url"].(string))
		if e != nil {
			t.Fatal(e)
		}
		return u.Query().Get("state")
	}
	action, _ := url.Parse(start["action_url"].(string))
	if action.Host != "github.com" || action.Path != "/organizations/engineering/settings/apps/new" {
		t.Fatal("wrong registration destination")
	}
	var manifest map[string]any
	if json.Unmarshal([]byte(start["manifest"].(string)), &manifest) != nil {
		t.Fatal("invalid manifest")
	}
	if manifest["public"] != false || manifest["redirect_url"] != "https://hako.example.test/settings/git/github/callback" || manifest["setup_url"] != "https://hako.example.test/settings/git/github/installed" || manifest["hook_attributes"].(map[string]any)["url"] != "https://hako.example.test/api/v1/webhooks/git/"+connectionID {
		t.Fatal("manifest callbacks/ownership differ")
	}
	if manifest["default_permissions"].(map[string]any)["workflows"] != "write" {
		t.Fatal("build grant missing")
	}
	draft := gitConnectionCall(t, h, session.Token, "GET", "/git/connections/"+connectionID, nil, 200)
	if draft["enabled"] != false || draft["status"] != "setup_required" || draft["capabilities"].(map[string]any)["read_source"] != false {
		t.Fatal("draft usable before verified installation")
	}
	parallel := gitConnectionCall(t, h, session.Token, "POST", "/git/connections/"+connectionID+"/github/setup", map[string]any{}, 200)
	callback := map[string]any{"code": "provider-code", "state": stateOf(start)}
	// Wrong browser cannot consume the correct browser's challenge.
	gitConnectionCall(t, h, other.Token, "POST", "/git/github/complete", callback, 401)
	install := gitConnectionCall(t, h, session.Token, "POST", "/git/github/complete", callback, 200)
	gitConnectionCall(t, h, session.Token, "POST", "/git/github/complete", callback, 401)
	gitConnectionCall(t, h, session.Token, "POST", "/git/github/complete", map[string]any{"code": "provider-code", "state": stateOf(parallel)}, 409)
	if conversions != 1 || install["phase"] != "install" {
		t.Fatal("conversion replayed")
	}
	encoded := string(store.JSON(install))
	if strings.Contains(encoded, secret) || strings.Contains(encoded, "PRIVATE KEY") {
		t.Fatal("provider secrets exposed")
	}
	c, err := s.readGitConnection(ctx, connectionID)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(c.encrypted, []byte(secret)) || bytes.Contains(c.encrypted, []byte("PRIVATE KEY")) {
		t.Fatal("provider secrets stored unencrypted")
	}
	v, err := s.decodeGitCredentials(c)
	if err != nil || v.PrivateKey != keyPEM || v.WebhookSecret != secret {
		t.Fatal("credentials not durably saved")
	}
	// A new Server can resume after callback navigation or process restart.
	resumedServer := &Server{Store: db, Auth: s.Auth, githubAPIURL: remote.URL, githubHTTP: remote.Client()}
	h = resumedServer.Handler()
	resume := func() map[string]any {
		return gitConnectionCall(t, h, session.Token, "POST", "/git/connections/"+connectionID+"/github/setup", map[string]any{}, 200)
	}
	for _, issue := range []string{"app", "owner", "permissions", "suspended", "webhook"} {
		bad = issue
		next := resume()
		gitConnectionCall(t, h, session.Token, "POST", "/git/github/install/complete", map[string]any{"state": stateOf(next), "installation_id": 71}, 400)
		c, _ = s.readGitConnection(ctx, connectionID)
		if c.Enabled {
			t.Fatal("invalid installation enabled:", issue)
		}
	}
	bad = ""
	expired := resume()
	state := stateOf(expired)
	_, err = db.Pool.Exec(ctx, "UPDATE auth_challenges SET expires_at=now()-interval '1 second' WHERE id=$1", strings.Split(state, ".")[0])
	if err != nil {
		t.Fatal(err)
	}
	gitConnectionCall(t, h, session.Token, "POST", "/git/github/install/complete", map[string]any{"state": state, "installation_id": 71}, 401)
	next := resume()
	body := map[string]any{"state": stateOf(next), "installation_id": 71}
	gitConnectionCall(t, h, other.Token, "POST", "/git/github/install/complete", body, 401)
	complete := gitConnectionCall(t, h, session.Token, "POST", "/git/github/install/complete", body, 200)
	if complete["configured"] != true || complete["account"] != "engineering" || complete["managed_app"] != true || complete["webhook_path"] != "/api/v1/webhooks/git/"+connectionID {
		t.Fatal("installation not verified", complete)
	}
	if strings.Contains(string(store.JSON(complete)), secret) || strings.Contains(string(store.JSON(complete)), "PRIVATE KEY") {
		t.Fatal("completion leaked credential")
	}
	gitConnectionCall(t, h, session.Token, "POST", "/git/github/install/complete", body, 401)
	gitConnectionCall(t, h, session.Token, "POST", "/git/connections/"+connectionID+"/github/setup", map[string]any{}, 409)
	// Metadata edits preserve the managed webhook path and encrypted secrets.
	gitConnectionCall(t, h, session.Token, "PUT", "/git/connections/"+connectionID, map[string]any{"name": "Engineering updated", "provider": "github", "auth_kind": "github_app", "expected_revision": 3, "enabled": true}, 200)
	gitConnectionCall(t, h, session.Token, "PUT", "/git/connections/"+connectionID, map[string]any{"name": "Engineering updated", "provider": "github", "auth_kind": "github_app", "expected_revision": 4, "webhook_secret": strings.Repeat("replacement", 4)}, 409)
	for _, valid := range []bool{false, true} {
		ping := []byte(`{"zen":"App-level ping"}`)
		req := httptest.NewRequest("POST", "/api/v1/webhooks/git/"+connectionID, bytes.NewReader(ping))
		req.Header.Set("X-GitHub-Event", "ping")
		signing := secret
		if !valid {
			signing = "incorrect-secret"
		}
		mac := hmac.New(sha256.New, []byte(signing))
		mac.Write(ping)
		req.Header.Set("X-Hub-Signature-256", "sha256="+hex.EncodeToString(mac.Sum(nil)))
		response := httptest.NewRecorder()
		h.ServeHTTP(response, req)
		want := 401
		if valid {
			want = 200
		}
		if response.Code != want {
			t.Fatalf("App-level ping: got %d want %d", response.Code, want)
		}
	}
	var audit int
	if err = db.Pool.QueryRow(ctx, "SELECT count(*) FROM audit_events WHERE resource=$1 AND action='git.app.install'", connectionID).Scan(&audit); err != nil || audit != 1 {
		t.Fatal("installation audit missing", err)
	}
}
