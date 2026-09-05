package cluster

import (
	"crypto/x509"
	"sort"
	"time"
)

func (c *Client) ServiceHostname(t Target, service string) string { return c.hostname(t, service) }
func (c *Client) serviceHostnames(t Target, service string) []string {
	custom := []string{}
	for host, name := range t.Spec.Domains {
		if name == service {
			custom = append(custom, host)
		}
	}
	sort.Strings(custom)
	return append([]string{c.hostname(t, service)}, custom...)
}
func (c *Client) validateServiceCertificate(t Target, service string, cert, key []byte) (*x509.Certificate, error) {
	var leaf *x509.Certificate
	for _, host := range c.serviceHostnames(t, service) {
		var err error
		leaf, err = validateTLSCertificate(cert, key, host, time.Now())
		if err != nil {
			return nil, err
		}
	}
	return leaf, nil
}
