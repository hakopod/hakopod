package license

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestSignedInstallationEntitlements(t *testing.T) {
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	v := NewVerifier(map[string]ed25519.PublicKey{"fixture": public})
	now := time.Now().UTC().Truncate(time.Second)
	claims := Claims{Version: 1, KeyID: "fixture", LicenseID: strings.Repeat("a", 32), InstallationID: strings.Repeat("b", 32), Customer: "Ephemeral test customer", Plan: "pro", Sequence: 1, IssuedAt: now.Add(-time.Minute).Unix(), NotBefore: now.Add(-time.Minute).Unix(), ExpiresAt: now.Add(time.Hour).Unix(), Features: []string{"teams", "invitations", "project_rbac"}}
	sign := func(c Claims) string {
		payload, _ := json.Marshal(c)
		signature := ed25519.Sign(private, append([]byte(Domain), payload...))
		return "hl1." + base64.RawURLEncoding.EncodeToString(payload) + "." + base64.RawURLEncoding.EncodeToString(signature)
	}
	token := sign(claims)
	if _, err = v.Verify(token, claims.InstallationID, now); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name, installation string
		at                 time.Time
		claim              Claims
		state              string
	}{
		{"wrong installation", strings.Repeat("c", 32), now, claims, "wrong_installation"},
		{"expired", claims.InstallationID, time.Unix(claims.ExpiresAt, 0), claims, "expired"},
		{"future", claims.InstallationID, time.Unix(claims.NotBefore-1, 0), claims, "not_yet_valid"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := v.Verify(sign(test.claim), test.installation, test.at); State(err) != test.state {
				t.Fatalf("state=%s error=%v", State(err), err)
			}
		})
	}
	parts := strings.Split(token, ".")
	payload, _ := base64.RawURLEncoding.DecodeString(parts[1])
	payload = []byte(strings.Replace(string(payload), "Ephemeral test customer", "Tampered customer", 1))
	parts[1] = base64.RawURLEncoding.EncodeToString(payload)
	if _, err = v.Verify(strings.Join(parts, "."), claims.InstallationID, now); State(err) != "invalid_signature" {
		t.Fatal("tampered payload accepted")
	}
	for _, featureSet := range [][]string{{"unknown"}, {"teams", "teams"}} {
		changed := claims
		changed.Features = featureSet
		if _, err = v.Verify(sign(changed), claims.InstallationID, now); err == nil {
			t.Fatal("invalid entitlement feature set accepted")
		}
	}
	changed := claims
	changed.Plan = "free"
	if _, err = v.Verify(sign(changed), claims.InstallationID, now); err == nil {
		t.Fatal("Free plan granted Pro features")
	}
	changed.Features = nil
	if c, err := v.Verify(sign(changed), claims.InstallationID, now); err != nil || c.Plan != "free" {
		t.Fatal("signed downgrade rejected")
	}
	if _, err = NewVerifier(nil).Verify(token, claims.InstallationID, now); State(err) != "issuer_not_configured" {
		t.Fatal("unconfigured issuer accepted paid entitlements")
	}
}
