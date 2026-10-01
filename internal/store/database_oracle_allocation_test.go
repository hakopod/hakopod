package store

import (
	"strings"
	"testing"

	"github.com/hakopod/hakopod/internal/database"
)

func TestOracleEnterpriseAllocationIncludesRecoveryVolumesAndBroker(t *testing.T) {
	s := database.Spec{SchemaVersion: 1, Name: "oracle-allocation", Engine: "oracle", Version: "19", Mode: "cluster", Replicas: 1, Shards: 1, CPU: "1", Memory: "4Gi", StorageGiB: 10, TLS: &database.TLSConfig{Mode: "required"}, Oracle: &database.OracleConfig{Edition: "enterprise", Image: "registry.example.com/oracle/database@sha256:" + strings.Repeat("a", 64), LicenseConfirmed: true}}
	if got := DatabaseStorageReservation(s); got != 60 {
		t.Fatalf("two Enterprise members need data, FRA and backup volumes: got %d GiB", got)
	}
	if got := DatabaseMemoryReservation(s); got != (3*(4096+50)+128+256+50)<<20 {
		t.Fatalf("Enterprise allocation omitted broker or recovery headroom: got %d", got)
	}
	if got, err := s.CPUReservationMilli(); err != nil || got != 3100 {
		t.Fatalf("Enterprise CPU reservation = %d, %v; want 3100m", got, err)
	}
	s.Mode, s.Replicas = "standalone", 0
	if got := DatabaseStorageReservation(s); got != 30 || s.OracleBrokerInstances() != 0 {
		t.Fatal("standalone Enterprise allocation changed its storage or added a broker")
	}
	s.Version, s.Oracle = "23.26", &database.OracleConfig{Edition: "free"}
	if got := DatabaseStorageReservation(s); got != 20 || s.OracleBrokerInstances() != 0 {
		t.Fatal("Free allocation inherited Enterprise infrastructure")
	}
}
