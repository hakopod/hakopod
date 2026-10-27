package logmask

import (
	"encoding/base64"
	"fmt"
	"math/rand"
	"net/url"
	"strings"
	"testing"
	"unicode"
)

var equivSecrets = []string{"hunter2-s3cret-value", "p@ss w0rd/+=", "tök€n-ünïcode-Ω"}

// equivCorpus builds deterministic lines covering every pattern the masker has.
func equivCorpus(n int) []string {
	r := rand.New(rand.NewSource(1))
	pick := func(xs ...string) string { return xs[r.Intn(len(xs))] }
	hex := func(k int) string {
		const a = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_"
		b := make([]byte, k)
		for i := range b {
			b[i] = a[r.Intn(len(a))]
		}
		return string(b)
	}
	secret := func() string {
		s := equivSecrets[r.Intn(len(equivSecrets))]
		switch r.Intn(5) {
		case 0:
			return base64.StdEncoding.EncodeToString([]byte(s))
		case 1:
			return url.QueryEscape(s)
		case 2:
			return base64.RawURLEncoding.EncodeToString([]byte(s))
		}
		return s
	}
	gens := []func() string{
		func() string {
			return fmt.Sprintf(`{"time":"2026-01-02T03:04:05Z","level":"info","method":"GET","path":"/api/v1/%s","status":%d,"ms":%d,"ua":"%s"}`, hex(8), 200+r.Intn(300), r.Intn(900), pick("curl/8", "Mozilla/5.0", "Go-http-client"))
		},
		func() string {
			return fmt.Sprintf(`{"path":"/x","%s":"%s","n":1}`, pick("password", "api_key", "Token", "msg"), secret())
		},
		func() string {
			return fmt.Sprintf("%s=%s %s=%s", pick("PASSWORD", "user", "secret_key", "x"), hex(10), pick("sig", "DB_TOKEN", "a"), secret())
		},
		func() string {
			return fmt.Sprintf("connect %s://user:%s@db.internal:5432/app", pick("postgres", "HTTPS", "amqp"), secret())
		},
		func() string {
			return pick("Authorization: ", "authorization:", "Proxy-Authorization : ", "Cookie: sid=", "X-Api-Key: ", "set-cookie: ") + hex(12)
		},
		func() string {
			return pick("Bearer", "bearer", "BEARER", "BeArEr", "Basic", "bAsIc", "Baſic", "BASIC") + " " + hex(16) + "." + hex(5)
		},
		func() string { return "using github_pat_" + hex(30) + " and ghp_" + hex(20) },
		func() string { return "glpat-" + hex(20) + " " + pick("glrt-", "glcbt-", "gldt-", "glptt-") + hex(12) },
		func() string { return pick("AKIA", "ASIA") + strings.ToUpper(hex(16)) + " key" },
		func() string { return "jwt eyJ" + hex(10) + ".eyJ" + hex(12) + "." + hex(14) },
		func() string { return "\x1b[31mred " + pick("token=", "x ") + hex(6) + "\x1b[0m \x1b]0;title\x07 end" },
		func() string { return "a\x00b\x07c\x0bd\x0e\x1f\x7fe tab\there " + hex(4) },
		func() string {
			return pick("section_start:1700000000:build[collapsed=true]\r\x1b[0K", "section_end:1700000000:build\r\x1b[0K", "2026-01-02T03:04:05.123Z section_start:12:x\r") + pick("Title ", "pass", "word=") + hex(3)
		},
		func() string { return "line with\rcarriage " + secret() + "\r" },
		func() string { return "::add-mask::" + pick("%0Dnew value", "dyn-"+hex(9), "two words here", "") },
		func() string {
			return "-----" + pick("BEGIN PRIVATE KEY", "BEGIN RSA PRIVATE KEY", "BEGIN EC PRIVATE KEY") + "-----"
		},
		func() string {
			return "-----" + pick("END PRIVATE KEY", "END RSA PRIVATE KEY", "END OPENSSH PRIVATE KEY") + "-----"
		},
		func() string { return "MIIEvQIBADANBgkqhkiG9w0BAQEFAASC" + hex(20) },
		func() string {
			return "héllo wörld 日本語 ünïcode 🚀 " + pick("pässword=1", "to\u212aen=abc", "x-api-\u212aey: v", "pa\u017f\u017fword=zz", "Ba\u017fic abc", "secret=ü")
		},
		func() string {
			return pick("--password ", "--TOKEN ", "--api-key ", "--client-secret ", "--other ", "-- ") + pick(hex(6), `"quoted val"`, `'single'`)
		},
		func() string { return "plain " + secret() + " and " + hex(8) + pick(":", "=", " @ ", " :// ") },
		func() string {
			return `key: "va\"l" 'x' set-cookie=abc; ` + pick("cookie", "COOKIE", "Cookie") + " = " + hex(5)
		},
		func() string { return "" },
	}
	out := make([]string, 0, n)
	for len(out) < n {
		out = append(out, gens[r.Intn(len(gens))]())
	}
	return out
}

// feed runs one sequence through both maskers and fails on any difference.
func feed(t testing.TB, lines []string) {
	t.Helper()
	nm, lm := New(equivSecrets...), legacyNew(equivSecrets...)
	for i, line := range lines {
		got, want := nm.Line(line), lm.Line(line)
		if got != want {
			t.Fatalf("line %d %q:\n new  %q\n old  %q", i, line, got, want)
		}
		if nm.InPrivateKey() != lm.privateKey || nm.failed != lm.failed || strings.Join(nm.Added(), "\x00") != strings.Join(lm.dynamic, "\x00") {
			t.Fatalf("line %d %q: state differs pk %v/%v failed %v/%v dyn %q/%q", i, line, nm.InPrivateKey(), lm.privateKey, nm.failed, lm.failed, nm.Added(), lm.dynamic)
		}
	}
}

func TestLineEquivalence(t *testing.T) {
	corpus := equivCorpus(20000)
	feed(t, corpus) // one long stateful sequence
	for i := 0; i+40 <= len(corpus); i += 40 {
		feed(t, corpus[i:i+40]) // many short ones, starting fresh
	}
	feed(t, []string{"x", strings.Repeat("a", MaxLineBytes+1), "password=hunter2-s3cret-value"})
}

// Case folding in (?i) patterns reaches beyond ASCII only through U+017F and U+212A.
func TestFoldAssumption(t *testing.T) {
	for r := 'A'; r <= 'z'; r++ {
		if !unicode.IsLetter(r) {
			continue
		}
		for f := unicode.SimpleFold(r); f != r; f = unicode.SimpleFold(f) {
			if f > 0x7f && f != 0x17f && f != 0x212a {
				t.Fatalf("%q folds to %q", r, f)
			}
		}
	}
}

func FuzzLineEquivalence(f *testing.F) {
	corpus := equivCorpus(400)
	for i := 0; i+3 <= len(corpus); i += 3 {
		f.Add(strings.Join(corpus[i:i+3], "\n"))
	}
	f.Fuzz(func(t *testing.T, input string) {
		feed(t, strings.Split(input, "\n"))
	})
}

const benchLine = `{"time":"2026-01-02T03:04:05.123Z","level":"info","msg":"request","method":"GET","path":"/api/v1/projects/p1/applications","status":200,"duration_ms":12,"bytes":4821,"remote":"10.1.2.3","request_id":"b1946ac92492d2347c6235b4d2611184"}`

func BenchmarkLine(b *testing.B) {
	secrets := []string{"hunter2-s3cret-value", "another-secret-value", "third/secret+value="}
	b.Logf("line bytes: %d", len(benchLine))
	b.Run("legacy", func(b *testing.B) {
		m := legacyNew(secrets...)
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			m.Line(benchLine)
		}
	})
	b.Run("new", func(b *testing.B) {
		m := New(secrets...)
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			m.Line(benchLine)
		}
	})
}
