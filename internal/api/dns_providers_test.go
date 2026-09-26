package api

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hakopod/hakopod/internal/dnsprovider"
	"github.com/hakopod/hakopod/internal/spec"
	"github.com/hakopod/hakopod/internal/store"
)

// fakeProviderText is what a real provider happily says back, token included.
// internal/dnsprovider never lets it into an error, but the fake does on
// purpose, so the tests below prove the API layer prints fixed prose rather than
// whatever it was handed.
const fakeProviderText = "Invalid API Token cf-token-SECRET-9f3"

type fakeDNSClient struct {
	present  map[string][]dnsprovider.Record // keyed by record type and name
	failOn   map[string]bool                 // record names the provider refuses
	created  []dnsprovider.Record
	replaced []dnsprovider.Record
}

func fakeKey(kind, name string) string { return kind + " " + name }

func (f *fakeDNSClient) refuse(name string) error {
	if f.failOn[name] {
		return fmt.Errorf("%w: %s", dnsprovider.ErrUnavailable, fakeProviderText)
	}
	return nil
}

func (f *fakeDNSClient) Zone(_ context.Context, hostname string) (dnsprovider.Zone, error) {
	if !strings.HasSuffix(hostname, "example.test") {
		return dnsprovider.Zone{}, fmt.Errorf("%w: no zone matches", dnsprovider.ErrInput)
	}
	return dnsprovider.Zone{ID: "zone-1", Name: "example.test"}, nil
}

func (f *fakeDNSClient) Records(_ context.Context, _ dnsprovider.Zone, name, kind string) ([]dnsprovider.Record, error) {
	if err := f.refuse(name); err != nil {
		return nil, err
	}
	return f.present[fakeKey(kind, name)], nil
}

func (f *fakeDNSClient) Create(_ context.Context, _ dnsprovider.Zone, r dnsprovider.Record) error {
	if err := f.refuse(r.Name); err != nil {
		return err
	}
	f.created = append(f.created, r)
	f.present[fakeKey(r.Type, r.Name)] = []dnsprovider.Record{r}
	return nil
}

func (f *fakeDNSClient) Replace(_ context.Context, _ dnsprovider.Zone, existing, r dnsprovider.Record) error {
	if err := f.refuse(r.Name); err != nil {
		return err
	}
	if existing.Name != r.Name || existing.Type != r.Type {
		return fmt.Errorf("%w: replacement changed name or type", dnsprovider.ErrInput)
	}
	f.replaced = append(f.replaced, r)
	f.present[fakeKey(r.Type, r.Name)] = []dnsprovider.Record{r}
	return nil
}

func ownershipRecord(hostname, value string) dnsprovider.Record {
	return dnsprovider.Record{Type: "TXT", Name: "_hakopod." + hostname, Value: value, TTL: dnsRecordTTL}
}

func testDNSProvider() dnsprovider.Provider {
	return dnsprovider.Provider{ID: "provider-1", Name: "zone-writer", Kind: dnsprovider.KindCloudflare, ZoneFilter: []string{"example.test"}, Enabled: true, Revision: 1}
}

// One request over four hostnames with four different outcomes answers with all
// four, because no transaction spans a third party and a caller has to be able
// to tell what landed.
func TestDNSRecordsMixedBulk(t *testing.T) {
	desired := map[string][]dnsprovider.Record{
		"created.example.test":  {ownershipRecord("created.example.test", "token-created")},
		"exists.example.test":   {ownershipRecord("exists.example.test", "token-exists")},
		"conflict.example.test": {ownershipRecord("conflict.example.test", "token-conflict")},
		"failed.example.test":   {ownershipRecord("failed.example.test", "token-failed")},
	}
	client := &fakeDNSClient{
		present: map[string][]dnsprovider.Record{
			fakeKey("TXT", "_hakopod.exists.example.test"):   {ownershipRecord("exists.example.test", "token-exists")},
			fakeKey("TXT", "_hakopod.conflict.example.test"): {ownershipRecord("conflict.example.test", "someone-elses-value")},
		},
		failOn: map[string]bool{"_hakopod.failed.example.test": true},
	}
	hostnames := []string{"created.example.test", "exists.example.test", "conflict.example.test", "failed.example.test", "not-this-application.example.test"}
	results, err := createDNSRecords(context.Background(), client, testDNSProvider(), "demo", "development", desired, hostnames, false)
	if err != nil {
		t.Fatal("a per-hostname failure became a request failure", err)
	}
	// A nil error is what makes the handler answer 200; the end-to-end test below
	// asserts that status against the route itself.
	want := map[string]string{
		"created.example.test":              dnsRecordCreated,
		"exists.example.test":               dnsRecordExists,
		"conflict.example.test":             dnsRecordConflict,
		"failed.example.test":               dnsRecordFailed,
		"not-this-application.example.test": dnsRecordSkipped,
	}
	if len(results) != len(hostnames) {
		t.Fatalf("got %d results for %d hostnames", len(results), len(hostnames))
	}
	seen := map[string]bool{}
	for _, result := range results {
		if result.Status != want[result.Hostname] {
			t.Fatalf("%s: got %q want %q (%s)", result.Hostname, result.Status, want[result.Hostname], result.Message)
		}
		seen[result.Status] = true
	}
	if len(seen) != 5 {
		t.Fatal("expected five distinct statuses, got", seen)
	}
	if len(client.created) != 1 || client.created[0].Value != "token-created" {
		t.Fatal("wrong set of records created", client.created)
	}
	if len(client.replaced) != 0 {
		t.Fatal("a record was replaced without replace_existing", client.replaced)
	}
	// Nothing a provider said may reach a caller, including a token it quoted
	// back inside an authentication message.
	body, err := json.Marshal(map[string]any{"results": results})
	if err != nil {
		t.Fatal(err)
	}
	for _, leak := range []string{fakeProviderText, "cf-token-SECRET-9f3", "Invalid API Token"} {
		if bytes.Contains(body, []byte(leak)) {
			t.Fatalf("provider text reached the response: %s", body)
		}
	}
	if !bytes.Contains(body, []byte("The DNS provider did not accept this change")) {
		t.Fatalf("the failed hostname lost its fixed message: %s", body)
	}
}

// replace_existing is the only way a value nobody asked about is overwritten.
func TestDNSRecordsReplaceExisting(t *testing.T) {
	desired := map[string][]dnsprovider.Record{"conflict.example.test": {ownershipRecord("conflict.example.test", "token-conflict")}}
	client := &fakeDNSClient{present: map[string][]dnsprovider.Record{
		fakeKey("TXT", "_hakopod.conflict.example.test"): {ownershipRecord("conflict.example.test", "someone-elses-value")},
	}}
	results, err := createDNSRecords(context.Background(), client, testDNSProvider(), "demo", "development", desired, []string{"conflict.example.test"}, true)
	if err != nil || len(results) != 1 || results[0].Status != dnsRecordCreated {
		t.Fatal("replacement refused", err, results)
	}
	if len(client.replaced) != 1 || len(client.created) != 0 {
		t.Fatal("a replacement was written as a create", client.replaced, client.created)
	}
}

// A provider scoped to one project and environment is refused everywhere else,
// and a disabled provider is refused everywhere.
func TestDNSRecordsProviderScopeRefused(t *testing.T) {
	desired := map[string][]dnsprovider.Record{"app.example.test": {ownershipRecord("app.example.test", "token")}}
	client := &fakeDNSClient{present: map[string][]dnsprovider.Record{}}
	scoped := testDNSProvider()
	scoped.Project, scoped.Environment = "demo", "development"
	for _, c := range []struct {
		name                 string
		provider             dnsprovider.Provider
		project, environment string
	}{
		{"another project", scoped, "other", "development"},
		{"another environment", scoped, "demo", "staging"},
		{"disabled", func() dnsprovider.Provider { p := testDNSProvider(); p.Enabled = false; return p }(), "demo", "development"},
	} {
		results, err := createDNSRecords(context.Background(), client, c.provider, c.project, c.environment, desired, []string{"app.example.test"}, false)
		if !errors.Is(err, store.ErrForbidden) || results != nil {
			t.Fatalf("%s: provider used out of scope: %v %v", c.name, err, results)
		}
	}
	if len(client.created) != 0 {
		t.Fatal("a refused request still wrote a record", client.created)
	}
}

// The record endpoint is a consequential create, so it takes the same
// idempotency header as the others through the same helper.
func TestDNSRecordsIdempotencyHeaderRequired(t *testing.T) {
	for _, key := range []string{"", "short"} {
		w := httptest.NewRecorder()
		r := httptest.NewRequest("POST", "/api/v1/applications/a/domains/dns-records", nil)
		if key != "" {
			r.Header.Set("Idempotency-Key", key)
		}
		if _, ok := backupIdempotency(w, r); ok || w.Code != 400 {
			t.Fatalf("idempotency key %q accepted: %d", key, w.Code)
		}
	}
	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/api/v1/applications/a/domains/dns-records", nil)
	r.Header.Set("Idempotency-Key", "dns-records-review")
	if _, ok := backupIdempotency(w, r); !ok {
		t.Fatal("a valid idempotency key was refused")
	}
}

// End to end over the route itself: a mixed bulk answers 200, a hostname this
// application does not own is refused, and the missing idempotency header is
// refused before anything is written. Needs a database, so it is skipped
// wherever HAKOPOD_TEST_DATABASE_URL is unset.
func TestDNSRecordsEndpoint(t *testing.T) {
	db := sourceDatabase(t)
	ctx := context.Background()
	raw, err := db.Bootstrap(ctx, "dns-records-test")
	if err != nil {
		t.Fatal(err)
	}
	principal, err := db.Authenticate(ctx, raw)
	if err != nil {
		t.Fatal(err)
	}
	next, err := spec.Normalize(spec.Application{Name: "dns-records-test", Services: map[string]spec.Service{"web": {Image: "python:3.13-alpine", Public: true, Port: 8080}}})
	if err != nil {
		t.Fatal(err)
	}
	d, err := db.Accept(ctx, principal, "demo", "development", next, 0, "dns-records-start")
	if err != nil {
		t.Fatal(err)
	}
	a, err := db.Application(ctx, d.ApplicationID)
	if err != nil {
		t.Fatal(err)
	}
	tokens := map[string]string{}
	for _, host := range []string{"created.example.test", "exists.example.test"} {
		v, err := db.BeginDomainVerification(ctx, principal, a, host, "web")
		if err != nil {
			t.Fatal(err)
		}
		tokens[host] = v.Token
	}
	key := bytes.Repeat([]byte{7}, 32)
	provider := testDNSProvider()
	provider.ID = ""
	sealed, err := dnsprovider.SealCredentials(key, provider, dnsprovider.Credentials{Token: "cf-token-SECRET-9f3"})
	if err != nil {
		t.Fatal(err)
	}
	provider.EncryptedCredentials = sealed
	stored, err := db.PutDNSProvider(ctx, principal, provider, 0)
	if err != nil {
		t.Fatal(err)
	}
	client := &fakeDNSClient{present: map[string][]dnsprovider.Record{
		fakeKey("TXT", "_hakopod.exists.example.test"): {ownershipRecord("exists.example.test", tokens["exists.example.test"])},
	}}
	s := &Server{Store: db, Auth: AuthConfig{EncryptionKey: base64.StdEncoding.EncodeToString(key)}}
	s.dnsClient = func(dnsprovider.Provider, dnsprovider.Credentials) (dnsprovider.Client, error) { return client, nil }
	handler := s.Handler()
	call := func(idempotency string, body any) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/api/v1/applications/"+a.ID+"/domains/dns-records", bytes.NewReader(store.JSON(body)))
		r.Header.Set("Authorization", "Bearer "+raw)
		if idempotency != "" {
			r.Header.Set("Idempotency-Key", idempotency)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	request := map[string]any{"provider_id": stored.ID, "hostnames": []string{"created.example.test", "exists.example.test", "not-this-application.example.test"}, "replace_existing": false}
	if w := call("", request); w.Code != 400 {
		t.Fatal("a create without an idempotency key was accepted", w.Code, w.Body.String())
	}
	if len(client.created) != 0 {
		t.Fatal("a refused request still wrote a record", client.created)
	}
	w := call("dns-records-review", request)
	if w.Code != 200 {
		t.Fatal("a mixed bulk did not answer 200", w.Code, w.Body.String())
	}
	var out struct {
		Results []dnsRecordResult `json:"results"`
	}
	if err = json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"created.example.test": dnsRecordCreated, "exists.example.test": dnsRecordExists, "not-this-application.example.test": dnsRecordSkipped}
	if len(out.Results) != 3 {
		t.Fatal("wrong number of results", w.Body.String())
	}
	for _, result := range out.Results {
		if result.Status != want[result.Hostname] {
			t.Fatalf("%s: got %q want %q", result.Hostname, result.Status, want[result.Hostname])
		}
	}
	if bytes.Contains(w.Body.Bytes(), []byte("cf-token-SECRET-9f3")) {
		t.Fatal("the stored provider token reached the response")
	}
	// Creating records is not verifying them: nothing in this response may say a
	// domain is verified.
	proofs, err := db.DomainVerifications(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, proof := range proofs {
		if proof.VerifiedAt != nil {
			t.Fatal("writing a record marked a domain verified", proof.Hostname)
		}
	}
	// The picker lists the same provider for this application, with no zone
	// filter in it.
	list := httptest.NewRequest("GET", "/api/v1/applications/"+a.ID+"/domains/dns-providers", nil)
	list.Header.Set("Authorization", "Bearer "+raw)
	lw := httptest.NewRecorder()
	handler.ServeHTTP(lw, list)
	if lw.Code != 200 || !strings.Contains(lw.Body.String(), stored.ID) || strings.Contains(lw.Body.String(), "zone_filter") {
		t.Fatal("application provider list is wrong", lw.Code, lw.Body.String())
	}
}

// The management routes end to end: create, list, re-save without re-entering the
// token, and delete. Needs a database, so it is skipped wherever
// HAKOPOD_TEST_DATABASE_URL is unset.
func TestDNSProviderManagementRoutes(t *testing.T) {
	db := sourceDatabase(t)
	ctx := context.Background()
	raw, err := db.Bootstrap(ctx, "dns-provider-test")
	if err != nil {
		t.Fatal(err)
	}
	key := bytes.Repeat([]byte{9}, 32)
	s := &Server{Store: db, Auth: AuthConfig{EncryptionKey: base64.StdEncoding.EncodeToString(key)}}
	handler := s.Handler()
	call := func(method, path string, body any, want int) map[string]any {
		t.Helper()
		r := httptest.NewRequest(method, "/api/v1"+path, bytes.NewReader(store.JSON(body)))
		r.Header.Set("Authorization", "Bearer "+raw)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s %s: got %d want %d: %s", method, path, w.Code, want, w.Body.String())
		}
		if strings.Contains(w.Body.String(), "cf-token-SECRET-9f3") {
			t.Fatalf("%s %s returned the stored token: %s", method, path, w.Body.String())
		}
		var out map[string]any
		if len(w.Body.Bytes()) > 0 {
			_ = json.Unmarshal(w.Body.Bytes(), &out)
		}
		return out
	}
	created := call("PUT", "/dns-providers/zone-writer", map[string]any{
		"kind": "cloudflare", "zone_filter": []string{"example.test"}, "enabled": true,
		"expected_revision": 0, "credentials": map[string]string{"token": "cf-token-SECRET-9f3"},
	}, 201)
	if created["revision"] != float64(1) || created["id"] == "" {
		t.Fatal("create did not return the stored row", created)
	}
	// A missing token on an update keeps the stored one.
	updated := call("PUT", "/dns-providers/zone-writer", map[string]any{
		"kind": "cloudflare", "zone_filter": []string{"example.test"}, "enabled": false, "expected_revision": 1,
	}, 200)
	if updated["revision"] != float64(2) || updated["enabled"] != false {
		t.Fatal("update did not apply", updated)
	}
	// A stale expected revision is a conflict, not a silent overwrite.
	call("PUT", "/dns-providers/zone-writer", map[string]any{
		"kind": "cloudflare", "zone_filter": []string{"example.test"}, "enabled": true, "expected_revision": 1,
	}, 409)
	// A widened zone filter without a token is refused, because the filter is the
	// boundary this feature enforces.
	call("PUT", "/dns-providers/zone-writer", map[string]any{
		"kind": "cloudflare", "zone_filter": []string{"example.test", "other.test"}, "enabled": true, "expected_revision": 2,
	}, 400)
	listed := call("GET", "/dns-providers", nil, 200)
	items, ok := listed["items"].([]any)
	if !ok || len(items) != 1 {
		t.Fatal("administrator listing is wrong", listed)
	}
	if first := items[0].(map[string]any); first["zone_filter"] == nil || first["credentials"] != nil {
		t.Fatal("administrator listing lost the zone filter or carried credentials", first)
	}
	call("DELETE", "/dns-providers/zone-writer", map[string]any{"expected_revision": 2}, 200)
	if again := call("GET", "/dns-providers", nil, 200); len(again["items"].([]any)) != 0 {
		t.Fatal("delete left the row behind", again)
	}
}
