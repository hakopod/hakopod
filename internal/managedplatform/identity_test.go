package managedplatform

import "testing"

func TestManagedTLSCatalogOmitsOnlyGeneratedIdentities(t *testing.T) {
	for _, entry := range CatalogEntries() {
		if entry.DefaultSpec.TLSMode != "managed" {
			t.Fatal("guided create does not enable managed TLS")
		}
		seen := map[string]bool{}
		for _, key := range entry.RequiredSecretKeys {
			seen[key] = true
		}
		if seen["database-tls-certificate"] || seen["gateway-tls-certificate"] || seen["broker-auth"] {
			t.Fatal("guided create still requires per-resource certificate provisioning")
		}
		if entry.Kind == "supabase" && (!seen["envoy-runtime-config"] || !seen["pooler-api-jwt-secret"]) {
			t.Fatal("managed TLS omitted independent routing or authentication prerequisites")
		}
	}
}

func TestManagedTLSClaimInventoryIsBounded(t *testing.T) {
	s := Spec{Kind: "supabase", TLSMode: "managed"}
	for _, name := range []string{ManagedTLSIssuerSecret, "platform-tls-database-0123456789abcdef-r1", "platform-tls-gateway-0123456789abcdef-r1"} {
		if !ManagedTLSSecretAllowed(s, name) {
			t.Fatalf("expected allowed %s", name)
		}
	}
	for _, name := range []string{"platform-tls-database-any-r1", "platform-tls-database-0123456789abcdef-r2", "platform-tls-compute-0123456789abcdef-r1", "platform-tls-database-0123456789abcdeg-r1"} {
		if ManagedTLSSecretAllowed(s, name) {
			t.Fatalf("unexpected allowed %s", name)
		}
	}
	s.TLSMode = "operator"
	if ManagedTLSSecretAllowed(s, ManagedTLSIssuerSecret) {
		t.Fatal("operator material acquired the managed issuer namespace")
	}
}
