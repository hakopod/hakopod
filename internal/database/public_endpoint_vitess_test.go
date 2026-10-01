package database

import (
	"strings"
	"testing"
)

func TestVitessPublicEndpointRouteRemainsQualificationGated(t *testing.T) {
	spec := Spec{Engine: "vitess", Mode: "cluster", Replicas: 2, Shards: 1, TLS: &TLSConfig{Mode: "required"}}
	if err := PublicEndpointAvailability(spec); err == nil || !strings.Contains(err.Error(), "qualification is complete") {
		t.Fatalf("Vitess public endpoint qualification gate opened or changed: %v", err)
	}
	routes := PublicEndpointRoutes(spec)
	if len(routes) != 1 {
		t.Fatalf("got %d Vitess routes, want one enforced route", len(routes))
	}
	want := []struct {
		purpose, target, fingerprint string
		readOnly                     bool
	}{
		{"read_write", "app@primary", "b7bd7b9360c64c9d83de5637af8455344c6b378b4924686036a8102174dd5b97", false},
	}
	for i, expected := range want {
		route := routes[i]
		if route.Purpose != expected.purpose || route.Protocol != "mysql" || route.Routing != "vitess_gateway" || route.ReadOnly != expected.readOnly || route.Pooled || route.BackendService != "database" || route.BackendPort != 3306 || route.BackendPortName != "mysql" || route.BackendUser != "app" || route.BackendDatabase != expected.target || route.Fingerprint() != expected.fingerprint {
			t.Fatalf("unexpected Vitess route %d: %#v fingerprint=%s", i, route, route.Fingerprint())
		}
	}
}

func TestVitessPublicEndpointDoesNotAdvertiseUnenforcedReadOnlyRouting(t *testing.T) {
	spec := Spec{Engine: "vitess", Mode: "cluster", Replicas: 2, Shards: 1, TLS: &TLSConfig{Mode: "required"}}
	routes := PublicEndpointRoutes(spec)
	if len(routes) != 1 || routes[0].Purpose != "read_write" {
		t.Fatalf("Vitess advertised an unenforced read-only route: %#v", routes)
	}
}
