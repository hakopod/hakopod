package access

import "testing"

func TestOneClientConnectorAtATime(t *testing.T) {
	p := Policy{Tailscale: &Tailscale{}, OpenVPN: &OpenVPN{Routes: []string{"10.20.0.0/16"}}}
	if _, err := p.Normalize(); err == nil {
		t.Fatal("two connectors accepted")
	}
	p = Policy{Tailscale: &Tailscale{Tags: []string{"tag:hakopod"}, LoginServer: "https://headscale.example.com"}}
	if got, err := p.Normalize(); err != nil || got.Tailscale.Hostname != "hakopod" {
		t.Fatalf("tailscale = %+v %v", got.Tailscale, err)
	}
}

func TestConnectorsRefuseUnsafeValues(t *testing.T) {
	for name, p := range map[string]Policy{
		"tailscale tag":      {Tailscale: &Tailscale{Tags: []string{"hakopod"}}},
		"tailscale http":     {Tailscale: &Tailscale{LoginServer: "http://headscale.example.com"}},
		"tailscale hostname": {Tailscale: &Tailscale{Hostname: "bad host"}},
		"openvpn default":    {OpenVPN: &OpenVPN{Routes: []string{"0.0.0.0/0"}}},
		"openvpn cluster":    {OpenVPN: &OpenVPN{Routes: []string{"10.43.0.0/24"}}},
		"ipsec id":           {IPsec: &IPsec{RemoteAddress: "vpn.example.com", LocalID: "a b", RemoteID: "vpn", RemoteNetworks: []string{"10.20.0.0/16"}}},
		"ipsec remote":       {IPsec: &IPsec{RemoteAddress: "vpn;reboot", LocalID: "hakopod", RemoteID: "vpn", RemoteNetworks: []string{"10.20.0.0/16"}}},
		"ipsec no networks":  {IPsec: &IPsec{RemoteAddress: "vpn.example.com", LocalID: "hakopod", RemoteID: "vpn"}},
	} {
		if _, err := p.Normalize(); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

func TestOpenVPNProfileAllowsOnlyInertDirectives(t *testing.T) {
	good := "client\ndev tun\nproto udp\nremote vpn.example.com 1194\nremote-cert-tls server\n<ca>\n-----BEGIN CERTIFICATE-----\nup /bin/sh\n-----END CERTIFICATE-----\n</ca>\n"
	if err := ValidateOpenVPNProfile(good); err != nil {
		t.Fatalf("good profile: %v", err)
	}
	for name, profile := range map[string]string{
		"script":          good + "up /tmp/run.sh\n",
		"script security": good + "script-security 2\n",
		"plugin":          good + "plugin /lib/evil.so\n",
		"redirect":        good + "redirect-gateway def1\n",
		"file key":        good + "key /etc/shadow\n",
		"config include":  good + "config /etc/other.conf\n",
		"unknown block":   good + "<connection>\nremote x\n</connection>\n",
		"no remote":       "client\ndev tun\n",
		"unterminated":    "client\nremote vpn 1194\n<ca>\n",
	} {
		if err := ValidateOpenVPNProfile(profile); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}
