package api_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/api"
	"github.com/hakopod/hakopod/internal/store"
)

func TestSecretProviderAPIAdminScopeAndCredentialRedaction(t *testing.T) {
	db, _ := database(t)
	ctx := context.Background()
	raw, err := db.Bootstrap(ctx, "provider-api-test")
	if err != nil {
		t.Fatal(err)
	}
	principal, err := db.Authenticate(ctx, raw)
	if err != nil {
		t.Fatal(err)
	}
	_, reader, err := db.CreateKey(ctx, principal, store.KeyInput{Name: "reader", Permissions: []string{"deployments:read"}, Project: "demo", Environment: "development", ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer((&api.Server{Store: db, Auth: api.AuthConfig{EncryptionKey: base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32))}}).Handler())
	defer server.Close()
	request := func(method, path, token string, body any) (int, string) {
		t.Helper()
		req, _ := http.NewRequest(method, server.URL+"/api/v1"+path, bytes.NewReader(store.JSON(body)))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		res, err := server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		data, _ := io.ReadAll(res.Body)
		return res.StatusCode, string(data)
	}
	input := map[string]any{"kind": "vault", "endpoint": "https://vault.example.com", "mount": "secret", "root_path": "shop", "scopes": []any{map[string]any{"project": "demo", "environments": []string{"development"}}}, "credentials": map[string]string{"token": "do-not-return-token"}, "expected_revision": 0}
	if code, _ := request("PUT", "/secret-providers/company", reader, input); code != 403 {
		t.Fatalf("non-admin write status: %d", code)
	}
	code, body := request("PUT", "/secret-providers/company", raw, input)
	if code != 201 || strings.Contains(body, "do-not-return-token") || strings.Contains(body, "credentials") {
		t.Fatalf("create response not safe: %d", code)
	}
	for _, path := range []string{"/secret-providers", "/secret-providers/company"} {
		code, body = request("GET", path, raw, nil)
		if code != 200 || strings.Contains(body, "do-not-return-token") || strings.Contains(body, "credentials") {
			t.Fatalf("read response not safe: %d", code)
		}
	}
	code, body = request("GET", "/secret-providers?project=demo&environment=development", reader, nil)
	if code != 200 || !strings.Contains(body, "company") || strings.Contains(body, "endpoint") || strings.Contains(body, "scopes") {
		t.Fatal("scoped discovery leaked admin metadata or omitted provider")
	}
	if code, _ = request("GET", "/secret-providers?project=other&environment=development", reader, nil); code != 403 {
		t.Fatal("cross-project discovery accepted")
	}
	if code, _ = request("GET", "/secret-providers/company", reader, nil); code != 403 {
		t.Fatal("provider configuration visible to deployment key")
	}
	delete(input, "credentials")
	input["expected_revision"] = 1
	code, body = request("PUT", "/secret-providers/company", raw, input)
	if code != 200 {
		t.Fatalf("preserve credentials update failed: %d", code)
	}
	var response map[string]any
	if json.Unmarshal([]byte(body), &response) != nil || response["revision"] != float64(2) {
		t.Fatal("revision not incremented")
	}
	if code, _ = request("PUT", "/secret-providers/company", raw, input); code != 409 {
		t.Fatal("stale API write accepted")
	}
	if code, _ = request("DELETE", "/secret-providers/company", raw, map[string]int{"expected_revision": 2}); code != 200 {
		t.Fatalf("delete failed: %d", code)
	}
}
