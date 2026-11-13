package store

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

func TestPlatformMigrationLifecycleErasesSecrets(t *testing.T) {
	db := isolatedDatabase(t)
	ctx := context.Background()
	if _, err := db.Pool.Exec(ctx, "INSERT INTO identities(id,name) VALUES('owner','Owner')"); err != nil {
		t.Fatal(err)
	}
	m, err := db.CreatePlatformMigration(ctx, PlatformMigration{ID: "m1", IdentityID: "owner", Project: "demo", Environment: "prod",
		ApplicationName: "shop", Provider: "railway", SourceProjectID: "p1", Proposal: json.RawMessage(`{}`)}, []byte("sealed"))
	if err != nil || m.Status != "planned" || !m.HasToken {
		t.Fatal(m, err)
	}
	if _, err = db.QueuePlatformMigration(ctx, "m1", "short"); !errors.Is(err, ErrInput) {
		t.Fatal("a short idempotency key must be refused", err)
	}
	if m, err = db.QueuePlatformMigration(ctx, "m1", "start-key-1"); err != nil || m.Status != "queued" {
		t.Fatal(m, err)
	}
	if m, err = db.QueuePlatformMigration(ctx, "m1", "start-key-1"); err != nil || m.Status != "queued" {
		t.Fatal("repeating the start with the same key must return the migration", err)
	}
	if _, err = db.QueuePlatformMigration(ctx, "m1", "another-key"); !errors.Is(err, ErrConflict) {
		t.Fatal("a different key must conflict", err)
	}
	if m, err = db.ClaimPlatformMigration(ctx, "lease-a"); err != nil || m.ID != "m1" || m.Status != "running" {
		t.Fatal(m, err)
	}
	if ok, err := db.HeartbeatPlatformMigration(ctx, "m1", "lease-b"); ok || err != nil {
		t.Fatal("another lease must not heartbeat", ok, err)
	}
	if err = db.RecordPlatformMigrationSteps(ctx, "m1", "lease-a", json.RawMessage(`[{"name":"secrets","status":"succeeded"}]`), "app1"); err != nil {
		t.Fatal(err)
	}
	if _, err = db.CancelPlatformMigration(ctx, "m1"); err != nil {
		t.Fatal(err)
	}
	if ok, _ := db.HeartbeatPlatformMigration(ctx, "m1", "lease-a"); ok {
		t.Fatal("a cancelled migration must tell its worker to stop")
	}
	if err = db.FinishPlatformMigration(ctx, "m1", "lease-a", "cancelled", "stopped by the user"); err != nil {
		t.Fatal(err)
	}
	token, variables, err := db.PlatformMigrationSecrets(ctx, "m1")
	if err != nil || token != nil || variables != nil {
		t.Fatal("finishing must erase the sealed token and variables", err)
	}
	if m, _ = db.PlatformMigration(ctx, "m1"); m.Status != "cancelled" || m.ApplicationID != "app1" || m.HasToken {
		t.Fatal(m)
	}
}

func TestExpiredPlannedMigrationFailsAndForgetsToken(t *testing.T) {
	db := isolatedDatabase(t)
	ctx := context.Background()
	if _, err := db.Pool.Exec(ctx, "INSERT INTO identities(id,name) VALUES('owner','Owner')"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.CreatePlatformMigration(ctx, PlatformMigration{ID: "m2", IdentityID: "owner", Project: "demo", Environment: "prod",
		ApplicationName: "web", Provider: "vercel", SourceProjectID: "prj", Proposal: json.RawMessage(`{}`)}, []byte("sealed")); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(ctx, "UPDATE platform_migrations SET expires_at=now()-interval '1 minute' WHERE id='m2'"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.QueuePlatformMigration(ctx, "m2", "start-key-2"); err == nil {
		t.Fatal("an expired migration must not start")
	}
	if err := db.ExpirePlatformMigrations(ctx); err != nil {
		t.Fatal(err)
	}
	m, _ := db.PlatformMigration(ctx, "m2")
	token, _, _ := db.PlatformMigrationSecrets(ctx, "m2")
	if m.Status != "failed" || token != nil {
		t.Fatal("an expired planned migration must fail and forget its token", m.Status)
	}
}
