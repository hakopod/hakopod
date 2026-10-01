package database

import (
	"encoding/json"
	"testing"
	"time"
)

func TestClickHousePublicEndpointsRemainGatedForBothTransports(t *testing.T) {
	d := publicEndpointDatabase()
	d.Spec.Engine, d.Spec.Version, d.Spec.Memory = "clickhouse", "26.3", "2Gi"
	d.Spec.Pooling = nil
	for _, mode := range []string{"standalone", "cluster"} {
		t.Run(mode, func(t *testing.T) {
			d.Spec.Mode = mode
			capabilities := PublicEndpointCapabilitiesFor(d.Spec)
			if capabilities.Available || capabilities.UnavailableReason == "" || len(capabilities.Routes) != 2 {
				t.Fatal("ClickHouse descriptor overstated qualification", capabilities)
			}
			for _, expected := range []struct {
				purpose, protocol, portName string
				port                        int
			}{{"native", "clickhouse_native", "tcp-secure", 9440}, {"https", "https", "https", 8443}} {
				route, err := PublicEndpointRouteFor(d.Spec, expected.purpose)
				if err != nil || route.Protocol != expected.protocol || route.Routing != "direct" || route.ReadOnly || route.Pooled || route.BackendService != "database" || route.BackendPortName != expected.portName || route.BackendPort != expected.port {
					t.Fatal("ClickHouse transport was mislabeled or misrouted", route, err)
				}
				d.Observation.Endpoints = []Endpoint{{Purpose: expected.purpose, Port: expected.port}}
				if _, err := PlanPublicEndpoint(d, PublicEndpointSpec{Purpose: expected.purpose, SourceCIDRs: []string{"192.0.2.0/24"}, MaxConnections: 8}, PublicEndpointAllocation{ID: "allocation", Host: "database.example.test", Address: "192.0.2.10", Port: 19440}, "endpoint", 0, time.Now()); err == nil {
					t.Fatal("qualification gate allowed publication", expected.purpose)
				}
			}
			for _, purpose := range []string{"read_write", "read_only", "pooled_read_write", "cluster"} {
				if _, err := PublicEndpointRouteFor(d.Spec, purpose); err == nil {
					t.Fatal("private or foreign route became public", purpose)
				}
			}
		})
	}
}

func TestClickHousePublicReviewBindsTransportAndBackend(t *testing.T) {
	d := publicEndpointDatabase()
	d.Spec.Engine, d.Spec.Version, d.Spec.Memory, d.Spec.Pooling = "clickhouse", "26.3", "2Gi", nil
	d.Observation.Endpoints = []Endpoint{{Purpose: "cluster", Port: 9440}, {Purpose: "native", Port: 9440}, {Purpose: "https", Port: 8443}}
	plan, err := planPublicEndpoint(d, PublicEndpointSpec{Purpose: "native", SourceCIDRs: []string{"192.0.2.0/24"}, MaxConnections: 8}, PublicEndpointAllocation{ID: "allocation", Host: "database.example.test", Address: "192.0.2.10", Port: 19440}, "endpoint", 0, time.Now())
	if err != nil || plan.Route == nil || plan.Route.Protocol != "clickhouse_native" {
		t.Fatal("native route missing", plan, err)
	}
	raw, err := json.Marshal(plan)
	var persisted PublicEndpointReview
	if err != nil || json.Unmarshal(raw, &persisted) != nil || !persisted.MatchesRoute(plan) {
		t.Fatal("persisted review lost route binding", err)
	}
	for _, change := range []func(*PublicEndpointRoute){
		func(r *PublicEndpointRoute) { r.Protocol = "https" },
		func(r *PublicEndpointRoute) { r.BackendPort = 9000 },
		func(r *PublicEndpointRoute) { r.BackendPortName = "http" },
		func(r *PublicEndpointRoute) { r.ReadOnly = true },
	} {
		changed := plan
		route := *plan.Route
		change(&route)
		changed.Route, changed.RouteFingerprint = &route, route.Fingerprint()
		if persisted.MatchesRoute(changed) {
			t.Fatal("review accepted changed transport")
		}
	}
	persisted.Route, persisted.RouteFingerprint = nil, ""
	if persisted.MatchesRoute(plan) {
		t.Fatal("PostgreSQL legacy exemption accepted ClickHouse")
	}
	d.Observation.Endpoints = []Endpoint{{Purpose: "cluster", Port: 9440}}
	if _, err := planPublicEndpoint(d, plan.Spec, plan.Allocation, plan.EndpointID, 0, time.Now()); err == nil {
		t.Fatal("legacy observation without migrated transport enabled review")
	}
}
