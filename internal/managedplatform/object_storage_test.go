package managedplatform

import (
	"context"
	"net"
	"net/http"
	"net/netip"
	"testing"
)

func TestObjectStorageRejectsPrivateAndMetadataOrigins(t *testing.T) {
	for _, value := range []string{"https://127.0.0.1", "https://[::1]", "https://169.254.169.254", "https://10.0.0.1", "https://localhost", "https://objects.local", "https://objects.svc.cluster.local", "https://objects.example.test:8443"} {
		if _, err := NewObjectStorageClient(value); err == nil {
			t.Fatal("unsafe object storage origin accepted")
		}
	}
}

func TestObjectStorageChecksAllDNSAnswersBeforeDialAndPinsTheCheckedAddress(t *testing.T) {
	c, err := NewObjectStorageClient("https://objects.example.test")
	if err != nil {
		t.Fatal(err)
	}
	dials := 0
	c.lookup = func(context.Context, string, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("8.8.8.8"), netip.MustParseAddr("10.0.0.1")}, nil
	}
	c.dial = func(context.Context, string, string) (net.Conn, error) { dials++; return nil, nil }
	if _, err = c.dialContext(context.Background(), "tcp", "objects.example.test:443"); err == nil || dials != 0 {
		t.Fatal("mixed DNS answer reached a denied network")
	}
	c.lookup = func(context.Context, string, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("8.8.8.8")}, nil
	}
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()
	c.dial = func(_ context.Context, network, address string) (net.Conn, error) {
		dials++
		if network != "tcp" || address != "8.8.8.8:443" {
			t.Fatal("DNS was resolved again")
		}
		return client, nil
	}
	if _, err = c.dialContext(context.Background(), "tcp", "objects.example.test:443"); err != nil || dials != 1 {
		t.Fatal("checked address was not dialed", err)
	}
	request, _ := http.NewRequest("GET", "https://other.example.test/object", nil)
	if _, err = c.Do(request); err == nil {
		t.Fatal("cross-origin request accepted")
	}
	if err = c.client.CheckRedirect(request, nil); err == nil {
		t.Fatal("cross-origin redirect accepted")
	}
}

func TestObjectStorageRejectsAuthorityOverrides(t *testing.T) {
	c, err := NewObjectStorageClient("https://objects.example.test")
	if err != nil {
		t.Fatal(err)
	}
	for _, host := range []string{"attacker.example", "Objects.example.test", "objects.example.test.", "objects.example.test:443"} {
		request, _ := http.NewRequest("GET", "https://objects.example.test/object", nil)
		request.Host = host
		if _, err = c.Do(request); err == nil {
			t.Fatal("unchecked HTTP authority reached transport")
		}
	}
}
