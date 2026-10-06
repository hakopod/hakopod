package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDatabaseConnectionCLIUsesScopedPasswordReference(t *testing.T) {
	id := strings.Repeat("a", 32)
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != "POST" || r.URL.Path != "/api/v1/databases/"+id+"/connection-plan" {
			t.Error("wrong connection planning endpoint")
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		password, ok := body["password"].(map[string]any)
		if !ok || len(password) != 1 || password["ref"] != "infisical-password" || body["username"] != "infisical_user" || body["database"] != "infisical" || body["ssl_mode"] != "verify-full" {
			t.Error("custom binding options or password reference were lost")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"review-fixture"}`))
	}))
	defer server.Close()
	c := &client{url: server.URL, http: server.Client()}
	flags := databaseConnectionFlags{ApplicationID: "app-fixture", Service: "api", Variable: "DATABASE_URL", Endpoint: "read_write", Username: "infisical_user", Database: "infisical", PasswordSecret: "infisical-password", SSLMode: "verify-full"}
	if err := databaseCommand(context.Background(), c, "demo", "development", []string{"connection-plan", id}, "", "", "", "", "", 0, flags); err != nil {
		t.Fatal(err)
	}
	flags.PasswordSecret = "invalid reference"
	if err := databaseCommand(context.Background(), c, "demo", "development", []string{"connection-plan", id}, "", "", "", "", "", 0, flags); err == nil {
		t.Fatal("invalid secret reference reached planning")
	}
	if err := databaseCommand(context.Background(), c, "demo", "development", []string{"connect", id}, "", "", "review-fixture", "", "app-fixture", 0, flags); err == nil {
		t.Fatal("connect accepted changes outside the reviewed plan")
	}
	if calls != 1 {
		t.Fatalf("expected one reviewed request, got %d", calls)
	}
}
