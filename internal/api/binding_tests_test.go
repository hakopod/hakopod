package api

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hakopod/hakopod/internal/spec"
	"github.com/hakopod/hakopod/internal/store"
)

func TestBindingTestRequestBounds(t *testing.T) {
	for _, in := range []bindingTestRequest{{}, {ExpectedRevision: -1}, {ExpectedRevision: 1, Pod: "../another-namespace"}, {ExpectedRevision: 1, Pod: strings.Repeat("p", 254)}} {
		if in.validate() == nil {
			t.Fatal("invalid binding test accepted")
		}
	}
	if (bindingTestRequest{ExpectedRevision: 1}).validate() != nil || (bindingTestRequest{ExpectedRevision: 1, Pod: "api-abc123"}).validate() != nil {
		t.Fatal("valid binding test rejected")
	}
}

func TestBindingTestHandlerChecksScopeRevisionAndDeclaredVariable(t *testing.T) {
	db := sourceDatabase(t)
	ctx := context.Background()
	raw, err := db.Bootstrap(ctx, "binding-test-operator")
	if err != nil {
		t.Fatal(err)
	}
	p, err := db.Authenticate(ctx, raw)
	if err != nil {
		t.Fatal(err)
	}
	app, err := spec.Normalize(spec.Application{Name: "binding-test", Services: map[string]spec.Service{
		"database": {Image: "postgres:17", Port: 5432},
		"api":      {Image: "python:3.13-alpine", Bindings: map[string]spec.Binding{"DATABASE_URL": {Service: "database", Protocol: "postgres", Username: "fixture", Database: "fixture", Password: &spec.SecretRef{Ref: "database-password"}}}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	deployment, err := db.Accept(ctx, p, "demo", "development", app, 0, "binding-test-initial")
	if err != nil {
		t.Fatal(err)
	}
	server := &Server{Store: db}
	call := func(principal store.Principal, body, service, variable string) int {
		r := httptest.NewRequest("POST", "/binding-test", strings.NewReader(body))
		r.SetPathValue("id", deployment.ApplicationID)
		r.SetPathValue("service", service)
		r.SetPathValue("variable", variable)
		r = r.WithContext(context.WithValue(r.Context(), principalKey{}, principal))
		response := httptest.NewRecorder()
		server.testServiceBinding(response, r)
		return response.Code
	}
	for _, tc := range []struct {
		name, body, service, variable string
		want                          int
	}{
		{"missing revision", `{}`, "api", "DATABASE_URL", 400},
		{"newer revision", `{"expected_revision":2}`, "api", "DATABASE_URL", 409},
		{"command injection", `{"expected_revision":1,"command":["env"]}`, "api", "DATABASE_URL", 400},
		{"URL injection", `{"expected_revision":1,"url":"postgres://another"}`, "api", "DATABASE_URL", 400},
		{"foreign pod path", `{"expected_revision":1,"pod":"../foreign"}`, "api", "DATABASE_URL", 400},
		{"foreign service", `{"expected_revision":1}`, "other", "DATABASE_URL", 404},
		{"undeclared secret", `{"expected_revision":1}`, "api", "AUTH_SECRET", 404},
		{"runtime unavailable", `{"expected_revision":1}`, "api", "DATABASE_URL", 503},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := call(p, tc.body, tc.service, tc.variable); got != tc.want {
				t.Fatalf("status %d, want %d", got, tc.want)
			}
		})
	}
	for _, scope := range []struct{ project, environment, application string }{{"other", "development", ""}, {"demo", "production", ""}, {"demo", "development", "other"}} {
		foreign := p
		foreign.Project, foreign.Environment, foreign.Application = scope.project, scope.environment, scope.application
		if got := call(foreign, `{"expected_revision":1}`, "api", "DATABASE_URL"); got != 403 {
			t.Fatal("binding test crossed an authorization boundary", got)
		}
	}
	reader := p
	reader.Admin, reader.Owner = false, false
	reader.Permissions, reader.IdentityPermissions = []string{"deployments:read"}, []string{"deployments:read"}
	if got := call(reader, `{"expected_revision":1}`, "api", "DATABASE_URL"); got != 403 {
		t.Fatal("a read-only principal was allowed to attempt database authentication", got)
	}
}
