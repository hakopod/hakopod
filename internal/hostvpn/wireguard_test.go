package hostvpn

import (
	"encoding/json"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"
)

const peerKey = "xTIBA5rboUvnH4htodjb6e697QjLERt1NAB4mZqp8Dg="

func vpn(change map[string]any) map[string]any {
	v := map[string]any{"address": "10.99.0.2/32", "peer_public_key": peerKey, "peer_endpoint": "vpn.example.com:51820", "allowed_ips": []string{"10.99.0.0/24"}, "keepalive": 25}
	for k, value := range change {
		v[k] = value
	}
	return v
}

// ipAnswers answers ip(8) like a host with the link present when exists.
func ipAnswers(exists bool, addresses, routes string) func([]string) (string, error) {
	return func(args []string) (string, error) {
		switch {
		case slices.Equal(args[:3], []string{"ip", "link", "show"}) && !exists:
			return "", errors.New("Device does not exist")
		case slices.Equal(args[:3], []string{"ip", "-j", "address"}):
			return addresses, nil
		case slices.Equal(args[:3], []string{"ip", "-j", "route"}):
			return routes, nil
		}
		return "", nil
	}
}

func TestVPNCreatesInterfacePeerRoutesAndReportsStatus(t *testing.T) {
	host, fake, wg := newHost(t, ipAnswers(false, `[{"addr_info":[{"local":"10.98.0.2","prefixlen":32}]}]`,
		`[{"dst":"10.50.0.0/24","protocol":"boot"},{"dst":"10.99.0.0/24","protocol":"kernel"}]`))
	peer, _ := wgtypes.ParseKey(peerKey)
	old := mustKey(t)
	wg.peers[vpnIface] = []wgtypes.Peer{{PublicKey: peer, LastHandshakeTime: time.Unix(1761700000, 0), ReceiveBytes: 1200, TransmitBytes: 3400}, {PublicKey: old}}
	status, err := host.VPN(raw(t, vpn(nil)))
	if err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(host.ClientKey)
	key, _ := privateKey(host.ClientKey)
	if info.Mode().Perm() != 0o600 || status.PublicKey != key.PublicKey().String() {
		t.Fatalf("key mode %v, status %+v", info.Mode(), status)
	}
	if status.RxBytes != 1200 || status.TxBytes != 3400 || status.LatestHandshake == nil || status.LatestHandshake.Year() != 2025 {
		t.Fatalf("status %+v", status)
	}
	for _, call := range [][]string{{"ip", "link", "add", "dev", "hakopod-wg", "type", "wireguard"}, {"ip", "address", "del", "10.98.0.2/32", "dev", "hakopod-wg"},
		{"ip", "address", "replace", "10.99.0.2/32", "dev", "hakopod-wg"}, {"ip", "route", "replace", "10.99.0.0/24", "dev", "hakopod-wg"},
		{"ip", "route", "del", "10.50.0.0/24", "dev", "hakopod-wg"}} {
		if !fake.called(call...) {
			t.Errorf("missing %q", call)
		}
	}
	if fake.called("ip", "route", "del", "10.99.0.0/24", "dev", "hakopod-wg") {
		t.Error("removed the kernel route")
	}
	cfg := wg.configs[0]
	if *cfg.PrivateKey != key || len(cfg.Peers) != 2 || cfg.Peers[0].PublicKey != peer || cfg.Peers[0].Endpoint.String() != "203.0.113.9:51820" ||
		*cfg.Peers[0].PersistentKeepaliveInterval != 25*time.Second || cfg.Peers[0].AllowedIPs[0].String() != "10.99.0.0/24" || !cfg.Peers[1].Remove || cfg.Peers[1].PublicKey != old {
		t.Fatalf("config %+v", cfg)
	}
}

func TestVPNKeepsAnExistingKeyAndHostRoutes(t *testing.T) {
	host, fake, _ := newHost(t, ipAnswers(true, "[]", `[{"dst":"10.99.0.5","protocol":"boot"}]`))
	existing, _ := wgtypes.GeneratePrivateKey()
	writeFile(host.ClientKey, existing.String()+"\n", 0o600)
	status, err := host.VPN(raw(t, vpn(map[string]any{"allowed_ips": []string{"10.99.0.5/32"}})))
	if err != nil || status.PublicKey != existing.PublicKey().String() {
		t.Fatalf("%v %+v", err, status)
	}
	if text, _ := os.ReadFile(host.ClientKey); string(text) != existing.String()+"\n" {
		t.Fatal("the key was replaced")
	}
	if fake.called("ip", "route", "del", "10.99.0.5", "dev", "hakopod-wg") || fake.called("ip", "link", "add", "dev", "hakopod-wg", "type", "wireguard") {
		t.Fatalf("calls %q", fake.calls)
	}
}

func TestNoConnectorRemovesTheInterface(t *testing.T) {
	host, fake, _ := newHost(t, ipAnswers(true, "", ""))
	if status, err := host.VPN(json.RawMessage("null")); status != nil || err != nil || !fake.called("ip", "link", "del", "dev", "hakopod-wg") {
		t.Fatalf("%v %v %q", status, err, fake.calls)
	}
}

func TestVPNRefusesUnsafeState(t *testing.T) {
	for _, change := range []map[string]any{{"allowed_ips": []string{"0.0.0.0/0"}}, {"allowed_ips": []string{"10.42.1.0/24"}}, {"peer_endpoint": "x;reboot:51820"},
		{"peer_public_key": "AAAA"}, {"keepalive": 0}, {"extra": 1}, {"allowed_ips": []string{"10.99.0.1/24"}}, {"peer_endpoint": "vpn.example.com:+5"},
		{"keepalive": 2.5}, {"allowed_ips": []string{}}} {
		host, fake, wg := newHost(t, ipAnswers(true, "", ""))
		if _, err := host.VPN(raw(t, vpn(change))); err == nil || len(fake.calls) != 0 || len(wg.configs) != 0 {
			t.Errorf("%v: err=%v calls=%q", change, err, fake.calls)
		}
	}
}

var serverConfig = config{NodeIP: "192.0.2.4", DashboardMode: "https", DashboardPort: "8443"}

func vpnServer(change map[string]any) map[string]any {
	v := map[string]any{"listen_port": 51820, "address": "10.100.0.1/24", "peers": []map[string]string{{"public_key": peerKey, "address": "10.100.0.2"}}}
	for k, value := range change {
		v[k] = value
	}
	return v
}

func TestServerListensWithOnePeerPerDeviceAndForwardsTheDashboard(t *testing.T) {
	host, fake, wg := newHost(t, ipAnswers(false, `[{"addr_info":[{"local":"10.200.0.1","prefixlen":24}]}]`, ""))
	peer, _ := wgtypes.ParseKey(peerKey)
	revoked := mustKey(t)
	wg.peers[serverIface] = []wgtypes.Peer{{PublicKey: revoked}}
	report, err := host.VPNServer(raw(t, vpnServer(nil)), serverConfig)
	if err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(host.ServerKey)
	key, _ := privateKey(host.ServerKey)
	if info.Mode().Perm() != 0o600 || report.ServerPublicKey != key.PublicKey().String() {
		t.Fatalf("key mode %v, report %+v", info.Mode(), report)
	}
	cfg := wg.configs[0]
	if *cfg.ListenPort != 51820 || *cfg.PrivateKey != key || len(cfg.Peers) != 2 || cfg.Peers[0].PublicKey != peer || cfg.Peers[0].AllowedIPs[0].String() != "10.100.0.2/32" ||
		!cfg.Peers[0].ReplaceAllowedIPs || !cfg.Peers[1].Remove || cfg.Peers[1].PublicKey != revoked {
		t.Fatalf("config %+v", cfg)
	}
	if !fake.called("ip", "address", "del", "10.200.0.1/24", "dev", "hakopod-wgs") || !fake.called("ip", "link", "add", "dev", "hakopod-wgs", "type", "wireguard") {
		t.Fatalf("calls %q", fake.calls)
	}
	nat := fake.nftInputs()
	if len(nat) != 1 || !strings.Contains(nat[0], `iifname "hakopod-wgs" ip daddr 10.100.0.1 tcp dport { 6443, 8443 } dnat to 192.0.2.4`) {
		t.Fatalf("%q", nat)
	}
	if len(report.Peers) != 0 {
		t.Fatalf("peers without a handshake were reported: %+v", report.Peers)
	}
	wg.peers[serverIface][0].LastHandshakeTime = time.Unix(1761700000, 0)
	if report, _ = host.VPNServer(raw(t, vpnServer(nil)), serverConfig); len(report.Peers) != 1 || report.Peers[0].PublicKey != peerKey {
		t.Fatalf("peers %+v", report.Peers)
	}
	if len(fake.nftInputs()) != 1 {
		t.Fatal("unchanged forwarding was applied again")
	}
}

func TestServerOffRemovesInterfaceAndForwarding(t *testing.T) {
	host, fake, _ := newHost(t, ipAnswers(true, "", ""))
	if report, err := host.VPNServer(nil, serverConfig); report != nil || err != nil || !fake.called("ip", "link", "del", "dev", "hakopod-wgs") {
		t.Fatalf("%v %v %q", report, err, fake.calls)
	}
	if nat := fake.nftInputs(); len(nat) != 1 || !strings.Contains(nat[0], "delete table ip hakopod_vpn_server") {
		t.Fatalf("%q", nat)
	}
}

func TestServerRefusesUnsafeState(t *testing.T) {
	for _, change := range []map[string]any{{"address": "10.42.0.1/24"}, {"listen_port": 0}, {"peers": []map[string]string{{"public_key": peerKey, "address": "10.200.0.2"}}},
		{"peers": []map[string]string{{"public_key": "AAAA", "address": "10.100.0.2"}}}, {"extra": 1}, {"address": "fd00::1/64"},
		{"peers": []map[string]string{{"public_key": peerKey, "address": "10.100.0.2", "name": "x"}}}} {
		host, fake, wg := newHost(t, ipAnswers(true, "", ""))
		if _, err := host.VPNServer(raw(t, vpnServer(change)), serverConfig); err == nil || len(fake.calls) != 0 || len(wg.configs) != 0 {
			t.Errorf("%v: err=%v calls=%q", change, err, fake.calls)
		}
	}
}
