package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/database"
	"github.com/jackc/pgx/v5"
)

func databaseFixture(t *testing.T) (*Store, Principal, database.Resource) {
	t.Helper()
	s := isolatedDatabase(t)
	p := bootstrapPrincipal(t, s)
	d := database.Resource{ID: NewID(), Project: "demo", Environment: "development", Spec: database.Spec{SchemaVersion: 1, Name: "database-development-fixture", Engine: "postgresql", Version: "17", Mode: "standalone", Shards: 1, CPU: "100m", Memory: "256Mi", StorageGiB: 1}, EncryptedCredentials: []byte("sealed-test-fixture")}
	return s, p, d
}
func TestManagedDatabaseIdempotencyScopeAndLeases(t *testing.T) {
	s, p, d := databaseFixture(t)
	ctx := context.Background()
	accepted, err := s.AcceptDatabase(ctx, p, d, 0, "create-database-fixture", "create")
	if err != nil {
		t.Fatal(err)
	}
	replay, err := s.AcceptDatabase(ctx, p, d, 0, "create-database-fixture", "create")
	if err != nil || replay.ID != accepted.ID {
		t.Fatal("creation replay", err)
	}
	changed := d
	changed.Spec.Memory = "512Mi"
	if _, err = s.AcceptDatabase(ctx, p, changed, 0, "create-database-fixture", "create"); !errors.Is(err, ErrConflict) {
		t.Fatal("changed replay accepted", err)
	}
	applicationKey := p
	applicationKey.Application = "unrelated-app"
	if _, err = s.Database(ctx, applicationKey, d.ID, false); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatal("application key read independent database", err)
	}
	if _, err = s.AcceptDatabase(ctx, applicationKey, d, 0, "app-key-create", "create"); !errors.Is(err, ErrForbidden) {
		t.Fatal("application key created database", err)
	}
	claim, err := s.ClaimDatabaseOperation(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.ClaimDatabaseOperation(ctx); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatal("concurrent lane claimed", err)
	}
	stale := claim
	stale.Lease = NewID()
	if err = s.RecordDatabaseStep(ctx, stale, database.Observation{}, "succeeded", "ready", ""); !errors.Is(err, ErrConflict) {
		t.Fatal("stale lease committed", err)
	}
	second := d
	second.ID = NewID()
	second.Spec.Name = "other-development-fixture"
	other, err := s.AcceptDatabase(ctx, p, second, 0, "create-other-fixture", "create")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.RecordDatabaseStep(ctx, claim, database.Observation{}, "queued", "provisioning", ""); err != nil {
		t.Fatal(err)
	}
	next, err := s.ClaimDatabaseOperation(ctx)
	if err != nil || next.ID != other.ID {
		t.Fatal("waiting operation starved newer resource", err)
	}
	if err = s.RecordDatabaseStep(ctx, next, database.Observation{}, "succeeded", "ready", ""); err != nil {
		t.Fatal(err)
	}
}
func TestManagedDatabaseResizeReviewIsBoundAndSingleUse(t *testing.T) {
	s, p, d := databaseFixture(t)
	ctx := context.Background()
	if _, err := s.AcceptDatabase(ctx, p, d, 0, "create-reviewed-fixture", "create"); err != nil {
		t.Fatal(err)
	}
	claim, err := s.ClaimDatabaseOperation(ctx)
	if err != nil {
		t.Fatal(err)
	}
	observation := database.Observation{Revision: 1, ObservedAt: time.Now().UTC(), Status: "ready"}
	if err = s.RecordDatabaseStep(ctx, claim, observation, "succeeded", "ready", ""); err != nil {
		t.Fatal(err)
	}
	d, err = s.Database(ctx, p, d.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	next := d.Spec
	next.CPU = "200m"
	plan, err := database.PlanResize(d, next, nil, time.Now().UTC())
	if err != nil || len(plan.BlockedReasons) > 0 {
		t.Fatal("valid plan", plan, err)
	}
	review, err := s.SaveDatabaseReview(ctx, p, d, "resize", plan, plan.ExpiresAt)
	if err != nil {
		t.Fatal(err)
	}
	d.Spec = next
	if _, err = s.AcceptDatabase(ctx, p, d, 1, "unreviewed-resize", "resize"); !errors.Is(err, ErrInput) {
		t.Fatal("resize bypassed review", err)
	}
	changed := d
	changed.Spec.CPU = "300m"
	if _, err = s.AcceptDatabaseResize(ctx, p, changed, 1, "changed-resize", review); !errors.Is(err, ErrConflict) {
		t.Fatal("review authorized changed spec", err)
	}
	op, err := s.AcceptDatabaseResize(ctx, p, d, 1, "reviewed-resize", review)
	if err != nil {
		t.Fatal(err)
	}
	if op.Review == nil || op.Review.Proposed != next {
		t.Fatal("immutable review missing")
	}
	replay, err := s.AcceptDatabaseResize(ctx, p, d, 1, "reviewed-resize", review)
	if err != nil || replay.ID != op.ID {
		t.Fatal("resize replay", err)
	}
	if _, err = s.AcceptDatabaseResize(ctx, p, d, 1, "reuse-resize-review", review); !errors.Is(err, ErrConflict) {
		t.Fatal("review reused", err)
	}
}

func TestManagedDatabaseNameIsReusableOnlyAfterDeletionCompletes(t *testing.T) {
	s, p, d := databaseFixture(t)
	ctx := context.Background()
	if _, err := s.AcceptDatabase(ctx, p, d, 0, "create-reusable-fixture", "create"); err != nil {
		t.Fatal(err)
	}
	claim, err := s.ClaimDatabaseOperation(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.RecordDatabaseStep(ctx, claim, database.Observation{}, "succeeded", "ready", ""); err != nil {
		t.Fatal(err)
	}
	if _, err = s.AcceptDatabase(ctx, p, d, 1, "delete-reusable-fixture", "delete"); err != nil {
		t.Fatal(err)
	}
	replacement := d
	replacement.ID = NewID()
	if _, err = s.AcceptDatabase(ctx, p, replacement, 0, "replace-before-deletion", "create"); !errors.Is(err, ErrConflict) {
		t.Fatal("name reused before reclamation", err)
	}
	claim, err = s.ClaimDatabaseOperation(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.RecordDatabaseStep(ctx, claim, database.Observation{}, "succeeded", "deleted", ""); err != nil {
		t.Fatal(err)
	}
	if _, err = s.AcceptDatabase(ctx, p, replacement, 0, "replace-after-deletion", "create"); err != nil {
		t.Fatal("deleted name stayed reserved", err)
	}
	if _, err = s.Database(ctx, p, d.ID, false); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatal("old resource identity became reachable", err)
	}
	current, err := s.Database(ctx, p, replacement.ID, false)
	if err != nil || current.ID == d.ID {
		t.Fatal("replacement reused historical identity", err)
	}
	operation, err := s.DatabaseOperation(ctx, p, claim.ID)
	if err != nil || operation.DatabaseID != d.ID || operation.Status != "succeeded" {
		t.Fatal("deleted database operation disappeared", err)
	}
	for _, limited := range []Principal{
		{Project: "elsewhere", Environment: "development", Permissions: []string{"deployments:read"}, Admin: true},
		{Project: "demo", Environment: "production", Permissions: []string{"deployments:read"}, Admin: true},
		{Project: "demo", Environment: "development", Application: "app", Permissions: []string{"deployments:read"}, Admin: true},
	} {
		if _, err := s.DatabaseOperation(ctx, limited, claim.ID); !errors.Is(err, pgx.ErrNoRows) {
			t.Fatal("operation scope leaked", err)
		}
	}
}
