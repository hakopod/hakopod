package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

// A lease renewal can invalidate another lifecycle transaction's serializable
// snapshot. Retry the complete database transaction so every attempt checks
// the current lease, ownership and authority again. The callback must not
// perform external work or retain values from an aborted transaction.
func retryManagedPlatformTransaction[T any](ctx context.Context, attempt func() (T, error)) (T, error) {
	var zero T
	for n := 0; ; n++ {
		if err := ctx.Err(); err != nil {
			return zero, err
		}
		result, err := attempt()
		if err == nil {
			return result, nil
		}
		var postgresError *pgconn.PgError
		if n >= 4 || !errors.As(err, &postgresError) || postgresError.Code != "40001" {
			return zero, err
		}
		timer := time.NewTimer(time.Duration(1<<n) * 10 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return zero, ctx.Err()
		case <-timer.C:
		}
	}
}

func retryManagedPlatformWrite(ctx context.Context, attempt func() error) error {
	_, err := retryManagedPlatformTransaction(ctx, func() (struct{}, error) {
		return struct{}{}, attempt()
	})
	return err
}
