package advisory

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

var testNow = time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)

func testFeed() map[string]any {
	return map[string]any{
		"format": 1, "revision": 100,
		"issued_at": testNow.Format(time.RFC3339), "expires_at": testNow.Add(14 * 24 * time.Hour).Format(time.RFC3339),
		"advisories": []any{map[string]any{
			"id": "HAKOPOD-1", "revision": 1, "source": "hakopod", "title": "t", "message": "m",
			"severity": "high", "url": "https://example.com/a", "published_at": testNow.Format(time.RFC3339), "action": "update",
			"targets": []any{map[string]any{"runtime": map[string]any{"runtime": "node",
				"ranges": []any{map[string]any{"introduced": "0", "fixed": "22.1.0"}}}}},
		}},
	}
}

func sign(t *testing.T, key ed25519.PrivateKey, domain string, feed map[string]any) []byte {
	t.Helper()
	payload, _ := json.Marshal(feed)
	out, _ := json.Marshal(SignedFeed{Payload: string(payload),
		Signature: base64.StdEncoding.EncodeToString(ed25519.Sign(key, append([]byte(domain), payload...)))})
	return out
}

func TestVerify(t *testing.T) {
	pub, key, _ := ed25519.GenerateKey(rand.Reader)
	_, other, _ := ed25519.GenerateKey(rand.Reader)
	adv := func(f map[string]any) map[string]any { return f["advisories"].([]any)[0].(map[string]any) }
	cases := []struct {
		name   string
		key    ed25519.PrivateKey
		domain string
		mutate func(map[string]any)
		minRev int64
		ok     bool
	}{
		{name: "valid", ok: true},
		{name: "expired is not an error", mutate: func(f map[string]any) {
			f["issued_at"] = testNow.Add(-30 * 24 * time.Hour).Format(time.RFC3339)
			f["expires_at"] = testNow.Add(-time.Hour).Format(time.RFC3339)
		}, ok: true},
		{name: "bad signature", key: other},
		{name: "wrong domain", domain: "other-domain\n"},
		{name: "revision rollback", minRev: 101},
		{name: "future issued_at", mutate: func(f map[string]any) { f["issued_at"] = testNow.Add(10 * time.Minute).Format(time.RFC3339) }},
		{name: "validity over 31 days", mutate: func(f map[string]any) { f["expires_at"] = testNow.Add(32 * 24 * time.Hour).Format(time.RFC3339) }},
		{name: "unknown field", mutate: func(f map[string]any) { adv(f)["command"] = "rm -rf /" }},
		{name: "update missing fix", mutate: func(f map[string]any) {
			adv(f)["targets"] = []any{map[string]any{"runtime": map[string]any{"runtime": "node", "ranges": []any{map[string]any{"introduced": "0"}}}}}
		}},
		{name: "aliases", mutate: func(f map[string]any) { adv(f)["aliases"] = []any{"CVE-2026-105482", "GHSA-x:y.z"} }, ok: true},
		{name: "empty alias", mutate: func(f map[string]any) { adv(f)["aliases"] = []any{""} }},
		{name: "alias bad char", mutate: func(f map[string]any) { adv(f)["aliases"] = []any{"CVE 1"} }},
		{name: "alias too long", mutate: func(f map[string]any) { adv(f)["aliases"] = []any{strings.Repeat("a", 65)} }},
		{name: "too many aliases", mutate: func(f map[string]any) {
			a := make([]any, 21)
			for i := range a {
				a[i] = "CVE-1"
			}
			adv(f)["aliases"] = a
		}},
		{name: "http url", mutate: func(f map[string]any) { adv(f)["url"] = "http://example.com" }},
		{name: "bad digest", mutate: func(f map[string]any) {
			adv(f)["targets"] = []any{map[string]any{"image": map[string]any{"repository": "docker.io/library/node", "fixed_digests": []any{"sha256:abc"}}}}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := testFeed()
			if tc.mutate != nil {
				tc.mutate(f)
			}
			k, d := key, SignatureDomain
			if tc.key != nil {
				k = tc.key
			}
			if tc.domain != "" {
				d = tc.domain
			}
			_, err := Verify(sign(t, k, d, f), pub, testNow, tc.minRev)
			if (err == nil) != tc.ok {
				t.Fatalf("ok=%v err=%v", tc.ok, err)
			}
		})
	}
}

func TestVerifyEmbeddedKeyParses(t *testing.T) {
	if len(PublicKey()) != ed25519.PublicKeySize {
		t.Fatal("bad embedded key")
	}
}

func TestFetch(t *testing.T) {
	cases := []struct {
		name    string
		handler http.HandlerFunc
		etag    string
		check   func(*testing.T, FetchResult, error)
	}{
		{"304", func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("If-None-Match") != `"v1"` || r.Header.Get("User-Agent") != "hakopod-advisories" {
				w.WriteHeader(400)
				return
			}
			w.WriteHeader(http.StatusNotModified)
		}, `"v1"`, func(t *testing.T, r FetchResult, err error) {
			if err != nil || !r.NotModified || r.ETag != `"v1"` {
				t.Fatalf("%+v %v", r, err)
			}
		}},
		{"200", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("ETag", `"v2"`)
			w.Write([]byte(`{"payload":"x"}`))
		}, "", func(t *testing.T, r FetchResult, err error) {
			if err != nil || r.NotModified || r.ETag != `"v2"` || string(r.Body) != `{"payload":"x"}` {
				t.Fatalf("%+v %v", r, err)
			}
		}},
		{"oversize", func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte(strings.Repeat("a", maxFeedBytes+1)))
		}, "", func(t *testing.T, r FetchResult, err error) {
			if err == nil {
				t.Fatal("expected oversize error")
			}
		}},
		{"retry-after", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Retry-After", "120")
			w.WriteHeader(http.StatusTooManyRequests)
		}, "", func(t *testing.T, r FetchResult, err error) {
			if err == nil || !strings.Contains(err.Error(), "429") || r.RetryAfter != 2*time.Minute {
				t.Fatalf("%+v %v", r, err)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(tc.handler)
			defer srv.Close()
			r, err := Fetch(context.Background(), srv.Client(), srv.URL, tc.etag)
			tc.check(t, r, err)
		})
	}
}

func TestBackoff(t *testing.T) {
	for _, tc := range []struct {
		failures int
		retry    time.Duration
		want     time.Duration
	}{
		{0, 0, time.Minute}, {3, 0, 8 * time.Minute}, {20, 0, 6 * time.Hour}, {1, time.Hour, time.Hour},
	} {
		if got := Backoff(tc.failures, tc.retry); got != tc.want {
			t.Errorf("Backoff(%d,%v)=%v want %v", tc.failures, tc.retry, got, tc.want)
		}
	}
}
