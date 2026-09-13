package api_test

import (
	"context"
	"net/url"
	"testing"
)

func TestProjectAdministratorsCreateIsolatedEnvironments(t *testing.T) {
	h := newAuthHarness(t, nil)
	owner := h.owner()
	team := h.call("POST", "/teams", owner, map[string]string{"name": "Environment operators"}, 201)
	invite := func(email, role string) string {
		t.Helper()
		result := h.call("POST", "/teams/"+team["id"].(string)+"/invites", owner, map[string]any{"email": email, "role": role, "project": "demo"}, 201)
		link, _ := url.Parse(result["invite_url"].(string))
		accepted := h.call("POST", "/auth/invites/accept", "", map[string]string{"token": link.Query().Get("token"), "name": role, "password": "fixture account password strong"}, 200)
		return accepted["token"].(string)
	}
	admin := invite("project-admin@example.test", "admin")
	developer := invite("project-developer@example.test", "developer")
	h.call("POST", "/projects/demo/environments", developer, map[string]string{"name": "staging"}, 403)
	h.call("POST", "/projects/demo/environments", admin, map[string]string{"name": "staging"}, 201)
	h.call("POST", "/projects/demo/environments", admin, map[string]string{"name": "staging"}, 409)
	h.call("POST", "/projects/demo/environments", admin, map[string]string{"name": "production"}, 201)
	h.call("POST", "/projects/demo/environments", admin, map[string]string{"name": "../invalid"}, 400)
	h.call("POST", "/projects", owner, map[string]string{"name": "other-project", "environment": "development"}, 201)
	h.call("POST", "/projects/other-project/environments", admin, map[string]string{"name": "staging"}, 403)
	h.call("POST", "/projects", admin, map[string]string{"name": "forbidden-new-project", "environment": "development"}, 403)
	if _, err := h.db.Pool.Exec(context.Background(), "INSERT INTO environments(project,name) SELECT 'demo','bounded-'||n FROM generate_series(1,29) n"); err != nil {
		t.Fatal(err)
	}
	h.call("POST", "/projects/demo/environments", admin, map[string]string{"name": "overflow"}, 400)
	h.call("POST", "/projects", owner, map[string]string{"name": "demo", "environment": "overflow"}, 400)
	var count int
	if err := h.db.Pool.QueryRow(context.Background(), "SELECT count(*) FROM audit_events WHERE action='environment.create' AND resource IN ('demo/staging','demo/production')").Scan(&count); err != nil || count != 2 {
		t.Fatal("environment audit was missing or duplicated", err)
	}
}
