package managedplatform

import "strings"

const ManagedTLSIssuerSecret = "platform-tls-issuer"

// UserSecretKeys excludes identities issued after the namespace is claimed.
// Authentication and application configuration still use reviewed snapshots.
func (s Spec) UserSecretKeys(keys []string) []string {
	result := make([]string, 0, len(keys))
	for _, key := range keys {
		if s.TLSMode == "managed" && (s.Kind == "supabase" && (key == "database-tls-certificate" || key == "gateway-tls-certificate") || s.Kind == "neon" && key == "broker-auth") {
			continue
		}
		result = append(result, key)
	}
	return result
}

// ManagedTLSSecretAllowed admits only the issuer and content-addressed leaf
// inventory. User-supplied references cannot acquire this reserved namespace.
func ManagedTLSSecretAllowed(s Spec, name string) bool {
	if s.TLSMode != "managed" {
		return false
	}
	if name == ManagedTLSIssuerSecret {
		return true
	}
	for _, key := range ManagedTLSLogicalNames(s.Kind) {
		prefix := "platform-tls-" + ManagedTLSName(key) + "-"
		value, ok := strings.CutPrefix(name, prefix)
		if !ok {
			continue
		}
		digest, ok := strings.CutSuffix(value, "-r1")
		if ok && len(digest) == 16 && strings.Trim(digest, "0123456789abcdef") == "" {
			return true
		}
	}
	return false
}

func ManagedTLSName(logical string) string {
	switch logical {
	case "database-tls-certificate":
		return "database"
	case "gateway-tls-certificate":
		return "gateway"
	case "controller-database-password":
		return "controller-db"
	default:
		return strings.TrimSuffix(logical, "-auth")
	}
}

func ManagedTLSLogicalNames(kind string) []string {
	if kind == "supabase" {
		return []string{"database-tls-certificate", "gateway-tls-certificate"}
	}
	if kind == "neon" {
		return []string{"broker-auth", "compute-auth", "controller-auth", "controller-database-password", "pageserver-auth", "proxy-auth", "safekeeper-auth"}
	}
	return nil
}
