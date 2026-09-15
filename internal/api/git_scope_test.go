package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/store"
)

func TestDelegatedGitCallbacksRemainBoundToCloudBrowser(t *testing.T) {
	db := sourceDatabase(t)
	ctx := context.Background()
	raw, err := db.Bootstrap(ctx, "callbacks")
	if err != nil {
		t.Fatal(err)
	}
	admin, err := db.Authenticate(ctx, raw)
	if err != nil {
		t.Fatal(err)
	}
	_, key, err := db.CreateKey(ctx, admin, store.KeyInput{Name: "git", Project: "demo", Environment: "development", Permissions: []string{"deployments:read", "deployments:write", "git:manage"}, ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	principal, err := db.Authenticate(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{Store: db, Auth: AuthConfig{EncryptionKey: strings.Repeat("17", 32), PublicURL: "https://cloud.example.test", GitWebhookPrefix: "https://cloud.example.test/api/v1/webhooks/nodes/" + strings.Repeat("c", 32)}}
	h := s.Handler()
	call := func(path, session string, body any) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/api/v1"+path, bytes.NewReader(store.JSON(body)))
		r.Header.Set("Authorization", "Bearer "+key)
		r.Header.Set("X-Hakopod-Git-Session", session)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	sessionA, sessionB := strings.Repeat("a", 64), strings.Repeat("b", 64)
	started := call("/git/github/start", sessionA, map[string]any{"name": "Scoped App"})
	if started.Code != 200 {
		t.Fatal(started.Code, started.Body.String())
	}
	var result map[string]any
	if json.Unmarshal(started.Body.Bytes(), &result) != nil {
		t.Fatal("invalid start response")
	}
	action, err := url.Parse(result["action_url"].(string))
	if err != nil {
		t.Fatal(err)
	}
	var manifest map[string]any
	json.Unmarshal([]byte(result["manifest"].(string)), &manifest)
	if manifest["hook_attributes"].(map[string]any)["url"] != s.Auth.GitWebhookPrefix+"/git/"+result["connection_id"].(string) {
		t.Fatal("BYO webhook path missing")
	}
	rejected := call("/git/github/complete", sessionB, map[string]any{"code": "fixture-code", "state": action.Query().Get("state")})
	if rejected.Code != 401 {
		t.Fatal("cross-session App callback", rejected.Code)
	}
	challenge, err := db.NewChallenge(ctx, "git-source-oauth", gitOAuthPending{ConnectionID: result["connection_id"].(string), KeyID: principal.KeyID + ":" + sessionA}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	rejected = call("/git/oauth/complete", sessionB, map[string]any{"code": "fixture-code", "state": challenge})
	if rejected.Code != 403 {
		t.Fatal("cross-session OAuth callback", rejected.Code)
	}
}
