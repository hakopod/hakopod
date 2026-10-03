package database

import (
	"os"
	"regexp"
	"strconv"
	"testing"
)

func TestCPUReservationIncludesSupportingProcesses(t *testing.T) {
	for _, tc := range []struct {
		name string
		spec Spec
		want int64
	}{
		{"mysql standalone", Spec{Engine: "mysql", Version: "8.4", Mode: "standalone", Shards: 1, CPU: "500m", Memory: "1Gi"}, 1400},
		{"vitess standalone", Spec{Engine: "vitess", Version: "23", Mode: "standalone", Shards: 1, CPU: "500m", Memory: "1Gi", Vitess: &VitessConfig{BackupDestinationID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", BackupDestinationRevision: 1}}, 5100},
		{"mysql cluster", Spec{Engine: "mysql", Version: "8.4", Mode: "cluster", Replicas: 2, Shards: 1, CPU: "500m", Memory: "1Gi"}, 2700},
		{"mongodb cluster", Spec{Engine: "mongodb", Version: "8.0", Mode: "cluster", Replicas: 2, Shards: 1, CPU: "500m", Memory: "1Gi"}, 2400},
		{"clickhouse two shards", Spec{Engine: "clickhouse", Version: "26.3", Mode: "cluster", Replicas: 1, Shards: 2, CPU: "500m", Memory: "2Gi"}, 3500},
		{"postgres two pooled routes", Spec{Engine: "postgresql", Version: "17", Mode: "cluster", Replicas: 1, Shards: 1, CPU: "250m", Memory: "512Mi", Pooling: &Pooling{Mode: "transaction", Instances: 2, MaxClientConnections: 100, DefaultPoolSize: 10, ReadOnly: true}}, 2250},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := tc.spec
			s.SchemaVersion, s.Name, s.StorageGiB = 1, "allocation-fixture", 5
			s.TLS = &TLSConfig{Mode: "required"}
			got, err := s.CPUReservationMilli()
			if err != nil || got != tc.want {
				t.Fatalf("CPU reservation = %d, %v; want %d", got, err, tc.want)
			}
		})
	}
	if _, err := (Spec{CPU: "invalid"}).CPUReservationMilli(); err == nil {
		t.Fatal("invalid database input was accepted")
	}
}

func TestVitessNativeAcceptanceReservationsMatchPreflight(t *testing.T) {
	preflight, err := os.ReadFile("../../scripts/run-development-vitess-acceptance.py")
	if err != nil {
		t.Fatal(err)
	}
	for _, fixture := range []struct {
		name                     string
		shards, replicas, copies int
	}{
		{"lifecycle", 2, 1, 1},
		{"recovery", 2, 1, 2},
		{"reseed", 1, 2, 1},
		{"revocation", 1, 0, 1},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			s := Spec{SchemaVersion: 1, Name: "native-capacity-fixture", Engine: "vitess", Version: "23", Mode: "standalone", Shards: fixture.shards, Replicas: fixture.replicas, CPU: "500m", Memory: "1Gi", StorageGiB: 1, TLS: &TLSConfig{Mode: "required"}, Vitess: &VitessConfig{BackupDestinationID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", BackupDestinationRevision: 1}}
			if fixture.replicas > 0 {
				s.Mode = "cluster"
			}
			if fixture.shards > 1 {
				s.Vitess.Tables = []VitessTable{{Name: "records", ShardingColumn: "id"}}
			}
			cpu, err := s.CPUReservationMilli()
			if err != nil {
				t.Fatal(err)
			}
			match := regexp.MustCompile(`(?m)^    '` + fixture.name + `': ([0-9]+),$`).FindSubmatch(preflight)
			if len(match) != 2 {
				t.Fatal("native preflight CPU reservation is missing")
			}
			got, err := strconv.ParseInt(string(match[1]), 10, 64)
			if err != nil || got != cpu*int64(fixture.copies) {
				t.Fatalf("preflight reserves %d milliCPU; fixture requires %d: %v", got, cpu*int64(fixture.copies), err)
			}
		})
	}
}
