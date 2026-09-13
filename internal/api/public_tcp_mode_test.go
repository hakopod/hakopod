package api_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/api"
	"github.com/hakopod/hakopod/internal/cluster"
	"github.com/hakopod/hakopod/internal/spec"
	"github.com/hakopod/hakopod/internal/store"
)

func TestManagedCloudDeniesPublicTCPForAdminScopedDeployAndRollback(t *testing.T) {
	db, _ := database(t)
	ctx := context.Background()
	root, err := db.Bootstrap(ctx, "cloud-operator")
	if err != nil {
		t.Fatal(err)
	}
	principal, err := db.Authenticate(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	var clusterRequests atomic.Int32
	kubernetes := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		clusterRequests.Add(1)
		http.Error(w, "no cluster call expected", http.StatusServiceUnavailable)
	}))
	defer kubernetes.Close()
	kubeconfig := filepath.Join(t.TempDir(), "kubeconfig")
	data := `{"apiVersion":"v1","kind":"Config","current-context":"fixture","contexts":[{"name":"fixture","context":{"cluster":"fixture","user":"fixture"}}],"clusters":[{"name":"fixture","cluster":{"server":"` + kubernetes.URL + `"}}],"users":[{"name":"fixture","user":{}}]}`
	if err = os.WriteFile(kubeconfig, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	kube, err := cluster.New(kubeconfig, cluster.Options{DeploymentMode: "managed-cloud"})
	if err != nil {
		t.Fatal(err)
	}
	public, err := spec.Parse([]byte(`name='cloud-mail'
[services.smtp]
image='docker.io/library/python@sha256:` + strings.Repeat("a", 64) + `'
port=2525
[[services.smtp.public_tcp]]
port=587
target_port=2525
source_cidrs=['0.0.0.0/0']
`))
	if err != nil {
		t.Fatal(err)
	}
	// Seed successful historical revisions without a running worker. They
	// represent a self-hosted installation before its public listener removal.
	db.ValidateDeployment = func(context.Context, store.Application, spec.Application) error { return nil }
	finish := func(app spec.Application, revision int64, key string) store.Deployment {
		t.Helper()
		deployment, err := db.Accept(ctx, principal, "demo", "development", app, revision, key)
		if err != nil {
			t.Fatal(err)
		}
		claim, err := db.Claim(ctx)
		if err != nil || claim == nil {
			t.Fatal("claim fixture revision", err)
		}
		defer claim.Release()
		if _, err = claim.SetResolved(ctx, app); err != nil {
			t.Fatal(err)
		}
		if err = claim.Finish(ctx, "succeeded", "", map[string]any{"status": "healthy"}); err != nil {
			t.Fatal(err)
		}
		return deployment
	}
	initial := finish(public, 0, "cloud-legacy-public")
	private, _ := spec.Normalize(public)
	svc := private.Services["smtp"]
	svc.PublicTCP = nil
	private.Services["smtp"] = svc
	finish(private, 1, "cloud-remove-public")
	db.ValidateDeployment = func(ctx context.Context, app store.Application, next spec.Application) error {
		return kube.ValidateDelivery(ctx, cluster.Target{ApplicationID: app.ID, Project: app.Project, Environment: app.Environment, Spec: next})
	}
	_, scoped, err := db.CreateKey(ctx, principal, store.KeyInput{Name: "cloud-deployer", Project: "demo", Environment: "development", Application: "cloud-mail", Permissions: []string{"deployments:read", "deployments:write"}, ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	handler := (&api.Server{Store: db, Cluster: kube}).Handler()
	call := func(method, path, token, key string, body any, want int) map[string]any {
		t.Helper()
		r := httptest.NewRequest(method, "/api/v1"+path, strings.NewReader(string(store.JSON(body))))
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("Idempotency-Key", key)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s %s status=%d, want=%d: %s", method, path, w.Code, want, w.Body.String())
		}
		var result map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		return result
	}
	input := map[string]any{"project": "demo", "environment": "development", "spec": public, "expected_revision": 2}
	for i, token := range []string{root, scoped} {
		for _, path := range []string{"/plan", "/deployments"} {
			response := call("POST", path, token, "cloud-blocked-request", input, 409)
			if !strings.Contains(response["error"].(map[string]any)["message"].(string), "managed cloud") {
				t.Fatalf("policy denial was not actionable: %v", response)
			}
		}
		response := call("POST", "/applications/"+initial.ApplicationID+"/rollback", token, "cloud-blocked-rollback", map[string]any{"revision": 1, "expected_revision": 2}, 409)
		if response["error"].(map[string]any)["code"] != "public_tcp_disabled" {
			t.Fatalf("identity %d rollback lost installation policy error: %v", i, response)
		}
		state := call("GET", "/applications/"+initial.ApplicationID+"/services/smtp/delivery", token, "", nil, 200)
		policy := state["public_tcp_policy"].(map[string]any)
		if policy["allowed"] != false || policy["mode"] != "managed-cloud" {
			t.Fatalf("delivery response lost installation policy: %v", policy)
		}
	}
	if _, err := db.Accept(ctx, principal, "demo", "development", public, 2, "cloud-durable-denial"); !errors.Is(err, cluster.ErrPublicTCPDisabled) {
		t.Fatalf("durable acceptance bypassed cloud restriction: %v", err)
	}
	var count int
	if err := db.Pool.QueryRow(ctx, "SELECT count(*) FROM deployments WHERE application_id=$1", initial.ApplicationID).Scan(&count); err != nil || count != 2 {
		t.Fatalf("denied requests persisted a deployment: count=%d err=%v", count, err)
	}
	input["spec"] = private
	call("POST", "/plan", root, "", input, 200)
	call("POST", "/deployments", root, "cloud-private-allowed", input, 202)
	if clusterRequests.Load() != 0 {
		t.Fatalf("cloud policy validation made %d Kubernetes calls", clusterRequests.Load())
	}
}
