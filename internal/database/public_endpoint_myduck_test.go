package database

import (
	"strings"
	"testing"
)

func TestMyDuckPublicEndpointsRemainDualProtocolQualificationGated(t *testing.T) {
	spec := Spec{Engine: "duckdb", Version: MyDuckVersion, Mode: "standalone", Shards: 1, TLS: &TLSConfig{Mode: "required"}}
	if err := PublicEndpointAvailability(spec); err == nil || !strings.Contains(err.Error(), "dual-protocol TLS") {
		t.Fatalf("MyDuck public endpoint qualification gate opened or changed: %v", err)
	}
	routes := PublicEndpointRoutes(spec)
	if len(routes) != 2 {
		t.Fatalf("MyDuck advertised %d public routes, want 2", len(routes))
	}
	want := []PublicEndpointRoute{
		{Purpose: "mysql", Protocol: "mysql", Routing: "direct", BackendService: "database", BackendPort: 3306, BackendPortName: "mysql", BackendUser: "root", BackendDatabase: "app"},
		{Purpose: "postgresql", Protocol: "postgresql", Routing: "direct", BackendService: "database", BackendPort: 5432, BackendPortName: "postgresql", BackendUser: "postgres", BackendDatabase: "app"},
	}
	for i := range want {
		if routes[i] != want[i] {
			t.Fatalf("MyDuck public route %d = %#v, want %#v", i, routes[i], want[i])
		}
		if _, err := (PublicEndpointSpec{Purpose: routes[i].Purpose, SourceCIDRs: []string{"192.0.2.0/24"}, MaxConnections: 8}).Normalize(); err != nil {
			t.Fatalf("MyDuck public route purpose was rejected: %v", err)
		}
	}
}
