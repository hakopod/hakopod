package api

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/database"
	"github.com/hakopod/hakopod/internal/store"
)

func TestDatabaseMonitoringAPIUsesDurableScopedHistory(t *testing.T) {
	s := notificationTestDB(t)
	ctx := context.Background()
	token, err := s.Bootstrap(ctx, "monitoring-api-fixture")
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
	d := database.Resource{ID: store.NewID(), Project: "demo", Environment: "development", Spec: database.Spec{SchemaVersion: 1, Name: "monitoring-api-fixture", Engine: "postgresql", Version: "17", Mode: "standalone", Shards: 1, CPU: "100m", Memory: "256Mi", StorageGiB: 1}, EncryptedCredentials: []byte("private-credentials-must-not-appear")}
	if _, err = s.AcceptDatabase(ctx, p, d, 0, "monitoring-api-create", "create"); err != nil {
		t.Fatal(err)
	}
	stamp := time.Now().UTC().Add(-2 * time.Minute)
	cpu, memory, connections := 42.0, int64(4096), int64(4)
	o := database.Observation{Revision: 1, ObservedAt: stamp, Status: "ready", Metrics: &database.Metrics{Available: true, CPU: &cpu, Memory: &memory, SampledAt: &stamp, PodsSampled: 1, PodsExpected: 1}, EngineMetrics: &database.EngineMetrics{Available: true, SampledAt: &stamp, Connections: &connections}}
	if err = s.ObserveDatabase(ctx, d.ID, 1, o); err != nil {
		t.Fatal(err)
	}
	// A new HTTP server instance reads the persisted samples without any browser
	// or process-local history and without contacting Kubernetes.
	handler := (&Server{Store: s}).Handler()
	for _, tc := range []struct {
		name, environment, application, window string
		want                                   int
	}{
		{"reader", "development", "", "1h", 200},
		{"long window", "development", "", "24h", 200},
		{"wrong scope", "production", "", "1h", 404},
		{"application key", "development", "unrelated-app", "1h", 404},
		{"unbounded window", "development", "", "7d", 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, key, err := s.CreateKey(ctx, p, store.KeyInput{Name: tc.name, Project: "demo", Environment: tc.environment, Application: tc.application, Permissions: []string{"deployments:read"}, ExpiresAt: time.Now().Add(time.Hour)})
			if err != nil {
				t.Fatal(err)
			}
			req := httptest.NewRequest("GET", "/api/v1/databases/"+d.ID+"/metrics?range="+tc.window, nil)
			req.Header.Set("Authorization", "Bearer "+key)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, req)
			if response.Code != tc.want {
				t.Fatalf("unexpected HTTP status %d", response.Code)
			}
			if strings.Contains(response.Body.String(), "private-credentials") {
				t.Fatal("credential body leaked")
			}
			if tc.want == 200 {
				var history database.MetricHistory
				if err = json.Unmarshal(response.Body.Bytes(), &history); err != nil {
					t.Fatal(err)
				}
				if len(history.Items) != 1 || history.Items[0].Resources == nil || history.Items[0].Resources.CPU == nil || *history.Items[0].Resources.CPU != 42 || history.Items[0].Engine == nil || history.Items[0].Engine.Connections == nil || *history.Items[0].Engine.Connections != 4 || history.RetentionHours != 24 || response.Header().Get("Cache-Control") != "no-store" {
					t.Fatal("persisted scoped metrics or cache policy missing")
				}
			}
		})
	}
}
