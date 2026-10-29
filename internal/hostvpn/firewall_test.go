package hostvpn

import (
	"net/netip"
	"os"
	"os/exec"
	"strings"
	"testing"
)

const nodes = `{"items":[{"status":{"addresses":[{"type":"InternalIP","address":"192.0.2.4"},{"type":"Hostname","address":"node-1"}]}}]}`

func firewallHost(t *testing.T) (*Host, *fakeHost) {
	host, fake, _ := newHost(t, func(args []string) (string, error) {
		if strings.HasSuffix(args[0], "kubectl") {
			return nodes, nil
		}
		return "", nil
	})
	return host, fake
}

var https = config{DashboardMode: "https", DashboardPort: "8443"}

func TestEnabledPolicyLimitsOnlyDashboardAndKubernetesAPI(t *testing.T) {
	host, fake := firewallHost(t)
	if err := host.Firewall(raw(t, map[string]any{"enabled": true, "allow_cidrs": []string{"203.0.113.0/24", "2001:db8::/32"}, "tunnel_only": false}), https); err != nil {
		t.Fatal(err)
	}
	text := fake.nftInputs()[0]
	for _, want := range []string{"table inet hakopod_access {}\ndelete table inet hakopod_access\n", "tcp dport { 6443, 8443 } counter drop",
		"elements = { 10.42.0.0/16, 10.43.0.0/16, 192.0.2.4/32, 203.0.113.0/24 }", "elements = { 2001:db8::/32 }", `iif "lo" accept`} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in\n%s", want, text)
		}
	}
	if !strings.HasPrefix(text, "table inet hakopod_access {}\n") || strings.Contains(strings.ReplaceAll(text, "8443", ""), " 80") {
		t.Errorf("unexpected ruleset\n%s", text)
	}
	if saved, _ := os.ReadFile(host.FirewallSaved); string(saved) != text {
		t.Error("the applied ruleset was not saved")
	}
}

func TestTunnelOnlyClosesKubernetesAPIToEverythingButTheTunnel(t *testing.T) {
	host, fake := firewallHost(t)
	if err := host.Firewall(raw(t, map[string]any{"enabled": true, "allow_cidrs": []string{"10.99.0.0/24"}, "tunnel_only": true}), https); err != nil {
		t.Fatal(err)
	}
	text := fake.nftInputs()[0]
	if !strings.Contains(text, `tcp dport { 6443 } iifname { "hakopod-wg", "hakopod-wgs" } accept`) || !strings.Contains(text, "tcp dport { 6443 } counter drop") ||
		strings.Contains(text, "8443") || strings.Contains(text, "10.99.0.0/24") {
		t.Fatalf("unexpected ruleset\n%s", text)
	}
}

func TestUnchangedRulesAreNotReapplied(t *testing.T) {
	host, fake := firewallHost(t)
	body := raw(t, map[string]any{"enabled": true, "allow_cidrs": []string{"203.0.113.0/24"}, "tunnel_only": false})
	host.Firewall(body, https)
	host.Firewall(body, https)
	if n := len(fake.nftInputs()); n != 1 {
		t.Fatalf("nft ran %d times", n)
	}
}

func TestSSHDashboardModeLimitsOnlyKubernetesAPI(t *testing.T) {
	host, fake := firewallHost(t)
	host.Firewall(raw(t, map[string]any{"enabled": true, "allow_cidrs": []string{"203.0.113.0/24"}, "tunnel_only": false}), config{DashboardMode: "ssh", DashboardPort: "3000"})
	if !strings.Contains(fake.nftInputs()[0], "tcp dport { 6443 } counter drop") {
		t.Fatal(fake.nftInputs()[0])
	}
}

func TestDisabledPolicyRemovesTheTable(t *testing.T) {
	host, fake := firewallHost(t)
	writeFile(host.FirewallSaved, "old", 0o600)
	if err := host.Firewall(raw(t, map[string]any{"enabled": false, "allow_cidrs": []string{}, "tunnel_only": false}), https); err != nil {
		t.Fatal(err)
	}
	if got := fake.nftInputs(); len(got) != 1 || got[0] != "table inet hakopod_access {}\ndelete table inet hakopod_access\n" {
		t.Fatalf("%q", got)
	}
	if _, err := os.Stat(host.FirewallSaved); !os.IsNotExist(err) {
		t.Fatal("saved ruleset kept")
	}
}

func TestFirewallRejectsUnsafeInput(t *testing.T) {
	for _, body := range []string{
		`{"enabled":true,"allow_cidrs":[],"tunnel_only":false}`, `{"enabled":true,"allow_cidrs":["0.0.0.0/0"],"tunnel_only":false}`,
		`{"enabled":true,"allow_cidrs":["10.0.0.1/8"],"tunnel_only":false}`, `{"enabled":true,"allow_cidrs":["1.2.3.0/24; flush ruleset"],"tunnel_only":false}`,
		`{"enabled":"yes","allow_cidrs":[],"tunnel_only":false}`, `{"enabled":true,"allow_cidrs":["1.2.3.0/24"],"tunnel_only":"yes"}`,
		`{"enabled":true,"allow_cidrs":[],"ports":[22]}`, `{"enabled":true,"allow_cidrs":null,"tunnel_only":false}`, ``,
	} {
		host, fake := firewallHost(t)
		if err := host.Firewall([]byte(body), https); err == nil || len(fake.calls) != 0 {
			t.Errorf("%s: err=%v calls=%q", body, err, fake.calls)
		}
	}
}

// GitHub-hosted runners have nftables and passwordless sudo; -c only checks.
func TestRulesetsAreValidNftables(t *testing.T) {
	if _, err := exec.LookPath("nft"); err != nil || exec.Command("sudo", "-n", "true").Run() != nil {
		t.Skip("nft with sudo is not available")
	}
	allow := []netip.Prefix{netip.MustParsePrefix("203.0.113.0/24"), netip.MustParsePrefix("203.0.113.7/32"), netip.MustParsePrefix("2001:db8::/32")}
	for _, text := range []string{
		Ruleset(allow, []int{6443, 8443}, "hakopod-wg", "hakopod-wgs"),
		Forwarding(netip.MustParseAddr("10.100.0.1"), netip.MustParseAddr("192.0.2.4"), []int{8443, 6443}),
	} {
		cmd := exec.Command("sudo", "-n", "nft", "-c", "-f", "-")
		cmd.Stdin = strings.NewReader(text)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Errorf("%v: %s\n%s", err, out, text)
		}
	}
}
