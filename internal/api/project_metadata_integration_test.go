package api_test

import (
	"context"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/store"
)

func TestProjectMetadataAndScopedListing(t *testing.T) {
	h := newAuthHarness(t, nil)
	owner := h.owner()
	input := map[string]string{"name": "z-workspace", "display_name": "My workspace", "description": "A private deployment project.", "environment": "development"}
	created := h.call("POST", "/projects", owner, input, 201)
	if created["display_name"] != input["display_name"] || created["description"] != input["description"] {
		t.Fatal("project metadata not persisted")
	}
	input["display_name"] = "Accidental overwrite"
	input["environment"] = "production"
	h.call("POST", "/projects", owner, input, 409)
	ctx := context.Background()
	var hasProduction bool
	if err := h.db.Pool.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM environments WHERE project='z-workspace' AND name='production')").Scan(&hasProduction); err != nil || hasProduction {
		t.Fatal("duplicate creation changed project", err)
	}
	added := h.call("POST", "/projects", owner, map[string]string{"name": "z-workspace", "environment": "production"}, 201)
	if added["display_name"] != "My workspace" {
		t.Fatal("adding an environment changed project metadata")
	}
	// A scoped user must not lose their workspace behind other projects' list limit.
	if _, err := h.db.Pool.Exec(ctx, "INSERT INTO projects(name) SELECT 'a-project-'||n FROM generate_series(1,200) n; INSERT INTO environments(project,name) SELECT name,'development' FROM projects WHERE name LIKE 'a-project-%'"); err != nil {
		t.Fatal(err)
	}
	p, err := h.db.Authenticate(ctx, owner)
	if err != nil {
		t.Fatal(err)
	}
	_, key, err := h.db.CreateKey(ctx, p, store.KeyInput{Name: "scoped", Project: "z-workspace", Environment: "development", Permissions: []string{"deployments:read"}, ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	result := h.call("GET", "/projects", key, nil, 200)
	items := result["items"].([]any)
	if len(items) != 1 {
		t.Fatal("scoped list leaked or hid projects", len(items))
	}
	project := items[0].(map[string]any)
	if project["name"] != "z-workspace" || project["display_name"] != "My workspace" || len(project["environments"].([]any)) != 1 {
		t.Fatal("scope or metadata mismatch")
	}
	h.call("POST", "/projects", key, map[string]string{"name": "unauthorized", "environment": "development"}, 403)
}
