package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/backup"
)

func TestBackupMaintenanceWaitRetriesOnlyMaintenance(t *testing.T) {
	attempts := 0
	result, err := waitForDatabaseMaintenanceBounded(context.Background(), time.Second, time.Millisecond, func(context.Context) (string, error) {
		attempts++
		if attempts < 3 {
			return "", managedBackupMaintenanceConflict()
		}
		return "accepted-once", nil
	})
	if err != nil || result != "accepted-once" || attempts != 3 {
		t.Fatal("maintenance release did not produce one accepted result", result, attempts, err)
	}

	attempts = 0
	substantive := errManagedBackupAdmissionConflict
	if _, err = waitForDatabaseMaintenanceBounded(context.Background(), time.Second, time.Millisecond, func(context.Context) (string, error) {
		attempts++
		return "", substantive
	}); !errors.Is(err, substantive) || attempts != 1 {
		t.Fatal("substantive conflict was retried", attempts, err)
	}
}

func TestBackupMaintenanceWaitCancellationAndExpiry(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	attempts := 0
	if _, err := waitForDatabaseMaintenanceBounded(ctx, time.Second, time.Millisecond, func(context.Context) (string, error) {
		attempts++
		return "", managedBackupMaintenanceConflict()
	}); !errors.Is(err, context.Canceled) {
		t.Fatal("caller cancellation was masked", err)
	}
	if attempts != 0 {
		t.Fatal("attempt ran after caller cancellation", attempts)
	}

	restoreConflict := managedRestoreMaintenanceConflict()
	started := time.Now()
	attempts = 0
	if _, err := waitForDatabaseMaintenanceBounded(context.Background(), 10*time.Millisecond, time.Millisecond, func(attempt context.Context) (string, error) {
		attempts++
		if attempts == 1 {
			return "", restoreConflict
		}
		<-attempt.Done()
		return "", attempt.Err()
	}); !errors.Is(err, backup.ErrConflict) || !errors.Is(err, errDatabaseMaintenanceActive) {
		t.Fatal("expired wait lost restore conflict semantics", err)
	}
	if elapsed := time.Since(started); elapsed > 250*time.Millisecond {
		t.Fatal("blocked attempt exceeded the total maintenance wait bound", elapsed)
	}
}

func TestDatabaseMaintenanceWaitPreservesCommittedReceipt(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	result, err := waitForDatabaseMaintenanceBounded(ctx, time.Second, time.Millisecond, func(context.Context) (string, error) {
		cancel()
		return "committed-receipt", nil
	})
	if err != nil || result != "committed-receipt" {
		t.Fatal("committed receipt was discarded when its caller context expired", result, err)
	}
}
