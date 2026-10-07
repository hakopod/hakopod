package store

import (
	"errors"
	"testing"

	"github.com/hakopod/hakopod/internal/database"
	"github.com/hakopod/hakopod/internal/spec"
)

func TestMyDuckBindingRequiresMatchingManagedProtocol(t *testing.T) {
	d := database.Resource{Status: "ready", Spec: database.Spec{Engine: "duckdb", TLS: &database.TLSConfig{Mode: "required"}}, Observation: database.Observation{Status: "ready"}}
	for _, binding := range []spec.Binding{
		{Protocol: "mysql", Endpoint: "mysql"},
		{Protocol: "postgres", Endpoint: "postgresql"},
		{Protocol: "mysql", Endpoint: "mysql", Username: "root", Database: "app"},
		{Protocol: "postgres", Endpoint: "postgresql", Username: "postgres", Database: "app"},
	} {
		if err := validateDatabaseBinding(d, binding); err != nil {
			t.Fatal(err)
		}
	}
	for _, binding := range []spec.Binding{
		{Protocol: "postgres", Endpoint: "mysql"},
		{Protocol: "mysql", Endpoint: "postgresql"},
		{Protocol: "mysql", Endpoint: "read_write"},
		{Protocol: "mysql", Endpoint: "mysql", Username: "another", Password: &spec.SecretRef{Ref: "password"}},
		{Protocol: "postgres", Endpoint: "postgresql", Database: "another"},
	} {
		if err := validateDatabaseBinding(d, binding); !errors.Is(err, ErrInput) {
			t.Fatal("unsupported MyDuck binding was accepted")
		}
	}
}
