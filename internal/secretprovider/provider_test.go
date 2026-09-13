package secretprovider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/spec"
)

func fixtureProvider(kind string) Provider {
	p := Provider{Name: "company", Kind: kind, Endpoint: "https://vault.example.com", RootPath: "applications", Scopes: []Scope{{Project: "demo", Environments: []string{"development"}}}}
	if kind == "vault" {
		p.Mount = "secret"
	} else {
		p.ProjectID = "project-id"
		p.Environment = "dev"
	}
	return p
}

type fixtureRepo struct {
	provider Provider
	calls    int
}

func (r *fixtureRepo) SecretProvider(context.Context, string) (Provider, error) {
	r.calls++
	return r.provider, nil
}

func fixtureService(t *testing.T, p Provider, h http.HandlerFunc) (*Service, *fixtureRepo, *atomic.Int32) {
	t.Helper()
	server := httptest.NewTLSServer(h)
	t.Cleanup(server.Close)
	key := bytes.Repeat([]byte{7}, 32)
	credentials := Credentials{Token: "fixture-token"}
	if p.Kind == "infisical" {
		credentials = Credentials{ClientID: "client-id", ClientSecret: "client-secret"}
	}
	var err error
	p.EncryptedCredentials, err = SealCredentials(key, p, credentials)
	if err != nil {
		t.Fatal(err)
	}
	repo := &fixtureRepo{provider: p}
	s := NewService(repo, key)
	creations := &atomic.Int32{}
	s.client = func(p Provider) (*providerClient, error) {
		creations.Add(1)
		p.Endpoint = server.URL
		httpClient := server.Client()
		httpClient.Timeout = 200 * time.Millisecond
		httpClient.CheckRedirect = func(*http.Request, []*http.Request) error { return ErrUnavailable }
		return &providerClient{provider: p, http: httpClient}, nil
	}
	return s, repo, creations
}

func fixtureApp() spec.Application {
	ref := spec.SecretRef{Provider: "company", Path: "shop", Key: "PASSWORD"}
	return spec.Application{Services: map[string]spec.Service{"api": {Secrets: map[string]spec.SecretRef{"PASSWORD": ref}}, "web": {Secrets: map[string]spec.SecretRef{"PASSWORD": ref}}}}
}

func TestVaultSnapshotAuthenticationCachingAndRotation(t *testing.T) {
	var reads atomic.Int32
	s, repo, _ := fixtureService(t, fixtureProvider("vault"), func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.URL.Path != "/v1/secret/data/applications/shop" || r.Header.Get("X-Vault-Token") != "fixture-token" {
			t.Error("unexpected Vault request")
		}
		if reads.Add(1) == 1 {
			io.WriteString(w, `{"data":{"data":{"PASSWORD":"first-value"}}}`)
		} else {
			io.WriteString(w, `{"data":{"data":{"PASSWORD":"rotated-value"}}}`)
		}
	})
	for _, want := range []string{"first-value", "rotated-value"} {
		values, err := s.Resolve(context.Background(), "demo", "development", fixtureApp())
		if err != nil || string(values["api"]["PASSWORD"]) != want || string(values["web"]["PASSWORD"]) != want {
			t.Fatalf("snapshot did not resolve consistent fresh values: %v", err)
		}
	}
	if reads.Load() != 2 || repo.calls != 2 {
		t.Fatal("credentials or values cached across deployments, or repeated reads within snapshot")
	}
}

func TestInfisicalUniversalAuthAndBoundedPath(t *testing.T) {
	var logins, reads atomic.Int32
	s, _, _ := fixtureService(t, fixtureProvider("infisical"), func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/auth/universal-auth/login" {
			var body map[string]string
			if r.Method != "POST" || json.NewDecoder(r.Body).Decode(&body) != nil || body["clientId"] != "client-id" || body["clientSecret"] != "client-secret" {
				t.Error("wrong universal auth")
			}
			logins.Add(1)
			io.WriteString(w, `{"accessToken":"access-token"}`)
			return
		}
		q := r.URL.Query()
		if r.Method != "GET" || r.URL.Path != "/api/v4/secrets/PASSWORD" || r.Header.Get("Authorization") != "Bearer access-token" || q.Get("secretPath") != "/applications/shop" || q.Get("environment") != "dev" || q.Get("projectId") != "project-id" || q.Get("expandSecretReferences") != "false" || q.Get("includeImports") != "false" {
			t.Error("wrong secret request or expanded provider boundaries")
		}
		reads.Add(1)
		io.WriteString(w, `{"secret":{"secretValue":"infisical-value","secretValueHidden":false}}`)
	})
	values, err := s.Resolve(context.Background(), "demo", "development", fixtureApp())
	if err != nil || string(values["api"]["PASSWORD"]) != "infisical-value" || reads.Load() != 1 || logins.Load() != 1 {
		t.Fatalf("Infisical resolution failed: %v", err)
	}
}

func TestProviderFailureDoesNotReturnPartialValuesOrBodies(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
		delay  bool
	}{
		{"forbidden", 403, "sensitive-provider-error", false},
		{"outage", 503, "sensitive-provider-error", false},
		{"bad-json", 200, "sensitive-provider-error", false},
		{"missing", 200, `{"data":{"data":{}}}`, false},
		{"null", 200, `{"data":{"data":{"PASSWORD":null}}}`, false},
		{"object", 200, `{"data":{"data":{"PASSWORD":{"secret":"value"}}}}`, false},
		{"too-large", 200, `{"data":{"data":{"PASSWORD":"` + strings.Repeat("x", maxValueBytes+1) + `"}}}`, false},
		{"body-limit", 200, strings.Repeat("x", maxResponseBytes+1), false},
		{"timeout", 200, `{}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _, _ := fixtureService(t, fixtureProvider("vault"), func(w http.ResponseWriter, r *http.Request) {
				if tc.delay {
					<-r.Context().Done()
					return
				}
				w.WriteHeader(tc.status)
				io.WriteString(w, tc.body)
			})
			values, err := s.Resolve(context.Background(), "demo", "development", fixtureApp())
			if values != nil || !errors.Is(err, ErrUnavailable) || strings.Contains(err.Error(), "sensitive-provider-error") {
				t.Fatal("provider failure leaked values or provider body")
			}
		})
	}
}

func TestScopesAndReferencesRejectBeforeNetwork(t *testing.T) {
	s, repo, creations := fixtureService(t, fixtureProvider("vault"), func(http.ResponseWriter, *http.Request) { t.Error("unexpected network request") })
	for _, scope := range [][2]string{{"other", "development"}, {"demo", "production"}} {
		if values, err := s.Resolve(context.Background(), scope[0], scope[1], fixtureApp()); err == nil || values != nil {
			t.Fatal("scope bypass")
		}
	}
	app := fixtureApp()
	svc := app.Services["api"]
	svc.Secrets = map[string]spec.SecretRef{"PASSWORD": {Provider: "company", Path: "../admin", Key: "PASSWORD"}}
	app.Services = map[string]spec.Service{"api": svc}
	if _, err := s.Resolve(context.Background(), "demo", "development", app); err == nil {
		t.Fatal("traversal reference accepted")
	}
	if creations.Load() != 0 {
		t.Fatal("unauthorized provider opened a client")
	}
	repo.provider.Scopes = []Scope{{Project: "demo"}}
	if !repo.provider.Allows("demo", "production") {
		t.Fatal("empty environment selection should mean all project environments")
	}
}

func TestEncryptedCredentialsBoundToProviderAndKey(t *testing.T) {
	p := fixtureProvider("vault")
	key := bytes.Repeat([]byte{8}, 32)
	sealed, err := SealCredentials(key, p, Credentials{Token: "highly-sensitive-token"})
	if err != nil || bytes.Contains(sealed, []byte("highly-sensitive-token")) {
		t.Fatal("credential encryption failed")
	}
	p.EncryptedCredentials = sealed
	data, _ := json.Marshal(p)
	if bytes.Contains(data, sealed) || bytes.Contains(data, []byte("highly-sensitive-token")) {
		t.Fatal("credentials serialized")
	}
	c, err := OpenCredentials(key, p)
	if err != nil || c.Token != "highly-sensitive-token" {
		t.Fatal("cannot decrypt")
	}
	for _, mutate := range []func(*Provider){func(p *Provider) { p.Name = "another" }, func(p *Provider) { p.Kind = "infisical" }, func(p *Provider) {
		p.EncryptedCredentials = append([]byte{}, p.EncryptedCredentials...)
		p.EncryptedCredentials[15] ^= 1
	}} {
		other := p
		mutate(&other)
		if _, err := OpenCredentials(key, other); err == nil {
			t.Fatal("substituted credentials decrypted")
		}
	}
	if _, err := OpenCredentials(bytes.Repeat([]byte{9}, 32), p); err == nil {
		t.Fatal("wrong key decrypted")
	}
	if _, err := OpenCredentials(nil, p); err == nil {
		t.Fatal("missing key accepted")
	}
}

func TestProviderAddressPolicyAndDNSPinning(t *testing.T) {
	for _, ip := range []string{"127.0.0.1", "::1", "169.254.169.254", "fe80::1", "0.0.0.0", "224.0.0.1", "100.100.100.200", "100.64.0.1", "::ffff:127.0.0.1"} {
		if allowedAddress(netip.MustParseAddr(ip), []string{"0.0.0.0/0", "::/0"}) {
			t.Fatalf("forbidden address accepted: %s", ip)
		}
	}
	if allowedAddress(netip.MustParseAddr("10.2.3.4"), nil) || !allowedAddress(netip.MustParseAddr("10.2.3.4"), []string{"10.2.3.0/24"}) || allowedAddress(netip.MustParseAddr("10.3.3.4"), []string{"10.2.3.0/24"}) {
		t.Fatal("private CIDR policy failed")
	}
	lookups, dials := 0, 0
	lookup := func(context.Context, string, string) ([]netip.Addr, error) {
		lookups++
		return []netip.Addr{netip.MustParseAddr("93.184.216.34")}, nil
	}
	dial := func(_ context.Context, network, address string) (net.Conn, error) {
		dials++
		if address != "93.184.216.34:443" {
			t.Error("hostname was resolved again")
		}
		return nil, errors.New("fixture-dial")
	}
	_, _ = safeDial(context.Background(), "tcp", "provider.example:443", nil, lookup, dial)
	if lookups != 1 || dials != 1 {
		t.Fatal("checked DNS address not used")
	}
	lookup = func(context.Context, string, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("93.184.216.34"), netip.MustParseAddr("169.254.169.254")}, nil
	}
	_, _ = safeDial(context.Background(), "tcp", "provider.example:443", nil, lookup, dial)
	if dials != 1 {
		t.Fatal("mixed DNS answer dialed")
	}
}

func TestRedirectCannotForwardCredentials(t *testing.T) {
	var destinationCalls atomic.Int32
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { destinationCalls.Add(1) }))
	defer destination.Close()
	s, _, _ := fixtureService(t, fixtureProvider("vault"), func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, destination.URL, http.StatusTemporaryRedirect)
	})
	if _, err := s.Resolve(context.Background(), "demo", "development", fixtureApp()); err == nil || destinationCalls.Load() != 0 {
		t.Fatal("credential-bearing redirect followed")
	}
}

func TestProviderConfigurationValidation(t *testing.T) {
	for _, mutate := range []func(*Provider){
		func(p *Provider) { p.Endpoint = "http://example.com" }, func(p *Provider) { p.Endpoint = "https://token@example.com" }, func(p *Provider) { p.Endpoint = "https://example.com/path" },
		func(p *Provider) { p.RootPath = "../outside" }, func(p *Provider) { p.RootPath = "apps/%2e%2e" }, func(p *Provider) { p.PrivateCIDRs = []string{"0.0.0.0/0"} }, func(p *Provider) { p.PrivateCIDRs = []string{"8.8.8.0/24"} }, func(p *Provider) { p.Scopes = nil },
	} {
		p := fixtureProvider("vault")
		mutate(&p)
		if p.Validate() == nil {
			t.Fatal("unsafe provider configuration accepted")
		}
	}
}
