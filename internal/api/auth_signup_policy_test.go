package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/api"
	"github.com/hakopod/hakopod/internal/cluster"
)

func TestPublicSignupPolicyFailsClosed(t *testing.T) {
	for _, available := range []bool{false, true} {
		for _, mode := range []string{"", cluster.DeploymentSelfHosted, cluster.DeploymentManagedCloud, "unknown"} {
			for _, enabled := range []bool{false, true} {
				config := api.AuthConfig{CloudSignupAvailable: available, DeploymentMode: mode, SignupEnabled: enabled}
				if config.PublicSignupEnabled() != (available && mode == cluster.DeploymentManagedCloud && enabled) {
					t.Fatalf("unexpected policy for available=%t mode=%q enabled=%t", available, mode, enabled)
				}
			}
		}
	}
}

func TestSelfHostedSignupBlocksEmailButPreservesSetupAndFreeInvites(t *testing.T) {
	for _, mode := range []string{cluster.DeploymentSelfHosted, cluster.DeploymentManagedCloud} {
		t.Run(mode, func(t *testing.T) {
			h := newAuthHarness(t, func(c *api.AuthConfig) {
				c.SignupEnabled = true
				c.DeploymentMode = mode
			})
			status := h.call("GET", "/auth/status", "", nil, 200)
			if status["setup_required"] != true || status["signup_enabled"] != false || status["deployment_mode"] != mode {
				t.Fatal("self-hosted installation did not expose only first-owner setup")
			}
			if _, err := h.db.Pool.Exec(context.Background(), "UPDATE installation_license SET token='',token_digest=NULL,highest_sequence=0"); err != nil {
				t.Fatal(err)
			}
			owner := h.owner()
			status = h.call("GET", "/auth/status", "", nil, 200)
			if status["setup_required"] != false || status["signup_enabled"] != false || status["deployment_mode"] != mode {
				t.Fatal("claiming the owner enabled public enrollment")
			}
			request := map[string]string{"name": "Blocked", "email": "blocked@example.test", "password": "blocked user password strong"}
			h.call("POST", "/auth/register", "", request, 403)
			// An unfinished verification from an earlier cloud process cannot bypass the current build.
			token, err := h.db.BeginRegistration(context.Background(), request["name"], request["email"], request["password"])
			if err != nil || token == "" {
				t.Fatal("could not prepare unfinished registration", err)
			}
			h.call("POST", "/auth/register/verify", "", map[string]string{"token": token}, 403)
			var count int
			if err := h.db.Pool.QueryRow(context.Background(), "SELECT count(*) FROM identities WHERE email=$1", request["email"]).Scan(&count); err != nil || count != 0 {
				t.Fatal("blocked verification created an account", err)
			}
			h.call("POST", "/auth/setup", "", map[string]string{"name": "Other", "email": "other@example.test", "password": "other owner password strong", "setup_token": h.config.SetupSecret}, 409)
			h.call("POST", "/auth/login", "", map[string]string{"email": "owner@example.test", "password": "correct horse battery staple"}, 200)
			team := h.call("POST", "/teams", owner, map[string]string{"name": "Operators"}, 201)
			invite := h.call("POST", "/teams/"+team["id"].(string)+"/invites", owner, map[string]string{"email": "invited@example.test", "role": "member"}, 201)
			u, _ := url.Parse(invite["invite_url"].(string))
			h.call("POST", "/auth/invites/accept", "", map[string]string{"token": u.Query().Get("token"), "name": "Invited", "password": "invited user password strong"}, 200)
		})
	}
}

func signupPolicyProvider(t *testing.T) string {
	t.Helper()
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/token":
			_, _ = w.Write([]byte(`{"access_token":"local-provider-proof","token_type":"Bearer"}`))
		case "/user":
			_, _ = w.Write([]byte(`{"id":12345,"name":"Policy User","login":"policy-user"}`))
		case "/user/emails":
			_, _ = w.Write([]byte(`[{"email":"policy@example.test","verified":true,"primary":true}]`))
		case "/userinfo":
			_, _ = w.Write([]byte(`{"sub":"policy-subject","email":"policy@example.test","email_verified":true,"name":"Policy User"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(provider.Close)
	return provider.URL
}

func configurePolicyProviders(c *api.AuthConfig, base string) {
	c.SignupEnabled = true
	c.DeploymentMode = cluster.DeploymentManagedCloud
	c.GitHubClientID, c.GitHubClientSecret = "local-client", "local-secret"
	c.GitHubAuthURL, c.GitHubTokenURL, c.GitHubAPIURL = base+"/authorize", base+"/token", base
	c.GoogleClientID, c.GoogleClientSecret = "local-client", "local-secret"
	c.GoogleAuthURL, c.GoogleTokenURL, c.GoogleUserInfoURL = base+"/authorize", base+"/token", base+"/userinfo"
	c.GitLabClientID, c.GitLabClientSecret = "local-client", "local-secret"
	c.GitLabAuthURL, c.GitLabTokenURL, c.GitLabUserInfoURL = base+"/authorize", base+"/token", base+"/userinfo"
}

func policyOAuthFlow(h *authHarness, provider string, query url.Values, beforeCallback func(), want int) map[string]any {
	h.t.Helper()
	client := &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	start, err := client.Get(h.server.URL + "/api/v1/auth/oauth/" + provider + "/start?" + query.Encode())
	if err != nil {
		h.t.Fatal(err)
	}
	defer start.Body.Close()
	if start.StatusCode != 302 {
		h.t.Fatalf("OAuth start: expected 302 got %d", start.StatusCode)
	}
	u, err := url.Parse(start.Header.Get("Location"))
	if err != nil {
		h.t.Fatal(err)
	}
	if beforeCallback != nil {
		beforeCallback()
	}
	req, _ := http.NewRequest("GET", h.server.URL+"/api/v1/auth/oauth/"+provider+"/callback?"+url.Values{"state": {u.Query().Get("state")}, "code": {"local-code"}}.Encode(), nil)
	for _, cookie := range start.Cookies() {
		req.AddCookie(cookie)
	}
	response, err := client.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	defer response.Body.Close()
	var result map[string]any
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		h.t.Fatal(err)
	}
	if response.StatusCode != want {
		h.t.Fatalf("OAuth callback: expected %d got %d", want, response.StatusCode)
	}
	return result
}

func TestSelfHostedOAuthRequiresLiveInvite(t *testing.T) {
	base := signupPolicyProvider(t)
	for _, provider := range []string{"github", "gitlab", "google"} {
		t.Run(provider, func(t *testing.T) {
			h := newAuthHarness(t, func(c *api.AuthConfig) { configurePolicyProviders(c, base) })
			if _, err := h.db.Pool.Exec(context.Background(), "UPDATE installation_license SET token='',token_digest=NULL,highest_sequence=0"); err != nil {
				t.Fatal(err)
			}
			owner := h.owner()
			h.call("GET", "/auth/oauth/"+provider+"/start?intent=register", "", nil, 403)
			policyOAuthFlow(h, provider, url.Values{"intent": {"login"}}, nil, 403)
			team := h.call("POST", "/teams", owner, map[string]string{"name": "Inviting team"}, 201)
			invite := h.call("POST", "/teams/"+team["id"].(string)+"/invites", owner, map[string]string{"email": "policy@example.test", "role": "member"}, 201)
			u, _ := url.Parse(invite["invite_url"].(string))
			query := url.Values{"intent": {"register"}, "invite_token": {u.Query().Get("token")}}
			// Provider consent does not extend the lifetime of an invitation.
			policyOAuthFlow(h, provider, query, func() {
				if _, err := h.db.Pool.Exec(context.Background(), "UPDATE invites SET expires_at=now()-interval '1 minute' WHERE id=$1", invite["invite"].(map[string]any)["id"]); err != nil {
					t.Fatal(err)
				}
			}, 403)
			invite = h.call("POST", "/teams/"+team["id"].(string)+"/invites", owner, map[string]string{"email": "policy@example.test", "role": "member"}, 201)
			u, _ = url.Parse(invite["invite_url"].(string))
			query.Set("invite_token", u.Query().Get("token"))
			registered := policyOAuthFlow(h, provider, query, nil, 200)
			user := registered["user"].(map[string]any)
			if user["admin"] == true || user["owner"] == true {
				t.Fatal("invitation granted installation administration")
			}
			// Existing verified accounts can still sign in without an invitation.
			policyOAuthFlow(h, provider, url.Values{"intent": {"login"}}, nil, 200)
		})
	}
}

func TestOAuthCallbackRechecksBuildAndMode(t *testing.T) {
	for _, change := range []string{"build", "mode", "signup"} {
		t.Run(change, func(t *testing.T) {
			base := signupPolicyProvider(t)
			h := newAuthHarness(t, func(c *api.AuthConfig) {
				configurePolicyProviders(c, base)
				c.CloudSignupAvailable = true
			})
			h.owner()
			policyOAuthFlow(h, "google", url.Values{"intent": {"login"}}, func() {
				h.server.Close()
				switch change {
				case "build":
					h.config.CloudSignupAvailable = false
				case "mode":
					h.config.DeploymentMode = cluster.DeploymentSelfHosted
				case "signup":
					h.config.SignupEnabled = false
				}
				h.server = httptest.NewServer((&api.Server{Store: h.db, Auth: h.config}).Handler())
				t.Cleanup(h.server.Close)
			}, 403)
			var count int
			if err := h.db.Pool.QueryRow(context.Background(), "SELECT count(*) FROM identities WHERE email='policy@example.test'").Scan(&count); err != nil || count != 0 {
				t.Fatal("old OAuth state created an account after signup policy changed", err)
			}
		})
	}
}

func TestPublicCloudOAuthAcceptsNewAndReturningUsersFromEveryEntry(t *testing.T) {
	base := signupPolicyProvider(t)
	for _, provider := range []string{"github", "gitlab", "google"} {
		for _, intent := range []string{"", "login", "register"} {
			t.Run(provider+"/"+intent, func(t *testing.T) {
				h := newAuthHarness(t, func(c *api.AuthConfig) {
					configurePolicyProviders(c, base)
					c.CloudSignupAvailable = true
				})
				h.owner()
				query := url.Values{"intent": {intent}}
				result := policyOAuthFlow(h, provider, query, nil, 200)
				user := result["user"].(map[string]any)
				if user["admin"] == true || user["owner"] == true {
					t.Fatal("public OAuth granted installation administration")
				}
				again := policyOAuthFlow(h, provider, query, nil, 200)
				if again["user"].(map[string]any)["id"] != user["id"] {
					t.Fatal("returning OAuth user received a different account")
				}
			})
		}
	}
}
