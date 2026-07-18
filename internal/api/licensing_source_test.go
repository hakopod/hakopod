package api

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"strings"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/license"
	"github.com/hakopod/hakopod/internal/store"
)

func sourcePaidFixture(t *testing.T, db *store.Store) {
	t.Helper()
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	db.LicenseVerifier = license.NewVerifier(map[string]ed25519.PublicKey{"ephemeral-source-fixture": public})
	status, err := db.LicenseStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().Add(-time.Minute).Unix()
	claims := license.Claims{Version: 1, KeyID: "ephemeral-source-fixture", LicenseID: strings.Repeat("a", 32), InstallationID: status.InstallationID, Customer: "Local source test fixture", Plan: "pro", Sequence: 1, IssuedAt: now, NotBefore: now, ExpiresAt: now + 7200, Features: []string{"teams", "invitations", "project_rbac"}}
	payload := store.JSON(claims)
	signature := ed25519.Sign(private, append([]byte(license.Domain), payload...))
	token := "hl1." + base64.RawURLEncoding.EncodeToString(payload) + "." + base64.RawURLEncoding.EncodeToString(signature)
	digest := sha256.Sum256([]byte(token))
	if _, err = db.Pool.Exec(context.Background(), "UPDATE installation_license SET token=$1,token_digest=$2,highest_sequence=1,revision=1 WHERE singleton", token, digest[:]); err != nil {
		t.Fatal(err)
	}
}
