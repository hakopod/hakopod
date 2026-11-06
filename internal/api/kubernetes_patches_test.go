package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hakopod/hakopod/internal/cluster"
	"k8s.io/client-go/rest"
)

func TestKubernetesPatchSettingsAreAdminOnlyAndConflictSafe(t *testing.T) {
	f := newAppLogFixture(t)
	if status, _ := f.call(t, "GET", "/settings/kubernetes-patches", f.reader1, nil); status != 403 {
		t.Fatalf("non-admin GET: %d", status)
	}
	if status, _ := f.call(t, "PUT", "/settings/kubernetes-patches", f.reader1, map[string]any{"enabled": true, "expected_revision": 0}); status != 403 {
		t.Fatalf("non-admin PUT: %d", status)
	}
	status, out := f.call(t, "GET", "/settings/kubernetes-patches", f.admin, nil)
	if status != 200 || out["enabled"] != false || out["revision"] != float64(0) {
		t.Fatalf("patches must default to off: %d %v", status, out)
	}
	put := map[string]any{"enabled": true, "expected_revision": 0}
	if status, out = f.call(t, "PUT", "/settings/kubernetes-patches", f.admin, put); status != 200 || out["enabled"] != true || out["revision"] != float64(1) {
		t.Fatalf("enable: %d %v", status, out)
	}
	if status, _ = f.call(t, "PUT", "/settings/kubernetes-patches", f.admin, put); status != 409 {
		t.Fatalf("stale revision: %d", status)
	}
}

func TestApplicationManifestsRenderWithoutApplying(t *testing.T) {
	f := newAppLogFixture(t)
	kube := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("rendering manifests sent %s %s", r.Method, r.URL.Path)
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(kube.Close)
	c, err := cluster.NewWithConfig(&rest.Config{Host: kube.URL}, cluster.Options{AppDomain: "apps.example.test"})
	if err != nil {
		t.Fatal(err)
	}
	f.server.Config.Handler = (&Server{Store: f.db, Cluster: c}).Handler()
	if status, _ := f.call(t, "GET", "/applications/"+f.appID+"/manifests", f.reader1, nil); status != 403 {
		t.Fatalf("manifests need deployments:read: %d", status)
	}
	if status, _ := f.call(t, "GET", "/applications/"+f.appID+"/manifests?service=missing", f.admin, nil); status != 404 {
		t.Fatalf("unknown service: %d", status)
	}
	status, out := f.call(t, "GET", "/applications/"+f.appID+"/manifests?service=web", f.admin, nil)
	text, _ := out["manifests"].(string)
	if status != 200 || !strings.Contains(text, "kind: Deployment") || strings.Contains(text, "kind: Secret") {
		t.Fatalf("manifests: %d %v", status, out)
	}
}
