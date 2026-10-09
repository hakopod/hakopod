package api

import (
	"github.com/hakopod/hakopod/internal/database"
	"testing"
)

func TestDatabaseResizeProgressUsesObservedTopology(t *testing.T) {
	d := database.Resource{Revision: 2, Spec: database.Spec{Engine: "postgresql", Shards: 1, Replicas: 2}}
	review := database.ResizePlan{PreviousPrimary: "database-1"}
	phase, _ := databaseResizeProgress(d, review, database.Observation{Revision: 2, Primary: "database-1", Members: []database.Member{{Name: "database-1", Ready: true}, {Name: "database-2", Phase: "Joining"}}})
	if phase != "replication-catch-up" {
		t.Fatal(phase)
	}
	phase, _ = databaseResizeProgress(d, review, database.Observation{Revision: 2, Primary: "database-2", Members: []database.Member{{Name: "database-1", Ready: true}, {Name: "database-2", Ready: true}, {Name: "database-3", Ready: true}}})
	if phase != "primary-transition" {
		t.Fatal(phase)
	}
	phase, _ = databaseResizeProgress(d, review, database.Observation{Revision: 2, Primary: "database-1", Members: []database.Member{{Name: "database-1", Ready: true}, {Name: "database-2", Ready: true}, {Name: "database-3", Ready: true}}})
	if phase != "verification" {
		t.Fatal(phase)
	}
	phase, _ = databaseResizeProgress(d, review, database.Observation{Revision: 1})
	if phase != "observing" {
		t.Fatal(phase)
	}
}
