package migrate

import (
	"bytes"
	"testing"
)

func TestSealBindsToMigrationAndPurpose(t *testing.T) {
	key := bytes.Repeat([]byte{7}, 32)
	sealed, err := Seal(key, "m1", "token", []byte("rw_secret"))
	if err != nil || bytes.Contains(sealed, []byte("rw_secret")) {
		t.Fatal("sealed token must not contain the plaintext", err)
	}
	if out, err := Open(key, "m1", "token", sealed); err != nil || string(out) != "rw_secret" {
		t.Fatal("round trip failed", err)
	}
	for _, c := range []struct{ id, purpose string }{{"m2", "token"}, {"m1", "variables"}} {
		if _, err := Open(key, c.id, c.purpose, sealed); err == nil {
			t.Fatalf("opened a token sealed for m1/token as %s/%s", c.id, c.purpose)
		}
	}
	if _, err := Seal(key[:16], "m1", "token", nil); err == nil {
		t.Fatal("a short key must be refused")
	}
}
