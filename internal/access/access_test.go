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
