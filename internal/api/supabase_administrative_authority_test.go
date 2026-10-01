package api

import (
	"strings"
	"testing"

	"github.com/hakopod/hakopod/internal/managedplatform"
)

func TestSupabaseAdministrativeAuthorityRequiresIndependentKeyMaterial(t *testing.T) {
	applicationRef := managedplatform.SecretReference{Name: "supabase-jwt-secret", Revision: 1}
	adminRef := managedplatform.SecretReference{Name: "supabase-pooler-api-jwt-secret", Revision: 1}
	spec := managedplatform.Spec{Secrets: map[string]managedplatform.SecretReference{
		"jwt-secret":            applicationRef,
		"pooler-api-jwt-secret": adminRef,
	}}
	snapshots := map[string]map[string][]byte{
		"supabase-jwt-secret-r1":            {"value": []byte(strings.Repeat("a", 32))},
		"supabase-pooler-api-jwt-secret-r1": {"value": []byte(strings.Repeat("a", 32))},
	}
	if err := validateSupabaseAdministrativeAuthority(spec, snapshots); err == nil {
		t.Fatal("shared application and pooler administrative JWT key material was accepted")
	}
	snapshots["supabase-pooler-api-jwt-secret-r1"]["value"] = []byte(strings.Repeat("b", 32))
	if err := validateSupabaseAdministrativeAuthority(spec, snapshots); err != nil {
		t.Fatal(err)
	}
}

func TestSupabaseAdministrativeAuthorityRejectsWeakOrSharedReferences(t *testing.T) {
	ref := managedplatform.SecretReference{Name: "shared", Revision: 1}
	spec := managedplatform.Spec{Secrets: map[string]managedplatform.SecretReference{"jwt-secret": ref, "pooler-api-jwt-secret": ref}}
	if err := validateSupabaseAdministrativeAuthority(spec, map[string]map[string][]byte{"shared-r1": {"value": []byte(strings.Repeat("a", 32))}}); err == nil {
		t.Fatal("shared application and pooler administrative JWT reference was accepted")
	}
	spec.Secrets["pooler-api-jwt-secret"] = managedplatform.SecretReference{Name: "admin", Revision: 1}
	snapshots := map[string]map[string][]byte{"shared-r1": {"value": []byte(strings.Repeat("a", 32))}, "admin-r1": {"value": []byte("short")}}
	if err := validateSupabaseAdministrativeAuthority(spec, snapshots); err == nil {
		t.Fatal("weak pooler administrative JWT key material was accepted")
	}
}
