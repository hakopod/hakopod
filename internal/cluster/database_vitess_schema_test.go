package cluster

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/hakopod/hakopod/internal/database"
)

// The upstream operator tests consume these review-only desired resources.
// Export occurs only in approved VM scratch space when explicitly requested.
func TestVitessExportCandidateSchemaManifests(t *testing.T) {
	directory := os.Getenv("HAKOPOD_VITESS_CANDIDATE_DIR")
	if directory == "" {
		t.Skip("no VM schema compatibility export requested")
	}
	if err := os.MkdirAll(directory, 0700); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"standalone", "cluster"} {
		_, d, storage := vitessRevocationFixture(t, false)
		d.Spec.Mode = mode
		if mode == "cluster" {
			d.Spec.Shards = 2
			d.Spec.Replicas = 1
			d.Spec.Vitess.Tables = []database.VitessTable{{Name: "orders", ShardingColumn: "customer_id"}}
		}
		object, err := DatabaseObject(d)
		if err != nil {
			t.Fatal(err)
		}
		applyVitessPolicy(object, d.Spec, DatabasePolicy{NodeNames: []string{"worker-a", "worker-b", "worker-c", "worker-d"}, Pool: "paid", RuntimeClass: "runsc", StorageClass: "block"})
		if err = applyVitessBackupStorage(object, d, storage); err != nil {
			t.Fatal(err)
		}
		mutateVitessComponents(object, func(component map[string]any, _ string) {
			component["annotations"] = map[string]any{"hakopod.io/vitess-identity": "fixture-public-certificate-fingerprint"}
		})
		raw, err := json.Marshal(object)
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(filepath.Join(directory, mode+".json"), raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
}
