package api_test

import (
	"context"
	"github.com/hakopod/hakopod/internal/api"
	"github.com/hakopod/hakopod/internal/cluster"
	"net/http/httptest"
	"testing"
)

func TestInternalRuntimeRechecksOwnerForAdministration(t *testing.T) {
	h := newAuthHarness(t, func(c *api.AuthConfig) { c.DeploymentMode = cluster.DeploymentManagedCloud })
	token := h.owner()
	// Ordinary managed runtimes deny even their local initial owner.
	h.call("GET", "/host-access", token, nil, 403)
	internal := httptest.NewServer((&api.Server{Store: h.db, Auth: h.config, OperatorRuntime: true}).Handler())
	defer internal.Close()
	h.server = internal
	h.call("GET", "/host-access", token, nil, 200)
	h.call("GET", "/users", token, nil, 200)
	// Being an administrator alone does not grant platform authority.
	if _, err := h.db.Pool.Exec(context.Background(), "UPDATE identities SET owner=false WHERE email='owner@example.test'"); err != nil {
		t.Fatal(err)
	}
	h.call("GET", "/host-access", token, nil, 403)
	h.call("GET", "/users", token, nil, 403)
	if _, err := h.db.Pool.Exec(context.Background(), "UPDATE identities SET disabled=true WHERE email='owner@example.test'"); err != nil {
		t.Fatal(err)
	}
	h.call("GET", "/host-access", token, nil, 401)
}
