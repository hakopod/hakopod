package store

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/requestlog"
)

func TestRequestsRetentionPaginationAndPermissions(t *testing.T) {
	db := isolatedDatabase(t)
	p := bootstrapPrincipal(t, db)
	ctx := context.Background()
	dep, err := db.Accept(ctx, p, "demo", "development", emptyTestSpec(), 0, "request-fixture-deployment")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	entry := func(i int, app string) requestlog.Entry {
		return requestlog.Entry{ID: fmt.Sprintf("%064d", i), Timestamp: now.Add(-time.Duration(i) * time.Second), ApplicationID: app, Service: "api", Method: "GET", Host: "example.test", Path: "/health", Status: 200, DurationMS: 42}
	}
	items := []requestlog.Entry{entry(1, dep.ApplicationID), entry(2, dep.ApplicationID), entry(3, ""), entry(4, dep.ApplicationID)}
	items[3].Timestamp = now.Add(-25 * time.Hour)
	if err = db.SaveRequests(ctx, items, map[string]time.Time{"pod": now}, "collecting", "Collecting", 0); err != nil {
		t.Fatal(err)
	}
	if err = db.SaveRequests(ctx, items[:3], map[string]time.Time{"pod": now}, "collecting", "Collecting", 0); err != nil {
		t.Fatal(err)
	}
	first, err := db.Requests(ctx, p, RequestQuery{Limit: 1})
	if err != nil || len(first.Items) != 1 || first.NextCursor == "" {
		t.Fatalf("first page %+v %v", first, err)
	}
	second, err := db.Requests(ctx, p, RequestQuery{Limit: 1, Cursor: first.NextCursor})
	if err != nil || second.Items[0].ID == first.Items[0].ID {
		t.Fatal("pagination repeated")
	}
	all, err := db.Requests(ctx, p, RequestQuery{})
	if err != nil || len(all.Items) != 3 {
		t.Fatalf("deduplication/retention failed: %d %v", len(all.Items), err)
	}
	limited := p
	limited.Project = "demo"
	limited.Application = "store-test"
	limited.Permissions = []string{"logs:read"}
	scoped, err := db.Requests(ctx, limited, RequestQuery{})
	if err != nil || len(scoped.Items) != 2 {
		t.Fatalf("scoped results: %+v %v", scoped, err)
	}
	for _, principal := range []Principal{
		{Admin: true, Permissions: []string{"deployments:read"}},
		{Admin: true, Permissions: []string{"logs:read"}, Project: "other"},
		{Admin: true, Permissions: []string{"logs:read"}, IdentityEnvironment: "production"},
		{Permissions: []string{"logs:read"}, IdentityPermissions: []string{"deployments:read"}},
		{Email: "reader@example.test", Permissions: []string{"logs:read"}, ProjectRoles: []ProjectRole{{Project: "other", Role: "viewer"}}},
	} {
		r, e := db.Requests(ctx, principal, RequestQuery{Search: "example"})
		if e != nil || len(r.Items) != 0 {
			t.Fatalf("unauthorized request exposed: %+v %v", principal, e)
		}
	}
	human := Principal{Email: "reader@example.test", Permissions: []string{"logs:read"}, ProjectRoles: []ProjectRole{{Project: "demo", Role: "viewer"}}}
	r, e := db.Requests(ctx, human, RequestQuery{Status: 2, Search: "health"})
	if e != nil || len(r.Items) != 2 {
		t.Fatalf("human scope: %+v %v", r, e)
	}
	if _, e = db.Requests(ctx, p, RequestQuery{Cursor: strings.Repeat("!", 4)}); e == nil {
		t.Fatal("invalid cursor accepted")
	}
	cursors, e := db.RequestCursors(ctx)
	if e != nil || !cursors["pod"].Equal(now.Truncate(time.Microsecond)) {
		t.Fatalf("cursor not persisted: %v %v", cursors, e)
	}
}
