package api_test

import (
	"bufio"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/api"
)

func TestOAuthVerifiedIdentityStateAndPKCE(t *testing.T) {
	var unverified atomic.Bool
	var expectedChallenge atomic.Value
	expectedChallenge.Store("")
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/token":
			if err := r.ParseForm(); err != nil {
				t.Error(err)
			}
			verifier := r.Form.Get("code_verifier")
			sum := sha256.Sum256([]byte(verifier))
			if verifier == "" || base64.RawURLEncoding.EncodeToString(sum[:]) != expectedChallenge.Load().(string) || r.Form.Get("client_secret") != "provider-secret" {
				http.Error(w, "invalid exchange", 400)
				return
			}
			io.WriteString(w, `{"access_token":"mock-provider-access-token","token_type":"Bearer","expires_in":300}`)
		case "/user":
			if r.Header.Get("Authorization") != "Bearer mock-provider-access-token" {
				http.Error(w, "bad auth", 401)
				return
			}
			io.WriteString(w, `{"id":314159,"name":"Provider Owner","login":"owner"}`)
		case "/user/emails":
			json.NewEncoder(w).Encode([]map[string]any{{"email": "owner@example.test", "primary": true, "verified": !unverified.Load()}})
		case "/userinfo":
			json.NewEncoder(w).Encode(map[string]any{"sub": "google-owner-subject", "email": "owner@example.test", "email_verified": !unverified.Load(), "name": "Provider Owner"})
		case "/gitlab/userinfo":
			json.NewEncoder(w).Encode(map[string]any{"sub": "gitlab-owner-subject", "email": "owner@example.test", "email_verified": !unverified.Load(), "name": "Provider Owner"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer provider.Close()
	h := newAuthHarness(t, func(c *api.AuthConfig) {
		c.GitHubClientID = "github-test"
		c.GitHubClientSecret = "provider-secret"
		c.GitHubAuthURL = provider.URL + "/authorize"
		c.GitHubTokenURL = provider.URL + "/token"
		c.GitHubAPIURL = provider.URL
		c.GoogleClientID = "google-test"
		c.GoogleClientSecret = "provider-secret"
		c.GoogleAuthURL = provider.URL + "/authorize"
		c.GoogleTokenURL = provider.URL + "/token"
		c.GoogleUserInfoURL = provider.URL + "/userinfo"
		c.GitLabClientID = "gitlab-test"
		c.GitLabClientSecret = "provider-secret"
		c.GitLabAuthURL = provider.URL + "/authorize"
		c.GitLabTokenURL = provider.URL + "/token"
		c.GitLabUserInfoURL = provider.URL + "/gitlab/userinfo"
	})
	ownerToken := h.owner()
	owner := h.call("GET", "/me", ownerToken, nil, 200)
	client := &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	begin := func(name string) (string, *http.Cookie) {
		t.Helper()
		res, err := client.Get(h.server.URL + "/api/v1/auth/oauth/" + name + "/start")
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		if res.StatusCode != 302 {
			t.Fatalf("oauth start %d", res.StatusCode)
		}
		location, err := url.Parse(res.Header.Get("Location"))
		if err != nil {
			t.Fatal(err)
		}
		query := location.Query()
		if query.Get("code_challenge_method") != "S256" || query.Get("code_challenge") == "" {
			t.Fatal("missing S256 PKCE")
		}
		expectedChallenge.Store(query.Get("code_challenge"))
		var binding *http.Cookie
		for _, c := range res.Cookies() {
			if c.Name == "hakopod_oauth" {
				binding = c
			}
		}
		if binding == nil || !binding.HttpOnly {
			t.Fatal("missing HttpOnly state binding")
		}
		return query.Get("state"), binding
	}
	callback := func(name, state string, cookie *http.Cookie, want int) map[string]any {
		t.Helper()
		req, _ := http.NewRequest("GET", h.server.URL+"/api/v1/auth/oauth/"+name+"/callback?"+url.Values{"state": {state}, "code": {"provider-code"}}.Encode(), nil)
		if cookie != nil {
			req.AddCookie(cookie)
		}
		res, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		var out map[string]any
		if err = json.NewDecoder(res.Body).Decode(&out); err != nil {
			t.Fatal(err)
		}
		if res.StatusCode != want {
			t.Fatalf("oauth callback expected %d got %d: %v", want, res.StatusCode, out)
		}
		return out
	}
	for _, name := range []string{"github", "google", "gitlab"} {
		state, cookie := begin(name)
		result := callback(name, state, cookie, 200)
		if result["user"].(map[string]any)["id"] != owner["id"] {
			t.Fatal("provider did not resolve verified local account")
		}
		callback(name, state, cookie, 401)
	}
	state, cookie := begin("github")
	callback("github", state, nil, 401)
	cookie.Value = "forged-state-binding"
	callback("github", state, cookie, 401)
	unverified.Store(true)
	state, cookie = begin("google")
	callback("google", state, cookie, 401)
	state, cookie = begin("gitlab")
	callback("gitlab", state, cookie, 401)
	t.Log("GitHub/Google/GitLab token exchange, verified email identity, S256 PKCE, HttpOnly state binding, replay and unverified-email rejection verified against local provider mocks")
}

func TestInvitationSMTPOnlyLocalFixture(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	received := make(chan string, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		conn.SetDeadline(time.Now().Add(15 * time.Second))
		reader := bufio.NewReader(conn)
		writer := bufio.NewWriter(conn)
		reply := func(line string) { writer.WriteString(line + "\r\n"); writer.Flush() }
		reply("220 localhost test SMTP")
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				return
			}
			command := strings.ToUpper(strings.TrimSpace(line))
			switch {
			case strings.HasPrefix(command, "EHLO"), strings.HasPrefix(command, "HELO"):
				reply("250 localhost")
			case strings.HasPrefix(command, "MAIL FROM:"), strings.HasPrefix(command, "RCPT TO:"):
				reply("250 accepted")
			case command == "DATA":
				reply("354 end with dot")
				var body strings.Builder
				for {
					line, err = reader.ReadString('\n')
					if err != nil {
						return
					}
					if line == ".\r\n" {
						break
					}
					body.WriteString(line)
				}
				received <- body.String()
				reply("250 queued locally")
			case command == "QUIT":
				reply("221 goodbye")
				return
			default:
				reply("500 unsupported")
			}
		}
	}()
	h := newAuthHarness(t, func(c *api.AuthConfig) {
		c.SMTPAddress = listener.Addr().String()
		c.SMTPFrom = "hakopod@example.test"
		c.SMTPAllowInsecure = true
		c.SMTPAllowDelivery = true
	})
	token := h.owner()
	team := h.call("POST", "/teams", token, map[string]string{"name": "Local SMTP Test"}, 201)
	invite := h.call("POST", "/teams/"+team["id"].(string)+"/invites", token, map[string]any{"email": "local-recipient@example.test", "role": "member", "deliver": true}, 201)
	if invite["delivered"] != true {
		t.Fatal("local invitation not delivered")
	}
	select {
	case body := <-received:
		if !strings.Contains(body, "To: local-recipient@example.test") || !strings.Contains(body, "/login/invite?token=") || strings.Contains(body, "correct horse") {
			t.Fatal("invalid invitation email content")
		}
	case <-time.After(time.Second):
		t.Fatal("local SMTP fixture did not receive message")
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("SMTP connection did not close")
	}
	t.Log("invitation delivered only to local SMTP fixture; no external email sent")
}
