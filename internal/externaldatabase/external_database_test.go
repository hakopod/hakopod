package externaldatabase

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/netip"
	"net/url"
	"strings"
	"testing"
	"time"
)

func fixtureSpec() Spec {
	return Spec{SchemaVersion: 1, Name: "provider-fixture", Provider: "planetscale", Engine: "mysql", Host: "fixture.us-east.psdb.cloud", Port: 3306, Database: "app"}
}

func TestExternalDatabaseSpecRejectsAmbiguousOrUntrustedTargets(t *testing.T) {
	for _, host := range []string{"127.0.0.1", "metadata.google.internal", "example.com", "psdb.cloud.attacker.example", "psdb.cloud", "fixture.psdb.cloud.", "fixture.psdb.cloud:3306", "fixture@psdb.cloud", "fixture..psdb.cloud", "-fixture.psdb.cloud"} {
		s := fixtureSpec()
		s.Host = host
		if s.Validate() == nil {
			t.Errorf("accepted %q", host)
		}
	}
	for _, engine := range []string{"mysql", "postgresql"} {
		s := fixtureSpec()
		s.Engine = engine
		if engine == "postgresql" {
			s.Port = 6432
		}
		if err := s.Validate(); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := Parse([]byte("schema_version=1\nname='provider-fixture'\nprovider='planetscale'\nengine='mysql'\nhost='fixture.psdb.cloud'\nport=3306\ndatabase='app'\npassword='must-not-be-stored'\n")); err == nil {
		t.Fatal("password entered the immutable spec")
	}
}

func TestExternalCredentialsAreBoundToResourceAndEndpoint(t *testing.T) {
	key := bytes.Repeat([]byte{7}, 32)
	id := strings.Repeat("a", 32)
	s := fixtureSpec()
	c := Credentials{Username: "branch-user", Password: "test-password-do-not-log"}
	sealed, err := SealCredentials(key, id, s, c)
	if err != nil {
		t.Fatal(err)
	}
	r := Resource{ID: id, Spec: s, EncryptedCredentials: sealed}
	got, err := OpenCredentials(key, r)
	if err != nil || got != c {
		t.Fatal("envelope did not round trip", err)
	}
	for _, mutate := range []func(*Resource){func(r *Resource) { r.ID = strings.Repeat("b", 32) }, func(r *Resource) { r.Spec.Host = "other.psdb.cloud" }, func(r *Resource) { r.Spec.Database = "other" }, func(r *Resource) { r.Spec.Port = 6432 }, func(r *Resource) {
		r.EncryptedCredentials = append([]byte(nil), sealed...)
		r.EncryptedCredentials[len(sealed)-1] ^= 1
	}} {
		changed := r
		mutate(&changed)
		if _, err = OpenCredentials(key, changed); err == nil {
			t.Fatal("moved or modified credentials were accepted")
		}
	}
	body, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(body, []byte(c.Password)) || bytes.Contains(body, []byte("EncryptedCredentials")) {
		t.Fatal("credentials entered a public resource")
	}
}

func TestExternalConnectionURLEscapesCredentialsAndRequiresIdentity(t *testing.T) {
	c := Credentials{Username: "user.with.branch|replica", Password: "a/b?c@d:#%"}
	for _, engine := range []string{"mysql", "postgresql"} {
		s := fixtureSpec()
		s.Engine = engine
		if engine == "postgresql" {
			s.Port = 5432
		}
		raw, err := ConnectionURL(s, c)
		if err != nil {
			t.Fatal(err)
		}
		u, err := url.Parse(raw)
		if err != nil {
			t.Fatal(err)
		}
		password, _ := u.User.Password()
		if password != c.Password || u.User.Username() != c.Username {
			t.Fatal("credentials changed during URL encoding")
		}
		if engine == "postgresql" && (u.Query().Get("sslmode") != "verify-full" || u.Query().Get("sslrootcert") != "system") {
			t.Fatal("PostgreSQL trust weakened")
		}
		if engine == "mysql" && u.Query().Get("ssl-mode") != "VERIFY_IDENTITY" {
			t.Fatal("MySQL hostname verification missing")
		}
	}
}

func TestExternalProbeRejectsPrivateMixedReservedAndReboundDNS(t *testing.T) {
	for _, raw := range []string{"127.0.0.1", "10.1.2.3", "172.16.0.1", "192.168.1.1", "169.254.169.254", "100.100.100.200", "192.0.2.1", "198.18.0.1", "203.0.113.1", "0.0.0.0", "240.1.1.1", "::1", "fc00::1", "fe80::1", "::ffff:127.0.0.1", "64:ff9b::a00:1", "2001:db8::1"} {
		if PublicAddress(netip.MustParseAddr(raw)) {
			t.Errorf("address %s is not public", raw)
		}
	}
	calls := 0
	p := NewProber()
	p.lookup = func(context.Context, string, string) ([]netip.Addr, error) {
		calls++
		if calls == 1 {
			return []netip.Addr{netip.MustParseAddr("8.8.8.8")}, nil
		}
		return []netip.Addr{netip.MustParseAddr("10.0.0.1")}, nil
	}
	dialed := []string{}
	p.dial = func(_ context.Context, _, address string) (net.Conn, error) {
		dialed = append(dialed, address)
		a, b := net.Pipe()
		_ = b.Close()
		return a, nil
	}
	conn, ips, err := p.safeDial(context.Background(), "fixture.psdb.cloud", 3306)
	if err != nil || len(ips) != 1 {
		t.Fatal(err)
	}
	_ = conn.Close()
	if _, _, err = p.safeDial(context.Background(), "fixture.psdb.cloud", 3306); !errors.Is(err, ErrUnavailable) {
		t.Fatal("rebound DNS was accepted")
	}
	if len(dialed) != 1 || dialed[0] != "8.8.8.8:3306" {
		t.Fatal("dial did not use the verified address")
	}
	p.lookup = func(context.Context, string, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("8.8.8.8"), netip.MustParseAddr("10.0.0.1")}, nil
	}
	if _, _, err = p.safeDial(context.Background(), "fixture.psdb.cloud", 3306); err == nil {
		t.Fatal("mixed public/private DNS was accepted")
	}
}

func TestExternalProbeFailureDoesNotPublishCredentialsOrInventReadiness(t *testing.T) {
	p := NewProber()
	p.lookup = func(context.Context, string, string) ([]netip.Addr, error) {
		return nil, errors.New("secret-upstream-message")
	}
	r := Resource{Revision: 3, Spec: fixtureSpec()}
	c := Credentials{Username: "sensitive-user", Password: "sensitive-password"}
	o := p.Observe(context.Background(), r, c)
	body, _ := json.Marshal(o)
	if o.Status != "unreachable" || o.QueryVerified || o.TLSVerified || o.LatencyMS != nil || o.ObservedAt.IsZero() || bytes.Contains(body, []byte("sensitive")) || bytes.Contains(body, []byte("secret-upstream")) {
		t.Fatal("failed probe exposed data or readiness")
	}
	r.Status = "ready"
	r.Observation = Observation{Status: "ready", Revision: 3, TLSVerified: true, QueryVerified: true, ObservedAt: time.Now().Add(-3 * time.Minute)}
	if r.Verified(time.Now()) {
		t.Fatal("expired observation was treated as verified")
	}
}
