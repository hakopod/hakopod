package database

import (
	"testing"
	"time"
)

func TestClickHouseAccessProfiles(t *testing.T) {
	for _, tc := range []struct {
		engine, mode, profile string
		valid, admin          bool
	}{
		{"clickhouse", "standalone", "application", true, false},
		{"clickhouse", "standalone", "tenant_admin", true, true},
		{"clickhouse", "cluster", "tenant_admin", false, true},
		{"postgresql", "standalone", "tenant_admin", false, false},
		{"clickhouse", "standalone", "root", false, false},
		{"clickhouse", "standalone", "", false, false},
	} {
		t.Run(tc.engine+"/"+tc.mode+"/"+tc.profile, func(t *testing.T) {
			s := Spec{Engine: tc.engine, Mode: tc.mode, ClickHouse: &ClickHouseConfig{AccessProfile: tc.profile}}
			if (s.validateClickHouseAccess() == nil) != tc.valid {
				t.Fatal("incorrect access validation")
			}
			if s.ClickHouseTenantAdmin() != tc.admin {
				t.Fatal("incorrect profile selection")
			}
		})
	}
	if (Spec{Engine: "clickhouse"}).ClickHouseTenantAdmin() {
		t.Fatal("default gained tenant administration")
	}
}

func TestClickHouseProfileReviewPreservesCapacityAndBlocksDowngrade(t *testing.T) {
	s := Spec{SchemaVersion: 1, Name: "analytics", Engine: "clickhouse", Version: "26.3", Mode: "standalone", Replicas: 0, Shards: 1, CPU: "500m", Memory: "2Gi", StorageGiB: 10, TLS: &TLSConfig{Mode: "required"}}
	next := s
	next.ClickHouse = &ClickHouseConfig{AccessProfile: "tenant_admin"}
	p, err := PlanResize(Resource{Spec: s}, next, nil, time.Now())
	if err != nil || len(p.Warnings) != 1 {
		t.Fatalf("profile review: %v %v", p.Warnings, err)
	}
	if _, err := PlanResize(Resource{Spec: next}, s, nil, time.Now()); err == nil {
		t.Fatal("unsafe downgrade accepted")
	}
	bigger := next
	bigger.Memory = "4Gi"
	if _, err := PlanResize(Resource{Spec: s}, bigger, nil, time.Now()); err == nil {
		t.Fatal("profile update bypassed capacity gate")
	}
}
