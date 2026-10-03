package managedplatform

import (
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"

	"github.com/golang-jwt/jwt/v5"
)

// Native signing keys stay in the control process. Domain-separated derivation
// keeps each platform independent and survives replay of its encrypted snapshot.
func neonSigningKey(key []byte, platformID, purpose string) (ed25519.PrivateKey, error) {
	if len(key) != 32 || !neonID.MatchString(platformID) || purpose != "storage" && purpose != "controller" {
		return nil, fmt.Errorf("invalid Neon authentication identity")
	}
	mac := hmac.New(sha256.New, key)
	_, _ = fmt.Fprintf(mac, "hakopod-neon-native-auth-v1\x00%s\x00%s", platformID, purpose)
	return ed25519.NewKeyFromSeed(mac.Sum(nil)), nil
}

func neonSignedToken(key ed25519.PrivateKey, scope, tenantID string) (string, error) {
	claims := jwt.MapClaims{"scope": scope}
	if tenantID != "" {
		claims["tenant_id"] = tenantID
	}
	return jwt.NewWithClaims(jwt.SigningMethodEdDSA, claims).SignedString(key)
}

// NeonAuthentication contains public verification keys and scoped bearer
// credentials for immutable runtime snapshots. It never contains a signing key.
func NeonAuthentication(key []byte, platformID string) (map[string]map[string][]byte, error) {
	result := make(map[string]map[string][]byte, 3)
	for _, entry := range []struct{ logical, purpose, scope string }{
		{"controller-auth", "controller", "admin"},
		{"pageserver-auth", "storage", "pageserverapi"},
		{"safekeeper-auth", "storage", "safekeeperdata"},
	} {
		signer, err := neonSigningKey(key, platformID, entry.purpose)
		if err != nil {
			return nil, err
		}
		public, err := x509.MarshalPKIXPublicKey(signer.Public())
		if err != nil {
			return nil, fmt.Errorf("encode Neon verification key")
		}
		token, err := neonSignedToken(signer, entry.scope, "")
		if err != nil {
			return nil, fmt.Errorf("sign Neon service credential")
		}
		values := map[string][]byte{"public-key.pem": pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: public}), "token": []byte(token)}
		if entry.logical == "controller-auth" {
			upcall, err := neonSignedToken(signer, "generations_api", "")
			if err != nil {
				return nil, fmt.Errorf("sign Neon upcall credential")
			}
			values["upcall-token"] = []byte(upcall)
		}
		result[entry.logical] = values
	}
	return result, nil
}

// BindNeonTenantAuthentication gives compute access to exactly the active
// tenant, including a restored tenant, using this platform's storage trust.
func BindNeonTenantAuthentication(raw json.RawMessage, key []byte, platformID, tenantID string) (json.RawMessage, error) {
	if !neonID.MatchString(tenantID) || len(raw) > maxNeonResponseBytes {
		return nil, fmt.Errorf("invalid Neon compute authentication binding")
	}
	var config map[string]json.RawMessage
	if json.Unmarshal(raw, &config) != nil {
		return nil, fmt.Errorf("invalid Neon compute authentication configuration")
	}
	var spec map[string]json.RawMessage
	if json.Unmarshal(config["spec"], &spec) != nil || spec == nil {
		return nil, fmt.Errorf("Neon compute spec is missing")
	}
	signer, err := neonSigningKey(key, platformID, "storage")
	if err != nil {
		return nil, err
	}
	token, err := neonSignedToken(signer, "tenant", tenantID)
	if err != nil {
		return nil, fmt.Errorf("sign Neon tenant credential")
	}
	spec["storage_auth_token"], _ = json.Marshal(token)
	config["spec"], err = json.Marshal(spec)
	if err != nil {
		return nil, fmt.Errorf("encode Neon compute authentication")
	}
	return json.Marshal(config)
}
