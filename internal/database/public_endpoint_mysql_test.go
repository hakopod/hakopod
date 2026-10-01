package database

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestMySQLPublicEndpointCapabilitiesRemainQualificationGated(t *testing.T) {
	d := publicEndpointDatabase()
	d.Spec.Engine, d.Spec.Version = "mysql", "8.4"
	capabilities := PublicEndpointCapabilitiesFor(d.Spec)
	if capabilities.Available || capabilities.UnavailableReason == "" || len(capabilities.Routes) != 2 {
		t.Fatal("unqualified MySQL public publication was enabled", capabilities)
	}
	for i, route := range capabilities.Routes {
		if route.Protocol != "mysql" || route.Routing != "mysql_router" || route.Pooled || route.ReadOnly != (i == 1) || route.BackendService != "database" || route.BackendPort != 6446+i {
			t.Fatal("MySQL public descriptor bypassed Router", route)
		}
	}
	d.Observation.Endpoints = []Endpoint{{Purpose: "read_write", Port: 6446}}
	if _, err := PlanPublicEndpoint(d, PublicEndpointSpec{Purpose: "read_write", SourceCIDRs: []string{"192.0.2.0/24"}, MaxConnections: 8}, PublicEndpointAllocation{ID: "allocation", Host: "database.example.test", Address: "192.0.2.10", Port: 16446}, "endpoint", 0, time.Now()); err == nil {
		t.Fatal("shipping model accepted unqualified MySQL publication")
	}
}

func TestPublicEndpointDescriptorsStayBoundedAndHideBackendControls(t *testing.T) {
	d := publicEndpointDatabase()
	d.Spec.Pooling = &Pooling{Mode: "transaction", Instances: 1, ReadOnly: true}
	capabilities := PublicEndpointCapabilitiesFor(d.Spec)
	if !capabilities.Available || len(capabilities.Routes) != MaxPublicEndpoints {
		t.Fatal("PostgreSQL routes were lost", capabilities)
	}
	encoded, err := json.Marshal(capabilities)
	if err != nil || strings.Contains(string(encoded), "backend") || strings.Contains(string(encoded), "database-rw") || strings.Contains(string(encoded), "5432") {
		t.Fatal("capability response exposed backend inputs", string(encoded), err)
	}
	for _, engine := range []string{"mysql", "redis", "mongodb", "clickhouse", "oracle", "vitess"} {
		d.Spec.Engine = engine
		got := PublicEndpointCapabilitiesFor(d.Spec)
		if got.Available || len(got.Routes) > MaxPublicEndpoints {
			t.Fatal("unsupported or unqualified engine was enabled", engine)
		}
	}
	d.Spec.Engine, d.Spec.Replicas, d.Spec.Pooling = "mysql", 0, nil
	if _, err := PublicEndpointRouteFor(d.Spec, "read_only"); err == nil {
		t.Fatal("standalone MySQL advertised a reader")
	}
	if _, err := PublicEndpointRouteFor(d.Spec, "pooled_read_write"); err == nil {
		t.Fatal("MySQL advertised PostgreSQL pooling")
	}
}

func TestPublicEndpointReviewBindsDescriptorAndPrivateBackend(t *testing.T) {
	d := publicEndpointDatabase()
	d.Spec.Engine, d.Spec.Version = "mysql", "8.4"
	d.Observation.Endpoints = []Endpoint{{Purpose: "read_write", Port: 6446}, {Purpose: "read_only", Port: 6447}}
	plan, err := planPublicEndpoint(d, PublicEndpointSpec{Purpose: "read_only", SourceCIDRs: []string{"192.0.2.0/24"}, MaxConnections: 8}, PublicEndpointAllocation{ID: "allocation", Host: "database.example.test", Address: "192.0.2.10", Port: 16447}, "endpoint", 0, time.Now())
	if err != nil || plan.Route == nil || plan.Route.BackendPortName != "mysql-ro" || len(plan.RouteFingerprint) != 64 {
		t.Fatal("MySQL route was not bound to its Router backend", plan, err)
	}
	raw, _ := json.Marshal(plan)
	var persisted PublicEndpointReview
	if err = json.Unmarshal(raw, &persisted); err != nil || !persisted.MatchesRoute(plan) {
		t.Fatal("persisted descriptor no longer matches", err)
	}
	changed := plan
	copyRoute := *plan.Route
	copyRoute.BackendPort = 3306
	changed.Route = &copyRoute
	changed.RouteFingerprint = copyRoute.Fingerprint()
	if persisted.MatchesRoute(changed) {
		t.Fatal("review accepted a changed private backend mapping")
	}
	persisted.Route = nil
	persisted.RouteFingerprint = ""
	if persisted.MatchesRoute(plan) {
		t.Fatal("legacy descriptor exemption accepted MySQL")
	}
}

func TestLegacyPostgreSQLReviewRetainsItsFixedRoute(t *testing.T) {
	d := publicEndpointDatabase()
	plan, err := PlanPublicEndpoint(d, PublicEndpointSpec{Purpose: "read_write", SourceCIDRs: []string{"192.0.2.0/24"}, MaxConnections: 8}, PublicEndpointAllocation{ID: "allocation", Host: "database.example.test", Address: "192.0.2.10", Port: 15432}, "endpoint", 0, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	legacy := plan
	legacy.Route, legacy.RouteFingerprint = nil, ""
	if !legacy.MatchesRoute(plan) {
		t.Fatal("existing PostgreSQL review lost backward compatibility")
	}
	legacy.Spec.Purpose = "read_only"
	if legacy.MatchesRoute(plan) {
		t.Fatal("legacy review accepted another route")
	}
}

func TestPublicEndpointNamesRejectUntrustedIdentityShapes(t *testing.T) {
	for _, names := range [][]string{{"*.example.test"}, {"192.0.2.1\nother.test"}, {"localhost"}, {"db.example.test/path"}, make([]string, MaxPublicEndpointNames+1)} {
		if _, err := NormalizePublicEndpointNames(names); err == nil {
			t.Fatal("unsafe public identity accepted", names)
		}
	}
	got, err := NormalizePublicEndpointNames([]string{"b.example.test", "a.example.test", "a.example.test"})
	if err != nil || len(got) != 2 || got[0] != "a.example.test" {
		t.Fatal("public names were not normalized", got, err)
	}
}
