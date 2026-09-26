package dnsprovider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func fixtureProvider() Provider {
	return Provider{ID: "dns-1", Name: "Company Cloudflare", Kind: KindCloudflare, ZoneFilter: []string{"example.com"}, Revision: 1, Enabled: true}
}

// fixtureClient injects a transport over a local test server: no test contacts
// the network.
func fixtureClient(t *testing.T, p Provider, h http.HandlerFunc) (*cloudflare, *atomic.Int32) {
	t.Helper()
	var requests atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Header.Get("Authorization") != "Bearer fixture-token" {
			t.Error("request reached the provider without the bearer token")
		}
		w.Header().Set("Content-Type", "application/json")
		h(w, r)
	}))
	t.Cleanup(server.Close)
	client := server.Client()
	client.Timeout = 2 * time.Second
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return ErrUnavailable }
	return &cloudflare{transport: transport{http: client, endpoint: server.URL, token: "fixture-token"}, provider: p}, &requests
}

func ok(t *testing.T, w http.ResponseWriter, result any) {
	t.Helper()
	data, err := json.Marshal(map[string]any{"success": true, "errors": []any{}, "result": result})
	if err != nil {
		t.Fatal(err)
	}
	w.Write(data)
}

func TestZoneLongestSuffixMatch(t *testing.T) {
	var asked []string
	c, _ := fixtureClient(t, fixtureProvider(), func(w http.ResponseWriter, r *http.Request) {
		name := r.URL.Query().Get("name")
		asked = append(asked, name)
		if name == "example.com" {
			ok(t, w, []Zone{{ID: "zone-root", Name: "example.com"}})
			return
		}
		ok(t, w, []Zone{})
	})
	zone, err := c.Zone(context.Background(), "api.staging.example.com")
	if err != nil || zone.ID != "zone-root" || zone.Name != "example.com" {
		t.Fatalf("expected the example.com zone, got %+v: %v", zone, err)
	}
	if strings.Join(asked, ",") != "api.staging.example.com,staging.example.com,example.com" {
		t.Fatalf("zone lookup did not try suffixes longest first: %v", asked)
	}
}

func TestZonePrefersTheMoreSpecificZone(t *testing.T) {
	p := fixtureProvider()
	c, _ := fixtureClient(t, p, func(w http.ResponseWriter, r *http.Request) {
		name := r.URL.Query().Get("name")
		if name == "staging.example.com" {
			ok(t, w, []Zone{{ID: "zone-staging", Name: "staging.example.com"}})
			return
		}
		ok(t, w, []Zone{{ID: "zone-root", Name: "example.com"}})
	})
	zone, err := c.Zone(context.Background(), "api.staging.example.com")
	if err != nil || zone.ID != "zone-staging" {
		t.Fatalf("expected the most specific zone, got %+v: %v", zone, err)
	}
}

func TestZoneRefusesAmbiguousAndMissing(t *testing.T) {
	c, _ := fixtureClient(t, fixtureProvider(), func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("name") == "example.com" {
			ok(t, w, []Zone{{ID: "zone-a", Name: "example.com"}, {ID: "zone-b", Name: "example.com"}})
			return
		}
		ok(t, w, []Zone{})
	})
	if _, err := c.Zone(context.Background(), "api.example.com"); !errors.Is(err, ErrInput) || !strings.Contains(err.Error(), "more than one") {
		t.Fatalf("two equally specific zones must be refused, got %v", err)
	}

	empty, _ := fixtureClient(t, fixtureProvider(), func(w http.ResponseWriter, r *http.Request) { ok(t, w, []Zone{}) })
	if _, err := empty.Zone(context.Background(), "api.example.com"); !errors.Is(err, ErrInput) || !strings.Contains(err.Error(), "no DNS zone") {
		t.Fatalf("a hostname with no zone must be refused, got %v", err)
	}
}

func TestZoneFilterRefusesBeforeAnyRequest(t *testing.T) {
	c, requests := fixtureClient(t, fixtureProvider(), func(w http.ResponseWriter, r *http.Request) { ok(t, w, []Zone{}) })
	if _, err := c.Zone(context.Background(), "api.other.test"); !errors.Is(err, ErrInput) || !strings.Contains(err.Error(), "permitted zones") {
		t.Fatalf("a hostname outside the zone filter must be refused, got %v", err)
	}
	if requests.Load() != 0 {
		t.Fatalf("the zone filter must be enforced before any request, saw %d", requests.Load())
	}
	// Create must refuse the same hostname, so the caller is not the only guard.
	err := c.Create(context.Background(), Zone{ID: "zone-a", Name: "example.com"}, Record{Type: "TXT", Name: "api.other.test", Value: "token", TTL: 300})
	if !errors.Is(err, ErrInput) || requests.Load() != 0 {
		t.Fatalf("create outside the zone filter must be refused without a request: %v, %d requests", err, requests.Load())
	}
}

func TestCreateSucceeds(t *testing.T) {
	var created wireRecord
	c, _ := fixtureClient(t, fixtureProvider(), func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			if r.URL.Query().Get("name") != "_hakopod.example.com" || r.URL.Query().Get("type") != "TXT" {
				t.Errorf("unexpected record query: %v", r.URL.RawQuery)
			}
			ok(t, w, []wireRecord{})
			return
		}
		if r.Method != http.MethodPost || r.URL.Path != "/client/v4/zones/zone-root/dns_records" {
			t.Errorf("unexpected create request: %s %s", r.Method, r.URL.Path)
		}
		data, _ := io.ReadAll(io.LimitReader(r.Body, 1<<16))
		if json.Unmarshal(data, &created) != nil {
			t.Error("create did not send a JSON record")
		}
		ok(t, w, created)
	})
	zone := Zone{ID: "zone-root", Name: "example.com"}
	record := Record{Type: "TXT", Name: "_hakopod.example.com", Value: "hakopod-ownership=abc", TTL: 300}
	if err := c.Create(context.Background(), zone, record); err != nil {
		t.Fatalf("create failed: %v", err)
	}
	if created.Type != "TXT" || created.Name != record.Name || created.Content != record.Value || created.TTL != 300 || created.Proxied {
		t.Fatalf("create sent the wrong record: %+v", created)
	}
}

func TestCreateReturnsErrExistsOnConflict(t *testing.T) {
	existing := wireRecord{ID: "record-1", Type: "CNAME", Name: "app.example.com", Content: "ingress.hakopod.test", TTL: 300}
	c, _ := fixtureClient(t, fixtureProvider(), func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Error("create must not write over an existing record")
		}
		ok(t, w, []wireRecord{existing})
	})
	zone := Zone{ID: "zone-root", Name: "example.com"}
	record := Record{Type: "CNAME", Name: "app.example.com", Value: "other.hakopod.test", TTL: 300}
	if err := c.Create(context.Background(), zone, record); !errors.Is(err, ErrExists) {
		t.Fatalf("a conflicting record must report ErrExists, got %v", err)
	}

	// The provider's own duplicate code is reported the same way.
	conflicting, _ := fixtureClient(t, fixtureProvider(), func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			ok(t, w, []wireRecord{})
			return
		}
		w.WriteHeader(http.StatusBadRequest)
		io.WriteString(w, `{"success":false,"errors":[{"code":81057,"message":"Record already exists."}],"result":null}`)
	})
	if err := conflicting.Create(context.Background(), zone, record); !errors.Is(err, ErrExists) {
		t.Fatalf("the provider's duplicate code must report ErrExists, got %v", err)
	}
}

func TestReplaceUpdatesOnlyTheRecordTheCallerRead(t *testing.T) {
	existing := wireRecord{ID: "record-1", Type: "CNAME", Name: "app.example.com", Content: "old.hakopod.test", TTL: 300}
	var path string
	c, _ := fixtureClient(t, fixtureProvider(), func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			ok(t, w, []wireRecord{existing})
			return
		}
		path = r.URL.Path
		ok(t, w, existing)
	})
	zone := Zone{ID: "zone-root", Name: "example.com"}
	old := Record{Type: "CNAME", Name: "app.example.com", Value: "old.hakopod.test", TTL: 300}
	next := Record{Type: "CNAME", Name: "app.example.com", Value: "new.hakopod.test", TTL: 300}
	if err := c.Replace(context.Background(), zone, old, next); err != nil {
		t.Fatalf("replace failed: %v", err)
	}
	if path != "/client/v4/zones/zone-root/dns_records/record-1" {
		t.Fatalf("replace addressed the wrong record: %s", path)
	}
	stale := Record{Type: "CNAME", Name: "app.example.com", Value: "stale.hakopod.test", TTL: 300}
	if err := c.Replace(context.Background(), zone, stale, next); !errors.Is(err, ErrInput) {
		t.Fatalf("replacing a record that is no longer there must be refused, got %v", err)
	}
}

func TestProviderErrorDoesNotLeakItsBody(t *testing.T) {
	const body = `{"success":false,"errors":[{"code":10000,"message":"Authentication error: token 0123456789abcdef is not valid for zone example.com"}],"result":null}`
	c, _ := fixtureClient(t, fixtureProvider(), func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		io.WriteString(w, body)
	})
	_, err := c.Zone(context.Background(), "app.example.com")
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("a provider failure must collapse to ErrUnavailable, got %v", err)
	}
	for _, leak := range []string{"0123456789abcdef", "Authentication error", "10000", "403", "Forbidden"} {
		if strings.Contains(err.Error(), leak) {
			t.Fatalf("the returned error leaked %q from the provider: %s", leak, err.Error())
		}
	}
	if err.Error() != ErrUnavailable.Error() {
		t.Fatalf("the returned error is not the opaque sentinel: %s", err.Error())
	}
}

func TestProviderAndCredentialValidation(t *testing.T) {
	valid := fixtureProvider()
	if err := valid.Validate(); err != nil {
		t.Fatalf("the fixture provider must validate: %v", err)
	}
	cases := map[string]func(p *Provider){
		"empty name":           func(p *Provider) { p.Name = "" },
		"unknown kind":         func(p *Provider) { p.Kind = "route53" },
		"half a scope":         func(p *Provider) { p.Project = "shop" },
		"no permitted zones":   func(p *Provider) { p.ZoneFilter = nil },
		"duplicate zone":       func(p *Provider) { p.ZoneFilter = []string{"example.com", "example.com"} },
		"wildcard zone":        func(p *Provider) { p.ZoneFilter = []string{"*.example.com"} },
		"uppercase zone":       func(p *Provider) { p.ZoneFilter = []string{"Example.com"} },
		"zone with a port":     func(p *Provider) { p.ZoneFilter = []string{"example.com:443"} },
		"negative revision":    func(p *Provider) { p.Revision = -1 },
		"name with a new line": func(p *Provider) { p.Name = "company\ncloudflare" },
	}
	for name, mutate := range cases {
		p := fixtureProvider()
		mutate(&p)
		if err := p.Validate(); !errors.Is(err, ErrInput) {
			t.Errorf("%s must be refused, got %v", name, err)
		}
	}

	for name, token := range map[string]string{"empty": "", "NUL": "abc\x00def", "carriage return": "abc\rdef", "new line": "abc\ndef", "too long": strings.Repeat("a", 8193)} {
		if err := (Credentials{Token: token}).Validate(); !errors.Is(err, ErrInput) {
			t.Errorf("a token with %s must be refused, got %v", name, err)
		}
	}
	if err := (Credentials{Token: "fixture-token"}).Validate(); err != nil {
		t.Errorf("a plain token must validate: %v", err)
	}
}

func TestScopeAndCredentialTransfer(t *testing.T) {
	wide := fixtureProvider()
	if !wide.Allows("shop", "production") || !wide.Allows("blog", "staging") {
		t.Error("an unscoped provider is installation-wide")
	}
	disabled := wide
	disabled.Enabled = false
	if disabled.Allows("shop", "production") {
		t.Error("a disabled provider must not be used")
	}
	scoped := wide
	scoped.Project, scoped.Environment = "shop", "production"
	if !scoped.Allows("shop", "production") || scoped.Allows("shop", "staging") || scoped.Allows("blog", "production") {
		t.Error("a scoped provider must match both project and environment")
	}
	if !wide.AllowsHostname("api.staging.example.com") || !wide.AllowsHostname("example.com") || wide.AllowsHostname("notexample.com") || wide.AllowsHostname("example.com.attacker.test") {
		t.Error("the zone filter must match a zone and its subdomains only")
	}
	changed := wide
	changed.ZoneFilter = []string{"example.com", "example.net"}
	if wide.CredentialsTransferable(changed) {
		t.Error("a widened zone filter is not the same source")
	}
	changed = wide
	changed.Kind = "route53"
	if wide.CredentialsTransferable(changed) {
		t.Error("a changed kind is not the same source")
	}
	same := fixtureProvider()
	same.Revision, same.ID = 9, "dns-2"
	if !wide.CredentialsTransferable(same) {
		t.Error("a revision or identifier change is still the same source")
	}
}

func TestSealAndOpenCredentials(t *testing.T) {
	key := bytes.Repeat([]byte{7}, 32)
	p := fixtureProvider()
	sealed, err := SealCredentials(key, p, Credentials{Token: "fixture-token"})
	if err != nil {
		t.Fatalf("seal failed: %v", err)
	}
	if bytes.Contains(sealed, []byte("fixture-token")) {
		t.Fatal("the sealed credential contains the token in the clear")
	}
	p.EncryptedCredentials = sealed
	opened, err := OpenCredentials(key, p)
	if err != nil || opened.Token != "fixture-token" {
		t.Fatalf("round trip failed: %+v %v", opened, err)
	}
	if _, err = OpenCredentials(bytes.Repeat([]byte{8}, 32), p); !errors.Is(err, errEncryption) {
		t.Fatalf("a wrong key must fail: %v", err)
	}
	for _, short := range [][]byte{nil, bytes.Repeat([]byte{7}, 16), bytes.Repeat([]byte{7}, 31), bytes.Repeat([]byte{7}, 33)} {
		if _, err = SealCredentials(short, p, Credentials{Token: "fixture-token"}); !errors.Is(err, errEncryption) {
			t.Errorf("a %d byte key must be refused: %v", len(short), err)
		}
	}
	// The additional data names the provider, so a sealed credential does not
	// open against a changed kind or name.
	moved := p
	moved.Name = "other"
	if _, err = OpenCredentials(key, moved); !errors.Is(err, errEncryption) {
		t.Fatalf("a renamed provider must not open a stored credential: %v", err)
	}
}

func TestNewCloudflareValidatesAndPinsTheEndpoint(t *testing.T) {
	client, err := NewCloudflare(fixtureProvider(), Credentials{Token: "fixture-token"})
	if err != nil {
		t.Fatalf("a valid provider must build a client: %v", err)
	}
	if c := client.(*cloudflare); c.endpoint != "https://api.cloudflare.com" {
		t.Fatalf("the endpoint must be pinned, got %q", c.endpoint)
	}
	if _, err = NewCloudflare(fixtureProvider(), Credentials{Token: "bad\ntoken"}); !errors.Is(err, ErrInput) {
		t.Fatalf("a token with a new line must be refused: %v", err)
	}
	disabled := fixtureProvider()
	disabled.Enabled = false
	if _, err = NewCloudflare(disabled, Credentials{Token: "fixture-token"}); !errors.Is(err, ErrInput) {
		t.Fatalf("a disabled provider must be refused: %v", err)
	}
}

func TestProxiedRecordsAreRefused(t *testing.T) {
	c, requests := fixtureClient(t, fixtureProvider(), func(w http.ResponseWriter, r *http.Request) { ok(t, w, []wireRecord{}) })
	zone := Zone{ID: "zone-root", Name: "example.com"}
	record := Record{Type: "CNAME", Name: "app.example.com", Value: "ingress.hakopod.test", TTL: 300, Proxied: true}
	if err := c.Create(context.Background(), zone, record); !errors.Is(err, ErrInput) || requests.Load() != 0 {
		t.Fatalf("a proxied record must be refused without a request: %v, %d requests", err, requests.Load())
	}
}
