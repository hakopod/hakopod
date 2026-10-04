package store

import (
	"strings"
	"testing"

	"github.com/hakopod/hakopod/internal/spec"
)

func TestSlackAcceptedEventsAreRedactedAndPerService(t *testing.T) {
	before := spec.Application{Services: map[string]spec.Service{
		"api": {Image: "registry.example/api@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Env: map[string]string{"TOKEN": "old"}},
	}}
	after := before
	after.Services = map[string]spec.Service{
		"api":    {Image: "registry.example/api@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", Env: map[string]string{"TOKEN": "never-send-me"}},
		"worker": {Image: "registry.example/worker@sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"},
	}
	events := slackAcceptedEvents(before, after, false)
	seen := map[string]bool{}
	for _, event := range events {
		seen[event.Type+":"+event.Service] = true
		if strings.Contains(event.Message, "never-send-me") || strings.Contains(event.Message, "registry.example") {
			t.Fatalf("event includes configuration: %#v", event)
		}
	}
	for _, want := range []string{"application.configuration.updated:", "service.image.updated:api", "service.variables.updated:api", "service.added:worker"} {
		if !seen[want] {
			t.Errorf("missing %s in %#v", want, events)
		}
	}
}

func TestSlackAcceptedEventsKeepsEveryBoundedService(t *testing.T) {
	before := spec.Application{Services: map[string]spec.Service{}}
	after := spec.Application{Services: map[string]spec.Service{}}
	for i := 0; i < 20; i++ {
		name := string(rune('a' + i))
		before.Services[name] = spec.Service{Image: "old", Env: map[string]string{"VALUE": "old"}, Size: "small", Replicas: 1, Networks: []string{"default"}}
		after.Services[name] = spec.Service{Image: "new", Env: map[string]string{"VALUE": "new"}, Size: "medium", Replicas: 2, Networks: []string{"private"}}
	}
	events := slackAcceptedEvents(before, after, false)
	if len(events) <= 64 {
		t.Fatalf("got %d events; granular events were silently truncated", len(events))
	}
	seen := map[string]bool{}
	for _, event := range events {
		seen[event.Type+":"+event.Service] = true
	}
	for _, kind := range []string{"service.configuration.updated", "service.image.updated", "service.variables.updated", "service.resources.updated", "service.scale.updated", "service.network.updated"} {
		if !seen[kind+":t"] {
			t.Errorf("last bounded service lost %s: %#v", kind, events)
		}
	}
}
