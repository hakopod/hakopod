package api

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/database"
	"github.com/hakopod/hakopod/internal/store"
)

func TestDatabasePublicTrustIsScopedAndNeverRevealsCredentials(t *testing.T) {
	s := notificationTestDB(t)
	ctx := context.Background()
	token, err := s.Bootstrap(ctx, "database-trust-fixture")
	if err != nil {
		t.Fatal(err)
	}
	p, err := s.Authenticate(ctx, token)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Pool.Exec(ctx, "INSERT INTO environments(project,name) VALUES('demo','production')"); err != nil {
		t.Fatal(err)
	}
	d := database.Resource{ID: store.NewID(), Project: "demo", Environment: "development", Spec: database.Spec{SchemaVersion: 1, Name: "trust-fixture", Engine: "postgresql", Version: "17", Mode: "standalone", Shards: 1, CPU: "100m", Memory: "256Mi", StorageGiB: 1}, EncryptedCredentials: []byte("private-credential-body-never-returned")}
	if _, err = s.AcceptDatabase(ctx, p, d, 0, "database-trust-create", "create"); err != nil {
		t.Fatal(err)
	}
	handler := (&Server{Store: s}).Handler()
	for _, tc := range []struct {
		name, environment, application string
		want                           int
	}{
		{"reader", "development", "", 409},
		{"wrong environment", "production", "", 404},
		{"application scope", "development", "unrelated-app", 404},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, key, err := s.CreateKey(ctx, p, store.KeyInput{Name: tc.name, Project: "demo", Environment: tc.environment, Application: tc.application, Permissions: []string{"deployments:read"}, ExpiresAt: time.Now().Add(time.Hour)})
			if err != nil {
				t.Fatal(err)
			}
			r := httptest.NewRequest("GET", "/api/v1/databases/"+d.ID+"/trust", nil)
			r.Header.Set("Authorization", "Bearer "+key)
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			if w.Code != tc.want {
				t.Fatalf("status %d: %s", w.Code, w.Body.String())
			}
			if strings.Contains(w.Body.String(), "private-credential-body") || strings.Contains(w.Body.String(), "PRIVATE KEY") {
				t.Fatal("private material returned")
			}
		})
	}
}
