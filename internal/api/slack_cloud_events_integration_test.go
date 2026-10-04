package api_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/api"
	"github.com/hakopod/hakopod/internal/store"
)

type failingSlackCloudSink struct{}

func (failingSlackCloudSink) Enqueue(context.Context, api.SlackCloudEvent) (api.SlackCloudEventOutcome, error) {
	return api.SlackCloudRetry, errors.New("fixture Cloud transport outage")
}

func TestSlackCloudEventFailureRemainsPendingAndReleasesLease(t *testing.T) {
	db, _ := database(t)
	ctx := context.Background()
	if _, err := db.Pool.Exec(ctx, `INSERT INTO slack_event_outbox(delivery_target,runtime_source_id,event_kind,source_id,payload) VALUES('cloud','shared-runtime','audit',41,$1)`, store.JSON(map[string]any{"project": "demo", "environment": "development", "event_id": 41})); err != nil {
		t.Fatal(err)
	}
	workerCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		(&api.Server{Store: db, SlackCloudEvents: failingSlackCloudSink{}}).RunSlackCloudEvents(workerCtx)
		close(done)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Error("Cloud event worker did not stop")
		}
	})

	deadline := time.Now().Add(3 * time.Second)
	for {
		var status, leaseID, message string
		var nextAttempt time.Time
		err := db.Pool.QueryRow(ctx, `SELECT status,lease_id,last_error,next_attempt_at FROM slack_event_outbox WHERE delivery_target='cloud' AND source_id=41`).Scan(&status, &leaseID, &message, &nextAttempt)
		if err != nil {
			t.Fatal(err)
		}
		if status == "pending" && leaseID == "" && nextAttempt.After(time.Now()) {
			if message != "Cloud event delivery failed; retry scheduled." {
				t.Fatalf("retry message=%q", message)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("failed Cloud delivery was not released for retry: status=%q lease=%q next_attempt=%s", status, leaseID, nextAttempt)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
