package store

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/managedplatform"
)

func TestNeonControllerStatePersistsBootstrapAndFencesRevision(t *testing.T) {
	s, principal, item, plan := managedPlatformFixture(t)
	ctx := context.Background()
	review := managedPlatformReview(t, s, principal, item, plan, 0, "create")
	if _, err := s.AcceptManagedPlatform(ctx, principal, item, plan, []byte("sealed-runtime"), review, 0, "controller-state-create", "create"); err != nil {
		t.Fatal(err)
	}
	tenant := strings.Repeat("a", 32)
	next := managedplatform.NeonControllerState{Attach: &managedplatform.NeonAttachNotification{TenantID: tenant, Shards: []managedplatform.NeonAttachShard{{NodeID: 1}}}}
	called := 0
	apply := func(input NeonControllerContext) (managedplatform.NeonControllerState, bool, error) {
		called++
		if input.Operation.PlatformID != item.ID || input.Operation.Revision != 1 || len(input.Claims) != 0 || input.PendingCompute {
			t.Fatal("bootstrap context is not fenced to the empty platform")
		}
		return next, true, nil
	}
	if applied, err := s.WithNeonControllerState(ctx, item.ID, 1, apply); err != nil || !applied {
		t.Fatal("initial callback cannot unblock bootstrap", err)
	}
	state, err := s.NeonControllerState(ctx, item.ID, 1)
	if err != nil || state.Attach == nil || state.Attach.TenantID != tenant {
		t.Fatal("initial callback was not durable", err)
	}
	if _, err = s.WithNeonControllerState(ctx, item.ID, 2, apply); !errors.Is(err, ErrConflict) || called != 1 {
		t.Fatal("different revision acquired callback authority", err)
	}
	if _, err = s.WithNeonControllerState(ctx, NewID(), 1, apply); !errors.Is(err, ErrConflict) || called != 1 {
		t.Fatal("different platform acquired callback authority", err)
	}
	_, err = s.WithNeonControllerState(ctx, item.ID, 1, func(input NeonControllerContext) (managedplatform.NeonControllerState, bool, error) {
		return managedplatform.NeonControllerState{}, false, ErrInput
	})
	if !errors.Is(err, ErrInput) {
		t.Fatal("invalid callback accepted")
	}
	state, err = s.NeonControllerState(ctx, item.ID, 1)
	if err != nil || state.Attach == nil {
		t.Fatal("invalid callback removed durable state")
	}
	if _, err = s.Pool.Exec(ctx, `UPDATE api_keys SET revoked_at=now() WHERE id=$1`, principal.KeyID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.WithNeonControllerState(ctx, item.ID, 1, apply); err == nil || called != 1 {
		t.Fatal("revoked operation authority reconfigured computes")
	}
}

func TestNeonControllerApplyBlocksConcurrentRevisionAndDeletion(t *testing.T) {
	for _, kind := range []string{"update", "delete"} {
		t.Run(kind, func(t *testing.T) {
			s, principal, item, plan := managedPlatformFixture(t)
			ctx := context.Background()
			review := managedPlatformReview(t, s, principal, item, plan, 0, "create")
			if _, err := s.AcceptManagedPlatform(ctx, principal, item, plan, []byte("sealed-runtime"), review, 0, "controller-fence-create", "create"); err != nil {
				t.Fatal(err)
			}
			op, err := s.ClaimManagedPlatformOperation(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if err = s.RecordManagedPlatformStep(ctx, op, "succeeded", "ready", "", nil); err != nil {
				t.Fatal(err)
			}
			nextReview := managedPlatformReview(t, s, principal, item, plan, 1, kind)
			entered, release, finished := make(chan struct{}), make(chan struct{}), make(chan error, 1)
			go func() {
				_, err := s.WithNeonControllerState(ctx, item.ID, 1, func(input NeonControllerContext) (managedplatform.NeonControllerState, bool, error) {
					close(entered)
					<-release
					return input.State, true, nil
				})
				finished <- err
			}()
			select {
			case <-entered:
			case <-time.After(3 * time.Second):
				t.Fatal("callback did not acquire platform fence")
			}
			bounded, cancel := context.WithTimeout(ctx, 150*time.Millisecond)
			_, err = s.AcceptManagedPlatform(bounded, principal, item, plan, []byte("next-runtime"), nextReview, 1, "controller-fence-"+kind, kind)
			cancel()
			close(release)
			if err == nil {
				t.Fatal("revision changed during compute notification")
			}
			if err = <-finished; err != nil {
				t.Fatal("callback failed", err)
			}
			if _, err = s.AcceptManagedPlatform(ctx, principal, item, plan, []byte("next-runtime"), nextReview, 1, "controller-fence-"+kind, kind); err != nil {
				t.Fatal("revision did not advance after callback", err)
			}
			called := false
			_, err = s.WithNeonControllerState(ctx, item.ID, 1, func(input NeonControllerContext) (managedplatform.NeonControllerState, bool, error) {
				called = true
				return input.State, true, nil
			})
			if !errors.Is(err, ErrConflict) || called {
				t.Fatal("stale controller retained mutation authority", err)
			}
		})
	}
}

func TestNeonControllerPendingApplySurvivesRetry(t *testing.T) {
	s, principal, item, plan := managedPlatformFixture(t)
	ctx := context.Background()
	review := managedPlatformReview(t, s, principal, item, plan, 0, "create")
	if _, err := s.AcceptManagedPlatform(ctx, principal, item, plan, []byte("sealed-runtime"), review, 0, "controller-state-pending", "create"); err != nil {
		t.Fatal(err)
	}
	next := managedplatform.NeonControllerState{Safekeepers: &managedplatform.NeonSafekeeperNotification{TenantID: strings.Repeat("b", 32), TimelineID: strings.Repeat("c", 32), Generation: 3, Safekeepers: []managedplatform.NeonSafekeeperMember{{ID: 1}, {ID: 2}, {ID: 3}}}}
	if applied, err := s.WithNeonControllerState(ctx, item.ID, 1, func(NeonControllerContext) (managedplatform.NeonControllerState, bool, error) {
		return next, false, nil
	}); err != nil || applied {
		t.Fatal("pending notification failed", err)
	}
	if applied, err := s.WithNeonControllerState(ctx, item.ID, 1, func(input NeonControllerContext) (managedplatform.NeonControllerState, bool, error) {
		if input.State.Safekeepers == nil || input.State.Safekeepers.Generation != 3 {
			t.Fatal("retry lost persisted generation")
		}
		return input.State, true, nil
	}); err != nil || !applied {
		t.Fatal("pending retry failed", err)
	}
}

func TestNeonControllerEstablishedRevisionSurvivesCreatorRevocation(t *testing.T) {
	for _, status := range []string{"running", "succeeded"} {
		t.Run(status, func(t *testing.T) {
			s, principal, item, plan := managedPlatformFixture(t)
			ctx := context.Background()
			review := managedPlatformReview(t, s, principal, item, plan, 0, "create")
			if _, err := s.AcceptManagedPlatform(ctx, principal, item, plan, []byte("sealed-runtime"), review, 0, "controller-revocation-"+status, "create"); err != nil {
				t.Fatal(err)
			}
			op, err := s.ClaimManagedPlatformOperation(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if status == "succeeded" {
				if err = s.RecordManagedPlatformStep(ctx, op, "succeeded", "ready", "", nil); err != nil {
					t.Fatal(err)
				}
			}
			if _, err = s.Pool.Exec(ctx, `UPDATE api_keys SET revoked_at=now() WHERE id=$1`, principal.KeyID); err != nil {
				t.Fatal(err)
			}
			called := false
			applied, err := s.WithNeonControllerState(ctx, item.ID, 1, func(input NeonControllerContext) (managedplatform.NeonControllerState, bool, error) {
				called = true
				return input.State, true, nil
			})
			if status == "succeeded" {
				if err != nil || !applied || !called {
					t.Fatal("creator revocation disabled established runtime recovery", err)
				}
			} else if err == nil || applied || called {
				t.Fatal("in-flight operation escaped revoked authority")
			}
		})
	}
}
