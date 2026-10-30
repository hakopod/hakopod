package hostvpn

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"
)

// The WireGuard client joins the administrator's network on hakopod-wg; the
// built-in server listens on hakopod-wgs with one peer per device. Private
// keys are generated here and never leave the host. Devices are configured
// through the kernel's netlink interface, so wireguard-tools is not needed.

const (
	vpnIface    = "hakopod-wg"
	serverIface = "hakopod-wgs"
	natTable    = "hakopod_vpn_server"
)

// privateKey reads the key at path, creating it once with mode 0600.
func privateKey(path string) (wgtypes.Key, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return wgtypes.Key{}, err
	}
	if file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600); err == nil {
		key, err := wgtypes.GeneratePrivateKey()
		if err == nil {
			_, err = file.WriteString(key.String() + "\n")
		}
		if err = errors.Join(err, file.Close()); err != nil {
			os.Remove(path)
			return wgtypes.Key{}, err
		}
	} else if !errors.Is(err, fs.ErrExist) {
		return wgtypes.Key{}, err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return wgtypes.Key{}, err
	}
	key, err := wgtypes.ParseKey(strings.TrimSpace(string(raw)))
	if err != nil {
		return wgtypes.Key{}, fmt.Errorf("%s is not a WireGuard private key", path)
	}
	return key, nil
}

func (h *Host) linkExists(name string) bool {
	_, err := h.Run([]string{"ip", "link", "show", "dev", name}, "")
	return err == nil
}

func (h *Host) ip(args ...string) error {
	_, err := h.Run(append([]string{"ip"}, args...), "")
	return err
}

func (h *Host) device(name string) (*wgtypes.Device, error) {
	if h.WG == nil {
		return nil, errors.New("WireGuard is unavailable on this host: load the wireguard kernel module")
	}
	return h.WG.Device(name)
}

// up creates the link if needed, sets its one address (removing others) and
// brings it up.
func (h *Host) up(name string, address netip.Prefix) error {
	out, err := h.Run([]string{"ip", "-j", "address", "show", "dev", name}, "")
	if err != nil {
		return err
	}
	var links []struct {
		AddrInfo []struct {
			Local     string `json:"local"`
			Prefixlen int    `json:"prefixlen"`
		} `json:"addr_info"`
	}
	if strings.TrimSpace(out) != "" {
		if err := json.Unmarshal([]byte(out), &links); err != nil {
			return err
		}
	}
	for _, link := range links {
		for _, a := range link.AddrInfo {
			addr, err := netip.ParseAddr(a.Local)
			if err != nil {
				return err
			}
			if current := netip.PrefixFrom(addr, a.Prefixlen); current != address {
				if err := h.ip("address", "del", current.String(), "dev", name); err != nil {
					return err
				}
			}
		}
	}
	if err := h.ip("address", "replace", address.String(), "dev", name); err != nil {
		return err
	}
	return h.ip("link", "set", "up", "dev", name)
}

type vpnStatus struct {
	PublicKey       string     `json:"public_key"`
	LatestHandshake *time.Time `json:"latest_handshake"`
	RxBytes         int64      `json:"rx_bytes"`
	TxBytes         int64      `json:"tx_bytes"`
	Error           string     `json:"error"`
}

type vpnConfig struct {
	Address       string   `json:"address"`
	PeerPublicKey string   `json:"peer_public_key"`
	PeerEndpoint  string   `json:"peer_endpoint"`
	AllowedIPs    []string `json:"allowed_ips"`
	Keepalive     int      `json:"keepalive"`
}

type vpnPlan struct {
	address  netip.Prefix
	peer     wgtypes.Key
	host     string
	port     int
	networks []netip.Prefix
}

func validateVPN(raw json.RawMessage) (vpnConfig, vpnPlan, error) {
	var v vpnConfig
	var plan vpnPlan
	if err := strict(raw, &v, "unexpected VPN fields", "address", "peer_public_key", "peer_endpoint", "allowed_ips", "keepalive"); err != nil {
		return v, plan, err
	}
	var err error
	if plan.address, err = iface(v.Address); err != nil {
		return v, plan, err
	}
	if plan.peer, err = wgtypes.ParseKey(v.PeerPublicKey); err != nil {
		return v, plan, errors.New("invalid peer public key")
	}
	cut := strings.LastIndex(v.PeerEndpoint, ":")
	host, port := v.PeerEndpoint[:max(cut, 0)], v.PeerEndpoint[cut+1:]
	if strings.HasPrefix(host, "[") && strings.HasSuffix(host, "]") {
		host = host[1 : len(host)-1]
	}
	if _, err := netip.ParseAddr(host); err != nil && !hostname(strings.TrimSuffix(host, ".")) {
		return v, plan, errors.New("invalid peer endpoint")
	}
	plan.host = host
	if plan.port, err = strconv.Atoi(port); err != nil || strings.Trim(port, "0123456789") != "" || plan.port < 1 || plan.port > 65535 {
		return v, plan, errors.New("invalid peer endpoint port")
	}
	if len(v.AllowedIPs) == 0 || len(v.AllowedIPs) > 64 {
		return v, plan, errors.New("allowed_ips needs between 1 and 64 networks")
	}
	if plan.networks, err = routes(v.AllowedIPs); err != nil {
		return v, plan, errors.New("allowed_ips cannot take the default route or the cluster networks")
	}
	if v.Keepalive < 1 || v.Keepalive > 3600 {
		return v, plan, errors.New("invalid keepalive")
	}
	return v, plan, nil
}

// VPN makes hakopod-wg match the connector, or removes it when there is
// none. It returns the status to report, or nil without a connector.
func (h *Host) VPN(raw json.RawMessage) (*vpnStatus, error) {
	if !present(raw) {
		if h.linkExists(vpnIface) {
			return nil, h.ip("link", "del", "dev", vpnIface)
		}
		return nil, nil
	}
	v, plan, err := validateVPN(raw)
	if err != nil {
		return nil, err
	}
	key, err := privateKey(h.ClientKey)
	if err != nil {
		return nil, err
	}
	status := &vpnStatus{PublicKey: key.PublicKey().String()}
	endpoint, err := h.Resolve("udp", net.JoinHostPort(plan.host, strconv.Itoa(plan.port)))
	if err != nil {
		return status, fmt.Errorf("resolve the peer endpoint: %w", err)
	}
	if !h.linkExists(vpnIface) {
		if err := h.ip("link", "add", "dev", vpnIface, "type", "wireguard"); err != nil {
			return status, err
		}
	}
	dev, err := h.device(vpnIface)
	if err != nil {
		return status, err
	}
	keepalive := time.Duration(v.Keepalive) * time.Second
	peer := wgtypes.PeerConfig{PublicKey: plan.peer, Endpoint: endpoint, ReplaceAllowedIPs: true, PersistentKeepaliveInterval: &keepalive}
	for _, n := range plan.networks {
		peer.AllowedIPs = append(peer.AllowedIPs, ipNet(n))
	}
	peers := []wgtypes.PeerConfig{peer}
	for _, p := range dev.Peers {
		if p.PublicKey != plan.peer {
			peers = append(peers, wgtypes.PeerConfig{PublicKey: p.PublicKey, Remove: true})
		}
	}
	if err := h.WG.ConfigureDevice(vpnIface, wgtypes.Config{PrivateKey: &key, Peers: peers}); err != nil {
		return status, err
	}
	if err := h.up(vpnIface, plan.address); err != nil {
		return status, err
	}
	wanted := map[string]bool{}
	for _, n := range plan.networks {
		wanted[n.String()] = true
		if err := h.ip("route", "replace", n.String(), "dev", vpnIface); err != nil {
			return status, err
		}
	}
	out, err := h.Run([]string{"ip", "-j", "route", "show", "dev", vpnIface}, "")
	if err != nil {
		return status, err
	}
	var current []struct{ Dst, Protocol string }
	if strings.TrimSpace(out) != "" {
		if err := json.Unmarshal([]byte(out), &current); err != nil {
			return status, err
		}
	}
	for _, r := range current {
		dst := r.Dst
		if p, err := network(dst); err == nil {
			dst = p.String() // ip prints host routes without /32
		}
		if r.Protocol != "kernel" && !wanted[dst] {
			if err := h.ip("route", "del", r.Dst, "dev", vpnIface); err != nil {
				return status, err
			}
		}
	}
	if dev, err = h.WG.Device(vpnIface); err != nil {
		return status, err
	}
	for _, p := range dev.Peers {
		if p.PublicKey == plan.peer {
			if !p.LastHandshakeTime.IsZero() {
				at := p.LastHandshakeTime.UTC()
				status.LatestHandshake = &at
			}
			status.RxBytes, status.TxBytes = p.ReceiveBytes, p.TransmitBytes
		}
	}
	return status, nil
}

type peerReport struct {
	PublicKey       string    `json:"public_key"`
	LatestHandshake time.Time `json:"latest_handshake"`
}

type serverReport struct {
	ServerPublicKey string       `json:"server_public_key"`
	Error           string       `json:"error"`
	Peers           []peerReport `json:"peers"`
}

type serverPeer struct {
	key     wgtypes.Key
	address netip.Addr
}

func validateServer(raw json.RawMessage) (int, netip.Prefix, []serverPeer, error) {
	var s struct {
		ListenPort int               `json:"listen_port"`
		Address    string            `json:"address"`
		Peers      []json.RawMessage `json:"peers"`
	}
	if err := strict(raw, &s, "unexpected VPN server fields", "listen_port", "address", "peers"); err != nil {
		return 0, netip.Prefix{}, nil, err
	}
	if s.ListenPort < 1 || s.ListenPort > 65535 {
		return 0, netip.Prefix{}, nil, errors.New("invalid listen port")
	}
	address, err := iface(s.Address)
	if err != nil || !address.Addr().Is4() || overlapsReserved(address.Masked()) {
		return 0, netip.Prefix{}, nil, errors.New("invalid server address")
	}
	if len(s.Peers) > 1024 {
		return 0, netip.Prefix{}, nil, errors.New("invalid peers")
	}
	var peers []serverPeer
	for _, raw := range s.Peers {
		var p struct {
			PublicKey string `json:"public_key"`
			Address   string `json:"address"`
		}
		if err := strict(raw, &p, "unexpected peer fields", "public_key", "address"); err != nil {
			return 0, netip.Prefix{}, nil, err
		}
		key, err := wgtypes.ParseKey(p.PublicKey)
		if err != nil {
			return 0, netip.Prefix{}, nil, errors.New("invalid peer key")
		}
		addr, err := netip.ParseAddr(p.Address)
		if err != nil || !address.Masked().Contains(addr) {
			return 0, netip.Prefix{}, nil, errors.New("peer address outside the VPN network")
		}
		peers = append(peers, serverPeer{key, addr})
	}
	return s.ListenPort, address, peers, nil
}

// Forwarding NATs the dashboard and K3s API from the server's tunnel address
// to the node address they listen on, so profiles route only the VPN network
// and never this host's public address.
func Forwarding(address, nodeIP netip.Addr, ports []int) string {
	var list []string
	for _, p := range slices.Compact(slices.Sorted(slices.Values(ports))) {
		list = append(list, strconv.Itoa(p))
	}
	return strings.Join([]string{"table ip " + natTable + " {}", "delete table ip " + natTable, "table ip " + natTable + " {",
		"  chain prerouting {", "    type nat hook prerouting priority dstnat; policy accept;",
		fmt.Sprintf(`    iifname "%s" ip daddr %s tcp dport { %s } dnat to %s`, serverIface, address, strings.Join(list, ", "), nodeIP),
		"  }", "}"}, "\n") + "\n"
}

// VPNServer makes hakopod-wgs match the server, or removes it and its NAT
// when the server is off. It returns the report, or nil when off.
func (h *Host) VPNServer(raw json.RawMessage, cfg config) (*serverReport, error) {
	if !present(raw) {
		if h.linkExists(serverIface) {
			if err := h.ip("link", "del", "dev", serverIface); err != nil {
				return nil, err
			}
			if err := h.nft(removeTable("ip", natTable)); err != nil {
				return nil, err
			}
			os.Remove(h.NATSaved)
		}
		return nil, nil
	}
	port, address, peers, err := validateServer(raw)
	if err != nil {
		return nil, err
	}
	node, err := netip.ParseAddr(cfg.NodeIP)
	if err != nil || !node.Is4() {
		return nil, errors.New("node_ip in /etc/hakopod/config.json must be an IPv4 address")
	}
	ports, err := cfg.ports()
	if err != nil {
		return nil, err
	}
	key, err := privateKey(h.ServerKey)
	if err != nil {
		return nil, err
	}
	report := &serverReport{ServerPublicKey: key.PublicKey().String(), Peers: []peerReport{}}
	if !h.linkExists(serverIface) {
		if err := h.ip("link", "add", "dev", serverIface, "type", "wireguard"); err != nil {
			return report, err
		}
	}
	dev, err := h.device(serverIface)
	if err != nil {
		return report, err
	}
	wanted := map[wgtypes.Key]bool{}
	var config []wgtypes.PeerConfig
	for _, p := range peers {
		wanted[p.key] = true
		config = append(config, wgtypes.PeerConfig{PublicKey: p.key, ReplaceAllowedIPs: true, AllowedIPs: []net.IPNet{ipNet(netip.PrefixFrom(p.address, 32))}})
	}
	for _, p := range dev.Peers {
		if !wanted[p.PublicKey] {
			config = append(config, wgtypes.PeerConfig{PublicKey: p.PublicKey, Remove: true})
		}
	}
	if err := h.WG.ConfigureDevice(serverIface, wgtypes.Config{PrivateKey: &key, ListenPort: &port, Peers: config}); err != nil {
		return report, err
	}
	if err := h.up(serverIface, address); err != nil {
		return report, err
	}
	if err := h.applyTable(h.NATSaved, Forwarding(address.Addr(), node, ports)); err != nil {
		return report, err
	}
	if dev, err = h.WG.Device(serverIface); err != nil {
		return report, err
	}
	for _, p := range dev.Peers {
		if !p.LastHandshakeTime.IsZero() {
			report.Peers = append(report.Peers, peerReport{p.PublicKey.String(), p.LastHandshakeTime.UTC()})
		}
	}
	return report, nil
}
