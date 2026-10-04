package managedplatform

import (
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/golang-jwt/jwt/v5"
)

var neonSQLRoleName = regexp.MustCompile(`^[a-z0-9_](?:[-_a-z0-9]{0,61}[a-z0-9_])?$`)

// ValidateNeonSQLRoleCredential uses PostgreSQL's SCRAM verifier shape. A string
// that only resembles SCRAM can otherwise become a different SQL password.
func ValidateNeonSQLRoleCredential(name, secret string) error {
	if !neonSQLRoleName.MatchString(name) || len(secret) > 4096 {
		return fmt.Errorf("Neon SQL role credential is invalid")
	}
	parts := strings.Split(secret, "$")
	if len(parts) != 3 || parts[0] != "SCRAM-SHA-256" {
		return fmt.Errorf("Neon SQL role requires a SCRAM-SHA-256 verifier")
	}
	parameters, keys := strings.Split(parts[1], ":"), strings.Split(parts[2], ":")
	if len(parameters) != 2 || len(keys) != 2 {
		return fmt.Errorf("Neon SQL SCRAM verifier is malformed")
	}
	iterations, err := strconv.ParseUint(parameters[0], 10, 31)
	if err != nil || iterations < 1000 || iterations > 999999999 || strconv.FormatUint(iterations, 10) != parameters[0] {
		return fmt.Errorf("Neon SQL SCRAM iteration count is invalid")
	}
	for i, encoded := range []string{parameters[1], keys[0], keys[1]} {
		decoded, err := base64.StdEncoding.Strict().DecodeString(encoded)
		if err != nil || base64.StdEncoding.EncodeToString(decoded) != encoded || i == 0 && (len(decoded) == 0 || len(decoded) > 1024) || i > 0 && len(decoded) != sha256.Size {
			return fmt.Errorf("Neon SQL SCRAM verifier has invalid salt or keys")
		}
	}
	return nil
}

// BindNeonSQLAuthentication installs the accepted proxy verifiers in the
// compute spec before it is sealed. A shared operator template stays unchanged.
func BindNeonSQLAuthentication(raw json.RawMessage, proxyRoles map[string]NeonProxyRoleState) (json.RawMessage, error) {
	if len(raw) == 0 || len(raw) > 65536 || len(proxyRoles) == 0 || len(proxyRoles) > 64 {
		return nil, fmt.Errorf("Neon SQL authentication binding is incomplete")
	}
	var root map[string]any
	if decodeNeonJSON(raw, &root) != nil {
		return nil, fmt.Errorf("Neon SQL authentication template is invalid")
	}
	spec, _ := root["spec"].(map[string]any)
	cluster, _ := spec["cluster"].(map[string]any)
	roles, ok := cluster["roles"].([]any)
	if !ok || len(roles) > 128 {
		return nil, fmt.Errorf("Neon SQL authentication requires bounded compute roles")
	}
	for name, role := range proxyRoles {
		if err := ValidateNeonSQLRoleCredential(name, role.SCRAMSecret); err != nil {
			return nil, err
		}
	}
	seen := make(map[string]bool, len(roles))
	for _, value := range roles {
		role, ok := value.(map[string]any)
		name, named := role["name"].(string)
		if !ok || !named || name == "" || seen[name] {
			return nil, fmt.Errorf("Neon compute roles must have unique names")
		}
		seen[name] = true
		if proxy, bound := proxyRoles[name]; bound {
			role["encrypted_password"] = proxy.SCRAMSecret
			options, ok := role["options"].([]any)
			if !ok && role["options"] != nil {
				return nil, fmt.Errorf("Neon compute role options are invalid")
			}
			filtered := make([]any, 0, len(options))
			for _, option := range options {
				fields, ok := option.(map[string]any)
				optionName, named := fields["name"].(string)
				if !ok || !named {
					return nil, fmt.Errorf("Neon compute role option is invalid")
				}
				switch strings.ToUpper(strings.TrimSpace(optionName)) {
				case "LOGIN", "NOLOGIN", "PASSWORD", "ENCRYPTED PASSWORD", "UNENCRYPTED PASSWORD":
					// The provider appends LOGIN and the accepted password itself.
					continue
				}
				filtered = append(filtered, option)
			}
			role["options"] = filtered
		}
	}
	for name := range proxyRoles {
		if !seen[name] {
			// New template roles receive the provider's administrative grants.
			// Credentials must never silently expand that reviewed inventory.
			return nil, fmt.Errorf("Neon proxy roles must be declared in the reviewed compute template")
		}
	}
	cluster["roles"] = roles
	bound, err := json.Marshal(root)
	if err != nil || len(bound) > 65536 {
		return nil, fmt.Errorf("Neon SQL authentication exceeds the compute template bound")
	}
	return bound, nil
}

// Native signing keys stay in the control process. Domain-separated derivation
// keeps each platform independent and survives replay of its encrypted snapshot.
func neonSigningKey(key []byte, platformID, purpose string) (ed25519.PrivateKey, error) {
	if len(key) != 32 || !neonID.MatchString(platformID) || purpose != "storage" && purpose != "controller" && purpose != "compute" {
		return nil, fmt.Errorf("invalid Neon authentication identity")
	}
	mac := hmac.New(sha256.New, key)
	_, _ = fmt.Fprintf(mac, "hakopod-neon-native-auth-v1\x00%s\x00%s", platformID, purpose)
	return ed25519.NewKeyFromSeed(mac.Sum(nil)), nil
}

// BindNeonComputeAuthentication replaces operator credentials with authority
// limited to this platform's computes. The private signer stays in Go; only a
// public JWKS and a compute-audience bearer credential enter the snapshot.
func BindNeonComputeAuthentication(raw json.RawMessage, key []byte, platformID string) (json.RawMessage, []byte, error) {
	if len(raw) == 0 || len(raw) > maxNeonResponseBytes {
		return nil, nil, fmt.Errorf("invalid Neon compute authentication configuration")
	}
	var config map[string]json.RawMessage
	if json.Unmarshal(raw, &config) != nil || config == nil {
		return nil, nil, fmt.Errorf("invalid Neon compute authentication configuration")
	}
	var control map[string]json.RawMessage
	if json.Unmarshal(config["compute_ctl_config"], &control) != nil || control == nil {
		return nil, nil, fmt.Errorf("Neon compute control configuration is missing")
	}
	signer, err := neonSigningKey(key, platformID, "compute")
	if err != nil {
		return nil, nil, err
	}
	public := signer.Public().(ed25519.PublicKey)
	control["jwks"], err = json.Marshal(map[string]any{"keys": []map[string]string{{"kty": "OKP", "crv": "Ed25519", "alg": "EdDSA", "use": "sig", "kid": "hakopod-compute-v1", "x": base64.RawURLEncoding.EncodeToString(public)}}})
	if err != nil {
		return nil, nil, fmt.Errorf("encode Neon compute verification key")
	}
	control["tls"], _ = json.Marshal(map[string]string{
		"key_path":  "/var/run/secrets/hakopod/compute-auth/tls.key",
		"cert_path": "/var/run/secrets/hakopod/compute-auth/tls.crt",
	})
	config["compute_ctl_config"], err = json.Marshal(control)
	if err != nil {
		return nil, nil, fmt.Errorf("encode Neon compute control configuration")
	}
	bound, err := json.Marshal(config)
	if err != nil {
		return nil, nil, fmt.Errorf("encode Neon compute authentication")
	}
	token := jwt.NewWithClaims(jwt.SigningMethodEdDSA, jwt.MapClaims{"scope": "compute_ctl:admin", "aud": []string{"compute"}})
	token.Header["kid"] = "hakopod-compute-v1"
	signed, err := token.SignedString(signer)
	if err != nil {
		return nil, nil, fmt.Errorf("sign Neon compute control credential")
	}
	return bound, []byte(signed), nil
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
