package api_test

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/api"
	"github.com/hakopod/hakopod/internal/store"
)

func TestInstallationAgentCanonicalAdministration(t *testing.T) {
	db, _ := database(t)
	ctx := context.Background()
	bootstrap, err := db.Bootstrap(ctx, "installation-agent-fixture")
	if err != nil {
		t.Fatal(err)
	}
	owner, err := db.Authenticate(ctx, bootstrap)
	if err != nil {
		t.Fatal(err)
	}
	_, token, err := db.CreateKey(ctx, owner, store.KeyInput{Name: "installation-agent", Permissions: []string{"admin", "agent:admin"}, ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	handler := (&api.Server{Store: db}).Handler()
	call := func(method, path, raw, body string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, "/api/v1"+path, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+raw)
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	for _, path := range []string{"/users", "/teams", "/roles", "/organization/security"} {
		if w := call("GET", path, bootstrap, ""); w.Code != 403 {
			t.Fatalf("admin without explicit agent grant reached %s: %d", path, w.Code)
		}
		if w := call("GET", path, token, ""); w.Code != 200 {
			t.Fatalf("installation grant rejected %s: %d %s", path, w.Code, w.Body.String())
		}
	}
	w := call("POST", "/teams", token, `{"name":"Agent administration fixture"}`)
	if w.Code != 201 {
		t.Fatalf("create team: %d %s", w.Code, w.Body.String())
	}
	var team store.Team
	if err := json.Unmarshal(w.Body.Bytes(), &team); err != nil || team.ID == "" {
		t.Fatal("missing created team", err)
	}
	if w = call("GET", "/teams/"+team.ID+"/members", token, ""); w.Code != 200 {
		t.Fatalf("members: %d %s", w.Code, w.Body.String())
	}
	if w = call("DELETE", "/teams/"+team.ID, token, ""); w.Code != 200 {
		t.Fatalf("delete team: %d %s", w.Code, w.Body.String())
	}
	var count int
	if err := db.Pool.QueryRow(ctx, "SELECT count(*) FROM teams WHERE id=$1", team.ID).Scan(&count); err != nil || count != 0 {
		t.Fatal("team mutation not persisted", count, err)
	}
	_, credentialToken, err := db.CreateKey(ctx, owner, store.KeyInput{Name: "credential-administration", Permissions: []string{"admin", "agent:admin", "agent:credentials"}, ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	keyBody, err := json.Marshal(store.KeyInput{Name: "created-via-agent", Permissions: []string{"admin", "agent:admin"}, ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	for _, denied := range []string{bootstrap, token} {
		if response := call("POST", "/keys", denied, string(keyBody)); response.Code != 403 {
			t.Fatalf("credential creation without explicit grant returned %d", response.Code)
		}
	}
	created := call("POST", "/keys", credentialToken, string(keyBody))
	var keyResult struct {
		Raw      string    `json:"key"`
		Metadata store.Key `json:"metadata"`
	}
	if created.Code != 201 || created.Header().Get("Cache-Control") != "no-store" || json.Unmarshal(created.Body.Bytes(), &keyResult) != nil || !strings.HasPrefix(keyResult.Raw, "hp_") || keyResult.Metadata.ID == "" {
		t.Fatalf("credential creation failed with status %d", created.Code)
	}
	rotation, _ := json.Marshal(map[string]any{"expires_at": time.Now().Add(time.Hour)})
	if response := call("POST", "/keys/"+keyResult.Metadata.ID+"/rotate", token, string(rotation)); response.Code != 403 {
		t.Fatalf("credential rotation without explicit grant returned %d", response.Code)
	}
	if response := call("POST", "/keys/"+keyResult.Metadata.ID+"/rotate", credentialToken, string(rotation)); response.Code != 201 || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("credential rotation failed with status %d", response.Code)
	}
	if _, err := db.Pool.Exec(ctx, "UPDATE identities SET admin=false WHERE id=$1", owner.ID); err != nil {
		t.Fatal(err)
	}
	if w = call("GET", "/users", token, ""); w.Code != 403 {
		t.Fatalf("revoked administrator retained access: %d", w.Code)
	}
}
