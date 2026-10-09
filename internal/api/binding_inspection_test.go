package api

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/hakopod/hakopod/internal/bindingprobe"
	"github.com/hakopod/hakopod/internal/spec"
	"github.com/hakopod/hakopod/internal/store"
)

func TestBindingInspectionReadScopeAndUnavailableRuntime(t *testing.T) {
	db := sourceDatabase(t)
	ctx := context.Background()
	raw, err := db.Bootstrap(ctx, "binding-inspection-operator")
	if err != nil {
		t.Fatal(err)
	}
	p, err := db.Authenticate(ctx, raw)
	if err != nil {
		t.Fatal(err)
	}
	a := spec.Application{Name: "binding-inspection", Services: map[string]spec.Service{
		"db":  {Image: "postgres:17", Port: 5432},
		"api": {Image: "nginx:alpine", Bindings: map[string]spec.Binding{"DATABASE_URL": {Service: "db", Protocol: "postgres", Username: "app", Database: "app", Password: &spec.SecretRef{Ref: "database-password"}}}},
	}}
	d, err := db.Accept(ctx, p, "demo", "development", a, 0, "binding-inspection-fixture")
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{Store: db}
	call := func(principal store.Principal, variable string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", "/inspection", nil)
		r.SetPathValue("id", d.ApplicationID)
		r.SetPathValue("service", "api")
		r.SetPathValue("variable", variable)
		r = r.WithContext(context.WithValue(ctx, principalKey{}, principal))
		w := httptest.NewRecorder()
		s.inspectServiceBinding(w, r)
		return w
	}
	reader := p
	reader.Admin, reader.Owner = false, false
	reader.Permissions, reader.IdentityPermissions = []string{"deployments:read"}, []string{"deployments:read"}
	w := call(reader, "DATABASE_URL")
	var result bindingprobe.Inspection
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &result) != nil || len(result.Steps) != 4 || result.Steps[0].Status != "passed" || result.Steps[3].Status != "unknown" || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("unavailable runtime invented verification", w.Code, w.Body.String())
	}
	reader.Project = "other"
	if call(reader, "DATABASE_URL").Code != 403 {
		t.Fatal("foreign scope inspected binding")
	}
	if call(p, "UNDECLARED").Code != 404 {
		t.Fatal("undeclared variable exposed")
	}
}
