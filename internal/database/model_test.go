package database

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func validSpec() Spec {
	return Spec{SchemaVersion: 1, Name: "orders", Engine: "redis", Version: "8", Mode: "cluster", Shards: 3, Replicas: 1, CPU: "250m", Memory: "256Mi", StorageGiB: 1}
}
func TestDatabaseReplicaSemantics(t *testing.T) {
	s := validSpec()
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
	if s.Members() != 6 {
		t.Fatal("replicas must be per shard")
	}
	s.Engine = "postgresql"
	s.Version = "17"
	s.Shards = 1
	s.Replicas = 2
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
	if s.Members() != 3 {
		t.Fatal("PostgreSQL needs one primary and two replicas")
	}
	s.Mode = "standalone"
	if s.Validate() == nil {
		t.Fatal("standalone accepted replicas")
	}
}
func TestRedisResizeRequiresCurrentVerifiedBackup(t *testing.T) {
	now := time.Now().UTC()
	s := validSpec()
	d := Resource{ID: "database", Revision: 4, Status: "ready", Spec: s, Observation: Observation{ObservedAt: now, Revision: 4, Status: "ready", SlotsAssigned: 16384, SlotsHealthy: true, TopologyFingerprint: "observed-topology"}}
	next := s
	next.Shards = 4
	good := BackupEvidence{ArtifactID: "archive", DatabaseID: d.ID, Revision: 4, CapturedAt: now.Add(-time.Minute), VerifiedAt: now, SHA256: strings.Repeat("a", 64)}
	for _, tc := range []struct {
		name   string
		change func(*Resource, *BackupEvidence)
	}{
		{"wrong database", func(_ *Resource, e *BackupEvidence) { e.DatabaseID = "another" }},
		{"old revision", func(_ *Resource, e *BackupEvidence) { e.Revision = 3 }},
		{"old capture", func(_ *Resource, e *BackupEvidence) { e.CapturedAt = now.Add(-2 * time.Hour) }},
		{"unverified", func(_ *Resource, e *BackupEvidence) { e.VerifiedAt = time.Time{} }},
		{"missing digest", func(_ *Resource, e *BackupEvidence) { e.SHA256 = "" }},
		{"stale observation", func(d *Resource, _ *BackupEvidence) { d.Observation.ObservedAt = now.Add(-time.Minute) }},
		{"future observation", func(d *Resource, _ *BackupEvidence) { d.Observation.ObservedAt = now.Add(time.Second) }},
		{"duplicate slots", func(d *Resource, _ *BackupEvidence) { d.Observation.SlotsHealthy = false }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, e := d, good
			tc.change(&db, &e)
			p, err := PlanResize(db, next, &e, now)
			if err != nil {
				t.Fatal(err)
			}
			if len(p.BlockedReasons) == 0 {
				t.Fatal("unsafe resize was allowed")
			}
		})
	}
	p, err := PlanResize(d, next, &good, now)
	if err != nil || len(p.BlockedReasons) != 0 {
		t.Fatalf("valid resize blocked: %+v %v", p, err)
	}
	p, err = PlanResize(d, next, nil, now)
	if err != nil || len(p.BlockedReasons) == 0 {
		t.Fatal("backup is mandatory for resharding")
	}
}
func redisFixture() string {
	rows := []string{}
	for i, span := range []string{"0-5460", "5461-10922", "10923-16383"} {
		rows = append(rows, fmt.Sprintf("%040x 10.0.0.%d:6379@16379 master - 0 0 1 connected %s", i+1, i+1, span))
		rows = append(rows, fmt.Sprintf("%040x 10.0.0.%d:6379@16379 slave %040x 0 0 1 connected", i+4, i+4, i+1))
	}
	return strings.Join(rows, "\n")
}
func TestRedisTopologyRequiresExactOwnership(t *testing.T) {
	raw := redisFixture()
	nodes, fingerprint, err := ParseRedisTopology(raw, 3, 1)
	if err != nil || len(nodes) != 6 || len(fingerprint) != 64 {
		t.Fatalf("valid topology: %v", err)
	}
	for _, tc := range []struct{ name, old, new string }{
		{"duplicate slot", "5461-10922", "5460-10922"}, {"missing slot", "5461-10922", "5462-10922"}, {"migration", "0-5460", "0-5460 [42->-node]"}, {"disconnected", "connected", "disconnected"}, {"failed", "master", "master,fail?"}, {"missing replica", fmt.Sprintf("slave %040x", 1), fmt.Sprintf("slave %040x", 9)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, _, err := ParseRedisTopology(strings.Replace(raw, tc.old, tc.new, 1), 3, 1); err == nil {
				t.Fatal("unsafe topology accepted")
			}
		})
	}
	reordered := strings.Split(raw, "\n")
	reordered[0], reordered[1] = reordered[1], reordered[0]
	_, again, err := ParseRedisTopology(strings.Join(reordered, "\n"), 3, 1)
	if err != nil || again != fingerprint {
		t.Fatal("fingerprint depends on discovery order")
	}
}
