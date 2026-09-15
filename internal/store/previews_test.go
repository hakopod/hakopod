package store

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
)

func TestPreviewAcceptanceLimitsAndExpiry(t *testing.T) {
	db := isolatedDatabase(t)
	p := bootstrapPrincipal(t, db)
	ctx := context.Background()
	spec := emptyTestSpec()
	d, err := db.Accept(ctx, p, "demo", "development", spec, 0, "parent-first")
	if err != nil {
		t.Fatal(err)
	}
	claim, err := db.Claim(ctx)
	if err != nil || claim == nil {
		t.Fatal(err)
	}
	if err = claim.Finish(ctx, "succeeded", "", map[string]string{"status": "healthy"}); err != nil {
		t.Fatal(err)
	}
	claim.Release()
	parent, err := db.Application(ctx, d.ApplicationID)
	if err != nil {
		t.Fatal(err)
	}
	input := InitialPreview{Name: "pr-123", ParentRevision: parent.Revision, TTLHours: 24}
	first, err := db.AcceptPreview(ctx, p, parent, spec, input, "preview-first")
	if err != nil {
		t.Fatal(err)
	}
	again, err := db.AcceptPreview(ctx, p, parent, spec, input, "preview-first")
	if err != nil || first.ID != again.ID {
		t.Fatal("idempotency lost", err)
	}
	var previewID string
	if err = db.Pool.QueryRow(ctx, "SELECT id FROM previews WHERE application_id=$1", first.ApplicationID).Scan(&previewID); err != nil {
		t.Fatal(err)
	}
	if first.ApplicationID == parent.ID || first.Spec.Name == parent.Name {
		t.Fatal("preview did not isolate application")
	}
	tx, err := db.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = rejectPreviewBackup(ctx, tx, first.ApplicationID); err == nil {
		t.Fatal("preview backup accepted")
	}
	if err = rejectPreviewBackup(ctx, tx, parent.ID); err != nil {
		t.Fatal("ordinary application backup blocked", err)
	}
	_ = tx.Rollback(ctx)
	input.TTLHours = 12
	if _, err = db.AcceptPreview(ctx, p, parent, spec, input, "preview-first"); !errors.Is(err, ErrConflict) {
		t.Fatal("changed retry accepted", err)
	}
	input.TTLHours = 24
	var accepted atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			in := input
			in.Name = fmt.Sprintf("extra-%d", i)
			if _, e := db.AcceptPreview(ctx, p, parent, spec, in, fmt.Sprintf("extra-preview-%d", i)); e == nil {
				accepted.Add(1)
			}
		}(i)
	}
	wg.Wait()
	if accepted.Load() != 2 {
		t.Fatal("preview quota race", accepted.Load())
	}
	// Normal application updates cannot escape preview limits.
	oversized := first.Spec
	s := oversized.Services["api"]
	s.Replicas = 2
	oversized.Services["api"] = s
	if _, err = db.Accept(ctx, p, parent.Project, parent.Environment, oversized, 1, "preview-too-large"); err == nil {
		t.Fatal("preview size bypass")
	}
	if _, err = db.Pool.Exec(ctx, "UPDATE previews SET expires_at=now()-interval '1 second' WHERE id=$1", previewID); err != nil {
		t.Fatal(err)
	}
	expired, err := db.ClaimPreviewCleanup(ctx)
	if err != nil || expired == nil || expired.App.ID != first.ApplicationID {
		t.Fatal("expired preview unclaimable", err)
	}
	if other, err := db.ClaimPreviewCleanup(ctx); err != nil || other != nil {
		t.Fatal("duplicate cleanup claim", err)
	}
	if err = expired.Finish(ctx, "fixture transient failure"); err != nil {
		t.Fatal(err)
	}
	expired.Release()
	expired, err = db.ClaimPreviewCleanup(ctx)
	if err != nil || expired == nil {
		t.Fatal("cleanup did not resume", err)
	}
	if err = expired.Finish(ctx, ""); err != nil {
		t.Fatal(err)
	}
	expired.Release()
	record, err := db.Preview(ctx, previewID)
	if err != nil || record.State != "deleted" || record.ApplicationID != nil {
		t.Fatal(record, err)
	}
	if _, err = db.Application(ctx, parent.ID); err != nil {
		t.Fatal("parent removed by cleanup", err)
	}
}
