package invocation

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"github.com/hakopod/hakopod/internal/spec"
	"strings"
	"testing"
)

func TestCipherBindsInvocationAndPurpose(t *testing.T) {
	key := bytes.Repeat([]byte{9}, 32)
	id := strings.Repeat("a", 32)
	plain := []byte("private-input")
	sealed, err := Seal(key, id, "input", plain)
	if err != nil {
		t.Fatal(err)
	}
	actual, err := Open(key, id, "input", sealed)
	if err != nil || !bytes.Equal(actual, plain) {
		t.Fatal("roundtrip failed", err)
	}
	for _, tc := range []struct {
		id, kind   string
		key, value []byte
	}{{strings.Repeat("b", 32), "input", key, sealed}, {id, "logs", key, sealed}, {id, "input", bytes.Repeat([]byte{8}, 32), sealed}, {id, "input", key, sealed[:len(sealed)-1]}} {
		if _, err := Open(tc.key, tc.id, tc.kind, tc.value); err == nil {
			t.Fatal("cipher accepted another context or corrupted value")
		}
	}
	if _, err := Seal(key, id, "logs", make([]byte, MaxLogBytes+1)); err == nil {
		t.Fatal("oversized log accepted")
	}
	if decoded, err := DecodeKey(base64.StdEncoding.EncodeToString(key)); err != nil || !bytes.Equal(decoded, key) {
		t.Fatal(err)
	}
	if _, err := DecodeKey("short"); err == nil {
		t.Fatal("short key accepted")
	}
}
func TestInputsRestrictKeysPreserveNumbersAndBoundSize(t *testing.T) {
	cfg := &spec.JobInvocation{InputKeys: []string{"payload"}, MaxInputBytes: 128}
	a, err := NormalizeInputs(cfg, map[string]json.RawMessage{"payload": json.RawMessage(`{"z":9007199254740993,"a":true}`)})
	if err != nil {
		t.Fatal(err)
	}
	if string(a) != `{"payload":{"a":true,"z":9007199254740993}}` {
		t.Fatalf("precision/order changed: %s", a)
	}
	for _, inputs := range []map[string]json.RawMessage{{"image": json.RawMessage(`"other"`)}, {"payload": json.RawMessage(`true false`)}, {"payload": json.RawMessage(`"` + strings.Repeat("x", 128) + `"`)}} {
		if _, err := NormalizeInputs(cfg, inputs); err == nil {
			t.Fatal("invalid input accepted")
		}
	}
}
func TestOwnerScopeIsOpaqueAndBounded(t *testing.T) {
	hash, err := OwnerHash("team:job-1")
	if err != nil || len(hash) != 64 || strings.Contains(hash, "team") {
		t.Fatal("invalid owner hash")
	}
	for _, owner := range []string{"", "secret\nvalue", strings.Repeat("a", 129)} {
		if _, err := OwnerHash(owner); err == nil {
			t.Fatal("invalid owner accepted")
		}
	}
	data, _ := json.Marshal(Record{EncryptedInput: []byte("secret"), OwnerHash: "private", LeaseToken: "lease"})
	if bytes.Contains(data, []byte("secret")) || bytes.Contains(data, []byte("private")) || bytes.Contains(data, []byte("lease")) {
		t.Fatal("private record metadata leaked")
	}
}
