package hostvpn

import (
	"encoding/json"
	"errors"
	"net"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"
)

// fakeHost records commands and answers the read-only ones like a host would.
type fakeHost struct {
	calls  [][]string
	inputs []string
	answer func(args []string) (string, error)
}

func (f *fakeHost) run(args []string, stdin string) (string, error) {
	f.calls = append(f.calls, args)
	f.inputs = append(f.inputs, stdin)
	if f.answer != nil {
		return f.answer(args)
	}
	return "", nil
}

func (f *fakeHost) called(args ...string) bool {
	return slices.ContainsFunc(f.calls, func(c []string) bool { return slices.Equal(c, args) })
}

// nftInputs are the rulesets sent to nft -f -.
func (f *fakeHost) nftInputs() []string {
	var out []string
	for i, c := range f.calls {
		if slices.Equal(c, []string{"nft", "-f", "-"}) {
			out = append(out, f.inputs[i])
		}
	}
	return out
}

// fakeWG keeps peers per device and applies configurations like the kernel.
type fakeWG struct {
	peers   map[string][]wgtypes.Peer
	configs []wgtypes.Config
}

func (f *fakeWG) Device(name string) (*wgtypes.Device, error) {
	return &wgtypes.Device{Name: name, Peers: slices.Clone(f.peers[name])}, nil
}

func (f *fakeWG) ConfigureDevice(name string, cfg wgtypes.Config) error {
	f.configs = append(f.configs, cfg)
	for _, p := range cfg.Peers {
		i := slices.IndexFunc(f.peers[name], func(q wgtypes.Peer) bool { return q.PublicKey == p.PublicKey })
		switch {
		case p.Remove && i >= 0:
			f.peers[name] = slices.Delete(f.peers[name], i, i+1)
		case !p.Remove && i < 0:
			f.peers[name] = append(f.peers[name], wgtypes.Peer{PublicKey: p.PublicKey, AllowedIPs: p.AllowedIPs})
		}
	}
	return nil
}

func newHost(t *testing.T, answer func([]string) (string, error)) (*Host, *fakeHost, *fakeWG) {
	dir := t.TempDir()
	fake := &fakeHost{answer: answer}
	wg := &fakeWG{peers: map[string][]wgtypes.Peer{}}
	return &Host{
		Run: fake.run, WG: wg,
		LookPath: func(name string) (string, error) { return "/usr/bin/" + name, nil },
		Resolve: func(_, address string) (*net.UDPAddr, error) {
			return &net.UDPAddr{IP: net.ParseIP("203.0.113.9"), Port: 51820}, nil
		},
		Config:        filepath.Join(dir, "config.json"),
		FirewallSaved: filepath.Join(dir, "run", "access-firewall.nft"),
		NATSaved:      filepath.Join(dir, "run", "vpn-server.nft"),
		ClientKey:     filepath.Join(dir, "wireguard", "private.key"),
		ServerKey:     filepath.Join(dir, "wireguard", "server.key"),
		State:         filepath.Join(dir, "state"),
		RunDir:        filepath.Join(dir, "run"),
		OpenVPNDir:    filepath.Join(dir, "openvpn"),
		OpenVPNUnit:   filepath.Join(dir, "hakopod-openvpn.service"),
		Swanctl:       filepath.Join(dir, "swanctl.conf"),
	}, fake, wg
}

func mustKey(t *testing.T) wgtypes.Key {
	key, err := wgtypes.GeneratePrivateKey()
	if err != nil {
		t.Fatal(err)
	}
	return key.PublicKey()
}

func raw(t *testing.T, value any) json.RawMessage {
	out, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestSyncReportsEveryPartWhenOneFails(t *testing.T) {
	state := `{"firewall":{"enabled":true,"allow_cidrs":["0.0.0.0/0"],"tunnel_only":false},"vpn":null,"vpn_server":null,"tailscale":null,"openvpn":null,"ipsec":null}`
	host, fake, _ := newHost(t, func(args []string) (string, error) {
		if args[len(args)-1] == "access-firewall-state" {
			return state, nil
		}
		if args[0] == "ip" {
			return "", errors.New("no such device")
		}
		return "", nil
	})
	writeFile(host.Config, `{"node_ip":"192.0.2.4","dashboard_mode":"https","dashboard_port":8443}`, 0o600)
	if code := host.Sync("1.2.3"); code != 1 {
		t.Fatalf("code %d, want 1 for the refused firewall", code)
	}
	var reports []string
	for i, c := range fake.calls {
		if c[0] == "systemd-run" {
			reports = append(reports, c[len(c)-1]+" "+fake.inputs[i])
		}
	}
	want := []string{"1.2.3 ", "access-firewall-state ",
		`access-connector-report {"type":"","connected":false,"address":"","error":""}`,
		`access-vpn-report {"public_key":"","latest_handshake":null,"rx_bytes":0,"tx_bytes":0,"error":""}`}
	if !slices.Equal(reports, want) {
		t.Fatalf("reports %q", reports)
	}
	if !slices.Contains(fake.calls[0], "access-agent-report") || !slices.Contains(fake.calls[0], "--uid=hakopod-api") {
		t.Fatalf("agent report %q", fake.calls[0])
	}
}

func TestSyncKeepsEverythingWhenStateIsUnavailable(t *testing.T) {
	host, fake, _ := newHost(t, func(args []string) (string, error) {
		if args[len(args)-1] == "access-firewall-state" {
			return "", errors.New("database down")
		}
		return "", nil
	})
	if host.Sync("1.2.3") != 1 || len(fake.calls) != 2 {
		t.Fatalf("calls %q", fake.calls)
	}
}

func TestClipKeepsReportsWithinBounds(t *testing.T) {
	if text := clip(errors.New(strings.Repeat("é", 400))); len(text) > 500 || !strings.HasPrefix(text, "é") {
		t.Fatalf("clip %d", len(text))
	}
}
