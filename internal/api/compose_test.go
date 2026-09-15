package api

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/spec"
	"github.com/hakopod/hakopod/internal/store"
)

func TestComposeConversionScopeRevisionAndNoMutation(t *testing.T) {
	db := sourceDatabase(t)
	ctx := context.Background()
	owner, err := db.Bootstrap(ctx, "compose-review")
	if err != nil {
		t.Fatal(err)
	}
	p, err := db.Authenticate(ctx, owner)
	if err != nil {
		t.Fatal(err)
	}
	keys := map[string]string{}
	for _, kind := range []string{"writer", "reader", "foreign"} {
		project := "demo"
		permissions := []string{"deployments:read"}
		if kind != "reader" {
			permissions = append(permissions, "deployments:write")
		}
		if kind == "foreign" {
			project = "other"
			if _, err = db.Pool.Exec(ctx, "INSERT INTO projects(name) VALUES('other'); INSERT INTO environments(project,name) VALUES('other','development')"); err != nil {
				t.Fatal(err)
			}
		}
		_, key, e := db.CreateKey(ctx, p, store.KeyInput{Name: kind, Project: project, Environment: "development", Permissions: permissions, ExpiresAt: time.Now().Add(time.Hour)})
		if e != nil {
			t.Fatal(e)
		}
		keys[kind] = key
	}
	s := &Server{Store: db}
	handler := s.Handler()
	input := map[string]any{"project": "demo", "environment": "development", "name": "imported", "yaml": "services:\n  api:\n    image: busybox:stable\n    command: [sleep, '3600']\n"}
	result := gitConnectionCall(t, handler, keys["writer"], "POST", "/compose/convert", input, 200)
	if result["expected_revision"] != float64(0) || !strings.Contains(result["toml"].(string), "busybox:stable") {
		t.Fatal("missing draft")
	}
	gitConnectionCall(t, handler, keys["reader"], "POST", "/compose/convert", input, 403)
	gitConnectionCall(t, handler, keys["foreign"], "POST", "/compose/convert", input, 403)
	var count int
	if err = db.Pool.QueryRow(ctx, "SELECT count(*) FROM applications").Scan(&count); err != nil || count != 0 {
		t.Fatal("conversion created an application", err)
	}
	app, err := spec.Parse([]byte(result["toml"].(string)))
	if err != nil {
		t.Fatal(err)
	}
	d, err := db.Accept(ctx, p, "demo", "development", app, 0, "compose-fixture")
	if err != nil {
		t.Fatal(err)
	}
	input["application_id"] = d.ApplicationID
	input["expected_revision"] = 1
	input["yaml"] = "services:\n  worker:\n    image: busybox:stable\n"
	result = gitConnectionCall(t, handler, keys["writer"], "POST", "/compose/convert", input, 200)
	app, err = spec.Parse([]byte(result["toml"].(string)))
	if err != nil || len(app.Services) != 2 {
		t.Fatal("merge did not preserve services", err)
	}
	current, err := db.Application(ctx, d.ApplicationID)
	if err != nil || len(current.Spec.Services) != 1 || current.Revision != 1 {
		t.Fatal("conversion mutated stored application", err)
	}
	input["expected_revision"] = 0
	gitConnectionCall(t, handler, keys["writer"], "POST", "/compose/convert", input, 409)
	input["expected_revision"] = 1
	gitConnectionCall(t, handler, keys["foreign"], "POST", "/compose/convert", input, 403)
	input["yaml"] = "services:\n  api:\n    image: busybox:stable\n"
	gitConnectionCall(t, handler, keys["writer"], "POST", "/compose/convert", input, 400)
}
