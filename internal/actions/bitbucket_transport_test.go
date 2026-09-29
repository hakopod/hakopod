package actions

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestBitbucketCredentialsRequireAnExplicitAuthMode(t *testing.T) {
	for _, credential := range []BitbucketCredential{
		{}, {AccessToken: "short"}, {AccessToken: bitbucketFixtureCredential, Email: "user@example.com"},
		{AccessToken: bitbucketFixtureCredential, APIToken: bitbucketFixtureCredential},
		{Email: "user@example.com"}, {APIToken: bitbucketFixtureCredential},
		{Email: "not-an-email", APIToken: bitbucketFixtureCredential},
		{Email: "user:password@example.com", APIToken: bitbucketFixtureCredential},
		{AccessToken: bitbucketFixtureCredential + "\r\nX-Injected: yes"},
		{Email: "user@example.com", APIToken: bitbucketFixtureCredential + "\n"},
	} {
		if _, err := bitbucketAuthorization(credential); err == nil || strings.Contains(err.Error(), bitbucketFixtureCredential) {
			t.Fatal("invalid credentials were accepted or exposed")
		}
	}
	credential := BitbucketCredential{Email: "user@example.com", APIToken: bitbucketFixtureCredential}
	header, err := bitbucketAuthorization(credential)
	request := &http.Request{Header: make(http.Header)}
	request.Header.Set("Authorization", header)
	user, token, ok := request.BasicAuth()
	if err != nil || !ok || user != credential.Email || token != credential.APIToken {
		t.Fatal("API token was not bound to its account email")
	}
	encoded, _ := json.Marshal(credential)
	if string(encoded) != "{}" {
		t.Fatal("credential is JSON serializable")
	}
}

func TestBitbucketProductionTransportIsFixedBoundedAndDoesNotUseProxy(t *testing.T) {
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:1")
	client := newBitbucketHTTP()
	transport, ok := client.Transport.(*http.Transport)
	if !ok || transport.Proxy != nil || transport.TLSClientConfig.InsecureSkipVerify || transport.TLSClientConfig.ServerName != "api.bitbucket.org" || transport.TLSClientConfig.MinVersion < tls.VersionTLS12 || transport.MaxConnsPerHost != 2 || transport.MaxResponseHeaderBytes > 16<<10 || transport.ResponseHeaderTimeout <= 0 || client.Timeout > 15*time.Second || client.Timeout <= 0 {
		t.Fatal("Bitbucket transport lost its destination, TLS or resource bounds")
	}
	for _, address := range []string{"attacker.invalid:443", "api.bitbucket.org:80", "127.0.0.1:443"} {
		if conn, err := transport.DialContext(context.Background(), "tcp", address); err == nil || conn != nil {
			t.Fatal("transport accepted a different destination")
		}
	}
}

func TestBitbucketNeverFollowsAuthenticatedRedirects(t *testing.T) {
	for _, status := range []int{301, 302, 303, 307, 308} {
		for _, destination := range []string{"https://api.bitbucket.org/2.0/other", "https://attacker.invalid/path", "http://api.bitbucket.org/2.0/other"} {
			t.Run(fmt.Sprintf("%d/%s", status, destination), func(t *testing.T) {
				calls := 0
				c := bitbucketTestClient(t, true, func(r *http.Request) (*http.Response, error) {
					calls++
					res := bitbucketResponse(status, map[string]string{"token": bitbucketFixtureCredential})
					res.Header.Set("Location", destination)
					return res, nil
				})
				_, err := c.FindOwned(context.Background(), c.target, bitbucketFixtureName)
				var failure *BitbucketError
				if calls != 1 || !errors.As(err, &failure) || failure.Kind != "redirect" || strings.Contains(err.Error(), destination) || strings.Contains(err.Error(), bitbucketFixtureCredential) {
					t.Fatal("redirect forwarded credentials or leaked its destination/body")
				}
			})
		}
	}
}

func TestBitbucketRejectsUnsafePaginationBeforeNetwork(t *testing.T) {
	c := bitbucketTestClient(t, true, func(*http.Request) (*http.Response, error) {
		t.Fatal("pagination validation queried network")
		return nil, nil
	})
	base := bitbucketOrigin + c.runnerScope()
	for _, raw := range []string{
		base, base + "?page=2&page=3", base + "?page=2&pagelen=101", base + "?page=2&fields=values", base + "?page=2&q=name%3Dnone", base + "?page=2#fragment",
		"http://api.bitbucket.org" + c.runnerScope() + "?page=2", "https://api.bitbucket.org:443" + c.runnerScope() + "?page=2",
		"https://user:password@api.bitbucket.org" + c.runnerScope() + "?page=2", "https://api.bitbucket.org.attacker.invalid" + c.runnerScope() + "?page=2",
		base + "?page=%0d%0a", base + "?page=2;pagelen=100", base + "?page=" + strings.Repeat("x", 1025),
	} {
		if _, err := c.nextPage(raw); err == nil {
			t.Fatal("unsafe or filtered pagination was accepted")
		}
	}
	if query, err := c.nextPage(base + "?page=opaque-cursor&pagelen=50"); err != nil || query.Get("page") != "opaque-cursor" {
		t.Fatal("bounded provider cursor was rewritten or rejected", err)
	}
}

func TestBitbucketFailedWritesRetainAmbiguityAndNeverExposeBodies(t *testing.T) {
	for _, variant := range []string{"transport", "503", "408", "201", "truncated", "oversized", "foreign OAuth endpoint"} {
		t.Run(variant, func(t *testing.T) {
			var c *BitbucketClient
			c = bitbucketTestClient(t, true, func(*http.Request) (*http.Response, error) {
				switch variant {
				case "transport":
					return nil, errors.New("provider URL with " + bitbucketFixtureCredential)
				case "503":
					return bitbucketResponse(503, map[string]string{"token": bitbucketFixtureCredential}), nil
				case "408":
					return bitbucketResponse(408, map[string]string{"token": bitbucketFixtureCredential}), nil
				case "201":
					return bitbucketResponse(201, nil), nil
				case "truncated":
					return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"uuid":`))}, nil
				case "oversized":
					return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(strings.Repeat("x", (1<<20)+1)))}, nil
				default:
					runner := bitbucketRunnerFixture(c, "UNREGISTERED")
					runner["oauth_client"] = map[string]string{"id": "fixture", "secret": "fixture-runner-secret", "token_endpoint": "http://169.254.169.254/credentials"}
					return bitbucketResponse(200, runner), nil
				}
			})
			_, err := c.Register(context.Background(), c.target, bitbucketFixtureName, []string{"self.hosted", "linux.arm64"})
			var failure *BitbucketError
			if !errors.As(err, &failure) || !failure.Ambiguous || strings.Contains(fmt.Sprintf("%+v", err), bitbucketFixtureCredential) {
				t.Fatal("failed mutation lost its ambiguity or leaked native diagnostics")
			}
		})
	}
}

func TestBitbucketDelete404IsNotVerifiedAbsence(t *testing.T) {
	var c *BitbucketClient
	c = bitbucketTestClient(t, false, func(r *http.Request) (*http.Response, error) {
		if r.Method == http.MethodDelete {
			return bitbucketResponse(404, nil), nil
		}
		runner := bitbucketRunnerFixture(c, "UNREGISTERED")
		if r.URL.Path == c.runnerScope() {
			return bitbucketResponse(200, map[string]any{"values": []any{runner}}), nil
		}
		return bitbucketResponse(200, runner), nil
	})
	if err := c.DeleteOwned(context.Background(), c.target, bitbucketFixtureRunner, bitbucketFixtureName); err == nil {
		t.Fatal("delete 404 was treated as successful revocation")
	}
}

func TestBitbucketCredentialBudgetSharesCooldownWithoutBlockingOtherProviders(t *testing.T) {
	now := time.Date(2026, 9, 29, 0, 0, 0, 0, time.UTC)
	budget := NewRequestBudget(func() time.Time { return now })
	calls := 0
	c := bitbucketTestClient(t, true, func(*http.Request) (*http.Response, error) {
		calls++
		res := bitbucketResponse(429, nil)
		res.Header.Set("Retry-After", "120")
		return res, nil
	})
	c.budget = &budget
	_, err := c.FindOwned(context.Background(), c.target, bitbucketFixtureName)
	var failure *BitbucketError
	if !errors.As(err, &failure) || !failure.RetryAt.Equal(now.Add(2*time.Minute)) {
		t.Fatal("provider cooldown was lost")
	}
	otherScope := bitbucketTestClient(t, false, func(*http.Request) (*http.Response, error) {
		calls++
		return bitbucketResponse(200, map[string]any{"values": []any{}}), nil
	})
	otherScope.budget = &budget
	if _, err := otherScope.FindOwned(context.Background(), otherScope.target, bitbucketFixtureName); err == nil || calls != 1 {
		t.Fatal("same credential bypassed cooldown in a different workspace scope")
	}
	if c.budgetKey != otherScope.budgetKey || c.budgetKey == sha256.Sum256([]byte(bitbucketFixtureCredential)) || c.budgetKey == gitlabBudgetKey(ProviderTarget{Provider: ProviderGitLab, GitLab: &GitLabTarget{URL: "https://gitlab.com", ProjectID: 12}}, bitbucketFixtureCredential) {
		t.Fatal("provider budget key broadened or narrowed its credential/origin scope")
	}
	now = now.Add(3 * time.Minute)
	if _, err := otherScope.FindOwned(context.Background(), otherScope.target, bitbucketFixtureName); err != nil || calls != 2 {
		t.Fatal("provider remained blocked beyond its bounded cooldown", err)
	}
}
