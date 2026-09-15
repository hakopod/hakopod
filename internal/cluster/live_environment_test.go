package cluster

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/logquery"
	"github.com/hakopod/hakopod/internal/spec"
	"github.com/pelletier/go-toml/v2"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/clientcmd"
)

// Runs disposable workloads only in the named development cluster. Assertions
// execute inside web, worker, one-shot and CronJob containers using real secrets.
func TestLiveApplicationEnvironmentAndPodLogs(t *testing.T) {
	if os.Getenv("HAKOPOD_ENVIRONMENT_TEST") != "1" {
		t.Skip("requires named development cluster")
	}
	path := os.Getenv("HAKOPOD_TEST_KUBECONFIG")
	cfg, err := clientcmd.LoadFromFile(path)
	if err != nil || cfg.CurrentContext != "k3d-hakopod-dev" {
		t.Fatal("refusing environment acceptance outside named development cluster")
	}
	c, err := New(path, Options{RolloutTimeout: 75 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	const image = "python:3.13.15-alpine@sha256:7415fbc3c9e4979cc717d92377ab2bc7b2b4a2af1ac03cc52b5f3f88efedaf3a"
	const checks = "import os; assert os.environ['DEFAULT']=='shared'; assert os.environ['SHARED_TOKEN']=='fixture-secret'; print('environment-fixture-ready',flush=True); "
	app, err := spec.Normalize(spec.Application{InjectEnv: true, Name: fmt.Sprintf("env-%d", time.Now().Unix()), Env: map[string]string{"DEFAULT": "shared", "OVERRIDE": "default", "REGION": "one"}, Secrets: map[string]spec.SecretRef{"SHARED_TOKEN": {Ref: "shared"}}, Services: map[string]spec.Service{
		"api":     {Image: image, Port: 8080, Command: []string{"python", "-B", "-u", "-c"}, Args: []string{checks + "assert os.environ['OVERRIDE']==''; import http.server; http.server.ThreadingHTTPServer(('0.0.0.0',8080),http.server.SimpleHTTPRequestHandler).serve_forever()"}, Env: map[string]string{"OVERRIDE": ""}},
		"worker":  {Image: image, Command: []string{"python", "-B", "-u", "-c"}, Args: []string{checks + "assert os.environ['OVERRIDE']=='default'; import time; time.sleep(600)"}},
		"migrate": {Image: image, Command: []string{"python", "-B", "-u", "-c"}, Args: []string{checks + "assert os.environ['OVERRIDE']=='default'"}, Job: &spec.Job{TimeoutSeconds: 60}},
		"report":  {Image: image, Command: []string{"python", "-B", "-u", "-c"}, Args: []string{checks + "assert os.environ['OVERRIDE']=='default'"}, Job: &spec.Job{TimeoutSeconds: 60, Schedule: &spec.JobSchedule{Cron: "* * * * *"}}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	// Import a file exactly as CLI/dashboard inputs do, then exercise the real
	// environment and secret projection in all four workload kinds.
	app.Env, app.Secrets = nil, nil
	encoded, err := toml.Marshal(app)
	if err != nil {
		t.Fatal(err)
	}
	imported, err := spec.ImportEnvironmentFiles(append([]byte("env_file = '.env'\n"), encoded...), map[string]string{".env": "DEFAULT=shared\nOVERRIDE=default\nREGION=one\nSHARED_TOKEN=fixture-secret"}, func(string, string) string { return "shared" })
	if err != nil || imported.Secrets["shared"] != "fixture-secret" {
		t.Fatal("environment file import failed", err)
	}
	app = imported.Spec
	target := Target{ApplicationID: app.Name, Project: "env-acceptance", Environment: "test", OperationID: "environment-initial", Revision: 1, Spec: app}
	ns := Namespace(target.ApplicationID)
	t.Cleanup(func() {
		clean, done := context.WithTimeout(context.Background(), 30*time.Second)
		defer done()
		n, e := c.kube.CoreV1().Namespaces().Get(clean, ns, metav1.GetOptions{})
		if e == nil && owned(n, target) == nil {
			if e = c.kube.CoreV1().Namespaces().Delete(clean, ns, deleteOptions(n)); e != nil {
				t.Error(e)
			}
		}
		if e = c.DeleteWorkloadSecret(clean, target.Project, target.Environment, app.Name, "shared"); e != nil {
			t.Error(e)
		}
	})
	if err = c.PutWorkloadSecret(ctx, target.Project, target.Environment, app.Name, "shared", "fixture-secret"); err != nil {
		t.Fatal(err)
	}
	if _, err = c.Deploy(ctx, target, func(e Event) { t.Log(e.Type, e.Service, e.Message) }); err != nil {
		t.Fatal(err)
	}
	match, err := logquery.Compile("message ILIKE '%environment-fixture-ready%'")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"api", "worker", "migrate", "report"} {
		options := LogQueryOptions{Service: name, Tail: 2000, Limit: 500, SinceSeconds: 3600}
		if err = options.Validate(); err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().Add(90 * time.Second)
		for {
			result, e := c.QueryLogs(ctx, target, options, match)
			if e == nil && result.Matched > 0 {
				t.Log("verified environment and pod logs for", name)
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("%s did not produce validated environment logs: %v", name, e)
			}
			if err = sleepContext(ctx, 2*time.Second); err != nil {
				t.Fatal(err)
			}
		}
	}
	// Changing an application default reaches existing workers and preserves an
	// explicitly empty service override.
	target.Previous = &app
	target.Spec.Env = map[string]string{"DEFAULT": "shared", "OVERRIDE": "default", "REGION": "two"}
	target.Revision = 2
	target.OperationID = "environment-update"
	if _, err = c.Deploy(ctx, target, nil); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"api", "worker"} {
		code := "import os; assert os.environ['REGION']=='two'; print('updated')"
		output, e := exec.CommandContext(ctx, "kubectl", "--kubeconfig", path, "--context", "k3d-hakopod-dev", "-n", ns, "exec", "deployment/"+name, "--", "python", "-B", "-c", code).CombinedOutput()
		if e != nil || strings.TrimSpace(string(output)) != "updated" {
			t.Fatal("inherited update not applied", name, e)
		}
	}
}
