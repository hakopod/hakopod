package api

import (
	"context"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/cluster"
	"github.com/hakopod/hakopod/internal/database"
	"github.com/hakopod/hakopod/internal/store"
)

func TestOracleSwitchoverRoutesEnforceScopeAndNativeGate(t *testing.T) {
	s := notificationTestDB(t)
	ctx := context.Background()
	token, err := s.Bootstrap(ctx, "oracle-role-api-fixture")
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
	d := database.Resource{ID: store.NewID(), Project: "demo", Environment: "development", Spec: database.Spec{SchemaVersion: 1, Name: "oracle-role-api-fixture", Engine: "oracle", Version: "19", Mode: "cluster", Shards: 1, Replicas: 2, CPU: "1", Memory: "4Gi", StorageGiB: 10, Oracle: &database.OracleConfig{Edition: "enterprise", Image: "registry.example.com/oracle@sha256:" + strings.Repeat("a", 64), LicenseConfirmed: true}}, EncryptedCredentials: []byte("synthetic-credential-never-returned")}
	d.Spec = d.Spec.WithSecureDefaults()
	if _, err = s.AcceptDatabase(ctx, p, d, 0, "oracle-api-create", "create"); err != nil {
		t.Fatal(err)
	}
	handler := (&Server{Store: s, Cluster: &cluster.Client{}}).Handler()
	for _, test := range []struct {
		name, environment, application, permission string
		want                                       int
	}{
		{"owner", "development", "", "deployments:write", 409},
		{"reader", "development", "", "deployments:read", 404},
		{"foreign environment", "production", "", "deployments:write", 404},
		{"application key", "development", "unrelated", "deployments:write", 404},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, key, err := s.CreateKey(ctx, p, store.KeyInput{Name: test.name, Project: "demo", Environment: test.environment, Application: test.application, Permissions: []string{test.permission}, ExpiresAt: time.Now().Add(time.Hour)})
			if err != nil {
				t.Fatal(err)
			}
			r := httptest.NewRequest("POST", "/api/v1/databases/"+d.ID+"/switchover-plan", strings.NewReader(`{"target_member":"development-standby"}`))
			r.Header.Set("Authorization", "Bearer "+key)
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			if w.Code != test.want {
				t.Fatalf("got %d: %s", w.Code, w.Body.String())
			}
			if strings.Contains(w.Body.String(), "synthetic-credential") {
				t.Fatal("route exposed private credentials")
			}
		})
	}
	for _, route := range []string{"switchover", "switchover-retry"} {
		body := fmt.Sprintf(`{"review_id":"%s","expected_revision":1,"confirm_name":"%s"}`, store.NewID(), d.Spec.Name)
		if route == "switchover-retry" {
			body = fmt.Sprintf(`{"operation_id":"%s","expected_revision":1,"confirm_name":"%s"}`, store.NewID(), d.Spec.Name)
		}
		r := httptest.NewRequest("POST", "/api/v1/databases/"+d.ID+"/"+route, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("Idempotency-Key", "oracle-role-gate-fixture")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != 503 {
			t.Fatalf("unqualified %s admitted: %d %s", route, w.Code, w.Body.String())
		}
	}
	var reviews, operations int
	if err = s.Pool.QueryRow(ctx, "SELECT count(*) FROM managed_database_reviews WHERE database_id=$1", d.ID).Scan(&reviews); err != nil {
		t.Fatal(err)
	}
	if err = s.Pool.QueryRow(ctx, "SELECT count(*) FROM managed_database_operations WHERE database_id=$1", d.ID).Scan(&operations); err != nil {
		t.Fatal(err)
	}
	if reviews != 0 || operations != 1 {
		t.Fatal("closed native gate persisted a role review or operation")
	}
}
