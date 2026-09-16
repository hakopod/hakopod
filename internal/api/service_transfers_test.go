package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/spec"
	"github.com/hakopod/hakopod/internal/store"
)

func TestServiceMoveAPISecretsAuthorizationAndRetries(t *testing.T) {
	db := sourceDatabase(t)
	ctx := context.Background()
	raw, err := db.Bootstrap(ctx, "service-move-test")
	if err != nil {
		t.Fatal(err)
	}
	p, err := db.Authenticate(ctx, raw)
	if err != nil {
		t.Fatal(err)
	}
	c := templateSecretKube(t)
	s := &Server{Store: db, Cluster: c}
	handler := s.Handler()
	create := func(name string) store.Application {
		a, _ := spec.Normalize(spec.Application{Name: name, Services: map[string]spec.Service{"api": {Image: "nginx@sha256:" + strings.Repeat("a", 64), Port: 8080}}})
		if name == "source" {
			a.Secrets = map[string]spec.SecretRef{"AUTH": {Ref: "auth"}}
		}
		d, e := db.Accept(ctx, p, "demo", "development", a, 0, "fixture-"+name)
		if e != nil {
			t.Fatal(e)
		}
		claim, e := db.Claim(ctx)
		if e != nil || claim == nil {
			t.Fatal(e)
		}
		if _, e = claim.SetResolved(ctx, a); e != nil {
			t.Fatal(e)
		}
		if e = claim.Finish(ctx, "succeeded", "", map[string]any{}); e != nil {
			t.Fatal(e)
		}
		claim.Release()
		app, e := db.Application(ctx, d.ApplicationID)
		if e != nil {
			t.Fatal(e)
		}
		return app
	}
	source, dest := create("source"), create("destination")
	const secret = "fixture-private-value-do-not-render"
	if err = c.CreateWorkloadSecret(ctx, "demo", "development", "source", "auth", secret); err != nil {
		t.Fatal(err)
	}
	call := func(key, method, path, idem string, body any, status int) []byte {
		t.Helper()
		r := httptest.NewRequest(method, "/api/v1"+path, bytes.NewReader(store.JSON(body)))
		r.Header.Set("Authorization", "Bearer "+key)
		r.Header.Set("Idempotency-Key", idem)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != status {
			t.Fatalf("%s: status %d expected %d: %s", path, w.Code, status, w.Body.String())
		}
		if strings.Contains(w.Body.String(), secret) {
			t.Fatal("secret value in API response")
		}
		return w.Body.Bytes()
	}
	in := transferInput{DestinationID: dest.ID, DestinationService: "worker", SourceRevision: 1, DestinationRevision: 1}
	path := "/applications/" + source.ID + "/services/api"
	_, reader, e := db.CreateKey(ctx, p, store.KeyInput{Name: "reader", Project: "demo", Environment: "development", Permissions: []string{"deployments:read"}, ExpiresAt: time.Now().Add(time.Hour)})
	if e != nil {
		t.Fatal(e)
	}
	_, scoped, e := db.CreateKey(ctx, p, store.KeyInput{Name: "only-source", Project: "demo", Environment: "development", Application: "source", Permissions: []string{"deployments:read", "deployments:write"}, ExpiresAt: time.Now().Add(time.Hour)})
	if e != nil {
		t.Fatal(e)
	}
	call(reader, "POST", path+"/move-plan", "", in, 403)
	call(scoped, "POST", path+"/move-plan", "", in, 403)
	call(raw, "POST", path+"/move-plan", "", in, 200)
	var d store.Deployment
	if err = json.Unmarshal(call(raw, "POST", path+"/move", "move-start-test", in, 202), &d); err != nil {
		t.Fatal(err)
	}
	call(raw, "POST", path+"/move", "move-start-test", in, 202)
	if d.Spec.Services["worker"].Secrets["AUTH"].Ref == "auth" {
		t.Fatal("did not isolate copied secret")
	}
	saved, e := c.ReadWorkloadSecrets(ctx, "demo", "development", "destination", []string{d.Spec.Services["worker"].Secrets["AUTH"].Ref})
	if e != nil || saved[d.Spec.Services["worker"].Secrets["AUTH"].Ref] != secret {
		t.Fatal("secret not copied")
	}
	finish := "/applications/" + source.ID + "/service-moves/" + d.ID + "/finish"
	call(raw, "POST", finish, "move-finish-test", nil, 409)
	claim, e := db.Claim(ctx)
	if e != nil || claim == nil {
		t.Fatal(e)
	}
	if _, e = claim.SetResolved(ctx, d.Spec); e != nil {
		t.Fatal(e)
	}
	if e = claim.Finish(ctx, "succeeded", "", map[string]any{}); e != nil {
		t.Fatal(e)
	}
	claim.Release()
	call(raw, "POST", finish, "move-finish-test", nil, 202)
	call(raw, "POST", finish, "move-finish-test", nil, 202)
	app, e := db.Application(ctx, source.ID)
	if e != nil || len(app.Spec.Services) != 0 {
		t.Fatal("original removal missing")
	}
}

func TestTemplateCanAddServicesToExistingApplication(t *testing.T) {
	db := sourceDatabase(t)
	ctx := context.Background()
	raw, err := db.Bootstrap(ctx, "catalog-add-test")
	if err != nil {
		t.Fatal(err)
	}
	p, err := db.Authenticate(ctx, raw)
	if err != nil {
		t.Fatal(err)
	}
	a, _ := spec.Normalize(spec.Application{Name: "existing", Services: map[string]spec.Service{"existing": {Image: "nginx:alpine", Port: 8080}}})
	d, err := db.Accept(ctx, p, "demo", "development", a, 0, "fixture-catalog")
	if err != nil {
		t.Fatal(err)
	}
	claim, err := db.Claim(ctx)
	if err != nil || claim == nil {
		t.Fatal(err)
	}
	pinned, _ := spec.Normalize(a)
	svc := pinned.Services["existing"]
	svc.Image = "nginx@sha256:" + strings.Repeat("a", 64)
	pinned.Services["existing"] = svc
	if _, err = claim.SetResolved(ctx, pinned); err != nil {
		t.Fatal(err)
	}
	if err = claim.Finish(ctx, "succeeded", "", map[string]any{}); err != nil {
		t.Fatal(err)
	}
	claim.Release()
	s := &Server{Store: db, Cluster: templateSecretKube(t)}
	h := s.Handler()
	rev := int64(1)
	input := templateConfiguration{Project: "demo", Environment: "development", ApplicationID: d.ApplicationID, ExpectedRevision: &rev, TemplateOptions: spec.TemplateOptions{Name: "existing"}}
	result := gitConnectionCall(t, h, raw, "POST", "/templates/nginx/plan", input, 200)
	var plan struct {
		Spec spec.Application `json:"spec"`
		TOML string           `json:"toml"`
	}
	if err = json.Unmarshal(store.JSON(result), &plan); err != nil {
		t.Fatal(err)
	}
	if len(plan.Spec.Services) != 2 || plan.Spec.Services["existing"].Image != "nginx@sha256:"+strings.Repeat("a", 64) {
		t.Fatal("catalog replaced existing service")
	}
	gitConnectionCall(t, h, raw, "POST", "/templates/nginx/deploy", map[string]any{"configuration": input, "toml": plan.TOML, "expected_revision": 1}, 202)
	gitConnectionCall(t, h, raw, "POST", "/templates/nginx/deploy", map[string]any{"configuration": input, "toml": plan.TOML, "expected_revision": 1}, 202)
	gitConnectionCall(t, h, raw, "POST", "/templates/nginx/plan", input, 409)
}
