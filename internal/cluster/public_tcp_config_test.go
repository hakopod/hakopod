package cluster

import "testing"

func TestPublicTCPPortOperatorConfiguration(t *testing.T) {
	for _, invalid := range []string{"80", "6443", "587,587", "587,", "0", "65536", "587;echo no", "2587,22"} {
		if _, err := ParsePublicTCPPorts(invalid); err == nil {
			t.Fatalf("accepted invalid port list %q", invalid)
		}
	}
	ports, err := ParsePublicTCPPorts("587, 465")
	if err != nil || len(ports) != 2 || ports[0] != 465 || ports[1] != 587 {
		t.Fatalf("ports=%v err=%v", ports, err)
	}
}
