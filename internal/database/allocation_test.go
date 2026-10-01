package database

import "testing"

func TestCPUReservationIncludesSupportingProcesses(t *testing.T) {
	for _, tc := range []struct {
		name string
		spec Spec
		want int64
	}{
		{"mysql standalone", Spec{Engine: "mysql", Version: "8.4", Mode: "standalone", Shards: 1, CPU: "500m", Memory: "1Gi"}, 1400},
		{"vitess standalone", Spec{Engine: "vitess", Version: "23", Mode: "standalone", Shards: 1, CPU: "500m", Memory: "1Gi", Vitess: &VitessConfig{BackupDestinationID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", BackupDestinationRevision: 1}}, 4900},
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
