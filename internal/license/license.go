// Package license verifies signed entitlements. It contains no signing keys or
// production license issuer; the issuer is maintained in a separate repository.
package license

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"strings"
	"time"
)

// ReleaseKeys contains the vendor's public verification keys, never signing
// keys. Keeping the default in source gives every release build the same trust
// anchor. A trusted distributor may override it with -ldflags -X using
// key-id=base64url-key[,next-key-id=base64url-key]. An empty trust set disables
// paid activation. Requests and environment variables cannot replace this set.
var ReleaseKeys = "hakopod-2026-09=CrQD0KiOhy_RZgzhyKoVhIHuDQ925whZzVx0VE7VrgE"

const Domain = "hakopod-license-v1\x00"
const MaxTokenBytes = 16 << 10

type Claims struct {
	Version        int      `json:"version"`
	KeyID          string   `json:"key_id"`
	LicenseID      string   `json:"license_id"`
	InstallationID string   `json:"installation_id"`
	Customer       string   `json:"customer"`
	Plan           string   `json:"plan"`
	Sequence       int64    `json:"sequence"`
	IssuedAt       int64    `json:"issued_at"`
	NotBefore      int64    `json:"not_before"`
	ExpiresAt      int64    `json:"expires_at"`
	Lifetime       bool     `json:"lifetime,omitempty"`
	Features       []string `json:"features"`
}

type Feature struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Plan        string `json:"plan"`
	Description string `json:"description"`
	Enabled     bool   `json:"enabled"`
}

var catalog = []Feature{
	{ID: "managed_actions", Name: "Managed Actions", Plan: "pro", Description: "Run repository-scoped GitHub Actions jobs in isolated, single-job runner pools."},
	{ID: "custom_roles", Name: "Custom project roles", Plan: "pro", Description: "Choose deployment and log permissions for people and teams."},
	{ID: "team_mfa", Name: "Organization MFA enforcement", Plan: "pro", Description: "Require verified multi-factor sessions for installation access."},
	{ID: "deployments", Name: "Application deployments", Plan: "free", Description: "Deploy, review, roll back and observe applications."},
	{ID: "source_builds", Name: "Source builds", Plan: "free", Description: "Reviewed remote builds and immutable image deployment."},
	{ID: "source_sync", Name: "Repository synchronization", Plan: "free", Description: "Approved repository configuration and signed source automation."},
	{ID: "runtime", Name: "Cluster and service operations", Plan: "free", Description: "Logs, metrics, nodes, registries, secrets, TLS and service workloads."},
	{ID: "account_security", Name: "Account security", Plan: "free", Description: "Owner recovery, password login, passkeys, TOTP and session revocation."},
	{ID: "teams", Name: "Teams", Plan: "free", Description: "One self-hosted team, membership and team-derived access."},
	{ID: "invitations", Name: "Member invitations", Plan: "free", Description: "Create and accept invitations for additional team or project members."},
	{ID: "project_rbac", Name: "Project roles", Plan: "free", Description: "Grant and use fixed administrator, developer and viewer roles for people and teams."},
	{ID: "multi_team", Name: "Multiple teams", Plan: "pro", Description: "Create additional teams on self-hosted installations."},
	{ID: "oauth_login", Name: "Social login", Plan: "pro", Description: "Sign in through Google, GitHub or GitLab."},
	{ID: "enterprise_sso", Name: "Enterprise SSO", Plan: "pro", Description: "Sign in through an OpenID Connect identity provider."},
	{ID: "audit_history", Name: "User audit history and export", Plan: "pro", Description: "Inspect user activity with paginated history and bounded CSV exports."},
}

// Preserve the original v1 entitlement names so installed licenses remain valid.
// Reserved advanced capabilities require explicit grants; legacy names never
// imply advanced authority. Unsupported capabilities are not shown in the catalog.
func knownEntitlement(id string) bool {
	switch id {
	case "teams", "invitations", "project_rbac", "custom_roles", "audit_history", "team_mfa", "multi_team", "oauth_login", "enterprise_sso", "managed_actions":
		return true
	}
	return false
}

func Catalog(enabled []string) []Feature {
	out := append([]Feature(nil), catalog...)
	for i := range out {
		out[i].Enabled = out[i].Plan == "free"
		for _, id := range enabled {
			out[i].Enabled = out[i].Enabled || id == out[i].ID
		}
	}
	return out
}

type ValidationError struct{ State string }

func (e *ValidationError) Error() string {
	return "license is " + strings.ReplaceAll(e.State, "_", " ")
}

type Verifier struct{ keys map[string]ed25519.PublicKey }

func NewVerifier(keys map[string]ed25519.PublicKey) *Verifier {
	v := &Verifier{keys: make(map[string]ed25519.PublicKey, len(keys))}
	for id, key := range keys {
		if len(key) == ed25519.PublicKeySize && keyIDPattern.MatchString(id) && len(v.keys) < 8 {
			v.keys[id] = append(ed25519.PublicKey(nil), key...)
		}
	}
	return v
}
func ReleaseVerifier() *Verifier {
	keys := map[string]ed25519.PublicKey{}
	for _, entry := range strings.Split(ReleaseKeys, ",") {
		id, encoded, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		key, err := base64.RawURLEncoding.DecodeString(encoded)
		if err == nil {
			keys[id] = key
		}
	}
	return NewVerifier(keys)
}
func (v *Verifier) Configured() bool { return v != nil && len(v.keys) > 0 }

var idPattern = regexp.MustCompile(`^[a-f0-9]{32}$`)
var keyIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

func (v *Verifier) Verify(token, installation string, now time.Time) (Claims, error) {
	invalid := func(state string) (Claims, error) { return Claims{}, &ValidationError{state} }
	if !v.Configured() {
		return invalid("issuer_not_configured")
	}
	if len(token) > MaxTokenBytes {
		return invalid("invalid")
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 || parts[0] != "hl1" {
		return invalid("invalid")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || len(payload) > 8<<10 {
		return invalid("invalid")
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || len(signature) != ed25519.SignatureSize {
		return invalid("invalid")
	}
	var c Claims
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&c) != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return invalid("invalid")
	}
	key, ok := v.keys[c.KeyID]
	if !ok || !ed25519.Verify(key, append([]byte(Domain), payload...), signature) {
		return invalid("invalid_signature")
	}
	if c.Version != 1 || !idPattern.MatchString(c.LicenseID) || !idPattern.MatchString(c.InstallationID) || c.Sequence < 1 || len(c.Customer) < 1 || len(c.Customer) > 200 || (c.Plan != "free" && c.Plan != "pro") || c.IssuedAt <= 0 || c.NotBefore < c.IssuedAt || len(c.Features) > 9 {
		return invalid("invalid")
	}
	// A missing or zero expiry must never turn a subscription into a lifetime
	// license. The issuer must explicitly sign a lifetime Pro grant.
	if c.Lifetime {
		if c.Plan != "pro" || c.ExpiresAt != 0 {
			return invalid("invalid")
		}
	} else if c.ExpiresAt <= c.NotBefore || c.ExpiresAt-c.IssuedAt > int64((10*366*24*time.Hour)/time.Second) {
		return invalid("invalid")
	}
	seen := map[string]bool{}
	for _, id := range c.Features {
		if c.Plan != "pro" || seen[id] || !knownEntitlement(id) {
			return invalid("invalid")
		}
		seen[id] = true
	}
	if c.InstallationID != installation {
		return invalid("wrong_installation")
	}
	if now.Unix() < c.NotBefore {
		return c, &ValidationError{"not_yet_valid"}
	}
	if !c.Lifetime && now.Unix() >= c.ExpiresAt {
		return c, &ValidationError{"expired"}
	}
	return c, nil
}

func State(err error) string {
	var validation *ValidationError
	if errors.As(err, &validation) {
		return validation.State
	}
	return "invalid"
}
