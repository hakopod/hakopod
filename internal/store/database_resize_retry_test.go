package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/database"
	"github.com/jackc/pgx/v5"
)

func failedMySQLResizeFixture(t *testing.T) (*Store, Principal, database.Resource, database.Operation) {
	t.Helper()
	s, p, d := databaseFixture(t)
	d.Spec.Engine, d.Spec.Version, d.Spec.Mode = "mysql", "8.4", "cluster"
	d.Spec.Replicas, d.Spec.CPU, d.Spec.Memory = 2, "500m", "1Gi"
	ctx := context.Background()
	if _, err := s.AcceptDatabase(ctx, p, d, 0, "mysql-retry-create", "create"); err != nil {
		t.Fatal(err)
	}
	claim, err := s.ClaimDatabaseOperation(ctx)
	if err != nil {
		t.Fatal(err)
	}
	ready := database.Observation{Revision: 1, ObservedAt: time.Now().UTC(), Status: "ready", TopologyFingerprint: "prior-topology"}
	if err = s.RecordDatabaseStep(ctx, claim, ready, "succeeded", "ready", ""); err != nil {
		t.Fatal(err)
	}
	d, err = s.Database(ctx, p, d.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	next := d.Spec
	next.Replicas = 4
	plan, err := database.PlanResize(d, next, nil, time.Now().UTC())
	if err != nil || len(plan.BlockedReasons) != 0 {
		t.Fatalf("resize plan: %+v %v", plan, err)
	}
	reviewID, err := s.SaveDatabaseReview(ctx, p, d, "resize", plan, plan.ExpiresAt)
	if err != nil {
		t.Fatal(err)
	}
	d.Spec = next
	if _, err = s.AcceptDatabaseResize(ctx, p, d, 1, "mysql-resize-fails", reviewID); err != nil {
		t.Fatal(err)
	}
	claim, err = s.ClaimDatabaseOperation(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.RecordDatabaseStep(ctx, claim, ready, "failed", "review", "review expired"); err != nil {
		t.Fatal(err)
	}
	d, err = s.Database(ctx, p, d.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	return s, p, d, claim
}

func saveResizeRetryReview(t *testing.T, s *Store, p Principal, d database.Resource, source database.Operation) string {
	t.Helper()
	resize := *source.Review
	resize.ExpiresAt = time.Now().UTC().Add(database.ReviewLifetime)
	resize.TopologyFingerprint = strings.Repeat("a", 64)
	plan := database.ResizeRetryReview{OperationID: source.ID, DatabaseID: d.ID, Revision: d.Revision, State: "prior", Resize: resize, ExpiresAt: resize.ExpiresAt}
	id, err := s.SaveDatabaseReview(context.Background(), p, d, "resize-retry", plan, plan.ExpiresAt)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestDatabaseResizeRetryPreservesRevisionAllocationAndReplay(t *testing.T) {
	s, p, d, source := failedMySQLResizeFixture(t)
	ctx := context.Background()
	reviewID := saveResizeRetryReview(t, s, p, d, source)
	var beforeCPU, beforeMemory, beforeStorage int64
	if err := s.Pool.QueryRow(ctx, "SELECT reserved_cpu_milli,reserved_memory_bytes,reserved_storage_gib FROM managed_databases WHERE id=$1", d.ID).Scan(&beforeCPU, &beforeMemory, &beforeStorage); err != nil {
		t.Fatal(err)
	}
	op, err := s.AcceptDatabaseResizeRetry(ctx, p, d.ID, source.ID, reviewID, d.Revision, "mysql-retry-attempt")
	if err != nil {
		t.Fatal(err)
	}
	if op.ID == source.ID || op.Kind != "resize-retry" || op.Revision != d.Revision || !op.Spec.Equal(d.Spec) {
		t.Fatalf("unexpected retry operation: %+v", op)
	}
	claim, err := s.ClaimDatabaseOperation(ctx)
	if err != nil || claim.ID != op.ID {
		t.Fatalf("claim retry: %+v %v", claim, err)
	}
	if err = s.RecordDatabaseStep(ctx, claim, d.Observation, "failed", "reconcile", "still unavailable"); err != nil {
		t.Fatal(err)
	}
	replay, err := s.AcceptDatabaseResizeRetry(ctx, p, d.ID, source.ID, reviewID, d.Revision, "mysql-retry-attempt")
	if err != nil || replay.ID != op.ID || replay.Status != "failed" {
		t.Fatalf("terminal retry replay: %+v %v", replay, err)
	}
	var revision, afterCPU, afterMemory, afterStorage int64
	if err = s.Pool.QueryRow(ctx, "SELECT revision,reserved_cpu_milli,reserved_memory_bytes,reserved_storage_gib FROM managed_databases WHERE id=$1", d.ID).Scan(&revision, &afterCPU, &afterMemory, &afterStorage); err != nil {
		t.Fatal(err)
	}
	if revision != d.Revision || beforeCPU != afterCPU || beforeMemory != afterMemory || beforeStorage != afterStorage {
		t.Fatal("retry changed revision or retained allocation")
	}
	if _, err = s.AcceptDatabaseResizeRetry(ctx, p, d.ID, source.ID, NewID(), d.Revision, "mysql-retry-attempt"); !errors.Is(err, ErrConflict) {
		t.Fatal("changed idempotent replay accepted", err)
	}
	nextReview := saveResizeRetryReview(t, s, p, d, replay)
	if _, err = s.AcceptDatabaseResizeRetry(ctx, p, d.ID, source.ID, nextReview, d.Revision, "mysql-second-retry"); !errors.Is(err, ErrConflict) {
		t.Fatal("superseded failed source accepted", err)
	}
	second, err := s.AcceptDatabaseResizeRetry(ctx, p, d.ID, replay.ID, nextReview, d.Revision, "mysql-second-retry")
	if err != nil || second.Kind != "resize-retry" || second.ID == replay.ID {
		t.Fatalf("retry of failed retry: %+v %v", second, err)
	}
}

func TestDatabaseResizeRetrySerializesAttemptsAndBindsPrincipal(t *testing.T) {
	s, p, d, source := failedMySQLResizeFixture(t)
	ctx := context.Background()
	reviewOne := saveResizeRetryReview(t, s, p, d, source)
	reviewTwo := saveResizeRetryReview(t, s, p, d, source)
	application := p
	application.Application = "app"
	if _, err := s.AcceptDatabaseResizeRetry(ctx, application, d.ID, source.ID, reviewOne, d.Revision, "app-retry-attempt"); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatal("application credential retried resize", err)
	}
	other := p
	other.ID = NewID()
	if _, err := s.Pool.Exec(ctx, "INSERT INTO identities(id,name) VALUES($1,'other-retry-actor')", other.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AcceptDatabaseResizeRetry(ctx, other, d.ID, source.ID, reviewOne, d.Revision, "other-retry-attempt"); !errors.Is(err, ErrConflict) {
		t.Fatal("another principal consumed the review", err)
	}
	type result struct {
		op  database.Operation
		err error
	}
	results := make(chan result, 2)
	var wg sync.WaitGroup
	for _, attempt := range []struct{ review, idem string }{{reviewOne, "concurrent-retry-one"}, {reviewTwo, "concurrent-retry-two"}} {
		wg.Add(1)
		go func(review, idem string) {
			defer wg.Done()
			op, err := s.AcceptDatabaseResizeRetry(ctx, p, d.ID, source.ID, review, d.Revision, idem)
			results <- result{op, err}
		}(attempt.review, attempt.idem)
	}
	wg.Wait()
	close(results)
	succeeded, failed := 0, 0
	for result := range results {
		if result.err == nil {
			succeeded++
		} else {
			failed++
		}
	}
	if succeeded != 1 || failed != 1 {
		t.Fatalf("concurrent retry results: succeeded=%d failed=%d", succeeded, failed)
	}
}

func TestDatabaseResizeRetryRequiresCurrentKeyAdmission(t *testing.T) {
	s, p, d, source := failedMySQLResizeFixture(t)
	reviewID := saveResizeRetryReview(t, s, p, d, source)
	s.RequireDatabaseAdmission = true
	s.AdmitDatabase = func(_ context.Context, _ pgx.Tx, admitted Principal, _, _, _ string) error {
		if admitted.KeyID != p.KeyID {
			return ErrUnauthorized
		}
		return nil
	}
	stale := p
	stale.KeyID = NewID()
	if _, err := s.AcceptDatabaseResizeRetry(context.Background(), stale, d.ID, source.ID, reviewID, d.Revision, "stale-key-retry"); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("stale key passed fresh admission", err)
	}
}

func TestDatabaseResizeRetryRejectsMalformedReview(t *testing.T) {
	s, p, d, source := failedMySQLResizeFixture(t)
	resize := *source.Review
	resize.ExpiresAt = time.Now().UTC().Add(database.ReviewLifetime)
	for _, fingerprint := range []string{"", "accepted-revision:999:", "not-native"} {
		resize.TopologyFingerprint = fingerprint
		plan := database.ResizeRetryReview{OperationID: source.ID, DatabaseID: d.ID, Revision: d.Revision, State: "prior", Resize: resize, ExpiresAt: resize.ExpiresAt}
		if _, err := s.SaveDatabaseReview(context.Background(), p, d, "resize-retry", plan, plan.ExpiresAt); !errors.Is(err, ErrInput) {
			t.Fatalf("malformed fingerprint %q accepted: %v", fingerprint, err)
		}
	}
	resize.TopologyFingerprint = strings.Repeat("b", 64)
	resize.BlockedReasons = []string{"blocked"}
	plan := database.ResizeRetryReview{OperationID: source.ID, DatabaseID: d.ID, Revision: d.Revision, State: "prior", Resize: resize, ExpiresAt: resize.ExpiresAt}
	if _, err := s.SaveDatabaseReview(context.Background(), p, d, "resize-retry", plan, plan.ExpiresAt); !errors.Is(err, ErrInput) {
		t.Fatal("blocked retry review accepted", err)
	}
	resize.BlockedReasons = []string{}
	resize.TopologyFingerprint = fmt.Sprintf("accepted-revision:%d:", d.Revision+1)
	plan = database.ResizeRetryReview{OperationID: source.ID, DatabaseID: d.ID, Revision: d.Revision, State: "accepted", Resize: resize, ExpiresAt: resize.ExpiresAt}
	if _, err := s.SaveDatabaseReview(context.Background(), p, d, "resize-retry", plan, plan.ExpiresAt); !errors.Is(err, ErrInput) {
		t.Fatal("wrong accepted-revision sentinel accepted", err)
	}
}

func TestDatabaseResizeRetryRejectsExpiredReview(t *testing.T) {
	s, p, d, source := failedMySQLResizeFixture(t)
	reviewID := saveResizeRetryReview(t, s, p, d, source)
	if _, err := s.Pool.Exec(context.Background(), "UPDATE managed_database_reviews SET expires_at=now()-interval '1 second' WHERE id=$1", reviewID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AcceptDatabaseResizeRetry(context.Background(), p, d.ID, source.ID, reviewID, d.Revision, "expired-retry-attempt"); !errors.Is(err, ErrConflict) {
		t.Fatal("expired retry review accepted", err)
	}
}
