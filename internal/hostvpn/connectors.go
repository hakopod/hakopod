package hostvpn

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Tailscale, OpenVPN and IPsec connectors. At most one runs. Each is brought
// up only when its configuration changes, torn down when it is removed, and
// never allowed to take the server's default route or run scripts. Secrets
// are written only to root-owned 0600 files; only digests are remembered.

type connectorReport struct {
	Type      string `json:"type"`
	Connected bool   `json:"connected"`
	Address   string `json:"address"`
	Error     string `json:"error"`
}

var (
	openVPNDirectives = map[string]bool{}
	openVPNBlocks     = map[string]bool{"ca": true, "cert": true, "key": true, "tls-auth": true, "tls-crypt": true, "tls-crypt-v2": true}
	tailscaleHost     = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`)
	tailscaleTag      = regexp.MustCompile(`^tag:[a-z0-9][a-z0-9-]{0,62}$`)
)

func init() {
	for _, name := range strings.Fields(`client proto remote port rport resolv-retry nobind persist-key persist-tun
		remote-cert-tls verify-x509-name cipher data-ciphers data-ciphers-fallback auth
		tls-version-min tls-cipher tls-ciphersuites key-direction verb mute tun-mtu mssfix
		keepalive ping ping-restart connect-retry connect-retry-max server-poll-timeout
		remote-random explicit-exit-notify reneg-sec auth-nocache`) {
		openVPNDirectives[name] = true
	}
}

func digest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// changed returns the digest when value differs from the one last applied
// for name, or "" when it is the same.
func (h *Host) changed(name string, value []byte) string {
	d := digest(value)
	if old, err := os.ReadFile(filepath.Join(h.State, name+".sha256")); err == nil && string(old) == d {
		return ""
	}
	return d
}

func (h *Host) remember(name, d string) error {
	if err := os.MkdirAll(h.State, 0o700); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(h.State, name+".sha256"), []byte(d), 0o600)
}

func (h *Host) forget(name string) bool {
	return os.Remove(filepath.Join(h.State, name+".sha256")) == nil
}

func writeSecret(path, text string) error {
	return writeFile(path, text, 0o600)
}

// OpenVPNConfig keeps only inert directives and inline key blocks, then forces
// the device, no pushed routes and no scripts.
func OpenVPNConfig(profile string, networks []netip.Prefix) (string, error) {
	var out []string
	block := ""
	for _, raw := range strings.Split(strings.ReplaceAll(profile, "\r\n", "\n"), "\n") {
		line := strings.TrimSpace(raw)
		if block != "" {
			out = append(out, raw)
			if line == "</"+block+">" {
				block = ""
			}
			continue
		}
		if line == "" || line[0] == '#' || line[0] == ';' {
			continue
		}
		if strings.HasPrefix(line, "<") && strings.HasSuffix(line, ">") {
			name := strings.Trim(line, "<>")
			if !openVPNBlocks[name] {
				return "", errors.New("unsupported OpenVPN block")
			}
			block = name
			out = append(out, line)
			continue
		}
		name := strings.ToLower(strings.Fields(line)[0])
		if name == "dev" || name == "dev-type" {
			continue
		}
		if !openVPNDirectives[name] {
			return "", fmt.Errorf("OpenVPN directive %s is not allowed", name)
		}
		out = append(out, line)
	}
	if block != "" {
		return "", errors.New("unterminated OpenVPN block")
	}
	out = append(out, "dev hakopod-ovpn", "dev-type tun", "route-nopull", "script-security 1")
	for _, n := range networks {
		if n.Addr().Is4() {
			out = append(out, "route "+n.Addr().String()+" "+net.IP(net.CIDRMask(n.Bits(), 32)).String())
		} else {
			out = append(out, "route-ipv6 "+n.String())
		}
	}
	return strings.Join(out, "\n") + "\n", nil
}

type ipsecConfig struct {
	RemoteAddress  string   `json:"remote_address"`
	LocalID        string   `json:"local_id"`
	RemoteID       string   `json:"remote_id"`
	RemoteNetworks []string `json:"remote_networks"`
	PSK            string   `json:"psk"`
}

func SwanctlConfig(c ipsecConfig) (string, error) {
	if _, err := netip.ParseAddr(c.RemoteAddress); err != nil && !hostname(c.RemoteAddress) {
		return "", errors.New("invalid IPsec remote address")
	}
	if !ikeID.MatchString(c.LocalID) || !ikeID.MatchString(c.RemoteID) {
		return "", errors.New("invalid IKE identity")
	}
	if n := utf8.RuneCountInString(c.PSK); n < 20 || n > 256 || strings.ContainsAny(c.PSK, "\"\n\r") {
		return "", errors.New("invalid IPsec pre-shared key")
	}
	networks, err := routes(c.RemoteNetworks)
	if err != nil {
		return "", err
	}
	var ts []string
	for _, n := range networks {
		ts = append(ts, n.String())
	}
	return fmt.Sprintf(`connections {
  hakopod {
    version = 2
    remote_addrs = %s
    local {
      auth = psk
      id = %s
    }
    remote {
      auth = psk
      id = %s
    }
    children {
      hakopod {
        local_ts = dynamic
        remote_ts = %s
        start_action = start
        dpd_action = restart
        close_action = start
      }
    }
  }
}
secrets {
  ike-hakopod {
    id-1 = %s
    id-2 = %s
    secret = "%s"
  }
}
`, c.RemoteAddress, c.LocalID, c.RemoteID, strings.Join(ts, ","), c.LocalID, c.RemoteID, c.PSK), nil
}

func (h *Host) need(tool, message string) error {
	if _, err := h.LookPath(tool); err != nil {
		return errors.New(message)
	}
	return nil
}

func (h *Host) tailscale(raw json.RawMessage) (*connectorReport, error) {
	if err := h.need("tailscale", "install Tailscale on the server to use the Tailscale connector"); err != nil {
		return nil, err
	}
	var c struct {
		Hostname    string   `json:"hostname"`
		Tags        []string `json:"tags"`
		LoginServer string   `json:"login_server"`
		AuthKey     string   `json:"auth_key"`
	}
	if err := json.Unmarshal(raw, &c); err != nil {
		return nil, errors.New("invalid Tailscale configuration")
	}
	if n := utf8.RuneCountInString(c.AuthKey); n < 16 || n > 512 || strings.IndexFunc(c.AuthKey, unicode.IsSpace) >= 0 {
		return nil, errors.New("invalid Tailscale auth key")
	}
	if !tailscaleHost.MatchString(c.Hostname) {
		return nil, errors.New("invalid Tailscale hostname")
	}
	for _, tag := range c.Tags {
		if !tailscaleTag.MatchString(tag) {
			return nil, errors.New("invalid Tailscale tag")
		}
	}
	if c.LoginServer != "" && (!strings.HasPrefix(c.LoginServer, "https://") || strings.IndexFunc(c.LoginServer, unicode.IsSpace) >= 0) {
		return nil, errors.New("invalid Tailscale login server")
	}
	canonical, _ := json.Marshal(c)
	if d := h.changed("tailscale", canonical); d != "" {
		if _, err := h.Run([]string{"systemctl", "enable", "--now", "tailscaled"}, ""); err != nil {
			return nil, err
		}
		keyFile := filepath.Join(h.RunDir, "tailscale.key")
		if err := writeSecret(keyFile, c.AuthKey); err != nil {
			return nil, err
		}
		// No DNS, no pushed routes and no netfilter changes: K3s owns iptables.
		args := []string{"tailscale", "up", "--reset", "--auth-key=file:" + keyFile, "--hostname=" + c.Hostname,
			"--accept-routes=false", "--accept-dns=false", "--netfilter-mode=off", "--timeout=60s"}
		if len(c.Tags) > 0 {
			args = append(args, "--advertise-tags="+strings.Join(c.Tags, ","))
		}
		if c.LoginServer != "" {
			args = append(args, "--login-server="+c.LoginServer)
		}
		_, err := h.Run(args, "")
		os.Remove(keyFile)
		if err != nil {
			return nil, err
		}
		if err := h.remember("tailscale", d); err != nil {
			return nil, err
		}
	}
	out, err := h.Run([]string{"tailscale", "status", "--json"}, "")
	if err != nil {
		return nil, err
	}
	var status struct {
		BackendState string
		Self         struct{ TailscaleIPs []string }
	}
	if err := json.Unmarshal([]byte(out), &status); err != nil {
		return nil, err
	}
	report := &connectorReport{Type: "tailscale", Connected: status.BackendState == "Running"}
	if len(status.Self.TailscaleIPs) > 0 {
		report.Address = status.Self.TailscaleIPs[0]
	}
	return report, nil
}

const openVPNUnit = `[Unit]
Description=Hakopod management OpenVPN connector
After=network-online.target

[Service]
ExecStart=/usr/sbin/openvpn --config /etc/hakopod/openvpn/hakopod.conf
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
`

func (h *Host) openvpn(raw json.RawMessage) (*connectorReport, error) {
	if err := h.need("openvpn", "install OpenVPN on the server to use the OpenVPN connector"); err != nil {
		return nil, err
	}
	var c struct {
		Routes  []string `json:"routes"`
		Profile string   `json:"profile"`
	}
	if err := json.Unmarshal(raw, &c); err != nil {
		return nil, errors.New("invalid OpenVPN configuration")
	}
	networks, err := routes(c.Routes)
	if err != nil {
		return nil, err
	}
	text, err := OpenVPNConfig(c.Profile, networks)
	if err != nil {
		return nil, err
	}
	if d := h.changed("openvpn", []byte(text)); d != "" {
		if err := writeSecret(filepath.Join(h.OpenVPNDir, "hakopod.conf"), text); err != nil {
			return nil, err
		}
		if err := os.WriteFile(h.OpenVPNUnit, []byte(openVPNUnit), 0o644); err != nil {
			return nil, err
		}
		for _, args := range [][]string{{"systemctl", "daemon-reload"}, {"systemctl", "enable", "hakopod-openvpn"}, {"systemctl", "restart", "hakopod-openvpn"}} {
			if _, err := h.Run(args, ""); err != nil {
				return nil, err
			}
		}
		if err := h.remember("openvpn", d); err != nil {
			return nil, err
		}
	}
	active, _ := h.Run([]string{"systemctl", "is-active", "hakopod-openvpn"}, "")
	out, _ := h.Run([]string{"ip", "-j", "-4", "address", "show", "dev", "hakopod-ovpn"}, "")
	var links []struct {
		AddrInfo []struct{ Local string } `json:"addr_info"`
	}
	json.Unmarshal([]byte(out), &links)
	address := ""
	if len(links) > 0 && len(links[0].AddrInfo) > 0 {
		address = links[0].AddrInfo[0].Local
	}
	return &connectorReport{Type: "openvpn", Connected: strings.TrimSpace(active) == "active" && address != "", Address: address}, nil
}

func (h *Host) ipsec(raw json.RawMessage) (*connectorReport, error) {
	if err := h.need("swanctl", "install strongSwan (swanctl) on the server to use the IPsec connector"); err != nil {
		return nil, err
	}
	var c ipsecConfig
	if err := json.Unmarshal(raw, &c); err != nil {
		return nil, errors.New("invalid IPsec configuration")
	}
	text, err := SwanctlConfig(c)
	if err != nil {
		return nil, err
	}
	if d := h.changed("ipsec", []byte(text)); d != "" {
		if err := writeSecret(h.Swanctl, text); err != nil {
			return nil, err
		}
		if _, err := h.Run([]string{"swanctl", "--load-all"}, ""); err != nil {
			return nil, err
		}
		if err := h.remember("ipsec", d); err != nil {
			return nil, err
		}
	}
	sas, _ := h.Run([]string{"swanctl", "--list-sas", "--ike", "hakopod"}, "")
	return &connectorReport{Type: "ipsec", Connected: strings.Contains(sas, "ESTABLISHED") && strings.Contains(sas, "INSTALLED")}, nil
}

// teardown stops every connector other than keep that was brought up before.
func (h *Host) teardown(keep string) {
	if keep != "tailscale" && h.forget("tailscale") {
		h.Run([]string{"tailscale", "logout"}, "")
	}
	if keep != "openvpn" && h.forget("openvpn") {
		h.Run([]string{"systemctl", "disable", "--now", "hakopod-openvpn"}, "")
		os.Remove(filepath.Join(h.OpenVPNDir, "hakopod.conf"))
	}
	if keep != "ipsec" && h.forget("ipsec") {
		os.Remove(h.Swanctl)
		h.Run([]string{"swanctl", "--terminate", "--ike", "hakopod"}, "")
		h.Run([]string{"swanctl", "--load-all"}, "")
	}
}

// Connectors brings the configured connector up and the others down. It
// returns the status to report, or nil when none is configured.
func (h *Host) Connectors(s state) *connectorReport {
	for _, c := range []struct {
		name  string
		raw   json.RawMessage
		apply func(json.RawMessage) (*connectorReport, error)
	}{{"tailscale", s.Tailscale, h.tailscale}, {"openvpn", s.OpenVPN, h.openvpn}, {"ipsec", s.IPsec, h.ipsec}} {
		if present(c.raw) {
			h.teardown(c.name)
			report, err := c.apply(c.raw)
			if err != nil {
				return &connectorReport{Type: c.name, Error: clip(err)}
			}
			return report
		}
	}
	h.teardown("")
	return nil
}
