package access

import (
	"net/netip"
	"testing"
)

func TestNormalizeRejectsUnsafePolicies(t *testing.T) {
	for name, p := range map[string]Policy{
		"enabled without CIDRs":    {Enabled: true},
		"non-canonical CIDR":       {Enabled: true, AllowCIDRs: []string{"10.0.0.1/8"}},
		"everything":               {Enabled: true, AllowCIDRs: []string{"0.0.0.0/0"}},
		"proxy without header":     {Enabled: true, AllowCIDRs: []string{"10.0.0.0/8"}, ClientIPSource: SourceTrustedProxy, TrustedProxyCIDRs: []string{"192.0.2.0/24"}},
		"proxy CIDRs on direct":    {Enabled: true, AllowCIDRs: []string{"10.0.0.0/8"}, TrustedProxyCIDRs: []string{"192.0.2.0/24"}},
		"unknown header":           {Enabled: true, AllowCIDRs: []string{"10.0.0.0/8"}, ClientIPSource: SourceTrustedProxy, TrustedProxyCIDRs: []string{"192.0.2.0/24"}, ClientIPHeader: "Forwarded"},
		"unknown client IP source": {Enabled: true, AllowCIDRs: []string{"10.0.0.0/8"}, ClientIPSource: "header"},
		"firewall while off":       {Firewall: true},
	} {
		if _, err := p.Normalize(); err == nil {
			t.Errorf("%s: accepted %+v", name, p)
		}
	}
	p, err := Policy{Enabled: true, AllowCIDRs: []string{" 10.0.0.0/8", "10.0.0.0/8", "2001:db8::/32"}}.Normalize()
	if err != nil || p.ClientIPSource != SourceConnection || len(p.AllowCIDRs) != 2 {
		t.Fatalf("canonical policy = %+v, %v", p, err)
	}
}

func TestClientIPTrustsOnlyConfiguredProxies(t *testing.T) {
	p := Policy{Enabled: true, AllowCIDRs: []string{"203.0.113.0/24"}, ClientIPSource: SourceTrustedProxy, TrustedProxyCIDRs: []string{"192.0.2.0/24"}, ClientIPHeader: "X-Forwarded-For"}
	header := func(value string) func(string) string { return func(string) string { return value } }
	proxy, stranger := netip.MustParseAddr("192.0.2.10"), netip.MustParseAddr("198.51.100.7")
	for _, c := range []struct {
		peer   netip.Addr
		header string
		want   string
		ok     bool
	}{
		{stranger, "203.0.113.5", "198.51.100.7", true},                       // untrusted peer: header ignored
		{proxy, "", "192.0.2.10", true},                                       // no header: the proxy itself
		{proxy, "203.0.113.5", "203.0.113.5", true},                           // one hop
		{proxy, "203.0.113.5, 198.51.100.7", "198.51.100.7", true},            // forged left entry ignored
		{proxy, "198.51.100.7, 203.0.113.5, 192.0.2.11", "203.0.113.5", true}, // trusted hops skipped
		{proxy, "192.0.2.11, 192.0.2.12", "192.0.2.11", true},                 // all trusted: leftmost
		{proxy, "not-an-ip", "", false},
	} {
		got, ok := p.ClientIP(c.peer, header(c.header))
		if ok != c.ok || (ok && got.String() != c.want) {
			t.Errorf("peer %s header %q = %s, %v; want %s, %v", c.peer, c.header, got, ok, c.want, c.ok)
		}
	}
	p.ClientIPHeader = "X-Real-IP"
	if got, ok := p.ClientIP(proxy, header("203.0.113.9")); !ok || got.String() != "203.0.113.9" {
		t.Fatalf("X-Real-IP = %s, %v", got, ok)
	}
}

func TestAllowsKeepsRecoveryPathsOpen(t *testing.T) {
	p := Policy{Enabled: true, AllowCIDRs: []string{"10.8.0.0/24"}}
	local := []netip.Addr{netip.MustParseAddr("192.0.2.4")}
	for address, want := range map[string]bool{
		"10.8.0.9": true, "10.9.0.1": false, "127.0.0.1": true, "::1": true, "192.0.2.4": true, "::ffff:10.8.0.9": true, "2001:db8::1": false,
	} {
		if got := p.Allows(netip.MustParseAddr(address), local); got != want {
			t.Errorf("Allows(%s) = %v, want %v", address, got, want)
		}
	}
	if !(Policy{}).Allows(netip.MustParseAddr("198.51.100.1"), nil) {
		t.Fatal("a disabled policy must allow every network")
	}
	if p.Allows(netip.Addr{}, nil) {
		t.Fatal("an unknown client must be denied")
	}
}

func TestVPNNormalizeRefusesRouteTakeover(t *testing.T) {
	good := VPN{Address: "10.99.0.2/32", PeerPublicKey: "xTIBA5rboUvnH4htodjb6e697QjLERt1NAB4mZqp8Dg=", PeerEndpoint: "vpn.example.com:51820", AllowedIPs: []string{"10.99.0.0/24", "10.99.0.0/24"}}
	v, err := good.Normalize()
	if err != nil || v.Keepalive != 25 || len(v.AllowedIPs) != 1 {
		t.Fatalf("valid VPN = %+v, %v", v, err)
	}
	for name, change := range map[string]func(*VPN){
		"default route":     func(v *VPN) { v.AllowedIPs = []string{"0.0.0.0/0"} },
		"cluster network":   func(v *VPN) { v.AllowedIPs = []string{"10.42.0.0/24"} },
		"service network":   func(v *VPN) { v.AllowedIPs = []string{"10.0.0.0/8"} },
		"short key":         func(v *VPN) { v.PeerPublicKey = "AAAA" },
		"no port":           func(v *VPN) { v.PeerEndpoint = "vpn.example.com" },
		"bad host":          func(v *VPN) { v.PeerEndpoint = "vpn example;rm:51820" },
		"address no prefix": func(v *VPN) { v.Address = "10.99.0.2" },
		"no allowed ips":    func(v *VPN) { v.AllowedIPs = nil },
	} {
		bad := good
		change(&bad)
		if _, err := bad.Normalize(); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

func TestVPNOnlyAdmitsOnlyTunnelNetworks(t *testing.T) {
	vpn := &VPN{Address: "10.99.0.2/32", PeerPublicKey: "xTIBA5rboUvnH4htodjb6e697QjLERt1NAB4mZqp8Dg=", PeerEndpoint: "vpn.example.com:51820", AllowedIPs: []string{"10.99.0.0/24"}}
	if _, err := (Policy{Enabled: true, VPNOnly: true, VPN: vpn, AllowCIDRs: []string{"10.99.0.0/25"}}).Normalize(); err != nil {
		t.Fatalf("tunnel subnet refused: %v", err)
	}
	for name, p := range map[string]Policy{
		"public network":    {Enabled: true, VPNOnly: true, VPN: vpn, AllowCIDRs: []string{"203.0.113.0/24"}},
		"wider than tunnel": {Enabled: true, VPNOnly: true, VPN: vpn, AllowCIDRs: []string{"10.99.0.0/16"}},
		"no connector":      {Enabled: true, VPNOnly: true, AllowCIDRs: []string{"10.99.0.0/24"}},
		"policy off":        {VPNOnly: true, VPN: vpn},
	} {
		if _, err := p.Normalize(); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

func TestVPNServerNetworkStaysOffClusterAndConnector(t *testing.T) {
	server, err := VPNServer{Network: "10.100.0.0/24", Endpoint: "vpn.example.com:51820"}.Normalize(nil)
	if err != nil || server.ListenPort != 51820 || server.Address().String() != "10.100.0.1/24" {
		t.Fatalf("server = %+v %v", server, err)
	}
	client := &VPN{Address: "10.99.0.2/32", AllowedIPs: []string{"10.100.0.0/16"}}
	for name, c := range map[string]struct {
		v      VPNServer
		client *VPN
	}{
		"cluster":       {VPNServer{Network: "10.42.0.0/24", Endpoint: "vpn.example.com:51820"}, nil},
		"connector":     {VPNServer{Network: "10.100.0.0/24", Endpoint: "vpn.example.com:51820"}, client},
		"ipv6":          {VPNServer{Network: "fd00::/64", Endpoint: "vpn.example.com:51820"}, nil},
		"too small":     {VPNServer{Network: "10.100.0.0/30", Endpoint: "vpn.example.com:51820"}, nil},
		"not canonical": {VPNServer{Network: "10.100.0.1/24", Endpoint: "vpn.example.com:51820"}, nil},
		"no endpoint":   {VPNServer{Network: "10.100.0.0/24"}, nil},
	} {
		if _, err := c.v.Normalize(c.client); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}
