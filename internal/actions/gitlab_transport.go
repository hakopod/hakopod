package actions

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"math"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
)

// GitLabTrustPolicy is resolved by the installation, never accepted as a tenant
// supplied permission grant. It applies only to its exact canonical base URL.
type GitLabTrustPolicy struct {
	Name                string
	BaseURL             string
	CAPEM               []byte
	AllowedPrivateCIDRs []string
	DeniedCIDRs         []string
}

type GitLabClientOptions struct {
	TrustPolicy    *GitLabTrustPolicy
	Budget         *RequestBudget
	TimeoutMinutes int64
	// FreshDeniedNetworks adds bounded current installation addresses to each
	// new connection. A failed inventory refresh denies the request.
	FreshDeniedNetworks func(context.Context) ([]string, error)
}

// GitLabError retains only bounded public diagnostics. It never wraps a native
// transport error, URL, response body, or credential-bearing request.
type GitLabError struct {
	Status    int
	Kind      string
	RetryAt   time.Time
	Ambiguous bool
}

func (e *GitLabError) Error() string {
	message := "GitLab request could not be completed"
	switch e.Kind {
	case "scope":
		message = "GitLab could not verify the configured scope"
	case "identity":
		message = "GitLab returned a different runner or job identity"
	case "response":
		message = "GitLab returned an invalid or oversized response"
	case "redirect":
		message = "GitLab redirected a credential-bearing request; verify the canonical instance URL"
	case "bounds":
		message = "GitLab inventory exceeds its supported bound"
	case "busy":
		message = "GitLab runner is busy or its execution state is unknown"
	case "rate":
		message = "GitLab requests are waiting for provider capacity"
	case "status":
		message = "GitLab request returned HTTP " + strconv.Itoa(e.Status)
	}
	if e.Ambiguous {
		message += "; reconcile the recorded operation before retrying"
	}
	if !e.RetryAt.IsZero() {
		message += "; retry after " + e.RetryAt.UTC().Format(time.RFC3339)
	}
	return message
}

func (e *GitLabError) Unwrap() error {
	if !e.RetryAt.IsZero() {
		return &RetryError{At: e.RetryAt, Status: e.Status}
	}
	return nil
}

type gitlabTransportPolicy struct {
	host        string
	port        string
	allowed     []netip.Prefix
	denied      []netip.Prefix
	freshDenied func(context.Context) ([]string, error)
}

func ValidateGitLabTrustPolicy(target ProviderTarget, policy *GitLabTrustPolicy) error {
	canonical, err := target.Canonical()
	if err != nil || canonical.Provider != ProviderGitLab {
		return errors.New("GitLab trust target is invalid")
	}
	_, _, err = gitlabPolicy(canonical, policy)
	return err
}

var gitlabSpecialNetworks = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"), netip.MustParsePrefix("100.64.0.0/10"), netip.MustParsePrefix("127.0.0.0/8"),
	netip.MustParsePrefix("169.254.0.0/16"), netip.MustParsePrefix("168.63.129.16/32"), netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"), netip.MustParsePrefix("198.18.0.0/15"), netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"), netip.MustParsePrefix("224.0.0.0/4"), netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("64:ff9b::/96"), netip.MustParsePrefix("64:ff9b:1::/48"), netip.MustParsePrefix("2001::/32"),
	netip.MustParsePrefix("2001:db8::/32"), netip.MustParsePrefix("2002::/16"), netip.MustParsePrefix("fd00:ec2::254/128"),
}

func gitlabPolicy(target ProviderTarget, configured *GitLabTrustPolicy) (gitlabTransportPolicy, *x509.CertPool, error) {
	u, _ := url.Parse(target.GitLab.URL)
	p := gitlabTransportPolicy{host: u.Hostname(), port: u.Port()}
	if p.port == "" {
		p.port = "443"
	}
	roots, err := x509.SystemCertPool()
	if err != nil {
		return p, nil, errors.New("GitLab system certificate trust is unavailable")
	}
	if configured == nil {
		if target.GitLab.TrustPolicy != "" || target.GitLab.URL != "https://gitlab.com" {
			return p, nil, errors.New("GitLab requires its resolved installation trust policy")
		}
		return p, roots, nil
	}
	if configured.Name != target.GitLab.TrustPolicy || configured.BaseURL != target.GitLab.URL || len(configured.CAPEM) > 64<<10 || len(configured.AllowedPrivateCIDRs) > 32 || len(configured.DeniedCIDRs) > 64 {
		return p, nil, errors.New("GitLab installation trust policy does not match the configured target or exceeds its bounds")
	}
	remaining := bytes.TrimSpace(configured.CAPEM)
	for count := 0; len(remaining) > 0; count++ {
		block, rest := pem.Decode(remaining)
		if count >= 32 || block == nil || block.Type != "CERTIFICATE" || len(block.Headers) != 0 || !bytes.HasPrefix(remaining, []byte("-----BEGIN CERTIFICATE-----")) {
			return p, nil, errors.New("GitLab installation trust policy contains an invalid CA bundle")
		}
		certificate, err := x509.ParseCertificate(block.Bytes)
		if err != nil || !certificate.IsCA || !certificate.BasicConstraintsValid {
			return p, nil, errors.New("GitLab installation trust policy requires CA certificates")
		}
		roots.AddCert(certificate)
		remaining = bytes.TrimSpace(rest)
	}
	for _, raw := range configured.AllowedPrivateCIDRs {
		prefix, err := netip.ParsePrefix(raw)
		if err != nil || prefix != prefix.Masked() || !prefix.Addr().IsPrivate() || prefix.Addr().Is4In6() || (prefix.Addr().Is4() && prefix.Bits() < 8) || (prefix.Addr().Is6() && prefix.Bits() < 7) {
			return p, nil, errors.New("GitLab installation policy must name explicit private network ranges")
		}
		p.allowed = append(p.allowed, prefix)
	}
	for _, raw := range configured.DeniedCIDRs {
		prefix, err := netip.ParsePrefix(raw)
		if err != nil || prefix != prefix.Masked() || prefix.Addr().Is4In6() {
			return p, nil, errors.New("GitLab installation policy contains an invalid denied network range")
		}
		p.denied = append(p.denied, prefix)
	}
	return p, roots, nil
}

func (p gitlabTransportPolicy) allows(address netip.Addr) bool {
	address = address.Unmap()
	if !address.IsValid() || !address.IsGlobalUnicast() || address.IsLoopback() || address.IsLinkLocalUnicast() || address.IsLinkLocalMulticast() {
		return false
	}
	for _, prefix := range gitlabSpecialNetworks {
		if prefix.Contains(address) {
			return false
		}
	}
	for _, prefix := range p.denied {
		if prefix.Contains(address) {
			return false
		}
	}
	if !address.IsPrivate() {
		return true
	}
	for _, prefix := range p.allowed {
		if prefix.Contains(address) {
			return true
		}
	}
	return false
}

func (p gitlabTransportPolicy) dial(ctx context.Context, network, address string, lookup func(context.Context, string, string) ([]netip.Addr, error), dial func(context.Context, string, string) (net.Conn, error)) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil || host != p.host || port != p.port {
		return nil, &GitLabError{Kind: "scope"}
	}
	if p.freshDenied != nil {
		// A policy value still shares slice capacity with other connections.
		// Inventory belongs to this dial and must not mutate that shared backing.
		p.denied = slices.Clone(p.denied)
		denied, err := p.freshDenied(ctx)
		if err != nil || len(denied) > 4096 {
			return nil, &GitLabError{Kind: "scope"}
		}
		for _, raw := range denied {
			prefix, err := netip.ParsePrefix(raw)
			if err != nil || prefix != prefix.Masked() {
				return nil, &GitLabError{Kind: "scope"}
			}
			p.denied = append(p.denied, prefix)
		}
	}
	addresses, err := lookup(ctx, "ip", host)
	if err != nil || len(addresses) < 1 || len(addresses) > 16 {
		return nil, &GitLabError{Kind: "scope"}
	}
	for _, address := range addresses {
		if !p.allows(address) {
			return nil, &GitLabError{Kind: "scope"}
		}
	}
	for _, address := range addresses {
		connection, err := dial(ctx, network, net.JoinHostPort(address.Unmap().String(), port))
		if err == nil {
			return connection, nil
		}
		if ctx.Err() != nil {
			break
		}
	}
	return nil, &GitLabError{Kind: "transport"}
}

func newGitLabHTTP(policy gitlabTransportPolicy, roots *x509.CertPool) *http.Client {
	dialer := &net.Dialer{Timeout: 5 * time.Second}
	transport := &http.Transport{
		Proxy: nil, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots, ServerName: policy.host},
		TLSHandshakeTimeout: 5 * time.Second, ResponseHeaderTimeout: 5 * time.Second, MaxResponseHeaderBytes: 16 << 10,
		MaxConnsPerHost: 2, MaxIdleConns: 2, IdleConnTimeout: 15 * time.Second, DisableCompression: true,
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			return policy.dial(ctx, network, address, net.DefaultResolver.LookupNetIP, dialer.DialContext)
		},
	}
	return &http.Client{Transport: transport, Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

type gitlabReservationKey struct{}
type gitlabReservation struct {
	client    *GitLabClient
	state     *credentialBudget
	remaining int
}

func (c *GitLabClient) reserve(ctx context.Context, cost int) (context.Context, func(), error) {
	if err := ctx.Err(); err != nil {
		return ctx, nil, err
	}
	if c.budget == nil {
		return ctx, func() {}, nil
	}
	if existing, ok := ctx.Value(gitlabReservationKey{}).(*gitlabReservation); ok && existing.client == c {
		return ctx, func() {}, nil
	}
	state, err := c.budget.reserve(c.budgetKey, cost)
	if err != nil {
		var retry *RetryError
		if errors.As(err, &retry) {
			return ctx, nil, &GitLabError{Kind: "rate", Status: retry.Status, RetryAt: retry.At}
		}
		return ctx, nil, &GitLabError{Kind: "rate"}
	}
	r := &gitlabReservation{client: c, state: state, remaining: cost}
	return context.WithValue(ctx, gitlabReservationKey{}, r), func() {
		c.budget.mu.Lock()
		state.tokens = math.Min(credentialBurst, state.tokens+float64(r.remaining))
		state.inflight--
		c.budget.mu.Unlock()
	}, nil
}

func (c *GitLabClient) request(ctx context.Context, method, path string, query url.Values, body any) (*http.Response, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !strings.HasPrefix(path, "/") || strings.ContainsAny(path, "?#%\\") || strings.Contains(path, "..") {
		return nil, &GitLabError{Kind: "scope"}
	}
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil || len(encoded) > 128<<10 {
			return nil, &GitLabError{Kind: "response"}
		}
		reader = bytes.NewReader(encoded)
	}
	address := c.target.GitLab.URL + "/api/v4" + path
	if len(query) > 0 {
		address += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, address, reader)
	if err != nil {
		return nil, &GitLabError{Kind: "scope"}
	}
	req.Header.Set("PRIVATE-TOKEN", c.credential)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	var state *credentialBudget
	if c.budget != nil {
		r, ok := ctx.Value(gitlabReservationKey{}).(*gitlabReservation)
		if !ok || r.client != c || r.remaining <= 0 {
			return nil, &GitLabError{Kind: "bounds"}
		}
		state = r.state
		c.budget.mu.Lock()
		blocked, until, status := state.blocked.After(c.budget.time()), state.blocked, state.status
		c.budget.mu.Unlock()
		if blocked {
			return nil, &GitLabError{Kind: "rate", Status: status, RetryAt: until}
		}
		r.remaining--
	}
	markRequest(ctx)
	res, err := c.http.Do(req)
	if err != nil {
		if state != nil && ctx.Err() == nil {
			c.budget.observe(state, 0, nil)
		}
		return nil, c.failure("transport", 0, method == http.MethodPost || method == http.MethodDelete)
	}
	if state != nil {
		c.budget.observe(state, res.StatusCode, res.Header)
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		_ = res.Body.Close()
		kind := "status"
		if res.StatusCode >= 300 && res.StatusCode < 400 {
			kind = "redirect"
		}
		return nil, c.failure(kind, res.StatusCode, false)
	}
	return res, nil
}

func (c *GitLabClient) failure(kind string, status int, ambiguous bool) *GitLabError {
	failure := &GitLabError{Kind: kind, Status: status, Ambiguous: ambiguous}
	if c.budget != nil {
		c.budget.mu.Lock()
		if state := c.budget.entries[c.budgetKey]; state != nil && state.blocked.After(c.budget.time()) {
			failure.RetryAt = state.blocked
		}
		c.budget.mu.Unlock()
	}
	return failure
}

func (c *GitLabClient) json(ctx context.Context, method, path string, query url.Values, body, out any) (http.Header, error) {
	res, err := c.request(ctx, method, path, query, body)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	data, err := io.ReadAll(io.LimitReader(res.Body, (1<<20)+1))
	if err != nil || len(data) > 1<<20 || (out != nil && json.Unmarshal(data, out) != nil) {
		return nil, c.failure("response", res.StatusCode, method == http.MethodPost)
	}
	return res.Header, nil
}

func gitlabBudgetKey(target ProviderTarget, credential string) [32]byte {
	u, _ := url.Parse(target.GitLab.URL)
	return sha256.Sum256([]byte("gitlab\x00" + u.Scheme + "://" + u.Host + "\x00" + credential))
}
