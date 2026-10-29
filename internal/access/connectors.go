package access

import (
	"errors"
	"net/netip"
	"regexp"
	"strings"
)

// Tailscale joins the server to a tailnet, or to a Headscale server with
// LoginServer. Its auth key is a write-only secret stored apart from the policy.
type Tailscale struct {
	Hostname    string   `json:"hostname"`
	Tags        []string `json:"tags"`
	LoginServer string   `json:"login_server"`
}

// OpenVPN runs the administrator's client profile (a write-only secret).
// Routes are the only networks sent through the tunnel; routes pushed by the
// server are ignored.
type OpenVPN struct {
	Routes []string `json:"routes"`
}

// IPsec is an IKEv2 tunnel with a pre-shared key (a write-only secret),
// carried by strongSwan.
type IPsec struct {
	RemoteAddress  string   `json:"remote_address"`
	LocalID        string   `json:"local_id"`
	RemoteID       string   `json:"remote_id"`
	RemoteNetworks []string `json:"remote_networks"`
}

var (
	tailscaleTag = regexp.MustCompile(`^tag:[a-z0-9][a-z0-9-]{0,62}$`)
	hostnameRe   = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`)
	ikeID        = regexp.MustCompile(`^[A-Za-z0-9@._:=,-]{1,128}$`)
)

func tunnelNetworks(values []string, field string) ([]string, error) {
	if len(values) == 0 || len(values) > 64 {
		return nil, errors.New(field + " needs between 1 and 64 networks")
	}
	prefixes, err := prefixes(values, field)
	if err != nil {
		return nil, err
	}
	out := []string{}
	for _, prefix := range prefixes {
		if prefix.Bits() == 0 {
			return nil, errors.New(field + " cannot route all traffic through the tunnel")
		}
		for _, reserved := range reservedTunnelNetworks {
			if prefix.Overlaps(reserved) {
				return nil, errors.New(field + " cannot overlap the cluster networks 10.42.0.0/16 and 10.43.0.0/16")
			}
		}
		out = append(out, prefix.String())
	}
	return out, nil
}

func (t Tailscale) Normalize() (Tailscale, error) {
	t.Hostname = strings.ToLower(strings.TrimSpace(t.Hostname))
	if t.Hostname == "" {
		t.Hostname = "hakopod"
	}
	if !hostnameRe.MatchString(t.Hostname) {
		return t, errors.New("tailscale.hostname must be a DNS label")
	}
	if len(t.Tags) > 16 {
		return t, errors.New("tailscale.tags has more than 16 entries")
	}
	for _, tag := range t.Tags {
		if !tailscaleTag.MatchString(tag) {
			return t, errors.New("tailscale.tags must look like tag:hakopod")
		}
	}
	if t.LoginServer != "" && (!strings.HasPrefix(t.LoginServer, "https://") || len(t.LoginServer) > 256 || strings.ContainsAny(t.LoginServer, " \t\n'\"")) {
		return t, errors.New("tailscale.login_server must be an https URL")
	}
	return t, nil
}

func (o OpenVPN) Normalize() (OpenVPN, error) {
	routes, err := tunnelNetworks(o.Routes, "openvpn.routes")
	o.Routes = routes
	return o, err
}

func (i IPsec) Normalize() (IPsec, error) {
	i.RemoteAddress = strings.TrimSpace(i.RemoteAddress)
	if _, err := netip.ParseAddr(i.RemoteAddress); err != nil && !validHost(i.RemoteAddress) {
		return i, errors.New("ipsec.remote_address must be an address or host name")
	}
	if !ikeID.MatchString(i.LocalID) || !ikeID.MatchString(i.RemoteID) {
		return i, errors.New("ipsec.local_id and ipsec.remote_id must be IKE identities such as hakopod.example.com")
	}
	networks, err := tunnelNetworks(i.RemoteNetworks, "ipsec.remote_networks")
	i.RemoteNetworks = networks
	return i, err
}

// OpenVPN directives a management profile may use. Anything that runs a
// program, loads a plugin, changes the server's routing or writes files is
// refused; route-nopull is always added on the host.
var openVPNDirectives = map[string]bool{
	"client": true, "dev": true, "dev-type": true, "proto": true, "remote": true, "port": true, "rport": true,
	"resolv-retry": true, "nobind": true, "persist-key": true, "persist-tun": true, "remote-cert-tls": true,
	"verify-x509-name": true, "cipher": true, "data-ciphers": true, "data-ciphers-fallback": true, "auth": true,
	"tls-version-min": true, "tls-cipher": true, "tls-ciphersuites": true, "key-direction": true, "verb": true,
	"mute": true, "tun-mtu": true, "mssfix": true, "keepalive": true, "ping": true, "ping-restart": true,
	"connect-retry": true, "connect-retry-max": true, "server-poll-timeout": true, "remote-random": true,
	"explicit-exit-notify": true, "reneg-sec": true, "auth-nocache": true, "setenv": false,
}

var openVPNBlocks = map[string]bool{"ca": true, "cert": true, "key": true, "tls-auth": true, "tls-crypt": true, "tls-crypt-v2": true}

// ValidateOpenVPNProfile checks a client profile line by line. Certificates
// and keys must be inline blocks; file references are refused.
func ValidateOpenVPNProfile(profile string) error {
	if len(profile) == 0 || len(profile) > 64<<10 {
		return errors.New("the OpenVPN profile must be between 1 byte and 64 KiB")
	}
	block := ""
	remote := false
	for _, raw := range strings.Split(strings.ReplaceAll(profile, "\r\n", "\n"), "\n") {
		line := strings.TrimSpace(raw)
		if block != "" {
			if line == "</"+block+">" {
				block = ""
			}
			continue
		}
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		if strings.HasPrefix(line, "<") && strings.HasSuffix(line, ">") {
			name := strings.Trim(line, "<>")
			if !openVPNBlocks[name] {
				return errors.New("the OpenVPN profile has an unsupported inline block <" + name + ">")
			}
			block = name
			continue
		}
		name := strings.ToLower(strings.Fields(line)[0])
		if openVPNBlocks[name] {
			return errors.New("the OpenVPN profile must include " + name + " inline, not as a file")
		}
		if !openVPNDirectives[name] {
			return errors.New("the OpenVPN profile directive " + name + " is not allowed")
		}
		remote = remote || name == "remote"
	}
	if block != "" {
		return errors.New("the OpenVPN profile has an unterminated <" + block + "> block")
	}
	if !remote {
		return errors.New("the OpenVPN profile needs a remote")
	}
	return nil
}
