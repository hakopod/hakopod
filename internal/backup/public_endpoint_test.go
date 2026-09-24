package backup

import (
	"net/netip"
	"testing"
)

func TestWorkspaceBackupEndpointsBlockPrivateAndMetadataNetworks(t *testing.T) {
	for _, ip := range []string{"127.0.0.1", "::1", "10.0.0.1", "169.254.169.254", "168.63.129.16", "100.100.100.200", "192.168.1.1", "172.17.0.1", "::ffff:127.0.0.1", "fe80::1", "fc00::1", "0.0.0.0", "224.0.0.1", "64:ff9b::a00:1"} {
		if publicBackupIP(netip.MustParseAddr(ip)) {
			t.Errorf("private address permitted: %s", ip)
		}
	}
	for _, ip := range []string{"1.1.1.1", "2606:4700::1111"} {
		if !publicBackupIP(netip.MustParseAddr(ip)) {
			t.Errorf("public address blocked: %s", ip)
		}
	}
}

func TestWorkspaceBackupEndpointOperatorDenylist(t *testing.T) {
	ip := netip.MustParseAddr("8.8.8.8")
	if publicBackupIP(ip, netip.MustParsePrefix("8.8.8.0/24")) {
		t.Fatal("operator network permitted")
	}
	if !publicBackupIP(ip, netip.MustParsePrefix("9.9.9.0/24")) {
		t.Fatal("unrelated network denied")
	}
}
