// Package access decides whether a client network may reach the installation's
// management surface: the dashboard, the API, the CLI, the SDK and MCP.
package access

import (
	"encoding/base64"
	"errors"
	"net"
	"net/netip"
	"regexp"
	"slices"
	"strconv"
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
	// Firewall also closes the dashboard port and the K3s API (6443) in the
	// host firewall. It needs the private-access installer module.
	Firewall bool `json:"firewall"`
	// VPN, when set, connects the server to a WireGuard network.
	VPN *VPN `json:"vpn,omitempty"`
	// VPNOnly admits only networks routed through the tunnel. The K3s API is
	// then reachable only over the tunnel in the host firewall; the dashboard
	// port stays open for signed webhooks and the gate refuses everything else.
	VPNOnly bool `json:"vpn_only"`
	// VPNServer runs a WireGuard server on this host for administrators.
	VPNServer *VPNServer `json:"vpn_server,omitempty"`
	// Other client connectors. At most one client connector runs at a time.
	Tailscale *Tailscale `json:"tailscale,omitempty"`
	OpenVPN   *OpenVPN   `json:"openvpn,omitempty"`
	IPsec     *IPsec     `json:"ipsec,omitempty"`
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
	if p.Firewall && !p.Enabled {
		return p, errors.New("the host firewall applies only to an enabled policy")
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
	if p.VPN != nil {
		vpn, err := p.VPN.Normalize()
		if err != nil {
			return p, err
		}
		p.VPN = &vpn
	}
	clients := 0
	for _, set := range []bool{p.VPN != nil, p.Tailscale != nil, p.OpenVPN != nil, p.IPsec != nil} {
		if set {
			clients++
		}
	}
	if clients > 1 {
		return p, errors.New("choose one client connector: WireGuard, Tailscale, OpenVPN or IPsec")
	}
	if p.Tailscale != nil {
		value, err := p.Tailscale.Normalize()
		if err != nil {
			return p, err
		}
		p.Tailscale = &value
	}
	if p.OpenVPN != nil {
		value, err := p.OpenVPN.Normalize()
		if err != nil {
			return p, err
		}
		p.OpenVPN = &value
	}
	if p.IPsec != nil {
		value, err := p.IPsec.Normalize()
		if err != nil {
			return p, err
		}
		p.IPsec = &value
	}
	if p.VPNServer != nil {
		server, err := p.VPNServer.Normalize(p.VPN)
		if err != nil {
			return p, err
		}
		p.VPNServer = &server
	}
	if p.VPNOnly {
		if !p.Enabled || (p.VPN == nil && p.VPNServer == nil) {
			return p, errors.New("vpn_only needs an enabled policy and a WireGuard connector or server")
		}
		var tunnels []string
		if p.VPN != nil {
			tunnels = append(tunnels, p.VPN.AllowedIPs...)
		}
		if p.VPNServer != nil {
			tunnels = append(tunnels, p.VPNServer.Network)
		}
		for _, allowed := range allow {
			inside := false
			for _, raw := range tunnels {
				tunnel := netip.MustParsePrefix(raw)
				inside = inside || (tunnel.Bits() <= allowed.Bits() && tunnel.Contains(allowed.Addr()))
			}
			if !inside {
				return p, errors.New("with vpn_only, every allowed network must be inside a VPN network")
			}
		}
	}
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

// VPN connects the server to the administrator's WireGuard network as a
// client. The server's private key is generated on the host and never leaves
// it; the administrator adds the server's public key as a peer.
type VPN struct {
	Address       string   `json:"address"`
	PeerPublicKey string   `json:"peer_public_key"`
	PeerEndpoint  string   `json:"peer_endpoint"`
	AllowedIPs    []string `json:"allowed_ips"`
	Keepalive     int      `json:"keepalive"`
}

// Networks the tunnel must never claim: they would take over the server's
// default route or the cluster's own Pod and Service networks.
var reservedTunnelNetworks = []netip.Prefix{netip.MustParsePrefix("10.42.0.0/16"), netip.MustParsePrefix("10.43.0.0/16")}

func (v VPN) Normalize() (VPN, error) {
	address, err := netip.ParsePrefix(strings.TrimSpace(v.Address))
	if err != nil || address.Bits() == 0 {
		return v, errors.New("vpn.address must be the server's tunnel address with a prefix, such as 10.99.0.2/32")
	}
	key, err := base64.StdEncoding.DecodeString(strings.TrimSpace(v.PeerPublicKey))
	if err != nil || len(key) != 32 {
		return v, errors.New("vpn.peer_public_key must be a WireGuard public key")
	}
	host, port, err := net.SplitHostPort(strings.TrimSpace(v.PeerEndpoint))
	if number, convErr := strconv.Atoi(port); err != nil || convErr != nil || number < 1 || number > 65535 || !validHost(host) {
		return v, errors.New("vpn.peer_endpoint must be host:port, such as vpn.example.com:51820")
	}
	if len(v.AllowedIPs) == 0 || len(v.AllowedIPs) > 64 {
		return v, errors.New("vpn.allowed_ips needs between 1 and 64 networks")
	}
	allowed, err := prefixes(v.AllowedIPs, "vpn.allowed_ips")
	if err != nil {
		return v, err
	}
	for _, prefix := range allowed {
		if prefix.Bits() == 0 {
			return v, errors.New("vpn.allowed_ips cannot route all traffic through the tunnel")
		}
		for _, reserved := range reservedTunnelNetworks {
			if prefix.Overlaps(reserved) {
				return v, errors.New("vpn.allowed_ips cannot overlap the cluster networks 10.42.0.0/16 and 10.43.0.0/16")
			}
		}
	}
	if v.Keepalive == 0 {
		v.Keepalive = 25
	}
	if v.Keepalive < 0 || v.Keepalive > 3600 {
		return v, errors.New("vpn.keepalive must be between 1 and 3600 seconds")
	}
	v.Address, v.PeerPublicKey, v.PeerEndpoint = address.String(), base64.StdEncoding.EncodeToString(key), net.JoinHostPort(host, port)
	v.AllowedIPs = nil
	for _, prefix := range allowed {
		v.AllowedIPs = append(v.AllowedIPs, prefix.String())
	}
	slices.Sort(v.AllowedIPs)
	v.AllowedIPs = slices.Compact(v.AllowedIPs)
	return v, nil
}

var hostLabel = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?$`)

func validHost(host string) bool {
	if _, err := netip.ParseAddr(host); err == nil {
		return true
	}
	if len(host) == 0 || len(host) > 253 {
		return false
	}
	for _, label := range strings.Split(strings.TrimSuffix(host, "."), ".") {
		if !hostLabel.MatchString(label) {
			return false
		}
	}
	return true
}

// VPNServer runs WireGuard on this host. People get their own peer and
// address inside Network; the server takes Network's first address. Endpoint
// is what client profiles dial: this host's public name or address and the
// UDP listen port.
type VPNServer struct {
	ListenPort int    `json:"listen_port"`
	Network    string `json:"network"`
	Endpoint   string `json:"endpoint"`
}

func (v VPNServer) Normalize(client *VPN) (VPNServer, error) {
	network, err := netip.ParsePrefix(strings.TrimSpace(v.Network))
	if err != nil || network != network.Masked() || !network.Addr().Is4() || network.Bits() < 16 || network.Bits() > 29 {
		return v, errors.New("vpn_server.network must be an IPv4 network from /16 to /29, such as 10.100.0.0/24")
	}
	for _, reserved := range reservedTunnelNetworks {
		if network.Overlaps(reserved) {
			return v, errors.New("vpn_server.network cannot overlap the cluster networks 10.42.0.0/16 and 10.43.0.0/16")
		}
	}
	if client != nil {
		for _, raw := range append([]string{client.Address}, client.AllowedIPs...) {
			if other, err := netip.ParsePrefix(raw); err == nil && other.Overlaps(network) {
				return v, errors.New("vpn_server.network cannot overlap the WireGuard connector's networks")
			}
		}
	}
	if v.ListenPort == 0 {
		v.ListenPort = 51820
	}
	if v.ListenPort < 1 || v.ListenPort > 65535 {
		return v, errors.New("vpn_server.listen_port must be 1–65535")
	}
	host, port, err := net.SplitHostPort(strings.TrimSpace(v.Endpoint))
	if number, convErr := strconv.Atoi(port); err != nil || convErr != nil || number < 1 || number > 65535 || !validHost(host) {
		return v, errors.New("vpn_server.endpoint must be this server's public host:port, such as vpn.example.com:51820")
	}
	v.Network, v.Endpoint = network.String(), net.JoinHostPort(host, port)
	return v, nil
}

// Address is the server's own tunnel address.
func (v VPNServer) Address() netip.Prefix {
	network := netip.MustParsePrefix(v.Network)
	return netip.PrefixFrom(network.Addr().Next(), network.Bits())
}
