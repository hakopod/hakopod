package managedplatform

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"

	"github.com/hakopod/hakopod/internal/externaldatabase"
)

func ValidateObjectStorageOrigin(value string) error {
	if err := ValidateHTTPSOrigin(value, "object_storage_url"); err != nil {
		return err
	}
	u, _ := url.Parse(value)
	host := u.Hostname()
	if ip, err := netip.ParseAddr(host); err == nil {
		if !externaldatabase.PublicAddress(ip) {
			return fmt.Errorf("object storage addresses require public egress")
		}
	} else if !strings.Contains(host, ".") || strings.HasSuffix(host, ".localhost") || strings.HasSuffix(host, ".local") || strings.HasSuffix(host, ".internal") || strings.HasSuffix(host, ".svc") || strings.HasSuffix(host, ".cluster.local") {
		return fmt.Errorf("object storage requires a public hostname")
	}
	return nil
}

// ObjectStorageClient is scoped to one origin. DNS is checked on every new
// connection and the checked address is dialed without a second DNS lookup.
// Private storage needs a separately reviewed private-egress contract.
type ObjectStorageClient struct {
	host      string
	authority string
	client    *http.Client
	lookup    func(context.Context, string, string) ([]netip.Addr, error)
	dial      func(context.Context, string, string) (net.Conn, error)
}

func NewObjectStorageClient(origin string) (*ObjectStorageClient, error) {
	if err := ValidateObjectStorageOrigin(origin); err != nil {
		return nil, err
	}
	u, _ := url.Parse(origin)
	c := &ObjectStorageClient{host: u.Hostname(), authority: u.Host, lookup: net.DefaultResolver.LookupNetIP, dial: (&net.Dialer{Timeout: 5 * time.Second}).DialContext}
	transport := &http.Transport{Proxy: nil, DialContext: c.dialContext, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, ServerName: c.host}, MaxIdleConns: 4, MaxIdleConnsPerHost: 2, MaxConnsPerHost: 4, IdleConnTimeout: 30 * time.Second, TLSHandshakeTimeout: 5 * time.Second, ResponseHeaderTimeout: 10 * time.Second, MaxResponseHeaderBytes: 32 << 10}
	c.client = &http.Client{Transport: transport, Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error {
		return fmt.Errorf("object storage redirects are not permitted")
	}}
	return c, nil
}

func (c *ObjectStorageClient) dialContext(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil || network != "tcp" || host != c.host || port != "443" {
		return nil, fmt.Errorf("object storage connection target changed")
	}
	addresses, err := c.lookup(ctx, "ip", host)
	if err != nil || len(addresses) == 0 || len(addresses) > 16 {
		return nil, fmt.Errorf("object storage DNS is unavailable")
	}
	for _, ip := range addresses {
		if !externaldatabase.PublicAddress(ip) {
			return nil, fmt.Errorf("object storage DNS includes a denied address")
		}
	}
	for _, ip := range addresses {
		conn, err := c.dial(ctx, "tcp", net.JoinHostPort(ip.Unmap().String(), "443"))
		if err == nil {
			return conn, nil
		}
		if ctx.Err() != nil {
			break
		}
	}
	return nil, fmt.Errorf("object storage connection is unavailable")
}

func (c *ObjectStorageClient) Do(request *http.Request) (*http.Response, error) {
	if request == nil || request.URL == nil || request.URL.Scheme != "https" || request.URL.User != nil || request.URL.Host != c.authority || request.Host != "" && request.Host != c.authority || request.URL.Fragment != "" || request.URL.Opaque != "" {
		return nil, fmt.Errorf("object storage request changed its origin")
	}
	copy := request.Clone(request.Context())
	copy.Host = c.authority
	return c.client.Do(copy)
}

func (c *ObjectStorageClient) CloseIdleConnections() { c.client.CloseIdleConnections() }
