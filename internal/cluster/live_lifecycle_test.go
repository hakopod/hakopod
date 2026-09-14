package cluster

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/spec"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/clientcmd"
)

// Cached small images only; this test owns one disposable namespace and secret.
func TestLiveDeploymentLifecycle(t *testing.T) {
	if os.Getenv("HAKOPOD_LIFECYCLE_TEST") != "1" {
		t.Skip("opt-in lifecycle acceptance")
	}
	path := os.Getenv("HAKOPOD_TEST_KUBECONFIG")
	cfg, err := clientcmd.LoadFromFile(path)
	if err != nil || cfg.CurrentContext != "k3d-hakopod-dev" {
		t.Fatal("requires named k3d-hakopod-dev context")
	}
	c, err := New(path, Options{AppDomain: "127.0.0.1.sslip.io", PublicPort: 18080, RolloutTimeout: 90 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 7*time.Minute)
	defer cancel()
	sample, _ := spec.Showcase()
	image := sample.Services["api"].Image
	content := "fixture-configuration"
	server := `import http.server,threading
class Handler(http.server.BaseHTTPRequestHandler):
 def do_GET(self):
  self.send_response(200);self.end_headers();self.wfile.write(str(self.server.server_port).encode())
 def log_message(self,*args): pass
threading.Thread(target=http.server.HTTPServer(('0.0.0.0',8081),Handler).serve_forever,daemon=True).start()
http.server.HTTPServer(('0.0.0.0',8080),Handler).serve_forever()`
	migrate := `import os,time,urllib.request,urllib.parse
assert open('/etc/app/config.yaml').read()=='fixture-configuration'
assert open('/app/password').read()=='fixture-password-not-production'
assert urllib.parse.urlparse(os.environ['DATABASE_URL']).password=='fixture-password-not-production'
# Endpoint and per-pod CNI rules can settle after Pod readiness. Bound retries
# while still requiring a real successful private request.
deadline=time.monotonic()+20
while True:
 try:
  assert urllib.request.urlopen('http://db:8080',timeout=3).status==200
  break
 except OSError:
  if time.monotonic()>=deadline: raise
  time.sleep(0.25)
print('Migration fixture completed')`
	app, err := spec.Normalize(spec.Application{Name: fmt.Sprintf("lifecycle-%d", time.Now().Unix()), Services: map[string]spec.Service{
		"db":      {Image: image, Port: 8080, Command: []string{"python", "-c"}, Args: []string{server}},
		"migrate": {Image: image, DependsOn: []string{"db"}, Command: []string{"python", "-c"}, Args: []string{migrate}, Job: &spec.Job{TimeoutSeconds: 60, Retries: 1}, Files: map[string]spec.File{"config": {MountPath: "/etc/app/config.yaml", Content: &content}, "password": {MountPath: "/app/password", Secret: &spec.SecretRef{Ref: "password"}}}, Bindings: map[string]spec.Binding{"DATABASE_URL": {Service: "db", Protocol: "postgres", Database: "app", Username: "app", Password: &spec.SecretRef{Ref: "password"}}}},
		"web":     {Image: image, Port: 8080, Public: true, Ports: []spec.Port{{Name: "admin", Port: 8081}}, HTTP: map[string]spec.HTTPEndpoint{"admin": {Port: 8081}}, DependsOn: []string{"migrate"}, Command: []string{"python", "-c"}, Args: []string{server}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	target := Target{ApplicationID: app.Name, OperationID: "lifecycle-1", Project: "acceptance", Environment: "test", Revision: 1, Spec: app}
	if err := c.PutWorkloadSecret(ctx, target.Project, target.Environment, app.Name, "password", "fixture-password-not-production"); err != nil {
		t.Fatal(err)
	}
	defer func() {
		clean, done := context.WithTimeout(context.Background(), 30*time.Second)
		defer done()
		ns, err := c.kube.CoreV1().Namespaces().Get(clean, Namespace(target.ApplicationID), metav1.GetOptions{})
		if err == nil && owned(ns, target) == nil {
			if err := c.kube.CoreV1().Namespaces().Delete(clean, ns.Name, deleteOptions(ns)); err != nil {
				t.Error(err)
			}
		}
		if err := c.DeleteWorkloadSecret(clean, target.Project, target.Environment, app.Name, "password"); err != nil {
			t.Error(err)
		}
	}()
	emit := func(e Event) { t.Log(e.Type, e.Service, e.Message) }
	observed, err := c.Deploy(ctx, target, emit)
	if err != nil {
		if logs, e := c.Logs(ctx, Namespace(target.ApplicationID), "migrate", 50, false); e == nil {
			body, _ := io.ReadAll(io.LimitReader(logs, 4096))
			logs.Close()
			t.Log(string(body))
		}
		t.Fatal(err)
	}
	if observed.Status != "healthy" {
		t.Fatal(observed.Status)
	}
	job, err := c.kube.BatchV1().Jobs(Namespace(target.ApplicationID)).Get(ctx, jobName("migrate"), metav1.GetOptions{})
	if err != nil || jobState(job) != "completed" {
		t.Fatal("job did not complete", err)
	}
	if _, err := c.Deploy(ctx, target, emit); err != nil {
		t.Fatal(err)
	}
	again, _ := c.kube.BatchV1().Jobs(Namespace(target.ApplicationID)).Get(ctx, job.Name, metav1.GetOptions{})
	if again.UID != job.UID {
		t.Fatal("same revision reran job")
	}
	for endpoint, port := range map[string]string{"": "8080", "admin": "8081"} {
		host := c.hostname(target, "web")
		if endpoint != "" {
			host = c.endpointHostname(target, "web", endpoint)
		}
		deadline := time.Now().Add(15 * time.Second)
		ok := false
		for time.Now().Before(deadline) {
			req, _ := http.NewRequestWithContext(ctx, "GET", "http://127.0.0.1:18080/", nil)
			req.Host = host
			response, e := (&http.Client{Timeout: 3 * time.Second}).Do(req)
			if e == nil {
				body, _ := io.ReadAll(io.LimitReader(response.Body, 1024))
				response.Body.Close()
				ok = response.StatusCode == 200 && strings.TrimSpace(string(body)) == port
			}
			if ok {
				break
			}
			time.Sleep(500 * time.Millisecond)
		}
		if !ok {
			t.Fatal("HTTP endpoint did not route to port", endpoint, port)
		}
	}
	oldWeb, _ := c.kube.AppsV1().Deployments(Namespace(target.ApplicationID)).Get(ctx, "web", metav1.GetOptions{})
	broken, _ := spec.Normalize(app)
	svc := broken.Services["migrate"]
	svc.Args = []string{"raise SystemExit(9)"}
	broken.Services["migrate"] = svc
	target.Spec = broken
	target.Revision = 2
	target.OperationID = "lifecycle-2"
	if _, err := c.Deploy(ctx, target, emit); err == nil {
		t.Fatal("failed migration accepted")
	}
	newWeb, _ := c.kube.AppsV1().Deployments(Namespace(target.ApplicationID)).Get(ctx, "web", metav1.GetOptions{})
	if newWeb.Generation != oldWeb.Generation {
		t.Fatal("failed job changed dependent deployment")
	}
	logs, err := c.Logs(ctx, Namespace(target.ApplicationID), "migrate", 100, false)
	if err != nil {
		t.Fatal("job logs unavailable", err)
	}
	logs.Close()
	recovery, _ := spec.Normalize(app)
	updated := "updated-configuration"
	web := recovery.Services["web"]
	web.Files = map[string]spec.File{"settings": {MountPath: "/etc/app/settings", Content: &updated}}
	web.Args = []string{"assert open('/etc/app/settings').read()=='updated-configuration'\n" + server}
	recovery.Services["web"] = web
	target.Spec = recovery
	target.Revision = 3
	target.OperationID = "lifecycle-3"
	if _, err := c.Deploy(ctx, target, emit); err != nil {
		if logs, e := c.Logs(ctx, Namespace(target.ApplicationID), "migrate", 100, false); e == nil {
			body, _ := io.ReadAll(io.LimitReader(logs, 8192))
			logs.Close()
			t.Log(string(body))
		}
		t.Fatal("recovery failed", err)
	}
	recovered, _ := c.kube.AppsV1().Deployments(Namespace(target.ApplicationID)).Get(ctx, "web", metav1.GetOptions{})
	if recovered.Generation <= oldWeb.Generation {
		t.Fatal("file update did not roll out")
	}
	target.Revision = 4
	target.OperationID = "lifecycle-4"
	if err := c.snapshotWorkloadSecrets(ctx, &target); err != nil {
		t.Fatal(err)
	}
	slow := target.Spec.Services["migrate"]
	slow.Args = []string{"import time; time.sleep(300)"}
	slow.TerminationGraceSeconds = 1
	short, stop := context.WithCancel(ctx)
	result := make(chan error, 1)
	go func() { result <- c.runJob(short, target, "migrate", slow) }()
	creationDeadline := time.Now().Add(30 * time.Second)
	created := false
	for time.Now().Before(creationDeadline) {
		job, e := c.kube.BatchV1().Jobs(Namespace(target.ApplicationID)).Get(ctx, jobName("migrate"), metav1.GetOptions{})
		if e == nil && job.Annotations[jobRevision] == "4" {
			created = true
			break
		}
		time.Sleep(250 * time.Millisecond)
	}
	stop()
	select {
	case err = <-result:
	case <-time.After(10 * time.Second):
		t.Fatal("job cancellation did not return")
	}
	if !created || err == nil {
		t.Fatal("could not cancel a created job", created, err)
	}
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		_, err = c.kube.BatchV1().Jobs(Namespace(target.ApplicationID)).Get(ctx, jobName("migrate"), metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if !apierrors.IsNotFound(err) {
		t.Fatal("cancelled job remained active", err)
	}
	t.Log("Verified recovery/file update and cancellation. Verified real job completion/reuse/failure gating, scoped file mounts, derived credentials, log access and two HTTP ingress ports")
}
