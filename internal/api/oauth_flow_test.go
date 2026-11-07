package api_test

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/hakopod/hakopod/internal/api"
	"github.com/hakopod/hakopod/internal/store"
)

func TestOAuthAuthorizationCodeReachesMCP(t *testing.T) {
	db, _ := database(t)
	ctx := context.Background()
	id := store.NewID()
	if _, err := db.Pool.Exec(ctx, "INSERT INTO projects(name) VALUES('demo'); INSERT INTO environments(project,name) VALUES('demo','development')"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(ctx, "INSERT INTO identities(id,name,email,admin,permissions,email_verified) VALUES($1,'OAuth fixture','oauth@example.test',true,ARRAY['admin'],true)", id); err != nil {
		t.Fatal(err)
	}
	browser, err := db.NewSession(ctx, id, "browser", "", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	handler := (&api.Server{Store: db, Auth: api.AuthConfig{PublicURL: "https://hako.example.com"}}).Handler()
	call := func(method, path, contentType, token, body string, out any) int {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", contentType)
		r.Header.Set("Accept", "application/json, text/event-stream")
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if out != nil {
			_ = json.Unmarshal(w.Body.Bytes(), out)
		}
		return w.Code
	}
	const redirect = "http://127.0.0.1:9000/callback"
	var client struct {
		ClientID string `json:"client_id"`
		Method   string `json:"token_endpoint_auth_method"`
	}
	if status := call("POST", "/api/v1/oauth/register", "application/json", "", `{"client_name":"Claude","redirect_uris":["`+redirect+`"],"token_endpoint_auth_method":"none","scope":"ignored"}`, &client); status != 201 || client.ClientID == "" || client.Method != "none" {
		t.Fatal(status, client)
	}
	verifier := strings.Repeat("a", 64)
	sum := sha256.Sum256([]byte(verifier))
	authorize, _ := json.Marshal(map[string]string{"client_id": client.ClientID, "redirect_uri": redirect, "response_type": "code", "code_challenge": base64.RawURLEncoding.EncodeToString(sum[:]), "code_challenge_method": "S256", "resource": "https://hako.example.com/api/v1/mcp?project=demo&environment=development"})
	var grant struct {
		DeviceCode  string   `json:"device_code"`
		UserCode    string   `json:"user_code"`
		Permissions []string `json:"permissions"`
	}
	if status := call("POST", "/api/v1/oauth/authorize", "application/json", browser.Token, string(authorize), &grant); status != 200 || grant.DeviceCode == "" {
		t.Fatal(status, grant)
	}
	if status := call("POST", "/api/v1/auth/device/approve", "application/json", browser.Token, `{"user_code":"`+grant.UserCode+`","approve":true,"project":"demo","environment":"development"}`, nil); status != 200 {
		t.Fatal("approve", status)
	}
	form := url.Values{"grant_type": {"authorization_code"}, "code": {grant.DeviceCode}, "code_verifier": {verifier}, "client_id": {client.ClientID}, "redirect_uri": {redirect}}
	var token struct {
		AccessToken string `json:"access_token"`
		TokenType   string `json:"token_type"`
		Scope       string `json:"scope"`
	}
	if status := call("POST", "/api/v1/oauth/token", "application/x-www-form-urlencoded", "", form.Encode(), &token); status != 200 || token.TokenType != "Bearer" || token.Scope != "deployments:read logs:read" {
		t.Fatal(status, token)
	}
	var reuse struct{ Error string }
	if status := call("POST", "/api/v1/oauth/token", "application/x-www-form-urlencoded", "", form.Encode(), &reuse); status != 400 || reuse.Error != "invalid_grant" {
		t.Fatal("code reused", status, reuse)
	}
	initialize := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"test","version":"1"}}}`
	if status := call("POST", "/api/v1/mcp?project=demo&environment=development", "application/json", token.AccessToken, initialize, nil); status != 200 {
		t.Fatal("MCP", status)
	}
	if status := call("POST", "/api/v1/mcp?project=demo&environment=development&allow_deploy=true", "application/json", token.AccessToken, initialize, nil); status != 403 {
		t.Fatal("read-only token deployed", status)
	}
}
