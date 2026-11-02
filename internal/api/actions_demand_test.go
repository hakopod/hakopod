package api

import (
	"context"
	"strings"
	"testing"

	"github.com/hakopod/hakopod/internal/spec"
	"github.com/hakopod/hakopod/internal/store"
)

func TestScaledActionsReplicas(t *testing.T) {
	for name, c := range map[string]struct {
		min, busy, queued, current int
		warm                       bool
		want                       int
	}{
		"asleep":              {want: 0},
		"queued wakes":        {queued: 2, want: 2},
		"capped at replicas":  {queued: 9, want: 4},
		"busy kept":           {busy: 1, queued: 1, current: 1, want: 2},
		"warm holds idle":     {current: 3, warm: true, want: 3},
		"idle drains":         {current: 3, want: 0},
		"minimum stays warm":  {min: 1, want: 1},
		"minimum with demand": {min: 1, queued: 3, want: 3},
	} {
		got := scaledActionsReplicas(&spec.Actions{MinReplicas: int64(c.min)}, 4, c.busy, c.queued, c.current, c.warm)
		if got != c.want {
			t.Errorf("%s: got %d, want %d", name, got, c.want)
		}
	}
}

func TestActionsJobMatchesRunsOnLabels(t *testing.T) {
	config := spec.Service{Architecture: "arm64", Actions: &spec.Actions{Labels: []string{"hakopod", "Large"}}}
	for labels, want := range map[string]bool{
		"hakopod":                         true,
		"self-hosted,hakopod,large":       true,
		"self-hosted,linux,arm64":         true,
		"self-hosted,linux,x64":           false,
		"ubuntu-latest":                   false,
		"":                                false,
		"hakopod,gpu":                     false,
		"self-hosted,Linux,ARM64,hakopod": true,
	} {
		var list []string
		if labels != "" {
			list = strings.Split(labels, ",")
		}
		if store.ActionsJobMatches(config, list) != want {
			t.Errorf("%q: want %v", labels, want)
		}
	}
}

// A scale-to-zero pool starts no runner until GitHub reports a queued job for
// its repository and labels, and returns to zero once the idle window ends.
func TestActionsScaleToZeroWakesOnQueuedJob(t *testing.T) {
	s, f, p, _ := actionsHarness(t)
	ctx := context.Background()
	p.Config.Replicas = 3
	p.Config.Actions.IdleSeconds = 60
	if _, err := s.Store.Pool.Exec(ctx, `UPDATE actions_pools SET config=$3,active_at=now()-interval '1 hour' WHERE application_id=$1 AND service=$2`, p.ApplicationID, p.Service, store.JSON(p.Config)); err != nil {
		t.Fatal(err)
	}
	reconcile := func() {
		t.Helper()
		if err := s.reconcileActionsPool(ctx, actionsTarget(p), p); err != nil {
			t.Fatal(err)
		}
	}
	reconcile()
	if f.next != 0 {
		t.Fatal("sleeping pool started a runner without demand")
	}
	observed, err := s.observeActions(ctx, actionsTarget(p), p.Service, p.Config)
	if err != nil || observed.Status != "sleeping" || observed.Desired != 0 {
		t.Fatalf("idle pool not reported sleeping: %+v, %v", observed, err)
	}
	// Another tenant's scoped connection and a non-matching label never wake it.
	for _, e := range []store.ActionsJobEvent{
		{Action: "queued", JobID: 7, Repository: "team/repo", Labels: []string{"hakopod"}, Project: "tenant-b", Environment: "production"},
		{Action: "queued", JobID: 8, Repository: "team/repo", Labels: []string{"gpu"}},
		{Action: "queued", JobID: 9, Repository: "other/repo", Labels: []string{"hakopod"}},
	} {
		if err = s.Store.RecordActionsJobEvent(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	if queued, _, err := s.Store.ActionsDemand(ctx, p.ApplicationID, p.Service, 60); err != nil || queued != 0 {
		t.Fatal("unrelated delivery recorded demand", queued, err)
	}
	for _, id := range []int64{1, 2} {
		if err = s.Store.RecordActionsJobEvent(ctx, store.ActionsJobEvent{Action: "queued", JobID: id, Repository: "Team/Repo", Labels: []string{"self-hosted", "hakopod"}, Project: "tenant-a", Environment: "production"}); err != nil {
			t.Fatal(err)
		}
	}
	reconcile()
	reconcile()
	reconcile()
	if f.next != 2 || len(f.pods) != 2 {
		t.Fatalf("queued jobs started %d runners, want 2", f.next)
	}
	for _, id := range []int64{1, 2} {
		if err = s.Store.RecordActionsJobEvent(ctx, store.ActionsJobEvent{Action: "completed", JobID: id, Repository: "team/repo", Labels: []string{"hakopod"}}); err != nil {
			t.Fatal(err)
		}
	}
	reconcile()
	if len(f.runners) != 2 {
		t.Fatal("idle window did not hold warm runners")
	}
	if _, err = s.Store.Pool.Exec(ctx, `UPDATE actions_pools SET active_at=now()-interval '2 minutes' WHERE application_id=$1 AND service=$2`, p.ApplicationID, p.Service); err != nil {
		t.Fatal(err)
	}
	reconcile()
	if len(f.runners) != 0 || len(f.pods) != 0 {
		t.Fatal("idle pool did not return to zero")
	}
}
