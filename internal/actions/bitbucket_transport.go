package actions

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// BitbucketError contains no provider response, request, URL or credential.
type BitbucketError struct {
	Status    int
	Kind      string
	RetryAt   time.Time
	Ambiguous bool
}

func (e *BitbucketError) Error() string {
	message := "Bitbucket request could not be completed"
	switch e.Kind {
	case "scope":
		message = "Bitbucket could not verify the configured scope"
	case "identity":
		message = "Bitbucket could not verify the recorded runner identity"
	case "response":
		message = "Bitbucket returned an invalid or oversized response"
	case "redirect":
		message = "Bitbucket redirected a credential-bearing request"
	case "bounds":
		message = "Bitbucket inventory exceeds its supported bound"
	case "drain":
		message = "Bitbucket cannot verify safe cleanup of a previously started runner"
	case "rate":
		message = "Bitbucket requests are waiting for provider capacity"
	case "status":
		message = "Bitbucket request returned HTTP " + strconv.Itoa(e.Status)
	}
	if e.Ambiguous {
		message += "; reconcile the recorded operation before retrying"
	}
	if !e.RetryAt.IsZero() {
		message += "; retry after " + e.RetryAt.UTC().Format(time.RFC3339)
	}
	return message
}

func (e *BitbucketError) Unwrap() error {
	if !e.RetryAt.IsZero() {
		return &RetryError{At: e.RetryAt, Status: e.Status}
	}
	return nil
}

func bitbucketPrivateString(value string, minimum, maximum int) bool {
	return len(value) >= minimum && len(value) <= maximum && strings.IndexFunc(value, func(r rune) bool { return r <= ' ' || r >= 127 }) < 0
}

func bitbucketOptionalPrivateString(value string, maximum int) bool {
	return value == "" || bitbucketPrivateString(value, 1, maximum)
}

func bitbucketAuthorization(credential BitbucketCredential) (string, error) {
	invalid := errors.New("supply either a Bitbucket OAuth access token or an Atlassian account email and scoped API token")
	if credential.AccessToken != "" {
		if credential.Email != "" || credential.APIToken != "" || !bitbucketPrivateString(credential.AccessToken, 16, 4096) {
			return "", invalid
		}
		return "Bearer " + credential.AccessToken, nil
	}
	if !bitbucketPrivateString(credential.Email, 3, 254) || strings.Count(credential.Email, "@") != 1 || strings.Contains(credential.Email, ":") || !bitbucketPrivateString(credential.APIToken, 16, 4096) {
		return "", invalid
	}
	req := &http.Request{Header: make(http.Header)}
	req.SetBasicAuth(credential.Email, credential.APIToken)
	return req.Header.Get("Authorization"), nil
}

func newBitbucketHTTP() *http.Client {
	dialer := &net.Dialer{Timeout: 5 * time.Second}
	// Reuse the validated-address dialer, with no private-network grants. The
	// origin is fixed and cannot be supplied by either a tenant or a response.
	policy := gitlabTransportPolicy{host: "api.bitbucket.org", port: "443"}
	transport := &http.Transport{
		Proxy: nil, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, ServerName: "api.bitbucket.org"},
		TLSHandshakeTimeout: 5 * time.Second, ResponseHeaderTimeout: 5 * time.Second, MaxResponseHeaderBytes: 16 << 10,
		MaxConnsPerHost: 2, MaxIdleConns: 2, IdleConnTimeout: 15 * time.Second, DisableCompression: true,
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			connection, err := policy.dial(ctx, network, address, net.DefaultResolver.LookupNetIP, dialer.DialContext)
			if err != nil {
				return nil, &BitbucketError{Kind: "transport"}
			}
			return connection, nil
		},
	}
	return &http.Client{Transport: transport, Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

type bitbucketReservationKey struct{}
type bitbucketReservation struct {
	client    *BitbucketClient
	state     *credentialBudget
	remaining int
}

func (c *BitbucketClient) reserve(ctx context.Context, cost int) (context.Context, func(), error) {
	if err := ctx.Err(); err != nil {
		return ctx, nil, err
	}
	state, err := c.budget.reserve(c.budgetKey, cost)
	if err != nil {
		var retry *RetryError
		if errors.As(err, &retry) {
			return ctx, nil, &BitbucketError{Kind: "rate", Status: retry.Status, RetryAt: retry.At}
		}
		return ctx, nil, &BitbucketError{Kind: "rate"}
	}
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	r := &bitbucketReservation{client: c, state: state, remaining: cost}
	var once sync.Once
	return context.WithValue(ctx, bitbucketReservationKey{}, r), func() {
		once.Do(func() {
			cancel()
			c.budget.mu.Lock()
			state.tokens = math.Min(credentialBurst, state.tokens+float64(r.remaining))
			state.inflight--
			c.budget.mu.Unlock()
		})
	}, nil
}

func (c *BitbucketClient) json(ctx context.Context, method, path string, query url.Values, body any, status int, out any) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if path != c.runnerScope() {
		id := strings.TrimPrefix(path, c.runnerScope()+"/")
		canonical, ok := canonicalProviderUUID(id)
		if !ok || canonical != id {
			return &BitbucketError{Kind: "scope"}
		}
	}
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil || len(encoded) > 128<<10 {
			return &BitbucketError{Kind: "response"}
		}
		reader = bytes.NewReader(encoded)
	}
	address := &url.URL{Scheme: "https", Host: "api.bitbucket.org", Path: path, RawQuery: query.Encode()}
	req, err := http.NewRequestWithContext(ctx, method, address.String(), reader)
	if err != nil {
		return &BitbucketError{Kind: "scope"}
	}
	req.Header.Set("Authorization", c.authorization)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	r, ok := ctx.Value(bitbucketReservationKey{}).(*bitbucketReservation)
	if !ok || r.client != c || r.remaining <= 0 {
		return &BitbucketError{Kind: "bounds"}
	}
	c.budget.mu.Lock()
	blocked, until, lastStatus := r.state.blocked.After(c.budget.time()), r.state.blocked, r.state.status
	c.budget.mu.Unlock()
	if blocked {
		return &BitbucketError{Kind: "rate", Status: lastStatus, RetryAt: until}
	}
	r.remaining--
	mutation := method == http.MethodPost || method == http.MethodDelete
	markRequest(ctx)
	res, err := c.http.Do(req)
	if err != nil {
		if ctx.Err() == nil {
			c.budget.observe(r.state, 0, nil)
		}
		return c.failure("transport", 0, mutation)
	}
	defer res.Body.Close()
	c.budget.observe(r.state, res.StatusCode, res.Header)
	if res.StatusCode != status {
		kind := "status"
		if res.StatusCode >= 300 && res.StatusCode < 400 {
			kind = "redirect"
		}
		// A failed write can still have reached the provider, including a gateway
		// failure after commit. Recovery uses the original recorded name and ID.
		ambiguous := mutation && (res.StatusCode >= 500 || res.StatusCode == http.StatusRequestTimeout || res.StatusCode == http.StatusConflict || (res.StatusCode >= 200 && res.StatusCode < 300))
		return c.failure(kind, res.StatusCode, ambiguous)
	}
	limit := int64(1 << 20)
	if out == nil {
		limit = 0
	}
	data, err := io.ReadAll(io.LimitReader(res.Body, limit+1))
	if err != nil || int64(len(data)) > limit || (out != nil && json.Unmarshal(data, out) != nil) {
		return c.failure("response", res.StatusCode, mutation)
	}
	return nil
}

func (c *BitbucketClient) failure(kind string, status int, ambiguous bool) *BitbucketError {
	failure := &BitbucketError{Kind: kind, Status: status, Ambiguous: ambiguous}
	c.budget.mu.Lock()
	if state := c.budget.entries[c.budgetKey]; state != nil && state.blocked.After(c.budget.time()) {
		failure.RetryAt = state.blocked
	}
	c.budget.mu.Unlock()
	return failure
}
