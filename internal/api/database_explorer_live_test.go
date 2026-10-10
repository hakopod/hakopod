package api

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/cluster"
)

// This opt-in fixture runs only on the isolated development VM. It retains a
// genuine engine browser session for the scoped dashboard and container smoke.
// The empty cluster is explicit: this does not qualify managed DB networking.
func TestExplorerBrowserDevelopmentFixture(t *testing.T) {
	if os.Getenv("HAKOPOD_EXPLORER_REVIEW") != "1" {
		t.Skip("enable isolated explorer browser fixture")
	}
	dir := os.Getenv("HAKOPOD_EXPLORER_REVIEW_DIR")
	if !strings.HasPrefix(dir, "/srv/hakopod-synehq-dev/") {
		t.Fatal("use the isolated development directory")
	}
	db := sourceDatabase(t)
	ctx := context.Background()
	owner, err := db.SetupOwner(ctx, "Development reviewer", "reviewer@example.test", "isolated development fixture password", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Pool.Exec(ctx, "INSERT INTO projects(name) VALUES('demo') ON CONFLICT DO NOTHING; INSERT INTO environments(project,name) VALUES('demo','development') ON CONFLICT DO NOTHING"); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Pool.Exec(ctx, "INSERT INTO project_members(project,identity_id,role) VALUES('demo',$1,'admin')", owner.ID); err != nil {
		t.Fatal(err)
	}
	session, err := db.NewSession(ctx, owner.ID, "browser", "", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(map[string]any{"token": session.Token, "actor": owner.ID, "name": owner.Name})
	if err = os.WriteFile(filepath.Join(dir, "session.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:38080")
	if err != nil {
		t.Fatal(err)
	}
	handler := (&Server{Store: db, Cluster: &cluster.Client{}}).Handler()
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/v1/") {
			_, restrictedErr := os.Stat(filepath.Join(dir, "restricted"))
			restricted := restrictedErr == nil
			permissions := []string{"deployments:read", "databases:query"}
			if !restricted {
				permissions = append(permissions, "deployments:write", "databases:write-query")
			}
			r = WithRuntimeScope(r, RuntimeScope{Identity: owner.ID, Project: "demo", Environment: "development", Permissions: permissions, Authorize: func(context.Context) error {
				_, currentErr := os.Stat(filepath.Join(dir, "restricted"))
				if (currentErr == nil) != restricted {
					return context.Canceled
				}
				return nil
			}})
		}
		handler.ServeHTTP(w, r)
	}), ReadHeaderTimeout: 5 * time.Second}
	go server.Serve(listener)
	t.Cleanup(func() {
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		server.Shutdown(shutdown)
		os.Remove(filepath.Join(dir, "session.json"))
	})
	t.Log("Development fixture ready at loopback port 38080; credentials are in the private review directory.")
	timer := time.NewTimer(20 * time.Minute)
	defer timer.Stop()
	<-timer.C
}
