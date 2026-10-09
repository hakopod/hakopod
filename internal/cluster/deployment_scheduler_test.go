package cluster

import (
	"context"
	"errors"
	"github.com/hakopod/hakopod/internal/spec"
	"sync"
	"testing"
	"time"
)

func TestScheduleServicesContinuesIndependentBranchesAndBoundsConcurrency(t *testing.T) {
	app := spec.Application{Name: "app", Services: map[string]spec.Service{"migration": {}, "api": {DependsOn: []string{"migration"}}, "worker": {}, "web": {DependsOn: []string{"api"}}, "independent": {}}}
	var mu sync.Mutex
	active, maximum := 0, 0
	ran := map[string]bool{}
	events := []Event{}
	err := scheduleServices(context.Background(), app, []string{"migration", "api", "worker", "web", "independent"}, func(name string) error {
		mu.Lock()
		active++
		if active > maximum {
			maximum = active
		}
		ran[name] = true
		mu.Unlock()
		time.Sleep(10 * time.Millisecond)
		mu.Lock()
		active--
		mu.Unlock()
		if name == "api" {
			return errors.New("api failed")
		}
		return nil
	}, func(e Event) { mu.Lock(); events = append(events, e); mu.Unlock() })
	if err == nil || !ran["worker"] || !ran["independent"] || ran["web"] || maximum < 2 || maximum > 4 {
		t.Fatalf("scheduler result err=%v ran=%v max=%d events=%v", err, ran, maximum, events)
	}
	var progress *DeploymentProgressError
	if !errors.As(err, &progress) || len(progress.Observation.Succeeded) != 3 || len(progress.Observation.Blocked) != 1 || progress.Observation.Blocked[0] != "web" {
		t.Fatal(progress)
	}
	if len(events) != 1 || events[0].Type != "blocked" || events[0].Service != "web" {
		t.Fatal(events)
	}
}

func TestScheduleServicesRunsOnlySelectedClosure(t *testing.T) {
	app := spec.Application{Name: "app", Services: map[string]spec.Service{"healthy": {}, "failed": {}, "dependent": {DependsOn: []string{"failed"}}}}
	ran := map[string]bool{}
	if err := scheduleServices(context.Background(), app, []string{"failed", "dependent"}, func(name string) error { ran[name] = true; return nil }, func(Event) {}); err != nil {
		t.Fatal(err)
	}
	if ran["healthy"] || !ran["failed"] || !ran["dependent"] {
		t.Fatal(ran)
	}
}
