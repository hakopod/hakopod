package logmask

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"strings"
	"testing"
)

func TestCredentialFormatsAndUsefulDiagnostics(t *testing.T) {
	known := "private-value.with+symbols"
	mask := New(known, `{"access_key":"fixture-access","secret_key":"fixture-private"}`)
	for _, line := range []string{
		"plain " + known,
		"encoded " + base64.StdEncoding.EncodeToString([]byte(known)),
		`{"password":"has spaces","token":"escaped\\value","count":2}`,
		"curl --password 'quoted password' --token=private-token",
		"Authorization: Bearer example-private-value",
		"Cookie: session=first-private; csrf=second-private",
		"postgres://user:database-password@db.example.test/database",
		"redis://:database-password@db.example.test/0",
		"https://storage.example.test/archive?X-Amz-Signature=private-signature&sig=another-signature",
		"github_pat_synthetic_private_token ghp_synthetic_private_token glrt-synthetic-private-token",
		"fixture-access fixture-private",
	} {
		got := mask.Line(line)
		for _, value := range []string{known, "has spaces", "escaped", "quoted password", "private-token", "example-private-value", "first-private", "second-private", "database-password", "private-signature", "another-signature", "synthetic_private_token", "synthetic-private-token", "fixture-access", "fixture-private"} {
			if strings.Contains(got, value) {
				t.Fatalf("credential remained in redacted output for format %q", line[:min(len(line), 20)])
			}
		}
	}
	ordinary := "password authentication failed; retrying build 42"
	if mask.Line(ordinary) != ordinary {
		t.Fatal("redaction removed useful diagnostics without a credential value")
	}
	if !json.Valid([]byte(mask.Line(`{"password":"private value","count":2}`))) {
		t.Fatal("structured log redaction broke JSON")
	}
}

func TestDynamicMasksPrivateKeysAndScopedState(t *testing.T) {
	mask := New()
	command := mask.Line("2026-09-30T00:00:00Z ::add-mask::first%0Asecond%25part")
	if strings.Contains(command, "first") || strings.Contains(command, "second") {
		t.Fatal("mask registration echoed its value")
	}
	if got := mask.Line("first second%part"); strings.Contains(got, "first") || strings.Contains(got, "second") {
		t.Fatal("decoded multiline mask was not applied")
	}
	for _, line := range []string{"-----BEGIN PRIVATE KEY-----", "private-key-body", "-----END PRIVATE KEY-----"} {
		if mask.Line(line) != Replacement {
			t.Fatal("private key record escaped masking")
		}
	}
	if mask.Line("ordinary") != "ordinary" || New().Line("first") != "first" {
		t.Fatal("mask state escaped its stream or outlived a key block")
	}
	if got := New("secret-value").Line("secret-\x1b[31mvalue"); strings.Contains(got, "secret-") {
		t.Fatal("ANSI split bypassed known-value masking")
	}
}

type pieces struct {
	text  string
	width int
}

func (p *pieces) Read(out []byte) (int, error) {
	if p.text == "" {
		return 0, io.EOF
	}
	n := min(len(out), p.width, len(p.text))
	copy(out, p.text[:n])
	p.text = p.text[n:]
	return n, nil
}

func TestStreamingReadBoundariesAndTruncatedSecretPrefixes(t *testing.T) {
	input := "::add-mask::dynamic-private-value\nvalue=dynamic-private-value\nAuthorization: Bearer runtime-private-value\npassword=another-private-value\nfinal-private-prefix"
	for width := 1; width <= len(input); width++ {
		out, err := io.ReadAll(New().Reader(&pieces{text: input, width: width}))
		if err != nil {
			t.Fatal(err)
		}
		for _, value := range []string{"dynamic-private-value", "runtime-private-value", "another-private-value", "final-private-prefix"} {
			if strings.Contains(string(out), value) {
				t.Fatalf("secret escaped across read width %d", width)
			}
		}
		if !strings.Contains(string(out), partial) {
			t.Fatal("truncated last record was emitted")
		}
	}
}

// A pod with a few database links holds hundreds of secret values, each
// registered in several encodings; the old 512-mask bound withheld all its logs.
func TestManySecretsStillMask(t *testing.T) {
	var secrets []string
	for i := range 400 {
		secrets = append(secrets, fmt.Sprintf("credential-%03d-%s", i, strings.Repeat("k", 24)))
	}
	mask := New(secrets...)
	if mask.Failed() {
		t.Fatal("redaction gave up on an ordinary set of secrets")
	}
	if got := mask.Line("connected with " + secrets[399]); got != "connected with "+Replacement {
		t.Fatalf("got %q", got)
	}
}

func TestMaskAndLineBoundsFailClosed(t *testing.T) {
	mask := New(strings.Repeat("x", MaxLineBytes+1))
	if mask.Line("must not be emitted") != withheld {
		t.Fatal("oversized secret disabled masking")
	}
	input := strings.Repeat("a", MaxLineBytes+1) + "::add-mask::unknown\nunknown\n"
	data, err := io.ReadAll(New().Reader(strings.NewReader(input)))
	if err != nil || strings.Contains(string(data), "unknown") {
		t.Fatal("oversized command leaked into a following line", err)
	}
}

func TestCredentialPartsUseTheSameEncodings(t *testing.T) {
	value := `private/part+with "quotes"`
	envelope, _ := json.Marshal(map[string]string{"secret_key": value})
	mask := New(string(envelope))
	encoded, _ := json.Marshal(value)
	for _, form := range []string{value, url.QueryEscape(value), url.PathEscape(value), base64.StdEncoding.EncodeToString([]byte(value)), base64.RawURLEncoding.EncodeToString([]byte(value)), string(encoded[1 : len(encoded)-1])} {
		if mask.Line(form) != Replacement {
			t.Fatal("a credential envelope component escaped masking")
		}
	}
}

func TestTimestampedReaderRejectsUncertainRecordBoundaries(t *testing.T) {
	input := "truncated-private-suffix\n2026-09-30T00:00:00Z safe diagnostic\n2026-09-30T00:00:01Z known-private-value\n2026-09-30T00:00:02Z partial-private"
	for width := 1; width <= len(input); width++ {
		data, err := io.ReadAll(New("known-private-value").TimestampedReader(&pieces{text: input, width: width}))
		if err != nil || strings.Count(string(data), partial) != 2 || !strings.Contains(string(data), "safe diagnostic") {
			t.Fatal("timestamped stream mishandled its record boundaries", err)
		}
		for _, value := range []string{"truncated-private-suffix", "known-private-value", "partial-private"} {
			if strings.Contains(string(data), value) {
				t.Fatal("timestamped stream exposed an uncertain or unmasked record")
			}
		}
	}
}

func TestClientControlCleanupCannotReconstructSecrets(t *testing.T) {
	const secret = "fixture-private-value"
	for control := byte(0); control <= 127; control++ {
		if control == '\t' || control == '\n' || control >= 32 && control != 127 {
			continue
		}
		input := "fixture-" + string(control) + "private-value"
		got := New(secret).Line(input)
		cleaned := strings.ReplaceAll(controls.ReplaceAllString(got, ""), "\r", "")
		if strings.Contains(cleaned, secret) || got != Replacement {
			t.Fatalf("client cleanup reconstructed a credential after control %d", control)
		}
	}
	section := "section_start:1:step_script\rBuild images"
	if New().Line(section) != section {
		t.Fatal("safe native section lost its carriage-return boundary")
	}
	if got := New("step_scriptBuild").Line(section); strings.Contains(strings.ReplaceAll(got, "\r", ""), "step_scriptBuild") {
		t.Fatal("a credential crossed a preserved section boundary")
	}
}

func TestPrivateKeyStateCanBeResumedByANewMasker(t *testing.T) {
	m := New()
	if m.InPrivateKey() || m.Line("-----BEGIN PRIVATE KEY-----") != Replacement || !m.InPrivateKey() {
		t.Fatal("begin line must open a block")
	}
	fresh := New()
	fresh.ResumePrivateKey()
	if got := fresh.Line("MIIBODYBODY"); got != Replacement {
		t.Fatalf("body leaked: %q", got)
	}
	if fresh.Line("-----END PRIVATE KEY-----"); fresh.InPrivateKey() {
		t.Fatal("end line must close the block")
	}
}

func TestAddedReturnsValuesLearnedFromMaskCommands(t *testing.T) {
	m := New()
	m.Line("::add-mask::hunter2-value")
	fresh := New()
	for _, v := range m.Added() {
		fresh.Add(v)
	}
	if got := fresh.Line("password was hunter2-value ok"); strings.Contains(got, "hunter2-value") {
		t.Fatalf("learned mask lost: %q", got)
	}
}
