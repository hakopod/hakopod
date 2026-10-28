// Command sign-advisories signs security-advisories.json into the published
// SignedFeed. The private key comes from HAKOPOD_ADVISORY_SIGNING_KEY.
package main

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/hakopod/hakopod/internal/advisory"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "sign-advisories:", err)
		os.Exit(1)
	}
}

func run() error {
	if len(os.Args) != 2 {
		return fmt.Errorf("usage: sign-advisories security-advisories.json")
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(os.Getenv("HAKOPOD_ADVISORY_SIGNING_KEY")))
	if err != nil || len(raw) != ed25519.PrivateKeySize {
		return fmt.Errorf("HAKOPOD_ADVISORY_SIGNING_KEY must be a base64 64-byte Ed25519 private key")
	}
	key := ed25519.PrivateKey(raw)
	if !key.Public().(ed25519.PublicKey).Equal(advisory.PublicKey()) {
		return fmt.Errorf("signing key does not match the embedded public key")
	}
	src, err := os.ReadFile(os.Args[1])
	if err != nil {
		return err
	}
	var in struct {
		Advisories []advisory.Advisory `json:"advisories"`
	}
	dec := json.NewDecoder(bytes.NewReader(src))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		return fmt.Errorf("decode %s: %w", os.Args[1], err)
	}
	if in.Advisories == nil {
		in.Advisories = []advisory.Advisory{}
	}
	now := time.Now().UTC().Truncate(time.Second)
	payload, err := json.Marshal(advisory.Feed{
		Format:     advisory.FeedFormat,
		Revision:   now.UnixMilli(),
		IssuedAt:   now,
		ExpiresAt:  now.Add(14 * 24 * time.Hour),
		Advisories: in.Advisories,
	})
	if err != nil {
		return err
	}
	out, err := json.MarshalIndent(advisory.SignedFeed{
		Payload:   string(payload),
		Signature: base64.StdEncoding.EncodeToString(ed25519.Sign(key, append([]byte(advisory.SignatureDomain), payload...))),
	}, "", "  ")
	if err != nil {
		return err
	}
	if _, err := advisory.Verify(out, advisory.PublicKey(), now, 0); err != nil {
		return fmt.Errorf("signed feed does not verify: %w", err)
	}
	_, err = os.Stdout.Write(append(out, '\n'))
	return err
}
