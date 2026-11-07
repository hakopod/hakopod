package api

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/database"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
)

func TestBoundDatabaseHealthGate(t *testing.T) {
	d := database.Resource{ID: "db1", Spec: database.Spec{Name: "orders"}}
	noDelay := func(int) time.Duration { return 0 }
	limiter := fmt.Errorf("client rate limiter Wait returned an error: %w", errors.New("rate: Wait(n=1) would exceed context deadline"))
	sequence := func(results ...error) (func(context.Context, database.Resource) (database.Observation, error), *int) {
		calls := 0
		return func(context.Context, database.Resource) (database.Observation, error) {
			err := results[min(calls, len(results)-1)]
			calls++
			if err != nil {
				return database.Observation{}, err
			}
			return database.Observation{Status: "ready"}, nil
		}, &calls
	}

	observe, calls := sequence(limiter, apierrors.NewTooManyRequests("slow", 1), nil)
	if o, err := checkBoundDatabase(context.Background(), observe, d, noDelay); err != nil || o.Status != "ready" || *calls != 3 {
		t.Fatalf("transient errors were not retried: %v after %d calls", err, *calls)
	}

	observe, calls = sequence(limiter)
	_, err := checkBoundDatabase(context.Background(), observe, d, noDelay)
	if err == nil || err.Error() != "could not check managed database orders (db1) health: Kubernetes API client throttled" || *calls != 3 {
		t.Fatalf("throttled check: %v after %d calls", err, *calls)
	}

	permanent := errors.New("database namespace ownership changed")
	observe, calls = sequence(permanent)
	_, err = checkBoundDatabase(context.Background(), observe, d, noDelay)
	if !errors.Is(err, permanent) || err.Error() != "managed database orders (db1): database namespace ownership changed" || *calls != 1 {
		t.Fatalf("permanent error: %v after %d calls", err, *calls)
	}

	degraded := func(context.Context, database.Resource) (database.Observation, error) {
		return database.Observation{Status: "degraded", Message: "1 of 2 members ready"}, nil
	}
	_, err = checkBoundDatabase(context.Background(), degraded, d, noDelay)
	if err == nil || err.Error() != "managed database orders (db1) is not healthy: degraded: 1 of 2 members ready" {
		t.Fatalf("unhealthy database: %v", err)
	}

	if got := []time.Duration{databaseRetryDelay(1), databaseRetryDelay(2)}; got[0] != time.Second || got[1] != 2*time.Second {
		t.Fatalf("retry backoff is %v", got)
	}
}
