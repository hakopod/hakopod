package cluster

import (
	"context"
	"crypto/x509"
	"sort"
	"time"
)

func (c *Client) ServiceHostname(t Target, service string) string { return c.hostname(t, service) }
func (c *Client) serviceHostnames(ctx context.Context, t Target, service string) ([]string, error) {
	var approved map[string]bool
	if c.options.ApprovedDomains != nil {
		var err error
		approved, err = c.options.ApprovedDomains(ctx, t.ApplicationID)
		if err != nil {
			return nil, err
		}
	}
	generated := []string{c.hostname(t, service)}
	names := []string{}
	for name := range t.Spec.Services[service].HTTP {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		generated = append(generated, c.endpointHostname(t, service, name))
	}
	custom := []string{}
	for host, name := range t.Spec.Domains {
		if name == service && approved[host] {
			custom = append(custom, host)
		}
	}
	sort.Strings(custom)
	return append(generated, custom...), nil
}
func (c *Client) validateServiceCertificate(ctx context.Context, t Target, service string, cert, key []byte) (*x509.Certificate, error) {
	var leaf *x509.Certificate
	hosts, err := c.serviceHostnames(ctx, t, service)
	if err != nil {
		return nil, err
	}
	for _, host := range hosts {
		var err error
		leaf, err = validateTLSCertificate(cert, key, host, time.Now())
		if err != nil {
			return nil, err
		}
	}
	return leaf, nil
}

func (c *Client) endpointHostname(t Target, service, endpoint string) string {
	return c.hostname(t, service+"-"+endpoint)
}
func (c *Client) httpHostPort(t Target, service, host string) int32 {
	svc := t.Spec.Services[service]
	for name, e := range svc.HTTP {
		if host == c.endpointHostname(t, service, name) || host == e.Domain {
			return e.Port
		}
	}
	return svc.Port
}
