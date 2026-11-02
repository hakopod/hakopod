package api

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/actions"
	"github.com/hakopod/hakopod/internal/cluster"
	"github.com/hakopod/hakopod/internal/spec"
	"github.com/hakopod/hakopod/internal/store"
	corev1 "k8s.io/api/core/v1"
	nodev1 "k8s.io/api/node/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

// liveJITProvider hands out one pre-issued single-job registration. CI never
// holds the repository administration token, so runner state is derived from
// the runner pod and the run's real job records instead of the runners API.
type liveJITProvider struct {
	mu       sync.Mutex
	pending  []actions.Registration
	slots    map[int64]string
	names    map[int64]string
	phase    func(context.Context, string) (string, error)
	busy     func(context.Context, string) (bool, error)
	register []string
}

func (p *liveJITProvider) Register(_ context.Context, _ actions.Target, name string, _ []string) (actions.Registration, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.pending) == 0 {
		return actions.Registration{}, errors.New("no pre-issued single-job registration remains")
	}
	r := p.pending[0]
	p.pending = p.pending[1:]
	// The slot is named hakopod-<slot ID>; the JIT carries the name it was
	// minted with. Map the provider ID back to the slot for pod lookups.
	p.slots[r.Runner.ID] = strings.TrimPrefix(name, "hakopod-")
	p.names[r.Runner.ID] = r.Runner.Name
	p.register = append(p.register, name)
	return r, nil
}

func (p *liveJITProvider) Get(ctx context.Context, _ actions.Target, id int64) (actions.Runner, error) {
	p.mu.Lock()
	slot, ok := p.slots[id]
	p.mu.Unlock()
	if !ok {
		return actions.Runner{}, actions.ErrRunnerAbsent
	}
	phase, err := p.phase(ctx, slot)
	if err != nil {
		return actions.Runner{}, err
	}
	r := actions.Runner{ID: id, Status: "offline", ObservedAt: time.Now()}
	switch phase {
	case "Running":
		r.Status = "online"
	case "Pending":
	default:
		// An ephemeral runner removes its own registration when its job ends.
		return actions.Runner{}, actions.ErrRunnerAbsent
	}
	p.mu.Lock()
	r.Name = p.names[id]
	p.mu.Unlock()
	if r.Busy, err = p.busy(ctx, r.Name); err != nil {
		return actions.Runner{}, err
	}
	return r, nil
}

func (p *liveJITProvider) Find(context.Context, actions.Target, string) (*actions.Runner, error) {
	return nil, nil
}

// Deleting a registration needs the administration token. An unused JIT
// registration is left for GitHub to expire; the test fails if one is unused.
func (p *liveJITProvider) Delete(context.Context, actions.Target, int64) error { return nil }

type liveGitHubJob struct {
	ID         int64    `json:"id"`
	Status     string   `json:"status"`
	Conclusion string   `json:"conclusion"`
	Labels     []string `json:"labels"`
	RunnerName string   `json:"runner_name"`
}

// TestActionsScaleToZeroGitHubLive runs the scale-to-zero path against a real
// queued GitHub job: verified webhook, PostgreSQL demand, the scheduled
// reconciler and a sandboxed runner pod on the named development cluster.
func TestActionsScaleToZeroGitHubLive(t *testing.T) {
	if os.Getenv("HAKOPOD_ACTIONS_SCALE_TEST") != "1" {
		t.Skip("requires a pre-issued single-job GitHub registration and the Actions sandbox")
	}
	if os.Getenv("GITHUB_ACTIONS") != "true" {
		t.Fatal("live runner fixture is restricted to disposable CI")
	}
	path := os.Getenv("HAKOPOD_TEST_KUBECONFIG")
	configuration, err := clientcmd.LoadFromFile(path)
	if err != nil || configuration.CurrentContext != "k3d-hakopod-dev" {
		t.Fatal("only the named development cluster is permitted")
	}
	repository, runID, label := os.Getenv("GITHUB_REPOSITORY"), os.Getenv("GITHUB_RUN_ID"), os.Getenv("HAKOPOD_ACTIONS_SCALE_LABEL")
	if !actions.ValidRepository(repository) || runID == "" || !actions.ValidLabels([]string{label}) {
		t.Fatal("repository, run and runner label are required")
	}
	readToken := readPrivateFixture(t, "HAKOPOD_ACTIONS_SCALE_READ_TOKEN")
	// The second registration covers the warm runner the idle window keeps
	// after the job; its retirement exercises idle scale-down.
	var registrations []actions.Registration
	if json.Unmarshal([]byte(readPrivateFixture(t, "HAKOPOD_ACTIONS_SCALE_JIT")), &registrations) != nil || len(registrations) != 2 {
		t.Fatal("expected two single-job registrations")
	}
	for _, r := range registrations {
		if r.Runner.ID < 1 || r.Runner.Name == "" || r.EncodedConfig == "" {
			t.Fatal("incomplete single-job registration")
		}
	}
	registration := registrations[0]

	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Minute)
	defer cancel()
	config, err := clientcmd.BuildConfigFromFlags("", path)
	if err != nil {
		t.Fatal(err)
	}
	config.Timeout = 10 * time.Second
	kube, err := kubernetes.NewForConfig(config)
	if err != nil {
		t.Fatal(err)
	}
	runtimeClass := &nodev1.RuntimeClass{ObjectMeta: metav1.ObjectMeta{Name: cluster.ActionsRuntime, Labels: map[string]string{"app.kubernetes.io/managed-by": "hakopod"}}, Handler: cluster.ActionsRuntime, Scheduling: &nodev1.Scheduling{NodeSelector: map[string]string{"hakopod.io/actions-runtime": "ready"}}, Overhead: &nodev1.Overhead{PodFixed: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("100m"), corev1.ResourceMemory: resource.MustParse("512Mi")}}}
	if _, err = kube.NodeV1().RuntimeClasses().Create(ctx, runtimeClass, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		t.Fatal(err)
	}
	node, err := kube.CoreV1().Nodes().Get(ctx, "k3d-hakopod-dev-server-0", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	node.Labels["hakopod.io/actions-runtime"] = "ready"
	if _, err = kube.CoreV1().Nodes().Update(ctx, node, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	runtime, err := cluster.New(path, cluster.Options{RolloutTimeout: 90 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if err = runtime.ActionsAvailable(ctx); err != nil {
		t.Fatal(err)
	}

	db := actionsDatabase(t)
	raw, err := db.Bootstrap(ctx, "actions-scale-acceptance")
	if err != nil {
		t.Fatal(err)
	}
	admin, err := db.Authenticate(ctx, raw)
	if err != nil {
		t.Fatal(err)
	}
	// Pro licensing is outside this gate; the trusted embedding hook grants it.
	db.ManagedCloud = true
	db.ActionsAccess = func(context.Context, string, string) error { return nil }
	db.ValidateDeployment = func(context.Context, store.Application, spec.Application) error { return nil }

	github := func(path string, out any) error {
		request, e := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.github.com/repos/"+repository+path, nil)
		if e != nil {
			return e
		}
		request.Header.Set("Authorization", "Bearer "+readToken)
		request.Header.Set("Accept", "application/vnd.github+json")
		response, e := http.DefaultClient.Do(request)
		if e != nil {
			return e
		}
		defer response.Body.Close()
		if response.StatusCode != 200 {
			return fmt.Errorf("GitHub returned %d for %s", response.StatusCode, path)
		}
		return json.NewDecoder(response.Body).Decode(out)
	}
	companion := func() (*liveGitHubJob, error) {
		var page struct {
			Jobs []liveGitHubJob `json:"jobs"`
		}
		if e := github("/actions/runs/"+runID+"/jobs?per_page=100", &page); e != nil {
			return nil, e
		}
		for _, job := range page.Jobs {
			if slices.Contains(job.Labels, label) {
				return &job, nil
			}
		}
		return nil, nil
	}

	app, err := spec.Normalize(spec.Application{Name: "scale-runners", Services: map[string]spec.Service{"runner": {
		Replicas:  2,
		Resources: &spec.Resources{CPURequest: "500m", CPULimit: "1500m", MemoryRequest: "1Gi", MemoryLimit: "4Gi"},
		Actions:   &spec.Actions{Repository: repository, Credential: "runner-token", Labels: []string{label}, TimeoutMinutes: 20, IdleSeconds: 60},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	d, err := db.Accept(ctx, admin, "demo", "development", app, 0, "scale-to-zero-acceptance", app)
	if err != nil {
		t.Fatal(err)
	}
	namespace := cluster.Namespace(d.ApplicationID)
	t.Cleanup(func() {
		clean, done := context.WithTimeout(context.Background(), 60*time.Second)
		defer done()
		ns, e := kube.CoreV1().Namespaces().Get(clean, namespace, metav1.GetOptions{})
		if e != nil || ns.Labels["app.kubernetes.io/managed-by"] != "hakopod" {
			return
		}
		uid := ns.UID
		_ = kube.CoreV1().Namespaces().Delete(clean, namespace, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &uid}})
	})
	// The control-plane credential is never used: registration comes from the
	// pre-issued JIT and observation from the read-only run token.
	if err = runtime.PutWorkloadSecret(ctx, "demo", "development", app.Name, "runner-token", "unused-ci-fixture"); err != nil {
		t.Fatal(err)
	}

	target := cluster.Target{ApplicationID: d.ApplicationID, Project: "demo", Environment: "development", Revision: 1, Spec: app}
	provider := &liveJITProvider{pending: registrations, slots: map[int64]string{}, names: map[int64]string{}}
	provider.phase = func(ctx context.Context, slot string) (string, error) {
		return runtime.ActionsPodPhase(ctx, target, slot)
	}
	provider.busy = func(_ context.Context, name string) (bool, error) {
		job, e := companion()
		return e == nil && job != nil && job.Status == "in_progress" && job.RunnerName == name && name != "", e
	}
	s := &Server{Store: db, Cluster: runtime, Auth: AuthConfig{EncryptionKey: base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{9}, 32))}, actionsClient: func(string) (runnerProvider, error) { return provider, nil }}
	s.ConfigureActions()
	httpServer := httptest.NewServer(s.Handler())
	defer httpServer.Close()
	httpServer.Client().Timeout = 10 * time.Second
	// Handler derives ManagedCloud from the deployment mode; restore the grant.
	db.ManagedCloud = true

	secretBytes := make([]byte, 32)
	if _, err = rand.Read(secretBytes); err != nil {
		t.Fatal(err)
	}
	webhookSecret := hex.EncodeToString(secretBytes)
	request, err := http.NewRequestWithContext(ctx, http.MethodPut, httpServer.URL+"/api/v1/integrations/github", bytes.NewReader(store.JSON(map[string]string{"webhook_secret": webhookSecret})))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+raw)
	request.Header.Set("Content-Type", "application/json")
	response, err := httpServer.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 200 {
		t.Fatalf("configuring the GitHub webhook secret returned %d", response.StatusCode)
	}

	deployment, err := db.Claim(ctx)
	if err != nil || deployment == nil {
		t.Fatal("deployment claim", err)
	}
	observation, err := runtime.Deploy(ctx, cluster.Target{ApplicationID: d.ApplicationID, OperationID: d.ID, Project: "demo", Environment: "development", Revision: 1, Spec: app}, func(cluster.Event) {})
	if err != nil {
		deployment.Release()
		t.Fatal("scale-to-zero pool deployment failed", err)
	}
	if err = deployment.Finish(ctx, "succeeded", "", observation); err != nil {
		deployment.Release()
		t.Fatal(err)
	}
	deployment.Release()
	runners := func() int {
		t.Helper()
		pods, e := kube.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{Limit: 50})
		if e != nil {
			t.Fatal(e)
		}
		count := 0
		for _, pod := range pods.Items {
			if strings.HasPrefix(pod.Name, "actions-") {
				count++
			}
		}
		return count
	}
	status, err := s.observeActions(ctx, target, "runner", app.Services["runner"])
	if err != nil || status.Status != "sleeping" || runners() != 0 {
		t.Fatalf("deployed pool is not asleep: %+v, %d pods, %v", status, runners(), err)
	}
	t.Log("Deployed pool is sleeping with no runner pods")

	workerCtx, stop := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { defer close(done); s.RunActions(workerCtx) }()
	defer func() { stop(); <-done }()

	deliver := func(action string, job liveGitHubJob) {
		t.Helper()
		body := store.JSON(map[string]any{"action": action, "workflow_job": map[string]any{"id": job.ID, "labels": job.Labels}, "repository": map[string]any{"full_name": repository}})
		mac := hmac.New(sha256.New, []byte(webhookSecret))
		mac.Write(body)
		delivery := make([]byte, 16)
		_, _ = rand.Read(delivery)
		request, e := http.NewRequestWithContext(ctx, http.MethodPost, httpServer.URL+"/api/v1/webhooks/github", bytes.NewReader(body))
		if e != nil {
			t.Fatal(e)
		}
		request.Header.Set("X-GitHub-Event", "workflow_job")
		request.Header.Set("X-GitHub-Delivery", hex.EncodeToString(delivery))
		request.Header.Set("X-Hub-Signature-256", "sha256="+hex.EncodeToString(mac.Sum(nil)))
		response, e := httpServer.Client().Do(request)
		if e != nil {
			t.Fatal(e)
		}
		response.Body.Close()
		if response.StatusCode != 202 {
			t.Fatalf("%s delivery returned %d", action, response.StatusCode)
		}
		t.Logf("Delivered signed workflow_job %s for job %d", action, job.ID)
	}
	wait := func(limit time.Duration, what string, ready func() bool) {
		t.Helper()
		deadline := time.Now().Add(limit)
		for !ready() {
			if time.Now().After(deadline) {
				t.Fatalf("%s did not happen within %s", what, limit)
			}
			if !invocationPause(ctx, 5*time.Second) {
				t.Fatal("test deadline reached while waiting for", what)
			}
		}
	}

	var job *liveGitHubJob
	wait(5*time.Minute, "companion job queued", func() bool {
		job, err = companion()
		if err != nil {
			t.Fatal(err)
		}
		return job != nil && job.Status == "queued"
	})
	// Still asleep: nothing starts until GitHub reports demand.
	if runners() != 0 {
		t.Fatal("pool started a runner before any delivery")
	}
	queuedAt := time.Now()
	deliver("queued", *job)
	wait(3*time.Minute, "runner pod start", func() bool { return runners() > 0 })
	t.Logf("Runner pod created %s after the queued delivery", time.Since(queuedAt).Round(time.Second))

	delivered := map[string]bool{"queued": true}
	wait(15*time.Minute, "companion job completion", func() bool {
		current, e := companion()
		if e != nil || current == nil {
			t.Fatal("companion job lookup", e)
		}
		for _, state := range []string{"in_progress", "completed"} {
			if (current.Status == state || current.Status == "completed") && !delivered[state] {
				deliver(state, *current)
				delivered[state] = true
			}
		}
		job = current
		return current.Status == "completed"
	})
	if job.Conclusion != "success" || job.RunnerName != registration.Runner.Name {
		t.Fatalf("companion job concluded %q on runner %q, want success on %q", job.Conclusion, job.RunnerName, registration.Runner.Name)
	}
	completedAt := time.Now()
	// Allow the 60-second idle window plus scheduling and pod termination.
	wait(5*time.Minute, "return to zero runners", func() bool {
		slots, e := db.ActionsSlots(ctx, d.ApplicationID, "runner")
		if e != nil {
			t.Fatal(e)
		}
		return len(slots) == 0 && runners() == 0
	})
	t.Logf("Pool returned to zero runners %s after the job completed", time.Since(completedAt).Round(time.Second))
	status, err = s.observeActions(ctx, target, "runner", app.Services["runner"])
	if err != nil || status.Status != "sleeping" {
		t.Fatalf("idle pool is not reported sleeping: %+v, %v", status, err)
	}
	t.Logf("Registrations used: %d (the job runner plus any idle-window warm runner)", len(provider.register))
}

// Fixture values arrive as private files so they never appear in arguments.
func readPrivateFixture(t *testing.T, variable string) string {
	t.Helper()
	path := os.Getenv(variable)
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("%s must name a mode-0600 file", variable)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(data))
}
