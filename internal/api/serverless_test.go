package api

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/cluster"
	"github.com/hakopod/hakopod/internal/spec"
	"github.com/hakopod/hakopod/internal/store"
)

func TestPlacementEndpointPermissions(t *testing.T) {
	db := notificationTestDB(t)
	ctx := context.Background()
	token, err := db.Bootstrap(ctx, "placement-tests")
	if err != nil {
		t.Fatal(err)
	}
	p, _ := db.Authenticate(ctx, token)
	_, writer, err := db.CreateKey(ctx, p, store.KeyInput{Name: "app-deployer", Project: "demo", Environment: "development", Application: "app", Permissions: []string{"deployments:write"}, ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	handler := (&Server{Store: db}).Handler()
	for _, test := range []struct {
		scope  string
		status int
	}{{"project=demo&environment=development&application=app", 503}, {"project=other&environment=development&application=app", 403}, {"project=demo&environment=production&application=app", 403}, {"project=demo&environment=development&application=other", 403}} {
		r := httptest.NewRequest("GET", "/api/v1/placement/nodes?"+test.scope, nil)
		r.Header.Set("Authorization", "Bearer "+writer)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != test.status {
			t.Fatal(test.scope, w.Code, w.Body.String())
		}
	}
}
func TestServerlessRequestBindingAndRequiredRuntime(t *testing.T) {
	db := notificationTestDB(t)
	ctx := context.Background()
	token, err := db.Bootstrap(ctx, "serverless-tests")
	if err != nil {
		t.Fatal(err)
	}
	p, _ := db.Authenticate(ctx, token)
	app, err := spec.Normalize(spec.Application{Name: "function", Services: map[string]spec.Service{"api": {Image: "node:24-alpine", Public: true, Port: 8080, Serverless: &spec.Serverless{}}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Accept(ctx, p, "demo", "development", app, 0, "no-runtime-required"); err == nil || !strings.Contains(err.Error(), "runtime validator") {
		t.Fatal("runtime admission was bypassed", err)
	}
	db.ValidateDeployment = func(context.Context, store.Application, spec.Application) error { return nil }
	dep, err := db.Accept(ctx, p, "demo", "development", app, 0, "serverless-binding")
	if err != nil {
		t.Fatal(err)
	}
	bindings, truncated, err := (&Server{Store: db}).requestBindings(ctx)
	binding := bindings[cluster.Namespace(dep.ApplicationID)+"_svc_"+cluster.ActivationServiceName("api")+"_http"]
	if err != nil || truncated || binding.ApplicationID != dep.ApplicationID || binding.Service != "api" {
		t.Fatal(binding, err)
	}
}

func TestServerlessGatewayCapacityIsCheckedAtAcceptance(t *testing.T) {
	db := notificationTestDB(t)
	ctx := context.Background()
	token, err := db.Bootstrap(ctx, "serverless-capacity")
	if err != nil {
		t.Fatal(err)
	}
	p, _ := db.Authenticate(ctx, token)
	db.ValidateDeployment = func(context.Context, store.Application, spec.Application) error { return nil }
	app, err := spec.Normalize(spec.Application{Name: "functions", Services: map[string]spec.Service{"api": {Image: "node:24-alpine", Public: true, Port: 8080, Serverless: &spec.Serverless{}}}})
	if err != nil {
		t.Fatal(err)
	}
	// Explicit database fixture seeds the global bound without creating workloads.
	if _, err = db.Pool.Exec(ctx, `INSERT INTO applications(id,name,project,environment,spec) SELECT 'function-'||i,'function-'||i,'demo','development',$1 FROM generate_series(1,512) i`, store.JSON(app)); err != nil {
		t.Fatal(err)
	}
	// Use a different environment so its existing per-environment app limit does
	// not hide the independent installation-wide activation limit.
	_, err = db.Pool.Exec(ctx, "INSERT INTO environments(project,name) VALUES('demo','production') ON CONFLICT DO NOTHING")
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Accept(ctx, p, "demo", "production", app, 0, "exceeds-function-bound")
	if err == nil || !strings.Contains(err.Error(), "512 serverless") {
		t.Fatal("global serverless capacity not enforced", err)
	}
}
