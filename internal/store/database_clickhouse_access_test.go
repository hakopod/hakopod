package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/database"
)

func TestClickHouseTenantAccessRequiresScopedSingleUseReview(t *testing.T) {
	s, p, d := databaseFixture(t)
	ctx := context.Background()
	d.Spec.Engine = "clickhouse"
	d.Spec.Version = "26.3"
	d.Spec.CPU = "500m"
	d.Spec.Memory = "2Gi"
	if _, err := s.AcceptDatabase(ctx, p, d, 0, "clickhouse-access-create", "create"); err != nil {
		t.Fatal(err)
	}
	claim, err := s.ClaimDatabaseOperation(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.RecordDatabaseStep(ctx, claim, database.Observation{Revision: 1, ObservedAt: time.Now().UTC(), Status: "ready"}, "succeeded", "ready", ""); err != nil {
		t.Fatal(err)
	}
	d, err = s.Database(ctx, p, d.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	next := d.Spec
	next.ClickHouse = &database.ClickHouseConfig{AccessProfile: "tenant_admin"}
	plan, err := database.PlanResize(d, next, nil, time.Now().UTC())
	if err != nil || len(plan.BlockedReasons) != 0 || len(plan.Warnings) == 0 {
		t.Fatal("tenant access plan lacks health or authority checks", err)
	}
	review, err := s.SaveDatabaseReview(ctx, p, d, "resize", plan, plan.ExpiresAt)
	if err != nil {
		t.Fatal(err)
	}
	d.Spec = next
	application := p
	application.Application = "unrelated-app"
	if _, err = s.AcceptDatabaseResize(ctx, application, d, 1, "clickhouse-app-denied", review); !errors.Is(err, ErrForbidden) {
		t.Fatal("application-scoped identity could grant tenant administration", err)
	}
	if _, err = s.AcceptDatabase(ctx, p, d, 1, "clickhouse-unreviewed", "resize"); !errors.Is(err, ErrInput) {
		t.Fatal("tenant administration bypassed review", err)
	}
	changed := d
	changed.Spec.ClickHouse = &database.ClickHouseConfig{AccessProfile: "application"}
	if _, err = s.AcceptDatabaseResize(ctx, p, changed, 1, "clickhouse-changed", review); !errors.Is(err, ErrConflict) {
		t.Fatal("changed profile reused a review", err)
	}
	op, err := s.AcceptDatabaseResize(ctx, p, d, 1, "clickhouse-reviewed", review)
	if err != nil {
		t.Fatal(err)
	}
	if op.Review == nil || !op.Review.Proposed.ClickHouseTenantAdmin() {
		t.Fatal("approved access profile missing from durable operation")
	}
	if _, err = s.AcceptDatabaseResize(ctx, p, d, 1, "clickhouse-review-reused", review); !errors.Is(err, ErrConflict) {
		t.Fatal("tenant administration review was reused", err)
	}
}
