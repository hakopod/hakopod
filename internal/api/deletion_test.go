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
	h.call("POST", "/projects/delete-api/environments", owner, map[string]string{"name": "other"}, 201)
	keys := make(map[string]string)
	for _, grant := range []struct {
		name        string
		environment string
		permissions []string
	}{
		{"deployer", "test", []string{"deployments:read", "deployments:write"}},
		{"manager-without-write", "test", []string{"deployments:read", "applications:manage"}},
		{"wrong-environment-manager", "other", []string{"deployments:read", "deployments:write", "applications:manage"}},
		{"manager", "test", []string{"deployments:read", "deployments:write", "applications:manage"}},
	} {
		_, key, keyErr := h.db.CreateKey(ctx, p, store.KeyInput{Name: grant.name, Project: "delete-api", Environment: grant.environment, Permissions: grant.permissions, ExpiresAt: time.Now().Add(time.Hour)})
		if keyErr != nil {
			t.Fatal(keyErr)
		}
		keys[grant.name] = key
	}
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
	deletion := map[string]any{"confirm_name": app.Name, "expected_revision": 1}
	for _, name := range []string{"deployer", "manager-without-write", "wrong-environment-manager"} {
		t.Run(name, func(t *testing.T) {
			request := *h
			request.t = t
			request.call("DELETE", "/applications/"+d.ApplicationID, keys[name], deletion, 403)
			request.call("GET", "/applications/"+d.ApplicationID, owner, nil, 200)
		})
	}
	h.call("DELETE", "/applications/"+d.ApplicationID, keys["manager"], deletion, 200)
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

func TestOwnerReplacesEmptyDemoWithNamedProject(t *testing.T) {
	h := newAuthHarness(t, nil)
	owner := h.owner()
	h.call("DELETE", "/projects/demo/environments/development", owner, map[string]string{"confirm_name": "wrong"}, 409)
	h.call("DELETE", "/projects/demo/environments/development", owner, map[string]string{"confirm_name": "development"}, 200)
	h.call("POST", "/projects/demo/environments", owner, map[string]string{"name": "development"}, 409)
	h.call("DELETE", "/projects/demo", owner, map[string]string{"confirm_name": "demo"}, 200)
	h.call("POST", "/projects", owner, map[string]string{"name": "my-workspace", "environment": "production", "display_name": "My workspace"}, 201)
	h.call("DELETE", "/projects/my-workspace/environments/production", owner, map[string]string{"confirm_name": "production"}, 200)
	h.call("DELETE", "/projects/my-workspace/environments/production", owner, map[string]string{"confirm_name": "production"}, 404)
}
