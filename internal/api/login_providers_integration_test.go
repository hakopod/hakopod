package api_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/hakopod/hakopod/internal/api"
	"github.com/hakopod/hakopod/internal/cluster"
	"github.com/hakopod/hakopod/internal/store"
)

func TestFreeTeamLimitConcurrentAndCloudExemption(t *testing.T) {
	for _, cloud := range []bool{false, true} {
		t.Run(map[bool]string{false: "self-hosted", true: "cloud"}[cloud], func(t *testing.T) {
			h := newAuthHarness(t, func(c *api.AuthConfig) {
				if cloud {
					c.DeploymentMode = cluster.DeploymentManagedCloud
				}
			})
			token := h.owner()
			ctx := context.Background()
			if cloud {
				h.call("DELETE", "/license", token, map[string]any{"expected_revision": 1}, 403)
			} else {
				h.call("DELETE", "/license", token, map[string]any{"expected_revision": 1}, 200)
			}
			p, err := h.db.Authenticate(ctx, token)
			if err != nil {
				t.Fatal(err)
			}
			if cloud {
				// Cloud installation changes are out of band. Keep this
				// fixture unlicensed to test the store's Cloud exemption.
				if _, err := h.db.RemoveLicense(ctx, p, 1); err != nil {
					t.Fatal(err)
				}
			}
			var wg sync.WaitGroup
			results := make(chan error, 8)
			for i := 0; i < 8; i++ {
				wg.Add(1)
				go func() { defer wg.Done(); _, err := h.db.CreateTeam(ctx, p, "Concurrent team"); results <- err }()
			}
			wg.Wait()
			close(results)
			successes := 0
			for err := range results {
				if err == nil {
					successes++
				} else if !errors.Is(err, store.ErrLicenseRequired) {
					t.Fatal(err)
				}
			}
			want := 1
			if cloud {
				want = 8
			}
			if successes != want {
				t.Fatalf("created %d teams, want %d", successes, want)
			}
		})
	}
}
func TestLoginProviderSettingsEncryptedGatedAndRevisioned(t *testing.T) {
	h := newAuthHarness(t, nil)
	owner := h.owner()
	path := "/installation/login-providers/github"
	initial := h.call("GET", path, owner, nil, 200)
	if initial["revision"] != float64(0) {
		t.Fatal(initial)
	}
	secret := "test-only-client-secret"
	in := map[string]any{"enabled": true, "client_id": "fixture", "client_secret": secret, "issuer_url": "", "expected_revision": 0}
	saved := h.call("PUT", path, owner, in, 200)
	if saved["secret_configured"] != true || saved["client_secret"] != nil {
		t.Fatal("response exposed/missed secret", saved)
	}
	var encrypted []byte
	if err := h.db.Pool.QueryRow(context.Background(), "SELECT configuration FROM installation_login_providers WHERE provider='github'").Scan(&encrypted); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encrypted), secret) {
		t.Fatal("plaintext secret persisted")
	}
	h.call("PUT", path, owner, in, 409)
	in["expected_revision"] = 1
	in["client_secret"] = ""
	h.call("PUT", path, owner, in, 200)
	h.call("DELETE", "/license", owner, map[string]any{"expected_revision": 1}, 200)
	h.call("GET", "/auth/oauth/github/start", "", nil, 402)
	status := h.call("GET", "/auth/status", "", nil, 200)
	if len(status["providers"].([]any)) != 0 {
		t.Fatal("unlicensed provider advertised")
	}
	in["expected_revision"] = 2
	h.call("PUT", path, owner, in, 402)
	in["enabled"] = false
	h.call("PUT", path, owner, in, 200)
	h.call("POST", "/auth/login", "", map[string]string{"email": "owner@example.test", "password": "correct horse battery staple"}, 200)
	h.call("GET", path, "", nil, 401)
}
func TestCloudLoginSettingsNeverCustomerConfigurable(t *testing.T) {
	h := newAuthHarness(t, func(c *api.AuthConfig) {
		c.DeploymentMode = cluster.DeploymentManagedCloud
		c.GitHubClientID = "operator"
		c.GitHubClientSecret = "operator-secret"
	})
	owner := h.owner()
	h.call("DELETE", "/license", owner, map[string]any{"expected_revision": 1}, 403)
	p, err := h.db.Authenticate(context.Background(), owner)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.db.RemoveLicense(context.Background(), p, 1); err != nil {
		t.Fatal(err)
	}
	h.call("GET", "/installation/login-providers/github", owner, nil, 403)
	h.call("PUT", "/installation/login-providers/github", owner, map[string]any{"enabled": false, "expected_revision": 0}, 403)
	status := h.call("GET", "/auth/status", "", nil, 200)
	if len(status["providers"].([]any)) != 1 {
		t.Fatal("operator cloud provider blocked")
	}
}

func TestProviderCallbackRechecksLicenseAndConfiguration(t *testing.T) {
	base := signupPolicyProvider(t)
	for _, change := range []string{"license", "configuration"} {
		t.Run(change, func(t *testing.T) {
			h := newAuthHarness(t, func(c *api.AuthConfig) {
				configurePolicyProviders(c, base)
				c.DeploymentMode = cluster.DeploymentSelfHosted
			})
			owner := h.owner()
			want := 402
			if change == "configuration" {
				want = 401
			}
			policyOAuthFlow(h, "github", nil, func() {
				if change == "license" {
					h.call("DELETE", "/license", owner, map[string]any{"expected_revision": 1}, 200)
				} else {
					h.call("PUT", "/installation/login-providers/github", owner, map[string]any{"enabled": true, "client_id": "changed-client", "client_secret": "changed-secret", "issuer_url": "", "expected_revision": 0}, 200)
				}
			}, want)
		})
	}
}
