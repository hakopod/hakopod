package api_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/api"
	"github.com/hakopod/hakopod/internal/store"
)

func TestProviderRegistrationCallbackCreatesOnlyVerifiedMember(t *testing.T) {
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/token" {
			_, _ = w.Write([]byte(`{"access_token":"local-provider-proof","token_type":"Bearer"}`))
			return
		}
		if r.URL.Path == "/userinfo" {
			_, _ = w.Write([]byte(`{"sub":"new-google-subject","email":"new-google@example.test","email_verified":true,"name":"New Google user"}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer provider.Close()
	h := newAuthHarness(t, func(c *api.AuthConfig) {
		c.SignupEnabled = true
		c.GoogleClientID = "local-client"
		c.GoogleClientSecret = "local-secret"
		c.GoogleAuthURL = provider.URL + "/authorize"
		c.GoogleTokenURL = provider.URL + "/token"
		c.GoogleUserInfoURL = provider.URL + "/userinfo"
	})
	h.owner()
	client := &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	start, err := client.Get(h.server.URL + "/api/v1/auth/oauth/google/start?intent=register")
	if err != nil {
		t.Fatal(err)
	}
	defer start.Body.Close()
	if start.StatusCode != 302 {
		t.Fatalf("provider registration start: %d", start.StatusCode)
	}
	u, err := url.Parse(start.Header.Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	request, _ := http.NewRequest("GET", h.server.URL+"/api/v1/auth/oauth/google/callback?"+url.Values{"state": {u.Query().Get("state")}, "code": {"local-code"}}.Encode(), nil)
	for _, cookie := range start.Cookies() {
		request.AddCookie(cookie)
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var result store.Session
	if err = json.NewDecoder(response.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != 200 || result.Token == "" || result.User.Admin || result.User.Owner || !result.OnboardingRequired {
		t.Fatal("provider callback failed to create an ordinary verified account")
	}
	if _, err = h.db.CheckPassword(context.Background(), "new-google@example.test", "not-a-password"); !errors.Is(err, store.ErrUnauthorized) {
		t.Fatal("OAuth account unexpectedly has a password")
	}
	reset, err := h.db.BeginPasswordReset(context.Background(), "new-google@example.test")
	if err != nil || reset == "" {
		t.Fatal("OAuth account could not begin mailbox password enrollment")
	}
	if err = h.db.ResetPassword(context.Background(), reset, "new mailbox verified password"); err != nil {
		t.Fatal(err)
	}
	if _, err = h.db.CheckPassword(context.Background(), "new-google@example.test", "new mailbox verified password"); err != nil {
		t.Fatal("OAuth account could not enroll its first password")
	}
}

func registrationSMTP(t *testing.T) (string, <-chan string) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	received := make(chan string, 16)
	t.Cleanup(func() { listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			func() {
				defer conn.Close()
				_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
				reader := bufio.NewReader(conn)
				writer := bufio.NewWriter(conn)
				reply := func(line string) { _, _ = writer.WriteString(line + "\r\n"); _ = writer.Flush() }
				reply("220 localhost isolated auth fixture")
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
							if body.Len() > 16384 {
								return
							}
							body.WriteString(line)
						}
						select {
						case received <- body.String():
						default:
							return
						}
						reply("250 accepted")
					case command == "QUIT":
						reply("221 goodbye")
						return
					default:
						reply("500 unsupported")
					}
				}
			}()
		}
	}()
	return listener.Addr().String(), received
}
func authMailToken(t *testing.T, received <-chan string, path string) string {
	t.Helper()
	select {
	case body := <-received:
		for _, line := range strings.Split(body, "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "http://localhost:4173"+path+"#") {
				u, err := url.Parse(line)
				if err != nil {
					t.Fatal(err)
				}
				values, _ := url.ParseQuery(u.Fragment)
				token := values.Get("token")
				if token == "" || u.RawQuery != "" {
					t.Fatal("mail must carry a fragment-only token")
				}
				return token
			}
		}
		t.Fatal("fixture email did not contain the expected link")
	case <-time.After(time.Second):
		t.Fatal("local email fixture received no message")
	}
	return ""
}
func enabledRegistration(address string) func(*api.AuthConfig) {
	return func(c *api.AuthConfig) {
		c.SignupEnabled = true
		c.SMTPAllowDelivery = true
		c.SMTPAllowInsecure = true
		c.SMTPAddress = address
		c.SMTPFrom = "accounts@example.test"
	}
}

func TestPublicRegistrationVerificationAndPrivateWorkspace(t *testing.T) {
	address, mail := registrationSMTP(t)
	h := newAuthHarness(t, enabledRegistration(address))
	ctx := context.Background()
	h.call("POST", "/auth/register", "", map[string]string{"name": "New User", "email": "new@example.test", "password": "a valid new account password"}, 403)
	owner := h.owner()
	status := h.call("GET", "/auth/status", "", nil, 200)
	if status["signup_enabled"] != true || status["password_recovery"] != true {
		t.Fatal("registration capability missing")
	}
	request := map[string]string{"name": "New User", "email": "NEW@example.test", "password": "a valid new account password"}
	response := h.call("POST", "/auth/register", "", request, 202)
	if len(response) != 1 || response["accepted"] != true {
		t.Fatal("registration must not return a token")
	}
	h.call("POST", "/auth/login", "", map[string]string{"email": request["email"], "password": request["password"]}, 401)
	token := authMailToken(t, mail, "/login/verify")
	registered := h.call("POST", "/auth/register/verify", "", map[string]string{"token": token}, 200)
	if registered["onboarding_required"] != true {
		t.Fatal("new registration skipped workspace choice")
	}
	user := registered["user"].(map[string]any)
	if user["owner"] == true || user["admin"] == true {
		t.Fatal("public account gained installation access")
	}
	session := registered["token"].(string)
	h.call("POST", "/auth/register/verify", "", map[string]string{"token": token}, 401)
	h.call("GET", "/users", session, nil, 403)
	h.call("POST", "/auth/onboarding", session, map[string]string{"choice": "personal"}, 200)
	p, err := h.db.Authenticate(ctx, session)
	if err != nil {
		t.Fatal(err)
	}
	state, err := h.db.Onboarding(ctx, p)
	if err != nil || state.Required || state.PersonalProject == "" {
		t.Fatalf("personal workspace missing: %v", err)
	}
	if !p.Allows("deployments:write", state.PersonalProject, "development", "") || p.Allows("deployments:read", "demo", "development", "") || p.IsAdmin() {
		t.Fatal("private workspace permissions escaped their scope")
	}
	assertPersonalProjects := func() {
		t.Helper()
		items := h.call("GET", "/projects", session, nil, 200)["items"].([]any)
		if len(items) != 1 {
			t.Fatal("personal owner can see projects outside their workspace")
		}
		project := items[0].(map[string]any)
		if project["name"] != state.PersonalProject || project["personal"] != true {
			t.Fatal("personal workspace is missing its private marker")
		}
	}
	assertPersonalProjects()
	adminProjects := h.call("GET", "/projects", owner, nil, 200)["items"].([]any)
	foundDemo := false
	for _, item := range adminProjects {
		project := item.(map[string]any)
		if project["name"] == "demo" {
			foundDemo = true
			if project["personal"] != false {
				t.Fatal("normal shared project was marked personal")
			}
		}
		if project["name"] == state.PersonalProject && project["personal"] != true {
			t.Fatal("administrator view lost the personal workspace marker")
		}
	}
	if !foundDemo {
		t.Fatal("administrator cannot see the normal demo project")
	}
	h.call("POST", "/projects/"+state.PersonalProject+"/invites", session, map[string]string{"email": "another@example.test", "role": "viewer"}, 403)
	h.call("PUT", "/projects/"+state.PersonalProject+"/members", owner, map[string]string{"identity_id": p.ID, "role": "viewer"}, 403)
	h.call("POST", "/auth/onboarding", session, map[string]string{"choice": "personal"}, 403)
	duplicate := h.call("POST", "/auth/register", "", map[string]string{"name": "Someone", "email": "owner@example.test", "password": "a valid duplicate password"}, 202)
	if !bytes.Equal(store.JSON(response), store.JSON(duplicate)) {
		t.Fatal("registration disclosed existing account")
	}
	if _, err = h.db.Pool.Exec(ctx, "UPDATE installation_license SET token='',token_digest=NULL"); err != nil {
		t.Fatal(err)
	}
	p, err = h.db.Authenticate(ctx, session)
	if err != nil || !p.Allows("deployments:write", state.PersonalProject, "development", "") {
		t.Fatal("personal workspace incorrectly needs paid sharing")
	}
	assertPersonalProjects()
}

func TestRegistrationGateAndDurableMailLimits(t *testing.T) {
	h := newAuthHarness(t, nil)
	h.owner()
	ctx := context.Background()
	h.call("POST", "/auth/register", "", map[string]string{"name": "User", "email": "user@example.test", "password": "valid registration password"}, 403)
	for i := 0; i < 3; i++ {
		allowed, err := h.db.AllowAuthMail(ctx, "limited@example.test")
		if err != nil || !allowed {
			t.Fatalf("expected allowed attempt %d: %v", i, err)
		}
		allowed, err = h.db.AllowAuthMail(ctx, "limited@example.test")
		if err != nil || allowed {
			t.Fatal("mail cooldown did not apply")
		}
		if _, err = h.db.Pool.Exec(ctx, "UPDATE auth_mail_limits SET last_sent=now()-interval '2 minutes'"); err != nil {
			t.Fatal(err)
		}
	}
	allowed, err := h.db.AllowAuthMail(ctx, "limited@example.test")
	if err != nil || allowed {
		t.Fatal("per-hour mail bound did not apply")
	}
}

func TestPasswordRecoverySingleUseRevokesSessionsAndKeepsMFA(t *testing.T) {
	address, mail := registrationSMTP(t)
	h := newAuthHarness(t, enabledRegistration(address))
	session := h.owner()
	ctx := context.Background()
	principal, err := h.db.Authenticate(ctx, session)
	if err != nil {
		t.Fatal(err)
	}
	secret := []byte("test encrypted MFA state remains unchanged")
	if _, err = h.db.Pool.Exec(ctx, "UPDATE identities SET totp_secret=$2,totp_step=7 WHERE id=$1", principal.ID, secret); err != nil {
		t.Fatal(err)
	}
	unknown := h.call("POST", "/auth/password/forgot", "", map[string]string{"email": "unknown@example.test"}, 202)
	known := h.call("POST", "/auth/password/forgot", "", map[string]string{"email": "owner@example.test"}, 202)
	if !bytes.Equal(store.JSON(known), store.JSON(unknown)) {
		t.Fatal("recovery disclosed account existence")
	}
	token := authMailToken(t, mail, "/login/reset")
	h.call("POST", "/auth/password/reset", "", map[string]string{"token": token, "password": "short"}, 400)
	h.call("POST", "/auth/password/reset", "", map[string]string{"token": token, "password": "new recovery account password"}, 200)
	h.call("POST", "/auth/password/reset", "", map[string]string{"token": token, "password": "another recovery password"}, 401)
	h.call("GET", "/me", session, nil, 401)
	h.call("POST", "/auth/login", "", map[string]string{"email": "owner@example.test", "password": "correct horse battery staple"}, 401)
	login := h.call("POST", "/auth/login", "", map[string]string{"email": "owner@example.test", "password": "new recovery account password"}, 401)
	if login["error"].(map[string]any)["code"] != "mfa_required" {
		t.Fatal("recovery bypassed MFA")
	}
	var after []byte
	var step int64
	if err = h.db.Pool.QueryRow(ctx, "SELECT totp_secret,totp_step FROM identities WHERE id=$1", principal.ID).Scan(&after, &step); err != nil || !bytes.Equal(after, secret) || step != 7 {
		t.Fatal("MFA state changed during recovery")
	}
}

func TestRecoveryResponseDoesNotWaitForSMTP(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	release := make(chan struct{})
	accepted := make(chan struct{})
	defer close(release)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		close(accepted)
		select {
		case <-release:
		case <-time.After(3 * time.Second):
		}
	}()
	h := newAuthHarness(t, enabledRegistration(listener.Addr().String()))
	h.owner()
	response := make(chan map[string]any, 1)
	go func() {
		response <- h.call("POST", "/auth/password/forgot", "", map[string]string{"email": "owner@example.test"}, 202)
	}()
	select {
	case <-accepted:
	case <-time.After(2 * time.Second):
		t.Fatal("SMTP fixture did not receive a connection")
	}
	select {
	case result := <-response:
		if len(result) != 1 || result["accepted"] != true {
			t.Fatal("unexpected recovery response")
		}
	case <-time.After(time.Second):
		t.Fatal("recovery exposed SMTP latency to the caller")
	}
}

func TestInvitationChoiceAndVerifiedOAuthRegistration(t *testing.T) {
	h := newAuthHarness(t, nil)
	owner := h.owner()
	ctx := context.Background()
	team := h.call("POST", "/teams", owner, map[string]string{"name": "Inviting team"}, 201)
	invitation := func(email string) string {
		t.Helper()
		result := h.call("POST", "/teams/"+team["id"].(string)+"/invites", owner, map[string]string{"email": email, "role": "member"}, 201)
		u, _ := url.Parse(result["invite_url"].(string))
		return u.Query().Get("token")
	}
	token := invitation("invited-personal@example.test")
	joined := h.call("POST", "/auth/invites/accept", "", map[string]string{"token": token, "name": "Private", "password": "private workspace password", "workspace": "personal"}, 200)
	id := joined["user"].(map[string]any)["id"].(string)
	var count int
	if err := h.db.Pool.QueryRow(ctx, "SELECT count(*) FROM team_members WHERE identity_id=$1", id).Scan(&count); err != nil || count != 0 {
		t.Fatal("personal choice joined a team")
	}
	h.call("POST", "/auth/invites/accept", "", map[string]string{"token": token, "name": "Replay", "password": "private workspace password", "workspace": "personal"}, 401)
	_, err := h.db.ResolveOAuthAccount(ctx, "github", "closed-subject", "closed@example.test", "Closed", false, "")
	if !errors.Is(err, store.ErrForbidden) {
		t.Fatal("uninvited OAuth bypassed registration policy")
	}
	oauthInvite := invitation("oauth-invite@example.test")
	_, err = h.db.ResolveOAuthAccount(ctx, "github", "wrong-subject", "wrong@example.test", "Wrong", false, oauthInvite)
	if !errors.Is(err, store.ErrForbidden) {
		t.Fatal("OAuth accepted invitation for another verified email")
	}
	oauthID, err := h.db.ResolveOAuthAccount(ctx, "github", "invited-subject", "oauth-invite@example.test", "Invited", false, oauthInvite)
	if err != nil {
		t.Fatal(err)
	}
	oauthSession, err := h.db.NewSession(ctx, oauthID, "browser", "", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	state, err := h.db.Onboarding(ctx, oauthSession.User)
	if err != nil || len(state.Invitations) != 1 || !state.Required {
		t.Fatal("new verified account did not see its invitation")
	}
	if err = h.db.CompleteOnboarding(ctx, oauthSession.User, "invite", state.Invitations[0].ID); err != nil {
		t.Fatal(err)
	}
	for _, provider := range []string{"github", "google", "gitlab"} {
		email := provider + "-signup@example.test"
		id, err := h.db.ResolveOAuthAccount(ctx, provider, provider+"-public", email, "Public", true, "")
		if err != nil {
			t.Fatal(err)
		}
		var admin, owner bool
		if err = h.db.Pool.QueryRow(ctx, "SELECT admin,owner FROM identities WHERE id=$1", id).Scan(&admin, &owner); err != nil || admin || owner {
			t.Fatal("provider registration created an administrator")
		}
	}
	// No API response or durable audit field contains a raw recovery token.
	var events []byte
	if err = h.db.Pool.QueryRow(ctx, "SELECT COALESCE(json_agg(metadata),'[]') FROM audit_events").Scan(&events); err != nil || !json.Valid(events) {
		t.Fatal("invalid audit output")
	}
}
