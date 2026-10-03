package store

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestManagedPlatformTransactionRetriesOnlySerialization(t *testing.T) {
	for _, failure := range []error{ErrConflict, ErrForbidden, ErrUnauthorized, ErrInput,
		&pgconn.PgError{Code: "23505"}, &pgconn.PgError{Code: "40P01"}, errors.New("connection lost")} {
		calls := 0
		value, err := retryManagedPlatformTransaction(context.Background(), func() (int, error) {
			calls++
			return 42, failure
		})
		if calls != 1 || value != 0 || !errors.Is(err, failure) {
			t.Fatalf("persistent failure was retried or returned an uncommitted result: calls=%d value=%d err=%v", calls, value, err)
		}
	}
	calls := 0
	value, err := retryManagedPlatformTransaction(context.Background(), func() (int, error) {
		calls++
		if calls < 3 {
			return -calls, fmt.Errorf("aborted: %w", &pgconn.PgError{Code: "40001"})
		}
		return 42, nil
	})
	if err != nil || calls != 3 || value != 42 {
		t.Fatalf("serialization retry did not return the committed result: calls=%d value=%d err=%v", calls, value, err)
	}
}

func TestManagedPlatformTransactionRetryIsBounded(t *testing.T) {
	failure := &pgconn.PgError{Code: "40001"}
	calls := 0
	err := retryManagedPlatformWrite(context.Background(), func() error {
		calls++
		return failure
	})
	if calls != 5 || !errors.Is(err, failure) {
		t.Fatalf("unbounded serialization retries: calls=%d err=%v", calls, err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	calls = 0
	err = retryManagedPlatformWrite(ctx, func() error {
		calls++
		cancel()
		return failure
	})
	if calls != 1 || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled retry continued: calls=%d err=%v", calls, err)
	}

	ctx, cancel = context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	calls = 0
	err = retryManagedPlatformWrite(ctx, func() error {
		calls++
		<-ctx.Done()
		return failure
	})
	if calls != 1 || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("retry ignored caller deadline: calls=%d err=%v", calls, err)
	}
}
