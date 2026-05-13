package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/store"
)

func TestDeviceLoginUsesBrowserApprovalAndScopedHumanToken(t *testing.T) {
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			t.Error("saved machine credential leaked into public browser login")
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/auth/device/start":
			var body struct {
				Project, Environment string
				Permissions          []string
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			if body.Project != "demo" || body.Environment != "development" || len(body.Permissions) != 1 || body.Permissions[0] != "deployments:read" {
				t.Error("device scope omitted")
			}
			json.NewEncoder(w).Encode(map[string]any{"device_code": strings.Repeat("d", 64), "user_code": "ABCD-EFGH", "verification_uri_complete": server.URL + "/login/device?user_code=ABCD-EFGH", "expires_in": 60, "interval": 5})
		case "/api/v1/auth/device/token":
			json.NewEncoder(w).Encode(store.Session{Token: "hs_" + strings.Repeat("1", 32) + "_" + strings.Repeat("2", 64), User: store.Principal{ID: "identity", Email: "user@example.test", CredentialType: "cli", Project: "demo", Environment: "development", Permissions: []string{"deployments:read"}}, ExpiresAt: time.Now().Add(time.Hour)})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	session, err := deviceLogin(ctx, config{URL: server.URL, Key: "old-private-machine-key", Project: "demo", Environment: "development"}, true, []string{"deployments:read"})
	if err != nil {
		t.Fatal(err)
	}
	if session.User.CredentialType != "cli" || !strings.HasPrefix(session.Token, "hs_") {
		t.Fatal("machine credential returned for human login")
	}
}
func TestDeviceFlagIsBoolean(t *testing.T) {
	got := reorder([]string{"--no-browser", "--project", "demo"})
	if len(got) != 3 || got[1] != "--project" {
		t.Fatalf("no-browser consumed a positional flag: %v", got)
	}
}
