package api_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/license"
	"github.com/hakopod/hakopod/internal/store"
)

// Tests generate private keys only in memory. No test key is trusted by release
// binaries and no signer/production secret is stored in the public repository.
func testSignedLicense(key ed25519.PrivateKey, claims license.Claims) string {
	payload, _ := json.Marshal(claims)
	signature := ed25519.Sign(key, append([]byte(license.Domain), payload...))
	return "hl1." + base64.RawURLEncoding.EncodeToString(payload) + "." + base64.RawURLEncoding.EncodeToString(signature)
}
func testLicenseClaims(installation string, sequence int64) license.Claims {
	now := time.Now().UTC().Add(-time.Minute).Unix()
	return license.Claims{Version: 1, KeyID: "ephemeral-fixture", LicenseID: strings.Repeat("a", 32), InstallationID: installation, Customer: "Local test fixture", Plan: "pro", Sequence: sequence, IssuedAt: now, NotBefore: now, ExpiresAt: now + 7200, Features: []string{"teams", "invitations", "project_rbac"}}
}
func testProLicense(t *testing.T, db *store.Store) ed25519.PrivateKey {
	t.Helper()
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	db.LicenseVerifier = license.NewVerifier(map[string]ed25519.PublicKey{"ephemeral-fixture": public})
	status, err := db.LicenseStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	token := testSignedLicense(private, testLicenseClaims(status.InstallationID, 1))
	digest := sha256.Sum256([]byte(token))
	if _, err = db.Pool.Exec(context.Background(), "UPDATE installation_license SET token=$1,token_digest=$2,highest_sequence=1,revision=1 WHERE singleton", token, digest[:]); err != nil {
		t.Fatal(err)
	}
	return private
}
