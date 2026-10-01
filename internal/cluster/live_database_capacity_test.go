package cluster

import (
	"context"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/database"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/clientcmd"
)

// This acceptance probe only reads existing node, pod and namespace state.
// It never creates a workload or changes a node's labels, UID or capacity.
func TestManagedDatabaseCapacityLive(t *testing.T) {
	if os.Getenv("HAKOPOD_DATABASE_CAPACITY_TEST") != "1" {
		t.Skip("set HAKOPOD_DATABASE_CAPACITY_TEST=1 for read-only development capacity acceptance")
	}
	path := os.Getenv("HAKOPOD_TEST_KUBECONFIG")
	config, err := clientcmd.LoadFromFile(path)
	if err != nil || config.CurrentContext != "k3d-hakopod-dev" {
		t.Fatal("database capacity acceptance requires k3d-hakopod-dev")
	}
	c, err := New(path, Options{AppDomain: "development.example.test"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	names := []string{"k3d-hakopod-dev-server-0", "k3d-hakopod-database-worker-0"}
	reservations := map[string]DatabaseNodeReservation{}
	for _, name := range names {
		node, err := c.kube.CoreV1().Nodes().Get(ctx, name, metav1.GetOptions{})
		if err != nil || node.UID == "" {
			t.Fatal("approved development node identity is unavailable", err)
		}
		reservation := DatabaseNodeReservation{UID: string(node.UID), Capacity: database.Capacity{CPUMilli: 100, MemoryBytes: 256 << 20}, Scopes: []string{"capacity-readonly/development"}}
		if err = c.checkDatabaseNodeReservation(ctx, *node, reservation); err != nil {
			t.Fatal("development node cannot fit the probe envelope after existing pods and host headroom", name, err)
		}
		reservations[name] = reservation
		reservation.UID = "unapproved-replacement"
		if c.checkDatabaseNodeReservation(ctx, *node, reservation) == nil {
			t.Fatal("replaced node identity retained capacity approval")
		}
		reservation.UID = string(node.UID)
		reservation.Capacity.CPUMilli = node.Status.Allocatable.Cpu().MilliValue() + 1
		if c.checkDatabaseNodeReservation(ctx, *node, reservation) == nil {
			t.Fatal("over-capacity envelope was accepted")
		}
	}
	if err = c.CheckDatabaseNodeReservations(ctx, reservations); err != nil {
		t.Fatal(err)
	}
	c.options.DatabasePlacementPolicy = func(context.Context, string, string) (DatabasePolicy, error) {
		return DatabasePolicy{NodeNames: names, Pool: "database-development", RuntimeClass: "runsc", NodeReservations: reservations}, nil
	}
	items, err := c.DatabasePlacementNodes(ctx, "capacity-readonly", "development")
	if err != nil || len(items) != 2 {
		t.Fatal("bounded database discovery failed", err)
	}
	for _, item := range items {
		if !slices.Contains(names, item.Name) || item.Architecture == "" || item.Available && item.Reason != "" {
			t.Fatal("discovery returned an unexpected node or contradictory availability")
		}
	}
	t.Log("Read-only acceptance passed for two approved development nodes: current workloads and headroom accounted, changed UID and excessive grant rejected, database discovery bounded to the requested pool")
}
