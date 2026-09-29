package actions

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strconv"
	"strings"
	"testing"
	"time"
)

func gitlabTLSFixture(t *testing.T, handler http.Handler) (*httptest.Server, []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	certificate := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "GitLab development fixture"}, DNSNames: []string{"gitlab.example.test"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, certificate, certificate, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	ca := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	server := httptest.NewUnstartedServer(handler)
	server.TLS = &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}, MinVersion: tls.VersionTLS12}
	server.StartTLS()
	t.Cleanup(server.Close)
	return server, ca
}

func gitlabTLSClient(t *testing.T, server *httptest.Server, ca []byte, host string) *GitLabClient {
	t.Helper()
	_, port, _ := net.SplitHostPort(server.Listener.Addr().String())
	base := "https://" + net.JoinHostPort(host, port) + "/team/gitlab"
	target := ProviderTarget{Provider: ProviderGitLab, GitLab: &GitLabTarget{URL: base, ProjectID: 12, TrustPolicy: "development-fixture"}}
	policy := &GitLabTrustPolicy{Name: "development-fixture", BaseURL: base, CAPEM: ca}
	c, err := NewGitLabClient(target, gitlabFixtureCredential, GitLabClientOptions{TrustPolicy: policy})
	if err != nil {
		t.Fatal(err)
	}
	checked, _, err := gitlabPolicy(c.target, policy)
	if err != nil {
		t.Fatal(err)
	}
	transport := c.http.Transport.(*http.Transport)
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		return checked.dial(ctx, network, address,
			func(context.Context, string, string) ([]netip.Addr, error) {
				return []netip.Addr{netip.MustParseAddr("8.8.8.8")}, nil
			},
			func(ctx context.Context, network, address string) (net.Conn, error) {
				if address != net.JoinHostPort("8.8.8.8", port) {
					t.Error("dial did not use the validated address")
				}
				var dialer net.Dialer
				return dialer.DialContext(ctx, network, server.Listener.Addr().String())
			})
	}
	t.Cleanup(transport.CloseIdleConnections)
	return c
}

func TestGitLabTransportPinsTLSHostPreservesPrefixAndIgnoresProxy(t *testing.T) {
	t.Setenv("HTTPS_PROXY", "http://untrusted-proxy.example.test:8080")
	calls := 0
	server, ca := gitlabTLSFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/team/gitlab/api/v4/projects/12" || r.Header.Get("PRIVATE-TOKEN") != gitlabFixtureCredential || r.TLS.ServerName != "gitlab.example.test" {
			t.Error("request lost its prefix, credential scope or TLS identity")
		}
		_, _ = io.WriteString(w, `{"id":12}`)
	}))
	c := gitlabTLSClient(t, server, ca, "gitlab.example.test")
	transport := c.http.Transport.(*http.Transport)
	if transport.Proxy != nil || transport.TLSClientConfig.InsecureSkipVerify || transport.TLSClientConfig.MinVersion < tls.VersionTLS12 || transport.MaxConnsPerHost != 2 {
		t.Fatal("production transport weakened its connection or TLS policy")
	}
	var result struct {
		ID int64 `json:"id"`
	}
	if _, err := c.json(context.Background(), http.MethodGet, "/projects/12", nil, nil, &result); err != nil || result.ID != 12 || calls != 1 {
		t.Fatal("scoped TLS fixture request failed", err)
	}
}

func TestGitLabTransportRejectsUntrustedCAAndWrongHostname(t *testing.T) {
	server, ca := gitlabTLSFixture(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("untrusted TLS connection reached an API handler") }))
	for name, entry := range map[string]struct {
		ca   []byte
		host string
	}{
		"untrusted CA":   {nil, "gitlab.example.test"},
		"wrong hostname": {ca, "other.example.test"},
	} {
		t.Run(name, func(t *testing.T) {
			c := gitlabTLSClient(t, server, entry.ca, entry.host)
			_, err := c.json(context.Background(), http.MethodGet, "/projects/12", nil, nil, &map[string]any{})
			if err == nil || strings.Contains(err.Error(), gitlabFixtureCredential) || strings.Contains(err.Error(), entry.host) {
				t.Fatal("TLS error accepted or leaked native request details")
			}
		})
	}
}

func TestGitLabTransportNeverFollowsCredentialRedirects(t *testing.T) {
	for _, status := range []int{301, 302, 303, 307, 308} {
		for _, location := range []string{"/outside-prefix", "/team/gitlab/api/v4/other", "https://other.example.test/leak", "http://gitlab.example.test/leak", "https://gitlab.example.test:8443/leak"} {
			t.Run(strconv.Itoa(status)+location, func(t *testing.T) {
				calls := 0
				server, ca := gitlabTLSFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls++
					if calls > 1 {
						t.Error("redirect received a second request")
					}
					w.Header().Set("Location", location+"?secret=fixture-signed-value")
					w.WriteHeader(status)
				}))
				c := gitlabTLSClient(t, server, ca, "gitlab.example.test")
				_, err := c.json(context.Background(), http.MethodPost, "/user/runners", nil, map[string]any{"description": gitlabFixtureName}, &map[string]any{})
				var failure *GitLabError
				if !errors.As(err, &failure) || failure.Kind != "redirect" || calls != 1 || strings.Contains(err.Error(), "fixture-signed-value") || strings.Contains(err.Error(), gitlabFixtureCredential) {
					t.Fatal("credential redirect was followed or disclosed its location")
				}
			})
		}
	}
}

func TestGitLabPolicyRejectsMismatchedOrUnboundedTrust(t *testing.T) {
	target := ProviderTarget{Provider: ProviderGitLab, GitLab: &GitLabTarget{URL: "https://git.example.test/gitlab", ProjectID: 12, TrustPolicy: "company"}}
	for name, policy := range map[string]*GitLabTrustPolicy{
		"missing":              nil,
		"wrong name":           {Name: "other", BaseURL: target.GitLab.URL},
		"wrong base":           {Name: "company", BaseURL: "https://git.example.test/other"},
		"noncanonical base":    {Name: "company", BaseURL: target.GitLab.URL + "/"},
		"invalid CA":           {Name: "company", BaseURL: target.GitLab.URL, CAPEM: []byte("not a CA")},
		"large CA":             {Name: "company", BaseURL: target.GitLab.URL, CAPEM: make([]byte, (64<<10)+1)},
		"public allow range":   {Name: "company", BaseURL: target.GitLab.URL, AllowedPrivateCIDRs: []string{"0.0.0.0/0"}},
		"noncanonical range":   {Name: "company", BaseURL: target.GitLab.URL, AllowedPrivateCIDRs: []string{"10.0.0.1/24"}},
		"invalid denied range": {Name: "company", BaseURL: target.GitLab.URL, DeniedCIDRs: []string{"invalid"}},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := NewGitLabClient(target, gitlabFixtureCredential, GitLabClientOptions{TrustPolicy: policy}); err == nil {
				t.Fatal("unresolved or invalid installation policy accepted")
			}
		})
	}
}

func TestGitLabDNSChecksEveryAnswerAndPinsTheDial(t *testing.T) {
	policy := gitlabTransportPolicy{host: "git.example.test", port: "443", allowed: []netip.Prefix{netip.MustParsePrefix("10.2.0.0/16"), netip.MustParsePrefix("fd00::/8")}, denied: []netip.Prefix{netip.MustParsePrefix("10.2.3.0/24"), netip.MustParsePrefix("8.8.4.4/32")}}
	for name, values := range map[string][]string{
		"mixed metadata": {"8.8.8.8", "169.254.169.254"},
		"loopback":       {"127.0.0.1"}, "mapped loopback": {"::ffff:127.0.0.1"}, "link local": {"fe80::1"},
		"unspecified": {"0.0.0.0"}, "multicast": {"ff02::1"}, "unapproved private": {"10.3.1.1"},
		"platform override": {"10.2.3.4"}, "public platform": {"8.8.4.4"}, "azure metadata": {"168.63.129.16"},
		"aws ipv6 metadata": {"fd00:ec2::254"}, "carrier metadata": {"100.100.100.200"}, "translation": {"64:ff9b::808:808"},
		"empty": {},
	} {
		t.Run(name, func(t *testing.T) {
			addresses := []netip.Addr{}
			for _, value := range values {
				addresses = append(addresses, netip.MustParseAddr(value))
			}
			called := false
			_, err := policy.dial(context.Background(), "tcp", "git.example.test:443", func(context.Context, string, string) ([]netip.Addr, error) { return addresses, nil }, func(context.Context, string, string) (net.Conn, error) { called = true; return nil, nil })
			if err == nil || called {
				t.Fatal("unsafe DNS answer reached the dialer")
			}
		})
	}
	for _, literal := range []string{"8.8.8.8", "10.2.4.5", "::ffff:8.8.8.8", "fd00::123"} {
		lookups, dials := 0, 0
		client, server := net.Pipe()
		defer client.Close()
		defer server.Close()
		connection, err := policy.dial(context.Background(), "tcp", "git.example.test:443", func(context.Context, string, string) ([]netip.Addr, error) {
			lookups++
			return []netip.Addr{netip.MustParseAddr(literal)}, nil
		}, func(_ context.Context, _ string, address string) (net.Conn, error) {
			dials++
			if address != net.JoinHostPort(netip.MustParseAddr(literal).Unmap().String(), "443") {
				t.Error("DNS was not pinned")
			}
			return client, nil
		})
		if err != nil || connection == nil || lookups != 1 || dials != 1 {
			t.Fatal("allowed address was not pinned", err)
		}
	}
	for _, destination := range []string{"other.example.test:443", "git.example.test:8443"} {
		_, err := policy.dial(context.Background(), "tcp", destination, func(context.Context, string, string) ([]netip.Addr, error) {
			t.Error("wrong origin performed DNS")
			return nil, nil
		}, nil)
		if err == nil {
			t.Fatal("dial accepted a different origin")
		}
	}
}
