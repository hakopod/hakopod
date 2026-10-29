// Package access decides whether a client network may reach the installation's
// management surface: the dashboard, the API, the CLI, the SDK and MCP.
package access

import (
	"errors"
	"net/netip"
	"slices"
	"strings"
)

const (
	SourceConnection   = "connection"
	SourceTrustedProxy = "trusted_proxy"
	maxCIDRs           = 256
)

// Headers a trusted proxy may use to name the client. The Node dashboard
// forwards each under PeerHeaderPrefix so that Go never reads them directly.
var ClientIPHeaders = []string{"X-Forwarded-For", "X-Real-IP", "CF-Connecting-IP"}

const (
	PeerIPHeader     = "X-Hakopod-Peer-IP"
	PeerHeaderPrefix = "X-Hakopod-Peer-"
)

type Policy struct {
	Enabled           bool     `json:"enabled"`
	ClientIPSource    string   `json:"client_ip_source"`
	TrustedProxyCIDRs []string `json:"trusted_proxy_cidrs"`
	ClientIPHeader    string   `json:"client_ip_header"`
	AllowCIDRs        []string `json:"allow_cidrs"`
}

func prefixes(values []string, field string) ([]netip.Prefix, error) {
	if len(values) > maxCIDRs {
		return nil, errors.New(field + " has more than 256 entries")
	}
	out := make([]netip.Prefix, 0, len(values))
	for _, raw := range values {
		prefix, err := netip.ParsePrefix(strings.TrimSpace(raw))
		if err != nil || prefix != prefix.Masked() {
			return nil, errors.New(field + " must contain canonical CIDRs such as 203.0.113.0/24")
		}
		out = append(out, prefix)
	}
	return out, nil
}

// Normalize validates p and returns its canonical form.
func (p Policy) Normalize() (Policy, error) {
	if p.ClientIPSource == "" {
		p.ClientIPSource = SourceConnection
	}
	allow, err := prefixes(p.AllowCIDRs, "allow_cidrs")
	if err != nil {
		return p, err
	}
	trusted, err := prefixes(p.TrustedProxyCIDRs, "trusted_proxy_cidrs")
	if err != nil {
		return p, err
	}
	for _, prefix := range allow {
		if prefix.Bits() == 0 {
			return p, errors.New("allow_cidrs cannot contain 0.0.0.0/0 or ::/0; turn the policy off instead")
		}
	}
	if p.Enabled && len(allow) == 0 {
		return p, errors.New("an enabled policy needs at least one allowed CIDR")
	}
	switch p.ClientIPSource {
	case SourceConnection:
		if len(trusted) != 0 || p.ClientIPHeader != "" {
			return p, errors.New("trusted proxies apply only to the trusted_proxy client IP source")
		}
	case SourceTrustedProxy:
		if len(trusted) == 0 || !slices.Contains(ClientIPHeaders, p.ClientIPHeader) {
			return p, errors.New("trusted_proxy needs trusted_proxy_cidrs and one of X-Forwarded-For, X-Real-IP or CF-Connecting-IP")
		}
	default:
		return p, errors.New("client_ip_source must be connection or trusted_proxy")
	}
	canonical := func(in []netip.Prefix) []string {
		out := []string{}
		for _, prefix := range in {
			out = append(out, prefix.String())
		}
		slices.Sort(out)
		return slices.Compact(out)
	}
	p.AllowCIDRs, p.TrustedProxyCIDRs = canonical(allow), canonical(trusted)
	return p, nil
}

func contains(cidrs []string, address netip.Addr) bool {
	for _, raw := range cidrs {
		if prefix, err := netip.ParsePrefix(raw); err == nil && prefix.Contains(address) {
			return true
		}
	}
	return false
}

// ClientIP returns the address a request came from. With trusted_proxy, the
// header is read only when the peer is a trusted proxy. X-Forwarded-For is
// read from the right, skipping trusted hops, so a client cannot prepend a
// forged address. ok is false when the header is present but unusable.
func (p Policy) ClientIP(peer netip.Addr, header func(string) string) (netip.Addr, bool) {
	peer = peer.Unmap()
	if p.ClientIPSource != SourceTrustedProxy || !contains(p.TrustedProxyCIDRs, peer) {
		return peer, true
	}
	value := strings.TrimSpace(header(p.ClientIPHeader))
	if value == "" {
		return peer, true
	}
	if p.ClientIPHeader != "X-Forwarded-For" {
		address, err := netip.ParseAddr(value)
		return address.Unmap(), err == nil
	}
	hops := strings.Split(value, ",")
	for i := len(hops) - 1; i >= 0; i-- {
		address, err := netip.ParseAddr(strings.TrimSpace(hops[i]))
		if err != nil {
			return netip.Addr{}, false
		}
		if address = address.Unmap(); i == 0 || !contains(p.TrustedProxyCIDRs, address) {
			return address, true
		}
	}
	return netip.Addr{}, false
}

// Allows reports whether client may reach the management surface. Loopback
// and the host's own addresses are always allowed: SSH tunnels, the
// maintenance readiness probe and break-glass recovery depend on them.
func (p Policy) Allows(client netip.Addr, local []netip.Addr) bool {
	client = client.Unmap()
	if !p.Enabled || client.IsLoopback() || slices.Contains(local, client) {
		return true
	}
	return client.IsValid() && contains(p.AllowCIDRs, client)
}
