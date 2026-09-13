package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/api"
	"github.com/hakopod/hakopod/internal/cluster"
	"github.com/hakopod/hakopod/internal/spec"
	"github.com/hakopod/hakopod/internal/store"
)

func TestCloudCapabilitiesRespectMachineKeyScope(t *testing.T) {
	db, _ := database(t)
	ctx := context.Background()
	root, err := db.Bootstrap(ctx, "operator")
	if err != nil {
		t.Fatal(err)
	}
	owner, err := db.Authenticate(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	_, key, err := db.CreateKey(ctx, owner, store.KeyInput{Name: "cloud", Project: "demo", Environment: "development", Permissions: []string{"deployments:read", "deployments:write", "logs:read"}, ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != "GET" || r.URL.Path != "/api/v1/nodes" {
			t.Error("unexpected Kubernetes request")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"apiVersion":"v1","kind":"NodeList","metadata":{},"items":[{"metadata":{"name":"one"}}]}`))
	}))
	defer fixture.Close()
	config := map[string]any{"apiVersion": "v1", "kind": "Config", "current-context": "fixture", "contexts": []any{map[string]any{"name": "fixture", "context": map[string]any{"cluster": "fixture", "user": "fixture"}}}, "clusters": []any{map[string]any{"name": "fixture", "cluster": map[string]any{"server": fixture.URL}}}, "users": []any{map[string]any{"name": "fixture", "user": map[string]any{}}}}
	data, _ := json.Marshal(config)
	path := filepath.Join(t.TempDir(), "kubeconfig")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	kube, err := cluster.New(path, cluster.Options{DeploymentMode: cluster.DeploymentManagedCloud})
	if err != nil {
		t.Fatal(err)
	}
	db.ValidateDeployment = func(ctx context.Context, app store.Application, next spec.Application) error {
		return kube.ValidateDelivery(ctx, cluster.Target{Spec: next})
	}
	handler := (&api.Server{Store: db, Cluster: kube}).Handler()
	for _, item := range []struct {
		query, key string
		status     int
	}{{"?project=demo&environment=development", key, 200}, {"?project=other&environment=development", key, 403}, {"?project=demo&environment=production", key, 403}, {"?project=demo", key, 403}, {"?project=demo&environment=development", "", 401}} {
		req := httptest.NewRequest("GET", "/api/v1/cloud/capabilities"+item.query, nil)
		if item.key != "" {
			req.Header.Set("Authorization", "Bearer "+item.key)
		}
		out := httptest.NewRecorder()
		handler.ServeHTTP(out, req)
		if out.Code != item.status {
			t.Fatalf("%s: %d %s", item.query, out.Code, out.Body.String())
		}
		if out.Code == 200 {
			var result cluster.CloudCapabilities
			if json.Unmarshal(out.Body.Bytes(), &result) != nil || result.NodeCount != 1 || !result.Enforced {
				t.Fatal(out.Body.String())
			}
		}
	}
	if calls != 1 {
		t.Fatal("unauthorized scope called Kubernetes", calls)
	}
}
