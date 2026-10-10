package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/database"
	"github.com/hakopod/hakopod/internal/store"
)

func TestExplorerPublicPaths(t *testing.T) {
	for _, path := range []string{"//evil.test", "/internal/hakopod/sync", "/api/auth/session", "/api/%2e%2e/internal", "/../internal", "/api/connections?target=other", "/api\\query"} {
		if explorerPublicPath("GET", path) {
			t.Fatal("unsafe path accepted", path)
		}
	}
	for _, path := range []string{"/connections/", "/api/session", "/api/connections", "/_next/static/chunks/app.js"} {
		if !explorerPublicPath("GET", path) {
			t.Fatal("public path rejected", path)
		}
	}
	if explorerPublicPath("POST", "/connections/") || explorerPublicPath("POST", "/api/setup") {
		t.Fatal("invalid write accepted")
	}
}
func TestExplorerFingerprintTracksCredentialsNotObservationTime(t *testing.T) {
	now := time.Now()
	d := database.Resource{ID: "fixture", Revision: 1, EncryptedCredentials: []byte("one"), Observation: database.Observation{TLS: &database.TLSObservation{Fingerprint: "tls", CheckedAt: &now}}}
	original := explorerFingerprint(d)
	later := now.Add(time.Minute)
	d.Observation.TLS.CheckedAt = &later
	if explorerFingerprint(d) != original {
		t.Fatal("observation time changed source authority")
	}
	d.EncryptedCredentials = []byte("two")
	if explorerFingerprint(d) == original {
		t.Fatal("credential rotation retained old source authority")
	}
}
func TestExplorerLiveAuthorityRejectsRevocationAndWrites(t *testing.T) {
	target := &explorerTarget{Scope: "scope", Project: "demo", Environment: "development", key: strings.Repeat("k", 64)}
	allowed := true
	p := store.Principal{ID: "alice", Name: "Alice", CredentialType: "browser", Project: "demo", Environment: "development", Permissions: []string{"deployments:read", "databases:query"}, IdentityPermissions: []string{"deployments:read", "databases:query"}}
	s := &Server{explorerRuntime: &explorerRuntime{tickets: map[string]*explorerTicket{"ticket": {target: target, expires: time.Now().Add(time.Minute), session: strings.Repeat("s", 64), authorize: func(context.Context) (store.Principal, string, string, string, error) {
		if !allowed {
			return p, "", "", "", store.ErrForbidden
		}
		return p, p.ID, p.Name, "", nil
	}}}}}
	call := func(key string, write bool) int {
		body, _ := json.Marshal(map[string]any{"version": 1, "scope": "scope", "ticket": "ticket", "write": write})
		r := httptest.NewRequest("POST", "/internal/database-explorer/authorize", strings.NewReader(string(body)))
		r.Header.Set("Authorization", "Bearer "+key)
		w := httptest.NewRecorder()
		s.authorizeExplorer(w, r)
		return w.Code
	}
	if code := call(target.key, false); code != 200 {
		t.Fatal(code)
	}
	if call("wrong", false) != 403 || call(target.key, true) != 403 {
		t.Fatal("authority or write grant bypassed")
	}
	allowed = false
	if call(target.key, false) != 403 {
		t.Fatal("revoked session accepted")
	}
}
func TestExplorerCloudIdentityAndScopeAreVerified(t *testing.T) {
	remote := explorerCloudIdentity{Version: 1, Workspace: "workspace", Project: "demo", Environment: "development", Actor: "alice", Name: "Alice", Session: strings.Repeat("s", 64), Permissions: []string{"deployments:read", "databases:query"}}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+strings.Repeat("t", 64) {
			w.WriteHeader(403)
			return
		}
		_ = json.NewEncoder(w).Encode(remote)
	}))
	defer upstream.Close()
	t.Setenv("HAKOPOD_EXPLORER_CLOUD_AUTHORITY_URL", upstream.URL)
	s := &Server{explorerRuntime: &explorerRuntime{client: upstream.Client()}}
	p := store.Principal{ID: "machine-owner", CredentialType: "machine", Project: "demo", Environment: "development", Permissions: []string{"deployments:read", "databases:query", "databases:write-query"}, IdentityPermissions: []string{"deployments:read", "databases:query", "databases:write-query"}}
	target := &explorerTarget{CloudWorkspace: "workspace", Project: "demo", Environment: "development"}
	granted, actor, err := s.explorerCloudPrincipal(context.Background(), p, target, strings.Repeat("t", 64))
	if err != nil || actor.Actor != "alice" || !granted.RuntimeScoped {
		t.Fatal("valid delegated actor rejected", err)
	}
	if databaseQueryAllowed(granted, database.Resource{Project: "demo", Environment: "development"}, false) {
		t.Fatal("node write grant exceeded caller grant")
	}
	remote.Workspace = "foreign"
	if _, _, err = s.explorerCloudPrincipal(context.Background(), p, target, strings.Repeat("t", 64)); err == nil {
		t.Fatal("foreign workspace accepted")
	}
}
