package idlehttp

import (
	"errors"
	"github.com/hakopod/hakopod/internal/spec"
	"github.com/hakopod/hakopod/internal/store"
	"testing"
)

func TestIdleTargetRequiresCurrentSuccessfulApplication(t *testing.T) {
	app := store.Application{ID: "app", Project: "free", Environment: "production", Revision: 2, Status: "healthy", Spec: spec.Application{Name: "test", Services: map[string]spec.Service{"web": {Image: "nginx:alpine", Port: 8080, Public: true}}}}
	target := IdleHTTPService{ApplicationID: "app", Project: "free", Environment: "production", Revision: 2, Service: "web"}
	if _, err := idleTarget(app, target); err != nil {
		t.Fatal("successful application ineligible", err)
	}
	for _, status := range []string{"queued", "running", "failed", "cancelled", "recovered"} {
		app.Status = status
		if _, err := idleTarget(app, target); !errors.Is(err, ErrIdleIneligible) {
			t.Fatal(status, err)
		}
	}
	app.Status = "healthy"
	target.Revision--
	if _, err := idleTarget(app, target); !errors.Is(err, ErrIdleIneligible) {
		t.Fatal("stale target accepted")
	}
}
