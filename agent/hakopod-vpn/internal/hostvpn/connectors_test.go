package hostvpn

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

const profile = "client\ndev tun0\nproto udp\nremote vpn.example.com 1194\n<ca>\nup /bin/sh\n</ca>\n"

func connectorHost(t *testing.T) (*Host, *fakeHost) {
	host, fake, _ := newHost(t, func(args []string) (string, error) {
		switch {
		case slices.Equal(args, []string{"tailscale", "status", "--json"}):
			return `{"BackendState":"Running","Self":{"TailscaleIPs":["100.101.1.2"]}}`, nil
		case len(args) > 1 && args[1] == "is-active":
			return "active\n", nil
		case len(args) > 1 && args[0] == "ip" && args[1] == "-j":
			return `[{"addr_info":[{"local":"10.8.0.6"}]}]`, nil
		case len(args) > 1 && args[1] == "--list-sas":
			return "hakopod: #1, ESTABLISHED, IKEv2\n  hakopod: #1, INSTALLED, TUNNEL\n", nil
		}
		return "", nil
	})
	return host, fake
}

func TestOpenVPNForcesItsDeviceIgnoresPushedRoutesAndRunsNoScripts(t *testing.T) {
	networks, err := routes([]string{"10.20.0.0/16", "2001:db8::/32"})
	if err != nil {
		t.Fatal(err)
	}
	text, err := OpenVPNConfig(profile, networks)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(text, "dev tun0") || !strings.Contains(text, "dev hakopod-ovpn\ndev-type tun\nroute-nopull\nscript-security 1\n") ||
		!strings.Contains(text, "route 10.20.0.0 255.255.0.0") || !strings.Contains(text, "route-ipv6 2001:db8::/32") ||
		!strings.Contains(text, "up /bin/sh") { // inside <ca>, certificate data only
		t.Fatal(text)
	}
	for _, bad := range []string{"up /tmp/x", "plugin /x.so", "redirect-gateway def1", "key /etc/shadow", "config /x", "<connection>\n</connection>", "<ca>"} {
		if _, err := OpenVPNConfig(profile+bad+"\n", nil); err == nil {
			t.Errorf("%q was accepted", bad)
		}
	}
}

func TestOpenVPNStartsOnceAndReports(t *testing.T) {
	host, fake := connectorHost(t)
	s := state{OpenVPN: raw(t, map[string]any{"routes": []string{"10.20.0.0/16"}, "profile": profile})}
	report := host.Connectors(s)
	if *report != (connectorReport{Type: "openvpn", Connected: true, Address: "10.8.0.6"}) || !fake.called("systemctl", "restart", "hakopod-openvpn") {
		t.Fatalf("%+v %q", report, fake.calls)
	}
	if info, err := os.Stat(filepath.Join(host.OpenVPNDir, "hakopod.conf")); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatal(info, err)
	}
	fake.calls = nil
	host.Connectors(s)
	if fake.called("systemctl", "restart", "hakopod-openvpn") {
		t.Fatal("restarted an unchanged connector")
	}
}

func TestTailscaleKeepsK3sNetworkingAndNeverLeavesTheKeyBehind(t *testing.T) {
	host, fake := connectorHost(t)
	report := host.Connectors(state{Tailscale: raw(t, map[string]any{"hostname": "hakopod", "tags": []string{"tag:hakopod"}, "login_server": "https://hs.example.com", "auth_key": "tskey-auth-0123456789abcdef"})})
	i := slices.IndexFunc(fake.calls, func(c []string) bool { return len(c) > 1 && c[0] == "tailscale" && c[1] == "up" })
	if i < 0 {
		t.Fatalf("calls %q", fake.calls)
	}
	up := fake.calls[i]
	for _, flag := range []string{"--netfilter-mode=off", "--accept-dns=false", "--accept-routes=false", "--advertise-tags=tag:hakopod", "--login-server=https://hs.example.com"} {
		if !slices.Contains(up, flag) {
			t.Errorf("missing %s", flag)
		}
	}
	if strings.Contains(strings.Join(up, " "), "tskey") {
		t.Error("the auth key is on the command line")
	}
	if _, err := os.Stat(filepath.Join(host.RunDir, "tailscale.key")); !os.IsNotExist(err) {
		t.Error("the auth key file was left behind")
	}
	if marker, _ := os.ReadFile(filepath.Join(host.State, "tailscale.sha256")); len(marker) != 64 || strings.Contains(string(marker), "tskey") {
		t.Errorf("marker %q", marker)
	}
	if report.Address != "100.101.1.2" || !report.Connected {
		t.Fatalf("%+v", report)
	}
	for _, bad := range []map[string]any{{"auth_key": "short"}, {"hostname": "Bad_Host"}, {"tags": []string{"hakopod"}}, {"login_server": "http://x"}} {
		config := map[string]any{"hostname": "hakopod", "tags": nil, "login_server": "", "auth_key": "tskey-auth-0123456789abcdef"}
		for k, v := range bad {
			config[k] = v
		}
		if report := host.Connectors(state{Tailscale: raw(t, config)}); report.Error == "" {
			t.Errorf("%v was accepted", bad)
		}
	}
}

func TestIPsecWritesAPrivateConfigAndReportsEstablished(t *testing.T) {
	host, _ := connectorHost(t)
	good := map[string]any{"remote_address": "vpn.example.com", "local_id": "hakopod.example.com", "remote_id": "vpn.example.com", "remote_networks": []string{"10.30.0.0/16"}, "psk": "a-long-pre-shared-key-value"}
	if report := host.Connectors(state{IPsec: raw(t, good)}); !report.Connected {
		t.Fatalf("%+v", report)
	}
	info, err := os.Stat(host.Swanctl)
	text, _ := os.ReadFile(host.Swanctl)
	if err != nil || info.Mode().Perm() != 0o600 || !strings.Contains(string(text), "remote_ts = 10.30.0.0/16") {
		t.Fatal(info, err, string(text))
	}
	for _, bad := range []map[string]any{{"remote_networks": []string{"0.0.0.0/0"}}, {"psk": "short"}, {"local_id": "a b"}, {"remote_address": "x;y"}, {"psk": "a-long-pre-shared\"key-value"}} {
		config := map[string]any{"remote_address": "vpn.example.com", "local_id": "h", "remote_id": "v", "remote_networks": []string{"10.30.0.0/16"}, "psk": "a-long-pre-shared-key-value"}
		for k, v := range bad {
			config[k] = v
		}
		if report := host.Connectors(state{IPsec: raw(t, config)}); report.Error == "" {
			t.Errorf("%v was accepted", bad)
		}
	}
}

func TestSwitchingConnectorsTearsDownThePreviousOne(t *testing.T) {
	host, fake := connectorHost(t)
	host.Connectors(state{OpenVPN: raw(t, map[string]any{"routes": []string{"10.20.0.0/16"}, "profile": profile})})
	if report := host.Connectors(state{OpenVPN: json.RawMessage("null")}); report != nil {
		t.Fatalf("%+v", report)
	}
	if !fake.called("systemctl", "disable", "--now", "hakopod-openvpn") {
		t.Fatalf("calls %q", fake.calls)
	}
	if _, err := os.Stat(filepath.Join(host.OpenVPNDir, "hakopod.conf")); !os.IsNotExist(err) {
		t.Fatal("the profile was left behind")
	}
}

func TestMissingClientIsReported(t *testing.T) {
	host, _ := connectorHost(t)
	host.LookPath = func(string) (string, error) { return "", os.ErrNotExist }
	if report := host.Connectors(state{IPsec: json.RawMessage(`{}`)}); !strings.Contains(report.Error, "swanctl") {
		t.Fatalf("%+v", report)
	}
}
