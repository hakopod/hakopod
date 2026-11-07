package store

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
)

func TestOAuthRedirectURIs(t *testing.T) {
	for raw, want := range map[string]bool{
		"https://claude.ai/api/mcp/auth_callback": true,
		"http://localhost:33418/callback":         true,
		"http://127.0.0.1:9000/cb":                true,
		"http://[::1]:9000/cb":                    true,
		"http://example.com/cb":                   false,
		"http://localhost.example.com/cb":         false,
		"https://claude.ai/cb#fragment":           false,
		"https://user:pass@claude.ai/cb":          false,
		"claude://callback":                       false,
		"javascript:alert(1)":                     false,
		"https:///cb":                             false,
		"/relative":                               false,
	} {
		if ValidRedirectURI(raw) != want {
			t.Errorf("%s: want %v", raw, want)
		}
	}
}

func TestOAuthCodeRequiresPKCEAndIsSingleUse(t *testing.T) {
	s := isolatedDatabase(t)
	ctx := context.Background()
	id := NewID()
	if _, err := s.Pool.Exec(ctx, "INSERT INTO projects(name) VALUES('demo'); INSERT INTO environments(project,name) VALUES('demo','development')"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Pool.Exec(ctx, "INSERT INTO identities(id,name,email,admin,permissions,email_verified) VALUES($1,'OAuth fixture','oauth@example.test',true,ARRAY['admin'],true)", id); err != nil {
		t.Fatal(err)
	}
	browser, err := s.NewSession(ctx, id, "browser", "", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.RegisterOAuthClient(ctx, "Bad", []string{"http://example.com/cb"}); !errors.Is(err, ErrInput) {
		t.Fatal("insecure redirect registered", err)
	}
	const redirect = "http://127.0.0.1:9000/cb"
	client, err := s.RegisterOAuthClient(ctx, "Claude", []string{redirect})
	if err != nil {
		t.Fatal(err)
	}
	verifier := strings.Repeat("v", 43)
	sum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])
	permissions := []string{"deployments:read", "logs:read"}
	start := func() DeviceAuthorization {
		t.Helper()
		v, _, err := s.StartOAuthDevice(ctx, browser.User, client.ID, redirect, challenge, "demo", "development", permissions)
		if err != nil {
			t.Fatal(err)
		}
		if err = s.ApproveDevice(ctx, browser.User, v.UserCode, true); err != nil {
			t.Fatal(err)
		}
		return v
	}
	if _, _, err = s.StartOAuthDevice(ctx, browser.User, client.ID, "http://127.0.0.1:9001/cb", challenge, "demo", "development", permissions); !errors.Is(err, ErrUnknownOAuthClient) {
		t.Fatal("unregistered redirect accepted", err)
	}

	// The CLI device exchange cannot bypass PKCE, and a wrong verifier burns the code.
	wrong := start()
	if _, err = s.PollDevice(ctx, wrong.DeviceCode); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("OAuth code redeemed without PKCE", err)
	}
	if _, err = s.ExchangeOAuthCode(ctx, wrong.DeviceCode, OAuthExchange{client.ID, redirect, strings.Repeat("w", 43)}); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("wrong verifier accepted", err)
	}
	if _, err = s.ExchangeOAuthCode(ctx, wrong.DeviceCode, OAuthExchange{client.ID, redirect, verifier}); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("code survived a failed exchange", err)
	}

	good := start()
	session, err := s.ExchangeOAuthCode(ctx, good.DeviceCode, OAuthExchange{client.ID, redirect, verifier})
	if err != nil {
		t.Fatal(err)
	}
	if session.User.CredentialType != "cli" || session.User.Project != "demo" || session.User.Environment != "development" || len(session.User.Permissions) != 2 {
		t.Fatalf("unexpected session %+v", session.User)
	}
	if _, err = s.ExchangeOAuthCode(ctx, good.DeviceCode, OAuthExchange{client.ID, redirect, verifier}); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("code reused", err)
	}
}
