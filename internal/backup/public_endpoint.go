package backup

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"time"
)

func publicBackupIP(ip netip.Addr, blocked ...netip.Prefix) bool {
	ip = ip.Unmap()
	if !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
		return false
	}
	for _, raw := range []string{"0.0.0.0/8", "100.64.0.0/10", "192.0.0.0/24", "192.0.2.0/24", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "240.0.0.0/4", "168.63.129.16/32", "2001:db8::/32", "64:ff9b::/96", "2002::/16"} {
		if netip.MustParsePrefix(raw).Contains(ip) {
			return false
		}
	}
	for _, prefix := range blocked {
		if prefix.Contains(ip) {
			return false
		}
	}
	return true
}

// Resolve for each connection and dial the validated address itself, preventing
// DNS rebinding. Scoped destinations never use environment proxies or metadata.
func publicBackupDial(blocked []netip.Prefix) func(context.Context, string, string) (net.Conn, error) {
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, err
		}
		if port != "443" {
			return nil, fmt.Errorf("workspace backup storage requires HTTPS on port 443")
		}
		addresses, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
		if err != nil {
			return nil, fmt.Errorf("backup endpoint resolution failed")
		}
		if len(addresses) == 0 {
			return nil, fmt.Errorf("backup endpoint has no address")
		}
		for _, ip := range addresses {
			if !publicBackupIP(ip, blocked...) {
				return nil, fmt.Errorf("workspace backup storage must use public addresses")
			}
		}
		dialer := net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
		return dialer.DialContext(ctx, network, net.JoinHostPort(addresses[0].String(), port))
	}
}
