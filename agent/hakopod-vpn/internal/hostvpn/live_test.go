//go:build live

// Live tests run the agent against a real kernel: nftables, WireGuard and
// network namespaces that stand in for clients and remote VPNs. They need
// root on a disposable Linux machine, such as a GitHub runner, and never run
// by default:
//
//	sudo -E go test -tags live -run Live -v ./internal/hostvpn
package hostvpn

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.zx2c4.com/wireguard/wgctrl"
	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"
)

const (
	liveHostAddr   = "198.51.100.1"
	liveClientAddr = "198.51.100.2"
	liveRemoteHost = "192.0.2.1"
	liveRemoteAddr = "192.0.2.2"
	liveDashboard  = 18443
)

type liveAgent struct {
	*Host
	mu      sync.Mutex
	state   string
	reports map[string]string
}

func sh(t *testing.T, args ...string) string {
	t.Helper()
	out, err := exec.Command(args[0], args[1:]...).CombinedOutput()
	if err != nil {
		t.Fatalf("%s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

func try(args ...string) error {
	return exec.Command(args[0], args[1:]...).Run()
}

// newLiveAgent gives the agent its real command runner and WireGuard client,
// but answers the hakopod-server state and report calls itself and keeps
// every file in a temporary directory.
func newLiveAgent(t *testing.T) *liveAgent {
	t.Helper()
	if os.Geteuid() != 0 {
		t.Skip("live tests need root")
	}
	dir := t.TempDir()
	config := filepath.Join(dir, "config.json")
	if err := os.WriteFile(config, []byte(fmt.Sprintf(`{"node_ip":%q,"dashboard_mode":"https","dashboard_port":%d}`, liveHostAddr, liveDashboard)), 0o600); err != nil {
		t.Fatal(err)
	}
	wg, err := wgctrl.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { wg.Close() })
	a := &liveAgent{Host: Default(), reports: map[string]string{}}
	a.WG = wg
	a.Config = config
	a.FirewallSaved, a.NATSaved = filepath.Join(dir, "firewall.nft"), filepath.Join(dir, "nat.nft")
	a.ClientKey, a.ServerKey = filepath.Join(dir, "wireguard", "private.key"), filepath.Join(dir, "wireguard", "server.key")
	a.State, a.RunDir = filepath.Join(dir, "state"), filepath.Join(dir, "run")
	a.Run = func(args []string, stdin string) (string, error) {
		a.mu.Lock()
		defer a.mu.Unlock()
		if args[0] == "systemd-run" {
			command := args[len(serverCommand)]
			if command == "access-firewall-state" {
				return a.state, nil
			}
			a.reports[command] = stdin
			return "", nil
		}
		if strings.HasSuffix(args[0], "kubectl") {
			return `{"items":[]}`, nil
		}
		return run(args, stdin)
	}
	return a
}

func (a *liveAgent) sync(t *testing.T, state string) {
	t.Helper()
	a.mu.Lock()
	a.state = state
	a.mu.Unlock()
	if code := a.Sync("live"); code != 0 {
		t.Fatalf("sync returned %d for state %s", code, state)
	}
}

func (a *liveAgent) report(name string) string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.reports[name]
}

// pair puts one end of a veth pair in a new namespace.
func pair(t *testing.T, namespace, hostEnd, hostAddr, nsEnd, nsAddr string) {
	t.Helper()
	try("ip", "netns", "del", namespace)
	try("ip", "link", "del", hostEnd)
	sh(t, "ip", "netns", "add", namespace)
	sh(t, "ip", "link", "add", hostEnd, "type", "veth", "peer", "name", nsEnd)
	sh(t, "ip", "link", "set", nsEnd, "netns", namespace)
	sh(t, "ip", "addr", "add", hostAddr+"/24", "dev", hostEnd)
	sh(t, "ip", "link", "set", hostEnd, "up")
	sh(t, "ip", "netns", "exec", namespace, "ip", "addr", "add", nsAddr+"/24", "dev", nsEnd)
	sh(t, "ip", "netns", "exec", namespace, "ip", "link", "set", nsEnd, "up")
	sh(t, "ip", "netns", "exec", namespace, "ip", "link", "set", "lo", "up")
	t.Cleanup(func() { try("ip", "netns", "del", namespace); try("ip", "link", "del", hostEnd) })
}

// dashboard listens like the real dashboard does, on the node address.
func dashboard(t *testing.T) {
	t.Helper()
	listener, err := net.Listen("tcp", fmt.Sprintf("%s:%d", liveHostAddr, liveDashboard))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			conn.Write([]byte("hakopod\n"))
			conn.Close()
		}
	}()
}

// reach reports whether namespace can open a TCP connection to address.
func reach(namespace, address string) bool {
	host, port, _ := net.SplitHostPort(address)
	return exec.Command("ip", "netns", "exec", namespace, "timeout", "3", "bash", "-c",
		fmt.Sprintf("exec 3<>/dev/tcp/%s/%s && head -c 7 <&3 | grep -q hakopod", host, port)).Run() == nil
}

func firewallState(enabled bool, allow ...string) string {
	if allow == nil {
		allow = []string{}
	}
	raw, _ := json.Marshal(map[string]any{"enabled": enabled, "allow_cidrs": allow, "tunnel_only": false})
	return `{"firewall":` + string(raw) + `,"vpn":null,"vpn_server":null,"tailscale":null,"openvpn":null,"ipsec":null}`
}

func TestLiveFirewallAdmitsOnlyAllowedNetworks(t *testing.T) {
	a := newLiveAgent(t)
	pair(t, "hk-outside", "hk-host0", liveHostAddr, "hk-out0", liveClientAddr)
	dashboard(t)
	target := fmt.Sprintf("%s:%d", liveHostAddr, liveDashboard)
	if !reach("hk-outside", target) {
		t.Fatal("the dashboard is unreachable before any policy")
	}
	a.sync(t, firewallState(true, "203.0.113.0/24"))
	if reach("hk-outside", target) {
		t.Fatal("a network outside the allowlist reached the dashboard port")
	}
	if out := sh(t, "nft", "list", "table", "inet", "hakopod_access"); !strings.Contains(out, "18443") || !strings.Contains(out, "6443") {
		t.Fatalf("the table does not cover the dashboard and Kubernetes ports:\n%s", out)
	}
	a.sync(t, firewallState(true, "198.51.100.0/24"))
	if !reach("hk-outside", target) {
		t.Fatal("an allowed network could not reach the dashboard port")
	}
	a.sync(t, firewallState(false))
	if try("nft", "list", "table", "inet", "hakopod_access") == nil {
		t.Fatal("turning the firewall off left its table behind")
	}
	if !reach("hk-outside", target) {
		t.Fatal("the dashboard is unreachable after the firewall was removed")
	}
}

func TestLiveVPNServerConnectsADeviceAndForwardsTheDashboard(t *testing.T) {
	a := newLiveAgent(t)
	pair(t, "hk-device", "hk-host1", liveHostAddr, "hk-dev1", liveClientAddr)
	dashboard(t)
	device, _ := wgtypes.GeneratePrivateKey()
	server := func(peers string) string {
		return `{"firewall":{"enabled":false,"allow_cidrs":[],"tunnel_only":false},"vpn":null,"tailscale":null,"openvpn":null,"ipsec":null,` +
			`"vpn_server":{"listen_port":51820,"address":"10.100.0.1/24","peers":` + peers + `}}`
	}
	a.sync(t, server(fmt.Sprintf(`[{"public_key":%q,"address":"10.100.0.2"}]`, device.PublicKey())))
	var report struct {
		ServerPublicKey string `json:"server_public_key"`
		Peers           []struct {
			PublicKey       string `json:"public_key"`
			LatestHandshake string `json:"latest_handshake"`
		} `json:"peers"`
	}
	if err := json.Unmarshal([]byte(a.report("access-vpn-server-report")), &report); err != nil || report.ServerPublicKey == "" {
		t.Fatalf("server report = %q %v", a.report("access-vpn-server-report"), err)
	}
	// The device is a WireGuard client in its own namespace, as a laptop would be.
	keyFile := filepath.Join(t.TempDir(), "device.key")
	os.WriteFile(keyFile, []byte(device.String()), 0o600)
	sh(t, "ip", "netns", "exec", "hk-device", "ip", "link", "add", "wgd", "type", "wireguard")
	sh(t, "ip", "netns", "exec", "hk-device", "wg", "set", "wgd", "private-key", keyFile, "peer", report.ServerPublicKey,
		"endpoint", fmt.Sprintf("%s:51820", liveHostAddr), "allowed-ips", "10.100.0.0/24", "persistent-keepalive", "5")
	sh(t, "ip", "netns", "exec", "hk-device", "ip", "addr", "add", "10.100.0.2/24", "dev", "wgd")
	sh(t, "ip", "netns", "exec", "hk-device", "ip", "link", "set", "wgd", "up")
	// The profile routes only the VPN network, so the dashboard is reached at
	// the server's VPN address and forwarded to the node address.
	viaTunnel := fmt.Sprintf("10.100.0.1:%d", liveDashboard)
	deadline := time.Now().Add(20 * time.Second)
	for !reach("hk-device", viaTunnel) {
		if time.Now().After(deadline) {
			t.Fatalf("the device could not reach the dashboard through the tunnel\n%s\n%s",
				sh(t, "nft", "list", "ruleset"), sh(t, "ip", "netns", "exec", "hk-device", "wg", "show"))
		}
		time.Sleep(time.Second)
	}
	a.sync(t, server(fmt.Sprintf(`[{"public_key":%q,"address":"10.100.0.2"}]`, device.PublicKey())))
	json.Unmarshal([]byte(a.report("access-vpn-server-report")), &report)
	if len(report.Peers) != 1 || report.Peers[0].LatestHandshake == "" {
		t.Fatalf("the device handshake was not reported: %s", a.report("access-vpn-server-report"))
	}
	// Revoking the device drops it at once.
	a.sync(t, server(`[]`))
	if out := sh(t, "ip", "netns", "exec", "hk-device", "timeout", "3", "bash", "-c", "true"); out != "" {
		t.Log(out)
	}
	if d, err := a.WG.Device(serverIface); err != nil || len(d.Peers) != 0 {
		t.Fatalf("revoked device is still a peer: %v %v", d, err)
	}
	// Turning the server off removes the interface and the forwarding table.
	a.sync(t, `{"firewall":{"enabled":false,"allow_cidrs":[],"tunnel_only":false},"vpn":null,"vpn_server":null,"tailscale":null,"openvpn":null,"ipsec":null}`)
	if try("ip", "link", "show", "dev", serverIface) == nil || try("nft", "list", "table", "ip", natTable) == nil {
		t.Fatal("the server interface or its forwarding table survived")
	}
}

func TestLiveConnectorJoinsARemoteWireGuardNetwork(t *testing.T) {
	a := newLiveAgent(t)
	pair(t, "hk-remote", "hk-host2", liveRemoteHost, "hk-rem2", liveRemoteAddr)
	// The remote VPN server lives in its own namespace.
	remote, _ := wgtypes.GeneratePrivateKey()
	client, _ := wgtypes.GeneratePrivateKey()
	os.MkdirAll(filepath.Dir(a.ClientKey), 0o700)
	if err := os.WriteFile(a.ClientKey, []byte(client.String()+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	remoteKey := filepath.Join(t.TempDir(), "remote.key")
	os.WriteFile(remoteKey, []byte(remote.String()), 0o600)
	sh(t, "ip", "netns", "exec", "hk-remote", "ip", "link", "add", "wgr", "type", "wireguard")
	sh(t, "ip", "netns", "exec", "hk-remote", "wg", "set", "wgr", "private-key", remoteKey, "listen-port", "51821",
		"peer", client.PublicKey().String(), "allowed-ips", "10.99.0.2/32")
	sh(t, "ip", "netns", "exec", "hk-remote", "ip", "addr", "add", "10.99.0.1/24", "dev", "wgr")
	sh(t, "ip", "netns", "exec", "hk-remote", "ip", "link", "set", "wgr", "up")
	connector := fmt.Sprintf(`{"firewall":{"enabled":false,"allow_cidrs":[],"tunnel_only":false},"vpn_server":null,"tailscale":null,"openvpn":null,"ipsec":null,`+
		`"vpn":{"address":"10.99.0.2/32","peer_public_key":%q,"peer_endpoint":"%s:51821","allowed_ips":["10.99.0.0/24"],"keepalive":5}}`, remote.PublicKey(), liveRemoteAddr)
	a.sync(t, connector)
	deadline := time.Now().Add(20 * time.Second)
	for try("ping", "-c", "1", "-W", "1", "10.99.0.1") != nil {
		if time.Now().After(deadline) {
			t.Fatalf("the server could not reach the remote VPN through the connector\n%s", sh(t, "ip", "route"))
		}
	}
	a.sync(t, connector)
	var status struct {
		PublicKey       string `json:"public_key"`
		LatestHandshake string `json:"latest_handshake"`
	}
	json.Unmarshal([]byte(a.report("access-vpn-report")), &status)
	if status.PublicKey != client.PublicKey().String() || status.LatestHandshake == "" {
		t.Fatalf("connector report = %s", a.report("access-vpn-report"))
	}
	if out := sh(t, "ip", "route", "show", "default"); strings.Contains(out, vpnIface) {
		t.Fatalf("the connector took the default route: %s", out)
	}
	a.sync(t, `{"firewall":{"enabled":false,"allow_cidrs":[],"tunnel_only":false},"vpn":null,"vpn_server":null,"tailscale":null,"openvpn":null,"ipsec":null}`)
	if try("ip", "link", "show", "dev", vpnIface) == nil {
		t.Fatal("the connector interface survived removal")
	}
}

// pki makes a throwaway CA, server and client certificate for OpenVPN.
func pki(t *testing.T, dir string) map[string]string {
	t.Helper()
	run := func(args ...string) { sh(t, append([]string{"openssl"}, args...)...) }
	for _, name := range []string{"ca", "server", "client"} {
		run("genpkey", "-algorithm", "EC", "-pkeyopt", "ec_paramgen_curve:P-256", "-out", filepath.Join(dir, name+".key"))
	}
	run("req", "-x509", "-new", "-key", filepath.Join(dir, "ca.key"), "-subj", "/CN=hakopod-live-ca", "-days", "1", "-out", filepath.Join(dir, "ca.crt"))
	for _, name := range []string{"server", "client"} {
		ext := filepath.Join(dir, name+".ext")
		usage := "clientAuth"
		if name == "server" {
			usage = "serverAuth"
		}
		os.WriteFile(ext, []byte("basicConstraints=CA:FALSE\nkeyUsage=digitalSignature,keyAgreement\nextendedKeyUsage="+usage+"\n"), 0o600)
		run("req", "-new", "-key", filepath.Join(dir, name+".key"), "-subj", "/CN=hakopod-live-"+name, "-out", filepath.Join(dir, name+".csr"))
		run("x509", "-req", "-in", filepath.Join(dir, name+".csr"), "-CA", filepath.Join(dir, "ca.crt"), "-CAkey", filepath.Join(dir, "ca.key"),
			"-CAcreateserial", "-days", "1", "-extfile", ext, "-out", filepath.Join(dir, name+".crt"))
	}
	out := map[string]string{}
	for _, name := range []string{"ca.crt", "server.crt", "server.key", "client.crt", "client.key"} {
		raw, _ := os.ReadFile(filepath.Join(dir, name))
		out[name] = string(raw)
	}
	return out
}

func TestLiveOpenVPNConnectorRoutesOnlyItsNetworks(t *testing.T) {
	if _, err := exec.LookPath("openvpn"); err != nil {
		t.Skip("openvpn is not installed")
	}
	a := newLiveAgent(t)
	// This test uses the agent's real OpenVPN paths: the systemd unit names them.
	def := Default()
	a.OpenVPNDir, a.OpenVPNUnit = def.OpenVPNDir, def.OpenVPNUnit
	pair(t, "hk-ovpn", "hk-host3", "192.0.2.17", "hk-ovs3", "192.0.2.18")
	dir := t.TempDir()
	certs := pki(t, dir)
	server := exec.Command("ip", "netns", "exec", "hk-ovpn", "openvpn", "--dev", "tun", "--proto", "udp", "--port", "1194",
		"--server", "10.8.0.0", "255.255.255.0", "--topology", "subnet", "--dh", "none", "--verb", "1",
		"--ca", filepath.Join(dir, "ca.crt"), "--cert", filepath.Join(dir, "server.crt"), "--key", filepath.Join(dir, "server.key"),
		"--push", "redirect-gateway def1", "--keepalive", "2", "10")
	if err := server.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { server.Process.Kill(); server.Wait() })
	// The server pushes a default route; the agent must ignore it.
	profile := "client\ndev tun\nproto udp\nremote 192.0.2.18 1194\nnobind\nremote-cert-tls server\n" +
		"<ca>\n" + certs["ca.crt"] + "</ca>\n<cert>\n" + certs["client.crt"] + "</cert>\n<key>\n" + certs["client.key"] + "</key>\n"
	state, _ := json.Marshal(map[string]any{"routes": []string{"10.8.0.0/24"}, "profile": profile})
	openvpn := `{"firewall":{"enabled":false,"allow_cidrs":[],"tunnel_only":false},"vpn":null,"vpn_server":null,"tailscale":null,"ipsec":null,"openvpn":` + string(state) + `}`
	t.Cleanup(func() {
		a.sync(t, `{"firewall":{"enabled":false,"allow_cidrs":[],"tunnel_only":false},"vpn":null,"vpn_server":null,"tailscale":null,"openvpn":null,"ipsec":null}`)
	})
	a.sync(t, openvpn)
	deadline := time.Now().Add(30 * time.Second)
	var report struct {
		Connected bool   `json:"connected"`
		Address   string `json:"address"`
	}
	for {
		a.sync(t, openvpn)
		json.Unmarshal([]byte(a.report("access-connector-report")), &report)
		if report.Connected {
			break
		}
		if time.Now().After(deadline) {
			out, _ := exec.Command("journalctl", "-u", "hakopod-openvpn", "-n", "40", "--no-pager").CombinedOutput()
			t.Fatalf("the OpenVPN connector did not connect: %s\n%s", a.report("access-connector-report"), out)
		}
		time.Sleep(2 * time.Second)
	}
	if try("ping", "-c", "1", "-W", "2", "10.8.0.1") != nil {
		t.Fatal("the server could not reach the OpenVPN network")
	}
	if out := sh(t, "ip", "route", "show", "default"); strings.Contains(out, "hakopod-ovpn") {
		t.Fatalf("the connector accepted the pushed default route: %s", out)
	}
	if out := sh(t, "ip", "route", "show", "0.0.0.0/1"); strings.Contains(out, "hakopod-ovpn") {
		t.Fatalf("the connector accepted the pushed redirect-gateway routes: %s", out)
	}
	a.sync(t, `{"firewall":{"enabled":false,"allow_cidrs":[],"tunnel_only":false},"vpn":null,"vpn_server":null,"tailscale":null,"openvpn":null,"ipsec":null}`)
	if try("systemctl", "is-active", "--quiet", "hakopod-openvpn") == nil {
		t.Fatal("the OpenVPN connector kept running after removal")
	}
}

func TestLiveIPsecConnectorReachesTheRemoteNetwork(t *testing.T) {
	if _, err := exec.LookPath("swanctl"); err != nil {
		t.Skip("strongSwan is not installed")
	}
	if _, err := os.Stat("/usr/lib/ipsec/charon"); err != nil {
		t.Skip("a standalone charon is needed for the remote gateway")
	}
	if _, err := os.Stat("/var/run/charon.pid"); err == nil {
		t.Skip("another charon owns /var/run/charon.pid; stop strongswan-starter and keep only charon-systemd")
	}
	a := newLiveAgent(t)
	a.Swanctl = Default().Swanctl
	pair(t, "hk-ipsec", "hk-host4", "192.0.2.33", "hk-gw4", "192.0.2.34")
	sh(t, "ip", "netns", "exec", "hk-ipsec", "ip", "link", "add", "hk-lan4", "type", "dummy")
	sh(t, "ip", "netns", "exec", "hk-ipsec", "ip", "addr", "add", "10.30.0.1/24", "dev", "hk-lan4")
	sh(t, "ip", "netns", "exec", "hk-ipsec", "ip", "link", "set", "hk-lan4", "up")
	// The remote gateway is a second charon in the namespace with its own
	// configuration and control socket.
	dir := t.TempDir()
	const psk = "a-long-live-test-pre-shared-key"
	os.WriteFile(filepath.Join(dir, "strongswan.conf"), []byte("charon {\n  plugins {\n    vici {\n      socket = unix://"+dir+"/charon.vici\n    }\n  }\n}\n"), 0o600)
	os.WriteFile(filepath.Join(dir, "swanctl.conf"), []byte(`connections {
  gw {
    version = 2
    local_addrs = 192.0.2.34
    local { auth = psk
      id = vpn.example.com }
    remote { auth = psk
      id = hakopod.example.com }
    children { gw { local_ts = 10.30.0.0/24
      remote_ts = dynamic } }
  }
}
secrets { ike-gw { id-1 = vpn.example.com
  id-2 = hakopod.example.com
  secret = "`+psk+`" } }
`), 0o600)
	// Ubuntu's AppArmor profile confines charon to its own files; the test
	// gateway reads its configuration from a temporary directory.
	gateway := []string{"ip", "netns", "exec", "hk-ipsec", "/usr/lib/ipsec/charon"}
	if _, err := exec.LookPath("aa-exec"); err == nil {
		gateway = []string{"ip", "netns", "exec", "hk-ipsec", "aa-exec", "-p", "unconfined", "--", "/usr/lib/ipsec/charon"}
	}
	charon := exec.Command(gateway[0], gateway[1:]...)
	charon.Env = append(os.Environ(), "STRONGSWAN_CONF="+filepath.Join(dir, "strongswan.conf"))
	if err := charon.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { charon.Process.Kill(); charon.Wait() })
	deadline := time.Now().Add(10 * time.Second)
	load := append(gateway[:len(gateway)-1:len(gateway)-1], "swanctl", "--load-all", "--uri", "unix://"+dir+"/charon.vici", "--file", filepath.Join(dir, "swanctl.conf"))
	for try(load...) != nil {
		if time.Now().After(deadline) {
			t.Fatal("the remote gateway did not start")
		}
		time.Sleep(500 * time.Millisecond)
	}
	ipsec, _ := json.Marshal(map[string]any{"remote_address": "192.0.2.34", "local_id": "hakopod.example.com", "remote_id": "vpn.example.com", "remote_networks": []string{"10.30.0.0/24"}, "psk": psk})
	state := `{"firewall":{"enabled":false,"allow_cidrs":[],"tunnel_only":false},"vpn":null,"vpn_server":null,"tailscale":null,"openvpn":null,"ipsec":` + string(ipsec) + `}`
	t.Cleanup(func() {
		a.sync(t, `{"firewall":{"enabled":false,"allow_cidrs":[],"tunnel_only":false},"vpn":null,"vpn_server":null,"tailscale":null,"openvpn":null,"ipsec":null}`)
	})
	var report struct {
		Connected bool   `json:"connected"`
		Error     string `json:"error"`
	}
	deadline = time.Now().Add(30 * time.Second)
	for {
		a.sync(t, state)
		json.Unmarshal([]byte(a.report("access-connector-report")), &report)
		if report.Connected {
			break
		}
		if time.Now().After(deadline) {
			out, _ := exec.Command("swanctl", "--list-sas").CombinedOutput()
			t.Fatalf("the IPsec connector did not establish: %s\n%s", a.report("access-connector-report"), out)
		}
		time.Sleep(2 * time.Second)
	}
	if try("ping", "-c", "1", "-W", "2", "10.30.0.1") != nil {
		t.Fatal("the server could not reach the remote network through IPsec")
	}
	if info, err := os.Stat(a.Swanctl); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("the swanctl file holding the key is not private: %v %v", info, err)
	}
	a.sync(t, `{"firewall":{"enabled":false,"allow_cidrs":[],"tunnel_only":false},"vpn":null,"vpn_server":null,"tailscale":null,"openvpn":null,"ipsec":null}`)
	if _, err := os.Stat(a.Swanctl); err == nil {
		t.Fatal("removing the connector left the swanctl file with its key")
	}
}

// The Tailscale connector needs a coordination server, so this test runs only
// where the workflow has installed Headscale and Tailscale and set
// HAKOPOD_LIVE_TAILSCALE. It takes over the machine's tailscaled.
func TestLiveTailscaleConnectorJoinsHeadscale(t *testing.T) {
	if os.Getenv("HAKOPOD_LIVE_TAILSCALE") == "" {
		t.Skip("set HAKOPOD_LIVE_TAILSCALE on a machine whose tailscaled may be taken over")
	}
	a := newLiveAgent(t)
	dir := t.TempDir()
	// Headscale serves HTTPS on headscale.test with a CA the system trusts.
	sh(t, "openssl", "req", "-x509", "-newkey", "ec", "-pkeyopt", "ec_paramgen_curve:P-256", "-nodes", "-days", "1",
		"-subj", "/CN=headscale.test", "-addext", "subjectAltName=DNS:headscale.test",
		"-keyout", filepath.Join(dir, "key.pem"), "-out", filepath.Join(dir, "cert.pem"))
	sh(t, "cp", filepath.Join(dir, "cert.pem"), "/usr/local/share/ca-certificates/hakopod-live-headscale.crt")
	sh(t, "update-ca-certificates")
	t.Cleanup(func() {
		os.Remove("/usr/local/share/ca-certificates/hakopod-live-headscale.crt")
		try("update-ca-certificates", "--fresh")
	})
	hosts, _ := os.ReadFile("/etc/hosts")
	os.WriteFile("/etc/hosts", append(hosts, []byte("127.0.0.1 headscale.test\n")...), 0o644)
	t.Cleanup(func() { os.WriteFile("/etc/hosts", hosts, 0o644) })
	config := fmt.Sprintf(`server_url: https://headscale.test:8443
listen_addr: 127.0.0.1:8443
metrics_listen_addr: 127.0.0.1:9090
grpc_listen_addr: 127.0.0.1:50443
tls_cert_path: %[1]s/cert.pem
tls_key_path: %[1]s/key.pem
noise:
  private_key_path: %[1]s/noise.key
prefixes:
  v4: 100.64.0.0/10
  v6: fd7a:115c:a1e0::/48
  allocation: sequential
derp:
  server:
    enabled: true
    region_id: 999
    region_code: live
    region_name: Live
    stun_listen_addr: 127.0.0.1:3478
    private_key_path: %[1]s/derp.key
    automatically_add_embedded_derp_region: true
    ipv4: 127.0.0.1
  urls: []
  paths: []
  auto_update_enabled: false
disable_check_updates: true
database:
  type: sqlite
  sqlite:
    path: %[1]s/db.sqlite
dns:
  magic_dns: false
  override_local_dns: false
unix_socket: %[1]s/headscale.sock
unix_socket_permission: "0770"
`, dir)
	os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(config), 0o600)
	headscale := func(args ...string) string {
		return sh(t, append([]string{"headscale", "--config", filepath.Join(dir, "config.yaml")}, args...)...)
	}
	server := exec.Command("headscale", "--config", filepath.Join(dir, "config.yaml"), "serve")
	log, _ := os.Create(filepath.Join(dir, "headscale.log"))
	server.Stdout, server.Stderr = log, log
	if err := server.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { server.Process.Kill(); server.Wait() })
	deadline := time.Now().Add(20 * time.Second)
	for try("headscale", "--config", filepath.Join(dir, "config.yaml"), "users", "list") != nil {
		if time.Now().After(deadline) {
			raw, _ := os.ReadFile(filepath.Join(dir, "headscale.log"))
			t.Fatalf("Headscale did not start:\n%s", raw)
		}
		time.Sleep(500 * time.Millisecond)
	}
	headscale("users", "create", "live")
	var users []struct {
		ID   json.Number `json:"id"`
		Name string      `json:"name"`
	}
	json.Unmarshal([]byte(headscale("users", "list", "-o", "json")), &users)
	if len(users) != 1 {
		t.Fatalf("users = %+v", users)
	}
	var key struct {
		Key string `json:"key"`
	}
	json.Unmarshal([]byte(headscale("preauthkeys", "create", "--user", users[0].ID.String(), "--reusable", "--expiration", "1h", "-o", "json")), &key)
	if len(key.Key) < 16 {
		t.Fatalf("no pre-auth key: %+v", key)
	}
	before := sh(t, "iptables-save")
	state, _ := json.Marshal(map[string]any{"hostname": "hakopod-live", "tags": []string{}, "login_server": "https://headscale.test:8443", "auth_key": key.Key})
	tailscale := `{"firewall":{"enabled":false,"allow_cidrs":[],"tunnel_only":false},"vpn":null,"vpn_server":null,"openvpn":null,"ipsec":null,"tailscale":` + string(state) + `}`
	t.Cleanup(func() {
		a.sync(t, `{"firewall":{"enabled":false,"allow_cidrs":[],"tunnel_only":false},"vpn":null,"vpn_server":null,"tailscale":null,"openvpn":null,"ipsec":null}`)
	})
	var report struct {
		Connected bool   `json:"connected"`
		Address   string `json:"address"`
		Error     string `json:"error"`
	}
	a.mu.Lock()
	a.state = tailscale
	a.mu.Unlock()
	deadline = time.Now().Add(60 * time.Second)
	for {
		// Joining can take a few ticks; a failing tick is retried like the timer would.
		a.Sync("live")
		json.Unmarshal([]byte(a.report("access-connector-report")), &report)
		if report.Connected {
			break
		}
		if time.Now().After(deadline) {
			raw, _ := os.ReadFile(filepath.Join(dir, "headscale.log"))
			t.Fatalf("the Tailscale connector did not join Headscale: %s\n%s", a.report("access-connector-report"), raw)
		}
		time.Sleep(2 * time.Second)
	}
	if !strings.HasPrefix(report.Address, "100.") {
		t.Fatalf("the tailnet address = %q", report.Address)
	}
	if !strings.Contains(headscale("nodes", "list"), "hakopod-live") {
		t.Fatal("Headscale does not list the server")
	}
	// The agent must leave the host's firewall to K3s.
	if after := sh(t, "iptables-save"); strings.Count(after, "ts-") > strings.Count(before, "ts-") {
		t.Fatalf("tailscaled changed netfilter:\n%s", after)
	}
	a.sync(t, `{"firewall":{"enabled":false,"allow_cidrs":[],"tunnel_only":false},"vpn":null,"vpn_server":null,"tailscale":null,"openvpn":null,"ipsec":null}`)
	var status struct {
		BackendState string
	}
	raw, _ := exec.Command("tailscale", "status", "--json").Output()
	json.Unmarshal(raw, &status)
	if status.BackendState == "Running" {
		t.Fatal("removing the connector left the server on the tailnet")
	}
}
