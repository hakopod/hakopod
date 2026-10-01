package spec

import (
	"strings"
	"testing"
)

func TestExternalDatabaseBindingRequiresExplicitEgressAndRevision(t *testing.T) {
	for _, fault := range []string{"", "internal", "missing-revision", "mixed-source", "inline-password", "inline-endpoint"} {
		t.Run(fault, func(t *testing.T) {
			binding := Binding{ExternalDatabase: strings.Repeat("a", 32), ExternalDatabaseRevision: 4, Protocol: "mysql"}
			network := Network{}
			switch fault {
			case "internal":
				network.Internal = true
			case "missing-revision":
				binding.ExternalDatabaseRevision = 0
			case "mixed-source":
				binding.ManagedDatabase = strings.Repeat("b", 32)
			case "inline-password":
				binding.Password = &SecretRef{Ref: "password"}
			case "inline-endpoint":
				binding.Endpoint = "read_write"
			}
			_, err := Normalize(Application{SchemaVersion: 1, Name: "external-binding-fixture", Networks: map[string]Network{"outbound": network}, Services: map[string]Service{"api": {Image: "nginx:alpine", Networks: []string{"outbound"}, Bindings: map[string]Binding{"DATABASE_URL": binding}}}})
			if (err == nil) != (fault == "") {
				t.Fatalf("fault=%s err=%v", fault, err)
			}
		})
	}
}
