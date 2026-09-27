package store

import (
	"context"
	"github.com/hakopod/hakopod/internal/spec"
	"github.com/jackc/pgx/v5"
	"testing"
)

func TestDatabaseAndApplicationAllocationSerialize(t *testing.T) {
	for _, budget := range []string{"memory", "storage"} {
		t.Run(budget, func(t *testing.T) {
			s, p, d := databaseFixture(t)
			ctx := context.Background()
			s.ValidateDeployment = func(context.Context, Application, spec.Application) error { return nil }
			if budget == "memory" {
				s.ComputeBudget = func(context.Context, pgx.Tx, string, string) (int64, error) { return 1024 << 20, nil }
			} else {
				s.StorageBudget = func(context.Context, string, string) (int64, error) { return 1, nil }
			}
			app := emptyTestSpec()
			svc := app.Services["api"]
			svc.Volume = &spec.Volume{SizeGiB: 1, MountPath: "/data"}
			app.Services["api"] = svc
			start := make(chan struct{})
			results := make(chan error, 2)
			go func() {
				<-start
				_, err := s.Accept(ctx, p, d.Project, d.Environment, app, 0, "allocation-app")
				results <- err
			}()
			go func() {
				<-start
				_, err := s.AcceptDatabase(ctx, p, d, 0, "allocation-database", "create")
				results <- err
			}()
			close(start)
			accepted := 0
			for range 2 {
				if err := <-results; err == nil {
					accepted++
				}
			}
			if accepted != 1 {
				t.Fatalf("accepted %d allocations with capacity for one", accepted)
			}
		})
	}
}

func TestAllocationRetainsLiveAndFailedDeploymentMemory(t *testing.T) {
	s, p, d := databaseFixture(t)
	ctx := context.Background()
	s.ValidateDeployment = func(context.Context, Application, spec.Application) error { return nil }
	app := emptyTestSpec()
	svc := app.Services["api"]
	svc.Resources = &spec.Resources{MemoryLimit: "512Mi"}
	app.Services["api"] = svc
	first, err := s.Accept(ctx, p, d.Project, d.Environment, app, 0, "allocation-first")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Pool.Exec(ctx, "UPDATE deployments SET status='succeeded',resolved_spec=spec WHERE id=$1", first.ID); err != nil {
		t.Fatal(err)
	}
	svc.Resources = &spec.Resources{MemoryLimit: "128Mi"}
	app.Services["api"] = svc
	second, err := s.Accept(ctx, p, d.Project, d.Environment, app, 1, "allocation-smaller")
	if err != nil {
		t.Fatal(err)
	}
	check := func(want int64) {
		t.Helper()
		tx, err := s.Pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback(ctx)
		got, err := allocationMemory(ctx, tx, d.Project, d.Environment, "", nil, "")
		if err != nil || got != want {
			t.Fatalf("reserved %d want %d: %v", got, want, err)
		}
	}
	check(562 << 20)
	if _, err = s.Pool.Exec(ctx, "UPDATE deployments SET status='failed' WHERE id=$1", second.ID); err != nil {
		t.Fatal(err)
	}
	check(562 << 20)
	if _, err = s.Pool.Exec(ctx, "UPDATE deployments SET status='succeeded',resolved_spec=spec WHERE id=$1", second.ID); err != nil {
		t.Fatal(err)
	}
	check(178 << 20)
}
