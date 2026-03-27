package store

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/spec"
	"github.com/jackc/pgx/v5"
)

func emptyTestSpec() spec.Application {
	app, err := spec.Normalize(spec.Application{Name: "store-test", Services: map[string]spec.Service{"api": {Image: "python:3.13-alpine", Port: 8080}}})
	if err != nil {
		panic(err)
	}
	return app
}

func isolatedDatabase(t *testing.T) *Store {
	t.Helper()
	dsn := os.Getenv("HAKOPOD_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set HAKOPOD_TEST_DATABASE_URL for isolated real PostgreSQL store checks")
	}
	connectCtx, stop := context.WithTimeout(context.Background(), 3*time.Second)
	admin, err := pgx.Connect(connectCtx, dsn)
	stop()
	if err != nil {
		t.Skip("PostgreSQL is temporarily unavailable; rerun these isolated store checks when it is reachable")
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = admin.Close(ctx)
	})
	name := "hakopod_store_test_" + NewID()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{name}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal("invalid test PostgreSQL URL")
	}
	u.Path = "/" + name
	db, err := Open(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		db.Close()
		cleanupCtx, done := context.WithTimeout(context.Background(), 5*time.Second)
		defer done()
		_, _ = admin.Exec(cleanupCtx, "DROP DATABASE "+pgx.Identifier{name}.Sanitize()+" WITH (FORCE)")
	})
	if err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	return db
}

func bootstrapPrincipal(t *testing.T, db *Store) Principal {
	t.Helper()
	ctx := context.Background()
	raw, err := db.Bootstrap(ctx, "store-integration")
	if err != nil {
		t.Fatal(err)
	}
	p, err := db.Authenticate(ctx, raw)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestClaimCanonicalArtifactsFinalizationAndConnectionLoss(t *testing.T) {
	db := isolatedDatabase(t)
	p := bootstrapPrincipal(t, db)
	ctx := context.Background()
	input := emptyTestSpec()
	accepted, err := db.Accept(ctx, p, "demo", "development", input, 0, "initial-store-claim")
	if err != nil {
		t.Fatal(err)
	}
	claim, err := db.Claim(ctx)
	if err != nil || claim == nil {
		t.Fatalf("claim failed: %v", err)
	}
	defer claim.Release()
	first, _ := spec.Normalize(input)
	service := first.Services["api"]
	service.Image = "docker.io/library/python@sha256:" + strings.Repeat("a", 64)
	first.Services["api"] = service
	second, _ := spec.Normalize(first)
	service = second.Services["api"]
	service.Image = "docker.io/library/python@sha256:" + strings.Repeat("b", 64)
	second.Services["api"] = service
	stored, err := claim.SetResolved(ctx, first)
	if err != nil {
		t.Fatal(err)
	}
	winner, err := claim.SetResolved(ctx, second)
	if err != nil {
		t.Fatal(err)
	}
	if winner.Services["api"].Image != stored.Services["api"].Image {
		t.Fatal("second resolver replaced the canonical immutable artifact")
	}
	if err := claim.Finish(ctx, "succeeded", "", map[string]any{"status": "healthy"}); err != nil {
		t.Fatal(err)
	}
	if err := claim.Finish(ctx, "failed", "stale result", map[string]any{}); !errors.Is(err, ErrClaimLost) {
		t.Fatalf("terminal result overwritten: %v", err)
	}
	claim.Release()
	finished, err := db.Deployment(ctx, accepted.ID)
	if err != nil || finished.Status != "succeeded" {
		t.Fatalf("incorrect final state: %+v %v", finished, err)
	}
	rollback, err := db.Accept(ctx, p, "demo", "development", first, 1, "seeded-rollback-store", first)
	if err != nil {
		t.Fatal(err)
	}
	if rollback.ResolvedSpec == nil || rollback.ResolvedSpec.Services["api"].Image != first.Services["api"].Image {
		t.Fatal("trusted rollback artifact was not committed atomically")
	}
	if _, err := db.Accept(ctx, p, "demo", "development", first, 1, "seeded-rollback-store"); !errors.Is(err, ErrConflict) {
		t.Fatalf("rollback intent not included in idempotency hash: %v", err)
	}
	broken, err := db.Claim(ctx)
	if err != nil || broken == nil {
		t.Fatalf("claim seeded rollback: %v", err)
	}
	defer broken.Release()
	var pid uint32
	if err := broken.Conn.QueryRow(ctx, "SELECT pg_backend_pid()").Scan(&pid); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(ctx, "SELECT pg_terminate_backend($1)", pid); err != nil {
		t.Fatal(err)
	}
	if err := broken.Check(ctx); err == nil {
		t.Fatal("terminated backend retained usable claim")
	}
	if _, err := broken.SetResolved(ctx, second); err == nil {
		t.Fatal("lost claim stored an artifact")
	}
	if err := broken.Finish(ctx, "succeeded", "", map[string]any{}); err == nil {
		t.Fatal("lost claim finalized via a different pooled connection")
	}
	broken.Release()
	resumed, err := db.Claim(ctx)
	if err != nil || resumed == nil {
		t.Fatalf("operation did not resume after connection loss: %v", err)
	}
	defer resumed.Release()
	if resumed.Deployment.ID != rollback.ID || resumed.Deployment.ResolvedSpec == nil {
		t.Fatal("resume lost operation identity or seeded digests")
	}
	if err := resumed.Finish(ctx, "succeeded", "", map[string]any{"status": "healthy"}); err != nil {
		t.Fatal(err)
	}
}

func TestConcurrentApplicationLimitAndBoundedPages(t *testing.T) {
	db := isolatedDatabase(t)
	p := bootstrapPrincipal(t, db)
	ctx := context.Background()
	input := emptyTestSpec()
	if _, err := db.Pool.Exec(ctx, "INSERT INTO applications(id,project,environment,name,spec) SELECT 'existing-'||n,'demo','development','existing-'||n,$1 FROM generate_series(1,199) n", JSON(input)); err != nil {
		t.Fatal(err)
	}
	var accepted atomic.Int32
	var group sync.WaitGroup
	for _, name := range []string{"last-a", "last-b"} {
		group.Add(1)
		go func(name string) {
			defer group.Done()
			app, _ := spec.Normalize(input)
			app.Name = name
			if _, err := db.Accept(ctx, p, "demo", "development", app, 0, "limit-test-"+name); err == nil {
				accepted.Add(1)
			} else if !strings.Contains(err.Error(), "200 applications") {
				t.Errorf("unexpected acceptance error: %v", err)
			}
		}(name)
	}
	group.Wait()
	if accepted.Load() != 1 {
		t.Fatalf("concurrent application cap allowed %d new apps", accepted.Load())
	}
	if _, err := db.Pool.Exec(ctx, "INSERT INTO projects(name) VALUES('paging'); INSERT INTO environments(project,name) VALUES('paging','development')"); err != nil {
		t.Fatal(err)
	}
	large, _ := spec.Normalize(input)
	service := large.Services["api"]
	service.Env = make(map[string]string)
	for i := 0; i < 8; i++ {
		service.Env[fmt.Sprintf("ITEM_%d", i)] = strings.Repeat("<", 4000)
	}
	large.Services["api"] = service
	large, err := spec.Normalize(large)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if _, err := db.Pool.Exec(ctx, "INSERT INTO applications(id,project,environment,name,spec) VALUES($1,'paging','development',$2,$3)", fmt.Sprintf("page-%d", i), fmt.Sprintf("large-%d", i), JSON(large)); err != nil {
			t.Fatal(err)
		}
	}
	items, cursor, err := db.ApplicationPage(ctx, "paging", "development", "")
	if err != nil {
		t.Fatal(err)
	}
	var bytes int
	for _, app := range items {
		bytes += len(JSON(app.Spec)) + len(JSON(app.Observed))
	}
	if len(items) != 2 || cursor == "" || bytes > 512<<10 {
		t.Fatalf("page exceeded serialized budget: count=%d bytes=%d cursor=%q", len(items), bytes, cursor)
	}
	remaining, next, err := db.ApplicationPage(ctx, "paging", "development", cursor)
	if err != nil || len(remaining) != 1 || next != "" {
		t.Fatalf("keyset continuation failed: count=%d next=%q err=%v", len(remaining), next, err)
	}
	smallPage, more, err := db.ApplicationPage(ctx, "demo", "development", "")
	if err != nil || len(smallPage) != 25 || more == "" {
		t.Fatalf("item bound failed: count=%d next=%q err=%v", len(smallPage), more, err)
	}
}
