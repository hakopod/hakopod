package dnsprovider

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"
)

// Every bound below is copied from the existing outbound clients,
// internal/api/deployment_notifications.go (notificationClient) and
// internal/secretprovider/http.go, so all of hakopod's outbound HTTP agrees on
// its ceilings.
const (
	requestTimeout         = 10 * time.Second // whole request, including reading the body
	dialTimeout            = 5 * time.Second  // TCP connect
	tlsHandshakeTimeout    = 5 * time.Second  // TLS handshake
	responseHeaderTimeout  = 5 * time.Second  // first response byte after the request is sent
	idleConnTimeout        = 30 * time.Second // pooled connection lifetime
	maxResponseHeaderBytes = 16 << 10         // header bytes accepted before the response is dropped
	maxResponseBytes       = 1 << 20          // body bytes read; a zone or record page is far smaller
	maxResolvedAddresses   = 16               // a resolver answering with a flood is not dialed
	maxConnsPerHost        = 1                // one provider call at a time per client
)

// The endpoint is a pinned public host, not user supplied, so the per-provider
// private-CIDR allow list in internal/secretprovider/http.go is not needed
// here: no operator can point this client at a private address on purpose. The
// address check below still stands, because DNS for a public name can answer
// with a private or metadata address (rebinding). Add the secretprovider
// machinery only when the endpoint becomes configurable, for instance for a
// self-hosted or air-gapped DNS API.
func publicAddress(ip netip.Addr) bool {
	ip = ip.Unmap()
	return ip.IsGlobalUnicast() && !ip.IsPrivate() && !ip.IsLoopback() && !ip.IsLinkLocalUnicast() && !ip.IsLinkLocalMulticast()
}

func newBoundedClient() *http.Client {
	dialer := &net.Dialer{Timeout: dialTimeout}
	return &http.Client{
		Timeout: requestTimeout,
		// Redirects are refused rather than followed: a redirect could carry the
		// API token to a host hakopod never chose.
		CheckRedirect: func(*http.Request, []*http.Request) error { return ErrUnavailable },
		Transport: &http.Transport{
			Proxy:                  nil, // no proxy: the token must not pass through one
			TLSClientConfig:        &tls.Config{MinVersion: tls.VersionTLS12},
			TLSHandshakeTimeout:    tlsHandshakeTimeout,
			ResponseHeaderTimeout:  responseHeaderTimeout,
			MaxResponseHeaderBytes: maxResponseHeaderBytes,
			MaxConnsPerHost:        maxConnsPerHost,
			MaxIdleConns:           maxConnsPerHost,
			IdleConnTimeout:        idleConnTimeout,
			DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
				host, port, err := net.SplitHostPort(address)
				if err != nil {
					return nil, ErrUnavailable
				}
				addresses, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
				if err != nil || len(addresses) == 0 || len(addresses) > maxResolvedAddresses {
					return nil, ErrUnavailable
				}
				for _, ip := range addresses {
					if !publicAddress(ip) {
						return nil, ErrUnavailable
					}
				}
				// Dial the checked address directly; a second lookup would allow rebinding.
				return dialer.DialContext(ctx, network, net.JoinHostPort(addresses[0].String(), port))
			},
		},
	}
}

// transport carries the bounded client, the pinned endpoint and the token.
// Package tests replace http and endpoint to run without a network, the way
// internal/secretprovider/service.go makes its client injectable.
type transport struct {
	http     *http.Client
	endpoint string
	token    string
}

// request sends a JSON request and decodes a bounded response. No status text,
// response body or transport error ever reaches the returned error: these
// failures surface in the API, so they collapse to the package sentinels.
func (t *transport) request(ctx context.Context, method, path string, query url.Values, body, output any) error {
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return ErrUnavailable
		}
		reader = bytes.NewReader(data)
	}
	u := strings.TrimSuffix(t.endpoint, "/") + path
	if len(query) != 0 {
		u += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, u, reader)
	if err != nil {
		return ErrUnavailable
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Authorization", "Bearer "+t.token)
	res, err := t.http.Do(req)
	if err != nil {
		return ErrUnavailable
	}
	defer res.Body.Close()
	data, err := io.ReadAll(io.LimitReader(res.Body, maxResponseBytes+1))
	if err != nil || len(data) > maxResponseBytes {
		return ErrUnavailable
	}
	// The provider reports application failures inside a successful envelope, so
	// the body is decoded whatever the status, then judged by the envelope.
	if json.Unmarshal(data, output) != nil {
		return ErrUnavailable
	}
	if res.StatusCode >= 500 {
		return ErrUnavailable
	}
	return nil
}
