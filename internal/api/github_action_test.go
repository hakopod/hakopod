package api_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/api"
	"github.com/hakopod/hakopod/internal/spec"
	"github.com/hakopod/hakopod/internal/store"
)

type actionFixture struct {
	db       *store.Store
	owner    store.Principal
	app      store.Application
	resolved spec.Application
	token    string
}

// These tests execute the shipped JavaScript action against the real Go handlers
// and an isolated PostgreSQL database. Resolved images are explicit development
// fixtures; wait=false proves durable acceptance, not Kubernetes rollout success.
func newActionFixture(t *testing.T) actionFixture {
	t.Helper()
	db, _ := database(t)
	ctx := context.Background()
	raw, err := db.Bootstrap(ctx, "github-action-development-fixture")
	if err != nil {
		t.Fatal(err)
	}
	owner, err := db.Authenticate(ctx, raw)
	if err != nil {
		t.Fatal(err)
	}
	initial, err := spec.Parse([]byte(`schema_version=1
name='action-fixture'
inject_env=true
[env]
REGION='application-default'
[services.web]
image='python:3.13-alpine'
[services.web.env]
KEEP='web-value'
REMOVE='old-value'
EMPTY='old-value'
[services.worker]
image='python:3.13-alpine'
[services.worker.env]
KEEP='worker-value'
[services.untouched]
image='python:3.13-alpine'
[services.untouched.env]
KEEP='untouched-value'
`))
	if err != nil {
		t.Fatal(err)
	}
	fixture := actionFixture{db: db, owner: owner}
	accepted, err := fixture.acceptResolved(ctx, initial, 0, "initial-action-fixture")
	if err != nil {
		t.Fatal(err)
	}
	fixture.app, err = db.Application(ctx, accepted.ApplicationID)
	if err != nil {
		t.Fatal(err)
	}
	fixture.resolved, err = spec.Normalize(initial)
	if err != nil {
		t.Fatal(err)
	}
	for name, service := range fixture.resolved.Services {
		service.Image = "docker.io/library/python@sha256:" + strings.Repeat("a", 64)
		fixture.resolved.Services[name] = service
	}
	_, fixture.token, err = db.CreateKey(ctx, owner, store.KeyInput{
		Name: "github-action-development-fixture", Project: fixture.app.Project,
		Environment: fixture.app.Environment, Application: fixture.app.Name,
		Permissions: []string{"deployments:read", "deployments:write"},
		ExpiresAt:   time.Now().Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	return fixture
}

func (f actionFixture) acceptResolved(ctx context.Context, next spec.Application, revision int64, key string) (store.Deployment, error) {
	accepted, err := f.db.Accept(ctx, f.owner, "demo", "development", next, revision, key)
	if err != nil {
		return accepted, err
	}
	claim, err := f.db.Claim(ctx)
	if err != nil || claim == nil {
		return accepted, fmt.Errorf("claim explicit development fixture: %v", err)
	}
	defer claim.Release()
	resolved, err := spec.Normalize(next)
	if err != nil {
		return accepted, err
	}
	for name, service := range resolved.Services {
		service.Image = "docker.io/library/python@sha256:" + strings.Repeat("a", 64)
		resolved.Services[name] = service
	}
	if _, err = claim.SetResolved(ctx, resolved); err != nil {
		return accepted, err
	}
	return accepted, claim.Finish(ctx, "succeeded", "", map[string]any{"status": "healthy"})
}

type actionResult struct {
	ID            string            `json:"id"`
	ApplicationID string            `json:"application_id"`
	Revision      int64             `json:"revision"`
	Status        string            `json:"status"`
	Error         string            `json:"error"`
	Outputs       map[string]string `json:"outputs"`
}

func runActionFixture(t *testing.T, serverURL, token, applicationID string, services any, sharedEnv any) (actionResult, error) {
	t.Helper()
	node := os.Getenv("HAKOPOD_ACTION_NODE")
	if node == "" {
		node = "node"
	}
	if _, err := exec.LookPath(node); err != nil {
		t.Skip("requires Node 24 or HAKOPOD_ACTION_NODE for GitHub action acceptance")
	}
	script, err := filepath.Abs("../../actions/deploy/testdata/api.mjs")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, node, script)
	for _, value := range os.Environ() {
		if !strings.HasPrefix(value, "INPUT_") {
			command.Env = append(command.Env, value)
		}
	}
	command.Env = append(command.Env,
		"INPUT_API-URL="+serverURL, "INPUT_API-TOKEN="+token,
		"INPUT_APPLICATION-ID="+applicationID, "INPUT_SERVICES="+string(store.JSON(services)),
		"INPUT_ENV="+string(store.JSON(sharedEnv)), "INPUT_WAIT=false", "INPUT_TIMEOUT=20",
	)
	output, commandErr := command.CombinedOutput()
	if strings.Contains(string(output), token) {
		t.Fatal("action exposed its bearer token in process output")
	}
	var result actionResult
	if err = json.Unmarshal(output, &result); err != nil {
		t.Fatalf("action returned invalid fixture output: %v (%v)\n%s", err, commandErr, output)
	}
	return result, commandErr
}

func TestGitHubActionDurableSelectedDeployment(t *testing.T) {
	f := newActionFixture(t)
	server := httptest.NewServer((&api.Server{Store: f.db}).Handler())
	defer server.Close()
	webImage := "ghcr.io/hakopod/action-fixture@sha256:" + strings.Repeat("b", 64)
	workerImage := "ghcr.io/hakopod/action-fixture@sha256:" + strings.Repeat("c", 64)
	result, err := runActionFixture(t, server.URL, f.token, f.app.ID, []any{
		map[string]any{"name": "web", "image": webImage, "env": map[string]any{"MODE": "production", "EMPTY": "", "REMOVE": nil}},
		map[string]any{"name": "worker", "image": workerImage, "env": map[string]any{"REGION": "worker-region"}},
	}, map[string]string{"REGION": "ci-region"})
	if err != nil {
		t.Fatalf("action failed: %v: %s", err, result.Error)
	}
	if result.ApplicationID != f.app.ID || result.Revision != 2 || result.Status != "queued" || result.Outputs["deployment-id"] != result.ID || result.Outputs["status"] != "queued" {
		t.Fatalf("incorrect acceptance result: %+v", result)
	}
	accepted, err := f.db.Deployment(context.Background(), result.ID)
	if err != nil {
		t.Fatal(err)
	}
	if accepted.Spec.Services["web"].Image != webImage || accepted.Spec.Services["worker"].Image != workerImage {
		t.Fatal("selected images were not accepted together")
	}
	if !reflect.DeepEqual(accepted.Spec.Services["web"].Env, map[string]string{"KEEP": "web-value", "EMPTY": "", "MODE": "production", "REGION": "ci-region"}) ||
		!reflect.DeepEqual(accepted.Spec.Services["worker"].Env, map[string]string{"KEEP": "worker-value", "REGION": "worker-region"}) {
		t.Fatal("environment patch lost existing values, empty values, deletion, or service precedence")
	}
	if !reflect.DeepEqual(accepted.Spec.Services["untouched"], f.resolved.Services["untouched"]) || !reflect.DeepEqual(accepted.Spec.Env, f.app.Spec.Env) || !accepted.Spec.InjectEnv {
		t.Fatal("deployment changed an unselected service or shared application defaults")
	}
	if result.Outputs["idempotency-key"] == "" {
		t.Fatal("accepted deployment omitted its recovery key")
	}
}

func TestGitHubActionRejectsApplicationScope(t *testing.T) {
	f := newActionFixture(t)
	_, wrongToken, err := f.db.CreateKey(context.Background(), f.owner, store.KeyInput{
		Name: "wrong-application-development-fixture", Project: f.app.Project,
		Environment: f.app.Environment, Application: "other-application",
		Permissions: []string{"deployments:read", "deployments:write"}, ExpiresAt: time.Now().Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer((&api.Server{Store: f.db}).Handler())
	defer server.Close()
	result, err := runActionFixture(t, server.URL, wrongToken, f.app.ID, []string{"web"}, map[string]string{})
	if err == nil || !strings.Contains(result.Error, "HTTP 403") {
		t.Fatalf("out-of-scope action was not rejected: %v, %+v", err, result)
	}
	current, err := f.db.Application(context.Background(), f.app.ID)
	if err != nil || current.Revision != f.app.Revision {
		t.Fatal("out-of-scope action changed the application")
	}
}

func TestGitHubActionRejectsStaleRevision(t *testing.T) {
	for _, beforePlan := range []bool{true, false} {
		name := "between-plan-and-submit"
		if beforePlan {
			name = "between-read-and-plan"
		}
		t.Run(name, func(t *testing.T) {
			f := newActionFixture(t)
			handler := (&api.Server{Store: f.db}).Handler()
			var submissions atomic.Int32
			mutationErrors := make(chan error, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/v1/deployments" && r.Method == http.MethodPost {
					submissions.Add(1)
				}
				if r.URL.Path != "/api/v1/plan" {
					handler.ServeHTTP(w, r)
					return
				}
				var planned *httptest.ResponseRecorder
				if !beforePlan {
					planned = httptest.NewRecorder()
					handler.ServeHTTP(planned, r)
				}
				next, err := spec.Normalize(f.app.Spec)
				if err == nil {
					service := next.Services["web"]
					service.Env["CONCURRENT"] = "preserved"
					next.Services["web"] = service
					_, err = f.acceptResolved(r.Context(), next, f.app.Revision, "concurrent-development-fixture")
				}
				mutationErrors <- err
				if err != nil {
					http.Error(w, "development fixture failed", http.StatusInternalServerError)
					return
				}
				if beforePlan {
					handler.ServeHTTP(w, r)
					return
				}
				for key, values := range planned.Header() {
					w.Header()[key] = values
				}
				w.WriteHeader(planned.Code)
				_, _ = w.Write(planned.Body.Bytes())
			}))
			defer server.Close()
			result, err := runActionFixture(t, server.URL, f.token, f.app.ID, []any{map[string]any{"name": "web", "env": map[string]string{"MODE": "new"}}}, map[string]string{})
			select {
			case mutationError := <-mutationErrors:
				if mutationError != nil {
					t.Fatal(mutationError)
				}
			default:
				t.Fatalf("action did not reach the concurrent-plan fixture: %v: %s", err, result.Error)
			}
			if err == nil {
				t.Fatal("stale action was accepted")
			}
			if beforePlan && (submissions.Load() != 0 || !strings.Contains(result.Error, "revision changed while planning")) {
				t.Fatalf("action submitted a plan based on a stale application: %+v", result)
			}
			if !beforePlan && (submissions.Load() != 1 || !strings.Contains(result.Error, "HTTP 409")) {
				t.Fatalf("API did not reject the stale submitted revision: %+v", result)
			}
			current, err := f.db.Application(context.Background(), f.app.ID)
			if err != nil || current.Revision != 2 || current.Spec.Services["web"].Env["CONCURRENT"] != "preserved" {
				t.Fatal("stale action overwrote the concurrent revision")
			}
		})
	}
}

func TestGitHubActionRecoversLostAcceptanceResponse(t *testing.T) {
	f := newActionFixture(t)
	handler := (&api.Server{Store: f.db}).Handler()
	var submissions, recoveries atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/v1/idempotency/") {
			recoveries.Add(1)
		}
		if r.URL.Path != "/api/v1/deployments" || r.Method != http.MethodPost {
			handler.ServeHTTP(w, r)
			return
		}
		submissions.Add(1)
		accepted := httptest.NewRecorder()
		handler.ServeHTTP(accepted, r)
		if accepted.Code != http.StatusAccepted {
			w.WriteHeader(accepted.Code)
			_, _ = w.Write(accepted.Body.Bytes())
			return
		}
		// Drop only the response after the real handler commits its transaction.
		connection, _, err := w.(http.Hijacker).Hijack()
		if err == nil {
			_ = connection.Close()
		}
	}))
	defer server.Close()
	result, err := runActionFixture(t, server.URL, f.token, f.app.ID, []string{"web"}, map[string]string{"MODE": "production"})
	if err != nil {
		t.Fatalf("action did not recover durable acceptance: %v: %s", err, result.Error)
	}
	if submissions.Load() != 1 || recoveries.Load() != 1 || result.Status != "queued" || result.Revision != 2 {
		t.Fatalf("recovery resubmitted or returned the wrong operation: %+v", result)
	}
	history, err := f.db.History(context.Background(), f.app.ID)
	if err != nil || len(history) != 2 {
		t.Fatal("lost response recovery created an extra deployment")
	}
}
