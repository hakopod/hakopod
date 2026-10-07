package api

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/database"
)

func readyMyDuckRecoveryObservation(d database.Resource) database.Observation {
	return database.Observation{
		Status:              "ready",
		Revision:            d.Revision,
		ObservedAt:          time.Now().UTC(),
		Members:             []database.Member{{Name: "database-0", UID: "member-uid", Ready: true}},
		Primary:             "database-0",
		TLS:                 &database.TLSObservation{Required: true, Verified: true, PlaintextRejected: true},
		TopologyFingerprint: "topology-fingerprint",
	}
}

func TestWaitForMyDuckRecoveryReadyRetriesPendingObservation(t *testing.T) {
	d := database.Resource{ID: "database", Revision: 3}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	checks, observations := 0, 0
	observed, err := waitForMyDuckRecoveryReady(ctx, d, "old-member-uid", 0, func(context.Context) error {
		checks++
		return nil
	}, func(context.Context, database.Resource) (database.Observation, error) {
		observations++
		if observations == 1 {
			return database.Observation{Status: "pending", Revision: d.Revision}, nil
		}
		if observations == 2 {
			stale := readyMyDuckRecoveryObservation(d)
			stale.Members[0].UID = "old-member-uid"
			return stale, nil
		}
		return readyMyDuckRecoveryObservation(d), nil
	})
	if err != nil || checks != 3 || observations != 3 || observed.Primary != "database-0" {
		t.Fatalf("readiness retry = checks %d observations %d primary %q err %v", checks, observations, observed.Primary, err)
	}
}

func TestWaitForMyDuckRecoveryReadyStopsAtDeadline(t *testing.T) {
	d := database.Resource{ID: "database", Revision: 3}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	checks, observations := 0, 0
	_, err := waitForMyDuckRecoveryReady(ctx, d, "old-member-uid", time.Millisecond, func(context.Context) error {
		checks++
		return nil
	}, func(context.Context, database.Resource) (database.Observation, error) {
		observations++
		return database.Observation{Status: "pending", Revision: d.Revision}, nil
	})
	if !errors.Is(err, context.DeadlineExceeded) || checks == 0 || observations == 0 {
		t.Fatalf("deadline result = checks %d observations %d err %v", checks, observations, err)
	}
}

func TestWaitForMyDuckRecoveryReadyStopsOnAuthorityLoss(t *testing.T) {
	d := database.Resource{ID: "database", Revision: 3}
	lost := errors.New("authority lost")
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	checks, observations := 0, 0
	_, err := waitForMyDuckRecoveryReady(ctx, d, "old-member-uid", 0, func(context.Context) error {
		checks++
		if checks > 1 {
			return lost
		}
		return nil
	}, func(context.Context, database.Resource) (database.Observation, error) {
		observations++
		return database.Observation{Status: "pending", Revision: d.Revision}, nil
	})
	if !errors.Is(err, lost) || checks != 2 || observations != 1 {
		t.Fatalf("authority loss = checks %d observations %d err %v", checks, observations, err)
	}
}
