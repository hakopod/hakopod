package secretprovider

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"
)

const maxResponseBytes = 1 << 20
const maxValueBytes = 64 << 10

type providerClient struct {
	provider Provider
	http     *http.Client
	token    string
}

func allowedAddress(ip netip.Addr, cidrs []string) bool {
	ip = ip.Unmap()
	// Metadata and cluster-local loopback/link-local access is never permitted,
	// including when an operator explicitly grants a private subnet.
	if !ip.IsGlobalUnicast() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip == netip.MustParseAddr("100.100.100.200") {
		return false
	}
	if !ip.IsPrivate() {
		// Shared carrier space and benchmark networks are not public providers.
		for _, raw := range []string{"100.64.0.0/10", "198.18.0.0/15", "192.0.0.0/24", "240.0.0.0/4", "64:ff9b::/96", "64:ff9b:1::/48", "2002::/16", "2001::/32"} {
			if netip.MustParsePrefix(raw).Contains(ip) {
				return false
			}
		}
		return true
	}
	for _, raw := range cidrs {
		prefix, err := netip.ParsePrefix(raw)
		if err == nil && prefix.Contains(ip) {
			return true
		}
	}
	return false
}

func safeDial(ctx context.Context, network, address string, cidrs []string, lookup func(context.Context, string, string) ([]netip.Addr, error), dial func(context.Context, string, string) (net.Conn, error)) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, ErrUnavailable
	}
	addresses, err := lookup(ctx, "ip", host)
	if err != nil || len(addresses) == 0 || len(addresses) > 16 {
		return nil, ErrUnavailable
	}
	for _, ip := range addresses {
		if !allowedAddress(ip, cidrs) {
			return nil, ErrUnavailable
		}
	}
	// Dial the checked address directly; a second DNS lookup would allow rebinding.
	return dial(ctx, network, net.JoinHostPort(addresses[0].String(), port))
}

func newProviderClient(p Provider) (*providerClient, error) {
	if p.Validate() != nil {
		return nil, ErrUnavailable
	}
	roots, err := x509.SystemCertPool()
	if err != nil {
		return nil, ErrUnavailable
	}
	if p.CACert != "" && !roots.AppendCertsFromPEM([]byte(p.CACert)) {
		return nil, ErrUnavailable
	}
	dialer := &net.Dialer{Timeout: 5 * time.Second}
	transport := &http.Transport{
		Proxy: nil, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots},
		TLSHandshakeTimeout: 5 * time.Second, ResponseHeaderTimeout: 5 * time.Second,
		MaxResponseHeaderBytes: 16 << 10, MaxConnsPerHost: 1, MaxIdleConns: 1, IdleConnTimeout: 15 * time.Second,
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			return safeDial(ctx, network, address, p.PrivateCIDRs, net.DefaultResolver.LookupNetIP, dialer.DialContext)
		},
	}
	return &providerClient{provider: p, http: &http.Client{Transport: transport, Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return ErrUnavailable }}}, nil
}

func (c *providerClient) request(ctx context.Context, method, path string, query url.Values, body any, output any) error {
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return ErrUnavailable
		}
		reader = bytes.NewReader(data)
	}
	u := strings.TrimSuffix(c.provider.Endpoint, "/") + path
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
	if c.token != "" {
		if c.provider.Kind == "vault" {
			req.Header.Set("X-Vault-Token", c.token)
		} else {
			req.Header.Set("Authorization", "Bearer "+c.token)
		}
	}
	if c.provider.Namespace != "" {
		req.Header.Set("X-Vault-Namespace", c.provider.Namespace)
	}
	res, err := c.http.Do(req)
	if err != nil {
		return ErrUnavailable
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return ErrUnavailable
	}
	data, err := io.ReadAll(io.LimitReader(res.Body, maxResponseBytes+1))
	if err != nil || len(data) > maxResponseBytes || json.Unmarshal(data, output) != nil {
		return ErrUnavailable
	}
	return nil
}

func (c *providerClient) authenticate(ctx context.Context, credentials Credentials) error {
	if credentials.Validate(c.provider.Kind) != nil {
		return ErrUnavailable
	}
	if c.provider.Kind == "vault" {
		c.token = credentials.Token
		return nil
	}
	var output struct {
		AccessToken string `json:"accessToken"`
	}
	err := c.request(ctx, http.MethodPost, "/api/v1/auth/universal-auth/login", nil, map[string]string{"clientId": credentials.ClientID, "clientSecret": credentials.ClientSecret}, &output)
	if err != nil || output.AccessToken == "" || len(output.AccessToken) > 8192 || strings.ContainsAny(output.AccessToken, "\r\n\x00") {
		return ErrUnavailable
	}
	c.token = output.AccessToken
	return nil
}

func (c *providerClient) read(ctx context.Context, path, key string) ([]byte, error) {
	location := strings.Trim(strings.Join([]string{c.provider.RootPath, path}, "/"), "/")
	var value *string
	if c.provider.Kind == "vault" {
		if location == "" {
			return nil, ErrUnavailable
		}
		var output struct {
			Data struct {
				Data map[string]json.RawMessage `json:"data"`
			} `json:"data"`
		}
		if err := c.request(ctx, http.MethodGet, "/v1/"+c.provider.Mount+"/data/"+location, nil, nil, &output); err != nil {
			return nil, err
		}
		if json.Unmarshal(output.Data.Data[key], &value) != nil {
			return nil, ErrUnavailable
		}
	} else {
		var output struct {
			Secret struct {
				Value  *string `json:"secretValue"`
				Hidden bool    `json:"secretValueHidden"`
			} `json:"secret"`
		}
		query := url.Values{"projectId": {c.provider.ProjectID}, "environment": {c.provider.Environment}, "secretPath": {"/" + location}, "type": {"shared"}, "expandSecretReferences": {"false"}, "includeImports": {"false"}}
		if err := c.request(ctx, http.MethodGet, "/api/v4/secrets/"+url.PathEscape(key), query, nil, &output); err != nil || output.Secret.Hidden {
			return nil, ErrUnavailable
		}
		value = output.Secret.Value
	}
	if value == nil || len(*value) > maxValueBytes || strings.ContainsRune(*value, 0) {
		return nil, ErrUnavailable
	}
	return []byte(*value), nil
}
