package cluster

import (
	"context"
	"testing"

	"github.com/hakopod/hakopod/internal/database"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestPoolerRequiresEncryptedRoutesAndPinnedResources(t *testing.T) {
	d := database.Resource{ID: "01234567890123456789012345678901", Revision: 1, Spec: database.Spec{SchemaVersion: 1, Name: "pooling-fixture", Engine: "postgresql", Version: "17", Mode: "cluster", Shards: 1, Replicas: 1, CPU: "250m", Memory: "512Mi", StorageGiB: 1, TLS: &database.TLSConfig{Mode: "required"}, Pooling: &database.Pooling{Mode: "session", Instances: 2, MaxClientConnections: 200, DefaultPoolSize: 10, ReadOnly: true}}}
	c := &Client{}
	for _, route := range []string{"rw", "ro"} {
		object, err := c.databasePoolerObject(context.Background(), d, route, "owned-cluster")
		if err != nil {
			t.Fatal(err)
		}
		for key, want := range map[string]string{"client_tls_sslmode": "require", "server_tls_sslmode": "verify-full", "max_db_connections": "10", "log_pooler_errors": "0"} {
			got, _, _ := unstructured.NestedString(object.Object, "spec", "pgbouncer", "parameters", key)
			if got != want {
				t.Fatal("unsafe pooler parameter", key, got)
			}
		}
		if !databasePoolerOwned(object, d, "owned-cluster", route) || databasePoolerOwned(object, d, "another-cluster", route) {
			t.Fatal("pooler ownership fence failed")
		}
		image, _, _ := unstructured.NestedString(object.Object, "spec", "pgbouncer", "image")
		if image != databasePoolerImage {
			t.Fatal("pooler image is not pinned")
		}
	}
	object, err := DatabaseObject(d)
	if err != nil {
		t.Fatal(err)
	}
	hosts, _, _ := unstructured.NestedSlice(object.Object, "spec", "certificates", "serverAltDNSNames")
	if len(hosts) != 8 {
		t.Fatal("pooler certificate names missing")
	}
}
