package store

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/bindingprobe"
	"github.com/hakopod/hakopod/internal/spec"
)

func TestBindingEvidencePersistsLatestAndEnforcesScope(t *testing.T) {
	s := isolatedDatabase(t)
	p := bootstrapPrincipal(t, s)
	ctx := context.Background()
	app, err := spec.Normalize(spec.Application{Name: "binding-evidence", Services: map[string]spec.Service{
		"db":  {Image: "postgres:17", Port: 5432},
		"api": {Image: "nginx:alpine", Bindings: map[string]spec.Binding{"DATABASE_URL": {Service: "db", Protocol: "postgres", Username: "app", Database: "app", Password: &spec.SecretRef{Ref: "db-password"}}}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	d, err := s.Accept(ctx, p, "demo", "development", app, 0, "binding-evidence-initial")
	if err != nil {
		t.Fatal(err)
	}
	result := bindingprobe.TestResult{SchemaVersion: 1, ApplicationID: d.ApplicationID, Service: "api", Variable: "DATABASE_URL", Revision: 1, ObservedAt: time.Now().UTC(), Outcome: "passed", Stages: []bindingprobe.Stage{}, Evidence: bindingprobe.RuntimeEvidence{ContainerID: "private-container-identity", SecretUID: "private-snapshot-identity", SecretVersion: "1"}}
	if err = s.RecordBindingTest(ctx, result); err != nil {
		t.Fatal(err)
	}
	old := result
	old.ObservedAt = result.ObservedAt.Add(-time.Minute)
	old.Outcome = "failed"
	if err = s.RecordBindingTest(ctx, old); err != nil {
		t.Fatal(err)
	}
	got, err := s.LatestBindingTest(ctx, p, d.ApplicationID, "api", "DATABASE_URL")
	if err != nil || got == nil || got.Outcome != "passed" || got.Evidence != result.Evidence {
		t.Fatal("latest evidence not retained", err)
	}
	public, _ := json.Marshal(got)
	if strings.Contains(string(public), "private-") || strings.Contains(string(public), "secret_version") {
		t.Fatal("private evidence exposed")
	}
	foreign := p
	foreign.Project = "other"
	if _, err = s.LatestBindingTest(ctx, foreign, d.ApplicationID, "api", "DATABASE_URL"); !errors.Is(err, ErrForbidden) {
		t.Fatal("foreign scope read evidence", err)
	}
	result.Revision++
	if err = s.RecordBindingTest(ctx, result); !errors.Is(err, ErrConflict) {
		t.Fatal("unaccepted revision recorded", err)
	}
}
