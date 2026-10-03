package managedplatform

import (
	"bytes"
	"crypto/ed25519"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"strings"
	"testing"

	"github.com/golang-jwt/jwt/v5"
)

func neonAuthPublicKey(t *testing.T, value []byte) ed25519.PublicKey {
	t.Helper()
	block, rest := pem.Decode(value)
	if block == nil || block.Type != "PUBLIC KEY" || len(rest) != 0 {
		t.Fatal("invalid public key")
	}
	key, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	public, ok := key.(ed25519.PublicKey)
	if !ok {
		t.Fatal("upstream requires Ed25519")
	}
	return public
}

func TestNeonComputeAuthenticationMatchesProviderAndPreservesUserConfiguration(t *testing.T) {
	key := bytes.Repeat([]byte{9}, 32)
	raw := json.RawMessage(`{"spec":{"format_version":1,"suspend_timeout_seconds":-1,"cluster":{"roles":[],"databases":[]}},"compute_ctl_config":{"jwks":{"keys":[]},"tls":{"key_path":"/tls/key","cert_path":"/tls/cert"}}}`)
	publicKeys := map[string]ed25519.PublicKey{}
	credentials := map[string][]byte{}
	for _, platform := range []string{strings.Repeat("a", 32), strings.Repeat("b", 32)} {
		bound, token, err := BindNeonComputeAuthentication(raw, key, platform)
		if err != nil {
			t.Fatal(err)
		}
		var config struct {
			Spec    json.RawMessage `json:"spec"`
			Control struct {
				JWKS struct {
					Keys []map[string]string `json:"keys"`
				} `json:"jwks"`
				TLS map[string]string `json:"tls"`
			} `json:"compute_ctl_config"`
		}
		if json.Unmarshal(bound, &config) != nil || len(config.Control.JWKS.Keys) != 1 || config.Control.TLS["key_path"] != "/tls/key" || config.Control.TLS["cert_path"] != "/tls/cert" {
			t.Fatal("compute control fields were lost")
		}
		var original map[string]json.RawMessage
		if json.Unmarshal(raw, &original) != nil || !bytes.Equal(config.Spec, original["spec"]) {
			t.Fatal("compute user specification changed")
		}
		jwk := config.Control.JWKS.Keys[0]
		if len(jwk) != 6 || jwk["kty"] != "OKP" || jwk["crv"] != "Ed25519" || jwk["alg"] != "EdDSA" || jwk["use"] != "sig" || jwk["kid"] != "hakopod-compute-v1" {
			t.Fatal("compute verification key differs from pinned provider contract")
		}
		public, err := base64.RawURLEncoding.DecodeString(jwk["x"])
		if err != nil || len(public) != ed25519.PublicKeySize {
			t.Fatal("compute public key is invalid")
		}
		publicKeys[platform], credentials[platform] = ed25519.PublicKey(public), token
		parsed, err := jwt.Parse(string(token), func(*jwt.Token) (any, error) { return ed25519.PublicKey(public), nil }, jwt.WithValidMethods([]string{"EdDSA"}), jwt.WithAudience("compute"))
		if err != nil || !parsed.Valid || parsed.Claims.(jwt.MapClaims)["scope"] != "compute_ctl:admin" || parsed.Claims.(jwt.MapClaims)["compute_id"] != nil {
			t.Fatal("compute credential does not satisfy provider signature, scope and audience")
		}
		replay, replayToken, err := BindNeonComputeAuthentication(raw, key, platform)
		if err != nil || !bytes.Equal(bound, replay) || !bytes.Equal(token, replayToken) {
			t.Fatal("compute trust changed on replay")
		}
		for _, forbidden := range []string{"PRIVATE KEY", `"d":`, `"k":`} {
			if bytes.Contains(bound, []byte(forbidden)) {
				t.Fatal("compute signer entered the snapshot")
			}
		}
	}
	if _, err := jwt.Parse(string(credentials[strings.Repeat("a", 32)]), func(*jwt.Token) (any, error) { return publicKeys[strings.Repeat("b", 32)], nil }, jwt.WithValidMethods([]string{"EdDSA"}), jwt.WithAudience("compute")); err == nil {
		t.Fatal("compute authority crossed platform trust")
	}
	for _, invalid := range []json.RawMessage{nil, []byte(`null`), []byte(`{"compute_ctl_config":null}`), []byte(`{"compute_ctl_config":[]}`)} {
		if _, _, err := BindNeonComputeAuthentication(invalid, key, strings.Repeat("a", 32)); err == nil {
			t.Fatal("malformed compute control configuration accepted")
		}
	}
}

func neonAuthClaims(t *testing.T, token []byte, public ed25519.PublicKey) jwt.MapClaims {
	t.Helper()
	parsed, err := jwt.Parse(string(token), func(*jwt.Token) (any, error) { return public, nil }, jwt.WithValidMethods([]string{"EdDSA"}))
	if err != nil || !parsed.Valid {
		t.Fatal("scoped credential did not verify")
	}
	return parsed.Claims.(jwt.MapClaims)
}

func TestNeonAuthenticationSeparatesPlatformsAndPrivileges(t *testing.T) {
	key := bytes.Repeat([]byte{7}, 32)
	a, err := NeonAuthentication(key, strings.Repeat("a", 32))
	if err != nil {
		t.Fatal(err)
	}
	b, err := NeonAuthentication(key, strings.Repeat("b", 32))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a["pageserver-auth"]["public-key.pem"], a["safekeeper-auth"]["public-key.pem"]) || bytes.Equal(a["controller-auth"]["public-key.pem"], a["pageserver-auth"]["public-key.pem"]) {
		t.Fatal("storage trust must be shared only within its platform and purpose")
	}
	for logical, scope := range map[string]string{"controller-auth": "admin", "pageserver-auth": "pageserverapi", "safekeeper-auth": "safekeeperdata"} {
		public := neonAuthPublicKey(t, a[logical]["public-key.pem"])
		claims := neonAuthClaims(t, a[logical]["token"], public)
		if claims["scope"] != scope || claims["tenant_id"] != nil {
			t.Fatal("service credential has wrong scope")
		}
		foreign := neonAuthPublicKey(t, b[logical]["public-key.pem"])
		if _, err := jwt.Parse(string(a[logical]["token"]), func(*jwt.Token) (any, error) { return foreign, nil }, jwt.WithValidMethods([]string{"EdDSA"})); err == nil {
			t.Fatal("credential crossed platform trust")
		}
		for field := range a[logical] {
			if field != "token" && field != "public-key.pem" && field != "upcall-token" {
				t.Fatal("unexpected signing material in snapshot")
			}
		}
	}
	upcall := neonAuthClaims(t, a["controller-auth"]["upcall-token"], neonAuthPublicKey(t, a["controller-auth"]["public-key.pem"]))
	if upcall["scope"] != "generations_api" {
		t.Fatal("pageserver upcall received controller administration")
	}
	if _, err := NeonAuthentication(nil, strings.Repeat("a", 32)); err == nil {
		t.Fatal("missing root accepted")
	}
}

func TestNeonRestoredComputeGetsTenantScopedCompatibleStorageCredential(t *testing.T) {
	key := bytes.Repeat([]byte{8}, 32)
	platform := strings.Repeat("a", 32)
	tenant := strings.Repeat("b", 32)
	auth, err := NeonAuthentication(key, platform)
	if err != nil {
		t.Fatal(err)
	}
	raw := json.RawMessage(`{"spec":{"tenant_id":"` + tenant + `","storage_auth_token":"old","mode":"Replica"}}`)
	bound, err := BindNeonTenantAuthentication(raw, key, platform, tenant)
	if err != nil {
		t.Fatal(err)
	}
	var value struct {
		Spec struct {
			Token string `json:"storage_auth_token"`
			Mode  string `json:"mode"`
		} `json:"spec"`
	}
	if json.Unmarshal(bound, &value) != nil || value.Spec.Mode != "Replica" {
		t.Fatal("compute settings changed")
	}
	for _, logical := range []string{"pageserver-auth", "safekeeper-auth"} {
		claims := neonAuthClaims(t, []byte(value.Spec.Token), neonAuthPublicKey(t, auth[logical]["public-key.pem"]))
		if claims["scope"] != "tenant" || claims["tenant_id"] != tenant {
			t.Fatal("compute token is not tenant scoped")
		}
	}
	replay, err := BindNeonTenantAuthentication(raw, key, platform, tenant)
	if err != nil || !bytes.Equal(replay, bound) {
		t.Fatal("retry changed the immutable credential")
	}
	for _, invalid := range []json.RawMessage{nil, []byte(`null`), []byte(`{"spec":null}`), []byte(`{"spec":[]}`)} {
		if _, err := BindNeonTenantAuthentication(invalid, key, platform, tenant); err == nil {
			t.Fatal("invalid compute template accepted")
		}
	}
}
