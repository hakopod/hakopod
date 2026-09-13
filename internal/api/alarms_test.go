package api

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/cluster"
	"github.com/hakopod/hakopod/internal/spec"
	"github.com/hakopod/hakopod/internal/store"
)

func TestAlarmSnapshotFreshnessRevisionAndActualReadiness(t *testing.T) {
	now := time.Now().UTC()
	app := store.Application{ID: "app", Name: "example", Project: "demo", Environment: "development", Revision: 2, Status: "healthy", Spec: spec.Application{Services: map[string]spec.Service{"web": {Replicas: 1}}}}
	observation := cluster.Observation{Revision: 2, Status: "pending", ObservedAt: now, Services: []cluster.ServiceStatus{{Name: "web", Status: "deploying", Ready: 0, Desired: 1, Message: "Unschedulable: untolerated disk-pressure taint"}}}
	app.Observed = store.JSON(observation)
	current, err := storedAlarmObservation(app, now)
	if err != nil {
		t.Fatal(err)
	}
	alarms := applicationAlarmObservations(app, current, nil, now)
	if len(alarms) != 2 || alarms[0].Health != "unhealthy" || alarms[1].Health != "unhealthy" || !strings.Contains(alarms[1].Summary, "Unschedulable") {
		t.Fatalf("persisted healthy status masked live failure: %+v", alarms)
	}
	for _, mutate := range []func(*cluster.Observation){
		func(o *cluster.Observation) { o.Revision = 1 },
		func(o *cluster.Observation) { o.ObservedAt = now.Add(-121 * time.Second) },
		func(o *cluster.Observation) { o.ObservedAt = time.Time{} },
		func(o *cluster.Observation) { o.ObservedAt = now.Add(time.Minute) },
		func(o *cluster.Observation) { o.Status = "unknown" },
	} {
		next := observation
		mutate(&next)
		app.Observed = store.JSON(next)
		current, err = storedAlarmObservation(app, now)
		if err == nil {
			t.Fatalf("untrusted snapshot accepted: %+v", next)
		}
		for _, alarm := range applicationAlarmObservations(app, current, err, now) {
			if alarm.Health != "unknown" {
				t.Fatalf("stale/error observation fabricated readiness: %+v", alarm)
			}
		}
	}
	observation.Services[0].Status = "ready"
	observation.Services[0].Ready = 1
	alarms = applicationAlarmObservations(app, observation, nil, now)
	if alarms[1].Health != "healthy" {
		t.Fatalf("actual recovery missed: %+v", alarms)
	}
	alarms = applicationAlarmObservations(app, observation, errors.New("API failed after first service"), now)
	if alarms[0].Health != "unknown" || alarms[1].Health != "unknown" {
		t.Fatal("partial observation recovered an alarm")
	}
}

func TestAlarmNodeConditionsPreserveUnknownAndPressure(t *testing.T) {
	node := cluster.AlarmNode{Name: "worker-a", Conditions: []cluster.AlarmNodeCondition{
		{Type: "Ready", Status: "Unknown", Message: "NodeStatusUnknown: kubelet stopped posting status"},
		{Type: "DiskPressure", Status: "True", Message: "KubeletHasDiskPressure: disk full"},
		{Type: "MemoryPressure", Status: "False"},
	}}
	items := alarmNodeObservations(node, time.Now().UTC())
	if len(items) != 4 || items[0].Health != "unhealthy" || items[1].Health != "unhealthy" || items[2].Health != "healthy" || items[3].Health != "unknown" {
		t.Fatalf("condition truth lost: %+v", items)
	}
	if !strings.Contains(items[1].Summary, "disk full") {
		t.Fatal("node diagnostic was discarded")
	}
	for _, item := range alarmNodeObservations(cluster.AlarmNode{Name: "missing"}, time.Now().UTC()) {
		if item.Health != "unknown" {
			t.Fatal("missing conditions invented health")
		}
	}
}
