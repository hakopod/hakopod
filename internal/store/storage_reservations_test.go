package store

import (
	"context"
	"github.com/hakopod/hakopod/internal/spec"
	"sync"
	"testing"
)

func TestStorageBudgetSerializesAndRetainsReservations(t *testing.T) {
	db := isolatedDatabase(t)
	p := bootstrapPrincipal(t, db)
	ctx := context.Background()
	db.StorageBudget = func(context.Context, string, string) (int64, error) { return 1, nil }
	db.ValidateDeployment = func(context.Context, Application, spec.Application) error { return nil }
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, name := range []string{"database-one", "database-two"} {
		wg.Add(1)
		go func(name string) {
			defer wg.Done()
			a := emptyTestSpec()
			a.Name = name
			v := a.Services["api"]
			v.Volume = &spec.Volume{SizeGiB: 1, MountPath: "/data"}
			a.Services["api"] = v
			_, err := db.Accept(ctx, p, "demo", "development", a, 0, "storage-"+name)
			results <- err
		}(name)
	}
	wg.Wait()
	close(results)
	accepted := 0
	for err := range results {
		if err == nil {
			accepted++
		}
	}
	if accepted != 1 {
		t.Fatalf("accepted %d allocations with room for one", accepted)
	}
	var appID string
	if err := db.Pool.QueryRow(ctx, "SELECT application_id FROM storage_reservations").Scan(&appID); err != nil {
		t.Fatal(err)
	}
	app, err := db.Application(ctx, appID)
	if err != nil {
		t.Fatal(err)
	}
	next := app.Spec
	v := next.Services["api"]
	v.Volume = nil
	next.Services["api"] = v
	if _, err = db.Accept(ctx, p, app.Project, app.Environment, next, app.Revision, "remove-volume"); err != nil {
		t.Fatal(err)
	}
	var retained int
	if err = db.Pool.QueryRow(ctx, "SELECT sum(size_gib) FROM storage_reservations").Scan(&retained); err != nil || retained != 1 {
		t.Fatal("removed volume lost its reservation", retained, err)
	}
	// New app IDs cannot reuse the original budget even after workload removal.
	next.Name = "another-database"
	v.Volume = &spec.Volume{SizeGiB: 1, MountPath: "/data"}
	next.Services["api"] = v
	if _, err = db.Accept(ctx, p, app.Project, app.Environment, next, 0, "retained-budget"); err == nil {
		t.Fatal("retained volume quota bypass")
	}
}
