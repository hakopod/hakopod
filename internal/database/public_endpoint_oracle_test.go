package database

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func oracleFreePublicEndpointFixture() Resource {
	d := publicEndpointDatabase()
	d.Spec.Engine = "oracle"
	d.Spec.Version = "23.26"
	d.Spec.Mode = "standalone"
	d.Spec.Replicas = 0
	d.Spec.Oracle = &OracleConfig{Edition: "free"}
	d.Observation.Endpoints = []Endpoint{{Purpose: "read_write", Port: 2484}}
	return d
}

func TestOracleFreePublicEndpointRemainsQualificationGated(t *testing.T) {
	d := oracleFreePublicEndpointFixture()
	capabilities := PublicEndpointCapabilitiesFor(d.Spec)
	if capabilities.Available || capabilities.UnavailableReason == "" || len(capabilities.Routes) != 1 {
		t.Fatal("unqualified Oracle Free publication was enabled", capabilities)
	}
	route := capabilities.Routes[0]
	if route.Purpose != "read_write" || route.Protocol != "oracle_tcps" || route.Routing != "direct" || route.ReadOnly || route.Pooled || route.BackendService != "database" || route.BackendPort != 2484 || route.BackendPortName != "tcps" || route.BackendUser != "APP" || route.BackendDatabase != "FREEPDB1" {
		t.Fatal("Oracle Free route bypassed its TCPS service", route)
	}
	encoded, err := json.Marshal(capabilities)
	if err != nil || strings.Contains(string(encoded), "backend") || strings.Contains(string(encoded), "2484") || strings.Contains(string(encoded), "APP") || strings.Contains(string(encoded), "FREEPDB1") {
		t.Fatal("Oracle capability exposed its private backend mapping", string(encoded), err)
	}
	if _, err = PlanPublicEndpoint(d, PublicEndpointSpec{Purpose: "read_write", SourceCIDRs: []string{"192.0.2.0/24"}, MaxConnections: 8}, PublicEndpointAllocation{ID: "allocation", Host: "database.example.test", Address: "192.0.2.10", Port: 12484}, "endpoint", 0, time.Now()); err == nil {
		t.Fatal("shipping model accepted unqualified Oracle Free publication")
	}
}

func TestOracleEnterprisePublicEndpointHasNoFreeRoute(t *testing.T) {
	d := oracleFreePublicEndpointFixture()
	d.Spec.Mode = "cluster"
	d.Spec.Replicas = 2
	d.Spec.Oracle.Edition = "enterprise"
	capabilities := PublicEndpointCapabilitiesFor(d.Spec)
	if capabilities.Available || capabilities.UnavailableReason == "" || len(capabilities.Routes) != 0 {
		t.Fatal("Oracle Enterprise inherited the unqualified Free route", capabilities)
	}
	if _, err := PublicEndpointRouteFor(d.Spec, "read_write"); err == nil {
		t.Fatal("Oracle Enterprise resolved the Free TCPS route")
	}
}

func TestOracleFreePublicReviewBindsPDBAndTCPSRoute(t *testing.T) {
	d := oracleFreePublicEndpointFixture()
	plan, err := planPublicEndpoint(d, PublicEndpointSpec{Purpose: "read_write", SourceCIDRs: []string{"192.0.2.0/24"}, MaxConnections: 8}, PublicEndpointAllocation{ID: "allocation", Host: "database.example.test", Address: "192.0.2.10", Port: 12484}, "endpoint", 0, time.Now())
	if err != nil || plan.Route == nil {
		t.Fatal("Oracle Free route was not planned", plan, err)
	}
	if len(plan.Warnings) != 2 || !strings.Contains(plan.Warnings[0], "restarts its single database member") {
		t.Fatal("Oracle Free review did not disclose the singleton restart", plan.Warnings)
	}
	persisted := plan
	for _, change := range []func(*PublicEndpointRoute){
		func(route *PublicEndpointRoute) { route.Protocol = "tcp" },
		func(route *PublicEndpointRoute) { route.Routing = "listener_proxy" },
		func(route *PublicEndpointRoute) { route.BackendPort = 1521 },
		func(route *PublicEndpointRoute) { route.BackendPortName = "tcp" },
		func(route *PublicEndpointRoute) { route.BackendUser = "SYS" },
		func(route *PublicEndpointRoute) { route.BackendDatabase = "FREE" },
	} {
		changed := plan
		route := *plan.Route
		change(&route)
		changed.Route = &route
		changed.RouteFingerprint = route.Fingerprint()
		if persisted.MatchesRoute(changed) {
			t.Fatal("Oracle review accepted a changed private route", route)
		}
	}
	persisted.Route = nil
	persisted.RouteFingerprint = ""
	if persisted.MatchesRoute(plan) {
		t.Fatal("PostgreSQL legacy descriptor exemption accepted Oracle")
	}
}
