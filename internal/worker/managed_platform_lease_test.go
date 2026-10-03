package worker

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/store"
)

func TestManagedPlatformLeaseRenewsDuringReconciliation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	var calls atomic.Int32
	renewedTwice := make(chan struct{})
	err, lost := reconcileManagedPlatformWithLease(ctx, time.Millisecond, func(check context.Context) error {
		deadline, ok := check.Deadline()
		if !ok || time.Until(deadline) > 3*time.Second {
			t.Error("lease check has no bounded deadline")
		}
		if calls.Add(1) == 2 {
			close(renewedTwice)
		}
		return nil
	}, func(attempt context.Context) error {
		select {
		case <-renewedTwice:
			return nil
		case <-attempt.Done():
			return attempt.Err()
		}
	})
	if err != nil || lost || calls.Load() < 2 {
		t.Fatalf("renewal result: err=%v lost=%v calls=%d", err, lost, calls.Load())
	}
}

func TestManagedPlatformLeaseFailureCancelsInFlightWork(t *testing.T) {
	for _, failure := range []error{store.ErrConflict, store.ErrForbidden, errors.New("database unavailable")} {
		t.Run(failure.Error(), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			var calls atomic.Int32
			err, lost := reconcileManagedPlatformWithLease(ctx, time.Millisecond, func(context.Context) error {
				calls.Add(1)
				return failure
			}, func(attempt context.Context) error {
				<-attempt.Done()
				if !errors.Is(context.Cause(attempt), failure) {
					t.Error("in-flight work did not receive the lease failure")
				}
				return attempt.Err()
			})
			if !errors.Is(err, failure) || !lost || calls.Load() != 1 {
				t.Fatalf("lease failure was not retained: err=%v lost=%v calls=%d", err, lost, calls.Load())
			}
		})
	}
}

func TestManagedPlatformCompletionStopsInFlightRenewal(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	started, stopped := make(chan struct{}), make(chan struct{})
	want := errors.New("reconcile needs another attempt")
	err, lost := reconcileManagedPlatformWithLease(ctx, time.Millisecond, func(check context.Context) error {
		close(started)
		<-check.Done()
		close(stopped)
		return check.Err()
	}, func(attempt context.Context) error {
		select {
		case <-started:
			return want
		case <-attempt.Done():
			return attempt.Err()
		}
	})
	if !errors.Is(err, want) || lost {
		t.Fatalf("completion changed the result: err=%v lost=%v", err, lost)
	}
	select {
	case <-stopped:
	default:
		t.Fatal("attempt returned with a live renewal")
	}
}

func TestManagedPlatformAttemptHonorsParentDeadline(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	err, lost := reconcileManagedPlatformWithLease(ctx, time.Hour, func(context.Context) error {
		t.Error("unexpected renewal")
		return nil
	}, func(attempt context.Context) error {
		<-attempt.Done()
		return attempt.Err()
	})
	if !errors.Is(err, context.DeadlineExceeded) || lost {
		t.Fatalf("parent deadline was not retained: err=%v lost=%v", err, lost)
	}
}

func TestManagedPlatformCompletionRetainsConcurrentLeaseLoss(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	started := make(chan struct{})
	err, lost := reconcileManagedPlatformWithLease(ctx, time.Millisecond, func(check context.Context) error {
		close(started)
		<-check.Done()
		// A completed database check can race with local shutdown. Its
		// authority result must survive cancellation of the renewer.
		return store.ErrForbidden
	}, func(attempt context.Context) error {
		select {
		case <-started:
			return nil
		case <-attempt.Done():
			return attempt.Err()
		}
	})
	if !errors.Is(err, store.ErrForbidden) || !lost {
		t.Fatalf("completion hid a lease failure: err=%v lost=%v", err, lost)
	}
}
