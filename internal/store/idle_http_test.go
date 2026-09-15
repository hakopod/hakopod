package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestIdleMutationSerializesWithEditsAndRuntimeClaims(t *testing.T) {
	db := isolatedDatabase(t)
	p := bootstrapPrincipal(t, db)
	ctx := context.Background()
	input := emptyTestSpec()
	d, err := db.Accept(ctx, p, "demo", "development", input, 0, "idle-initial")
	if err != nil {
		t.Fatal(err)
	}
	claim, err := db.Claim(ctx)
	if err != nil || claim == nil {
		t.Fatal(err)
	}
	if err = db.WithRuntimeApplication(ctx, d.ApplicationID, func(Application) error { t.Error("entered active rollout"); return nil }); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	if err = claim.Finish(ctx, "succeeded", "", map[string]any{"status": "healthy"}); err != nil {
		t.Fatal(err)
	}
	claim.Release()
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		done <- db.WithRuntimeApplication(ctx, d.ApplicationID, func(app Application) error {
			if app.Status != "healthy" {
				return errors.New("not healthy")
			}
			close(entered)
			<-release
			return nil
		})
	}()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("mutation blocked")
	}
	edited := make(chan error, 1)
	go func() { _, err := db.Accept(ctx, p, "demo", "development", input, 1, "idle-next"); edited <- err }()
	select {
	case err := <-edited:
		t.Fatal("edit overtook runtime mutation", err)
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	if err = <-done; err != nil {
		t.Fatal(err)
	}
	if err = <-edited; err != nil {
		t.Fatal(err)
	}
	if err = db.WithRuntimeApplication(ctx, d.ApplicationID, func(app Application) error {
		if app.Revision != 2 || app.Status != "queued" {
			t.Fatal("callback saw stale revision")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
