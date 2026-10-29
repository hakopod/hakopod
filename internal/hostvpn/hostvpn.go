// Package hostvpn is the root host side of private management access: the
// nftables firewall, the WireGuard client and server, and the Tailscale,
// OpenVPN and IPsec connectors. Each timer tick reads the desired state from
// the release's own server binary, run as the API user, makes the host match
// it and reports back. Every value is validated again here because it ends up
// in a root command.
package hostvpn

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"
)

// WireGuard is the part of wgctrl.Client the agent uses.
type WireGuard interface {
	Device(name string) (*wgtypes.Device, error)
	ConfigureDevice(name string, cfg wgtypes.Config) error
}

// Host holds the commands and paths one tick touches; tests replace them.
type Host struct {
	// Run executes argv with stdin and returns stdout; the error is set when
	// the command fails, and stdout is still returned.
	Run      func(args []string, stdin string) (string, error)
	WG       WireGuard
	LookPath func(name string) (string, error)
	Resolve  func(network, address string) (*net.UDPAddr, error)

	Config, FirewallSaved, NATSaved, ClientKey, ServerKey string
	State, RunDir, OpenVPNDir, OpenVPNUnit, Swanctl       string
}

func Default() *Host {
	return &Host{
		Run: run, LookPath: exec.LookPath, Resolve: net.ResolveUDPAddr,
		Config: "/etc/hakopod/config.json",
		// Rendered rules live under /run so a reboot, which empties nftables,
		// also forgets them and the next tick applies the tables again.
		FirewallSaved: "/run/hakopod-access/access-firewall.nft",
		NATSaved:      "/run/hakopod-access/vpn-server.nft",
		ClientKey:     "/etc/hakopod/wireguard/private.key",
		ServerKey:     "/etc/hakopod/wireguard/server.key",
		State:         "/var/lib/hakopod/maintenance",
		RunDir:        "/run/hakopod-access",
		OpenVPNDir:    "/etc/hakopod/openvpn",
		OpenVPNUnit:   "/etc/systemd/system/hakopod-openvpn.service",
		Swanctl:       "/etc/swanctl/conf.d/hakopod.conf",
	}
}

func run(args []string, stdin string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		// No argument carries a secret: the Tailscale auth key goes by file.
		return stdout.String(), fmt.Errorf("%s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}

var serverCommand = []string{"systemd-run", "--wait", "--pipe", "--quiet", "--collect", "--uid=hakopod-api", "--gid=hakopod-api",
	"--property=EnvironmentFile=/etc/hakopod/api.env", "/opt/hakopod/current/hakopod-server"}

func server(args ...string) []string { return append(append([]string{}, serverCommand...), args...) }

type state struct {
	Firewall  json.RawMessage `json:"firewall"`
	VPN       json.RawMessage `json:"vpn"`
	VPNServer json.RawMessage `json:"vpn_server"`
	Tailscale json.RawMessage `json:"tailscale"`
	OpenVPN   json.RawMessage `json:"openvpn"`
	IPsec     json.RawMessage `json:"ipsec"`
}

// Sync is one timer tick. If the state cannot be read the current host
// configuration stays; `nft delete table inet hakopod_access` or the access
// reset command is the way out. One part failing does not stop the others.
func (h *Host) Sync(version string) int {
	if _, err := h.Run(server("access-agent-report", version), ""); err != nil {
		fmt.Fprintln(os.Stderr, "The agent check-in was not recorded: "+err.Error())
	}
	out, err := h.Run(server("access-firewall-state"), "")
	var s state
	if err == nil {
		err = json.Unmarshal([]byte(out), &s)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "Access state is unavailable; keeping the current configuration: "+err.Error())
		return 1
	}
	code := 0
	fail := func(part string, err error) {
		fmt.Fprintf(os.Stderr, "%s: %v\n", part, err)
		code = 1
	}
	cfg, cfgErr := h.config()
	if cfgErr != nil {
		fail("Access firewall could not be applied", cfgErr)
	} else if err := h.Firewall(s.Firewall, cfg); err != nil {
		fail("Access firewall could not be applied", err)
	}
	status, err := h.VPN(s.VPN)
	if status == nil {
		status = &vpnStatus{}
	}
	if err != nil {
		fail("WireGuard connector", err)
		status.Error = clip(err)
	}
	var srv *serverReport
	if cfgErr != nil && present(s.VPNServer) {
		err = cfgErr
	} else {
		srv, err = h.VPNServer(s.VPNServer, cfg)
	}
	if err != nil {
		fail("WireGuard server", err)
		if srv == nil {
			srv = &serverReport{Peers: []peerReport{}}
		}
		srv.Error = clip(err)
	}
	connector := h.Connectors(s)
	if connector != nil && connector.Error != "" {
		fail("VPN connector", errors.New(connector.Error))
	}
	if connector == nil {
		connector = &connectorReport{}
	}
	h.report("access-connector-report", connector)
	if srv != nil {
		h.report("access-vpn-server-report", srv)
	}
	h.report("access-vpn-report", status)
	return code
}

func (h *Host) report(name string, value any) {
	body, _ := json.Marshal(value)
	if _, err := h.Run(server(name), string(body)); err != nil {
		fmt.Fprintln(os.Stderr, "Report "+name+" failed: "+err.Error())
	}
}

type config struct {
	NodeIP        string      `json:"node_ip"`
	DashboardMode string      `json:"dashboard_mode"`
	DashboardPort json.Number `json:"dashboard_port"`
}

func (h *Host) config() (config, error) {
	var c config
	raw, err := os.ReadFile(h.Config)
	if err == nil {
		err = json.Unmarshal(raw, &c)
	}
	return c, err
}

// ports is the K3s API plus the dashboard port when the dashboard listens
// publicly (https mode); in ssh mode it is reached through a tunnel.
func (c config) ports() ([]int, error) {
	if c.DashboardMode != "https" {
		return []int{6443}, nil
	}
	port, err := c.DashboardPort.Int64()
	if err != nil || port < 1 || port > 65535 {
		return nil, errors.New("invalid dashboard_port in /etc/hakopod/config.json")
	}
	return []int{6443, int(port)}, nil
}

// Validation shared by the parts.

var reserved = []netip.Prefix{netip.MustParsePrefix("10.42.0.0/16"), netip.MustParsePrefix("10.43.0.0/16")}

var (
	hostLabel = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?(?:\.[A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?)*$`)
	ikeID     = regexp.MustCompile(`^[A-Za-z0-9@._:=,-]{1,128}$`)
)

func hostname(value string) bool { return len(value) <= 253 && hostLabel.MatchString(value) }

// network parses a CIDR whose host bits are zero; a bare address is a host route.
func network(value string) (netip.Prefix, error) {
	if !strings.Contains(value, "/") {
		addr, err := netip.ParseAddr(value)
		if err != nil || addr.Zone() != "" {
			return netip.Prefix{}, fmt.Errorf("invalid network %q", value)
		}
		return netip.PrefixFrom(addr, addr.BitLen()), nil
	}
	p, err := netip.ParsePrefix(value)
	if err != nil || p.Masked() != p {
		return netip.Prefix{}, fmt.Errorf("invalid network %q", value)
	}
	return p, nil
}

// iface parses an address with its prefix length, like 10.99.0.2/24.
func iface(value string) (netip.Prefix, error) {
	if !strings.Contains(value, "/") {
		return network(value)
	}
	p, err := netip.ParsePrefix(value)
	if err != nil {
		return netip.Prefix{}, fmt.Errorf("invalid address %q", value)
	}
	return p, nil
}

// routes are the networks a tunnel may carry: never the default route or the
// cluster networks.
func routes(values []string) ([]netip.Prefix, error) {
	if len(values) == 0 || len(values) > 64 {
		return nil, errors.New("routes need between 1 and 64 networks")
	}
	out := []netip.Prefix{}
	for _, value := range values {
		p, err := network(value)
		if err != nil {
			return nil, err
		}
		if p.Bits() == 0 || overlapsReserved(p) {
			return nil, errors.New("routes cannot take the default route or the cluster networks")
		}
		out = append(out, p)
	}
	return out, nil
}

func overlapsReserved(p netip.Prefix) bool {
	for _, r := range reserved {
		if p.Overlaps(r) {
			return true
		}
	}
	return false
}

// strict decodes an object that must have exactly fields, none of them null.
func strict(raw json.RawMessage, v any, message string, fields ...string) error {
	var m map[string]json.RawMessage
	if json.Unmarshal(raw, &m) != nil || len(m) != len(fields) {
		return errors.New(message)
	}
	for _, f := range fields {
		if value, ok := m[f]; !ok || string(value) == "null" {
			return errors.New(message)
		}
	}
	if json.Unmarshal(raw, v) != nil {
		return errors.New(message)
	}
	return nil
}

func present(raw json.RawMessage) bool { return len(raw) > 0 && string(raw) != "null" }

func clip(err error) string {
	text := err.Error()
	for len(text) > 500 {
		_, size := utf8.DecodeLastRuneInString(text)
		text = text[:len(text)-size]
	}
	return text
}

func ipNet(p netip.Prefix) net.IPNet {
	return net.IPNet{IP: p.Addr().AsSlice(), Mask: net.CIDRMask(p.Bits(), p.Addr().BitLen())}
}

func (h *Host) nft(text string) error {
	_, err := h.Run([]string{"nft", "-f", "-"}, text)
	return err
}

// applyTable replaces a table with nft -f, skipping it when the rendered rules
// are those last applied.
func (h *Host) applyTable(saved, text string) error {
	if old, err := os.ReadFile(saved); err == nil && string(old) == text {
		return nil
	}
	if err := h.nft(text); err != nil {
		return err
	}
	return writeFile(saved, text, 0o600)
}

func writeFile(path, text string, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	temporary := path + ".tmp"
	os.Remove(temporary)
	if err := os.WriteFile(temporary, []byte(text), mode); err != nil {
		return err
	}
	if err := os.Chmod(temporary, mode); err != nil {
		return err
	}
	return os.Rename(temporary, path)
}
