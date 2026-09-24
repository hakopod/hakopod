package api_test

import (
	"context"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/spec"
	"github.com/hakopod/hakopod/internal/store"
)

func TestResourceDeletionAPIReviewAndPermissions(t *testing.T) {
	h := newAuthHarness(t, nil)
	owner := h.owner()
	ctx := context.Background()
	p, err := h.db.Authenticate(ctx, owner)
	if err != nil {
		t.Fatal(err)
	}
	h.call("POST", "/projects", owner, map[string]string{"name": "delete-api", "environment": "test"}, 201)
	_, limited, err := h.db.CreateKey(ctx, p, store.KeyInput{Name: "viewer", Project: "delete-api", Environment: "test", Permissions: []string{"deployments:read"}, ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	h.call("DELETE", "/projects/delete-api", limited, map[string]string{"confirm_name": "delete-api"}, 403)
	app, err := spec.Normalize(spec.Application{Name: "remove-api", Services: map[string]spec.Service{"web": {Image: "python:3.13"}}})
	if err != nil {
		t.Fatal(err)
	}
	d, err := h.db.Accept(ctx, p, "delete-api", "test", app, 0, "delete-api-fixture")
	if err != nil {
		t.Fatal(err)
	}
	h.call("POST", "/plan", owner, map[string]string{"project": "delete-api", "environment": "test", "toml": "schema_version=1\nname='remove-api'\n[services]\n"}, 200)
	h.call("DELETE", "/projects/delete-api", owner, map[string]string{"confirm_name": "delete-api"}, 409)
	h.call("DELETE", "/applications/"+d.ApplicationID, owner, map[string]any{"confirm_name": app.Name}, 400)
	h.call("DELETE", "/applications/"+d.ApplicationID, limited, map[string]any{"confirm_name": app.Name, "expected_revision": 1}, 403)
	h.call("DELETE", "/applications/"+d.ApplicationID, owner, map[string]any{"confirm_name": app.Name, "expected_revision": 1}, 409)
	app.Services = map[string]spec.Service{}
	if _, err = h.db.Pool.Exec(ctx, "UPDATE applications SET spec=$2 WHERE id=$1", d.ApplicationID, store.JSON(app)); err != nil {
		t.Fatal(err)
	}
	if _, err = h.db.Pool.Exec(ctx, "UPDATE deployments SET status='succeeded',spec=$2 WHERE id=$1", d.ID, store.JSON(app)); err != nil {
		t.Fatal(err)
	}
	h.call("DELETE", "/applications/"+d.ApplicationID, owner, map[string]any{"confirm_name": app.Name, "expected_revision": 1}, 200)
	h.call("GET", "/applications/"+d.ApplicationID, owner, nil, 404)
	h.call("DELETE", "/projects/delete-api", owner, map[string]string{"confirm_name": "delete-api"}, 409)
	h.call("GET", "/storage/retained?project=delete-api&environment=test", limited, nil, 403)
	h.call("DELETE", "/storage/retained/"+d.ApplicationID, owner, map[string]string{"confirm_name": app.Name}, 202)
	cleanup, err := h.db.ClaimRetainedCleanup(ctx)
	if err != nil || cleanup == nil {
		t.Fatal(err)
	}
	if err = cleanup.Finish(ctx, ""); err != nil {
		t.Fatal(err)
	}
	cleanup.Release()
	h.call("DELETE", "/projects/delete-api", owner, map[string]string{"confirm_name": "delete-api"}, 200)
	h.call("POST", "/projects", owner, map[string]string{"name": "delete-api", "environment": "test"}, 409)
}
