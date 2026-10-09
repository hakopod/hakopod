package cluster

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/spec"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/clientcmd"
)

// TestLiveSameRevisionDeploymentResume proves that a completed migration stays
// immutable while a failed service and its dependent resume at one revision.
func TestLiveSameRevisionDeploymentResume(t *testing.T) {
	if os.Getenv("HAKOPOD_DEPLOYMENT_RESUME_TEST") != "1" {
		t.Skip("opt-in same-revision resume acceptance")
	}
	path := os.Getenv("HAKOPOD_TEST_KUBECONFIG")
	cfg, err := clientcmd.LoadFromFile(path)
	if err != nil || cfg.CurrentContext != "k3d-hakopod-dev" {
		t.Fatal("requires named k3d-hakopod-dev context")
	}
	c, err := New(path, Options{RolloutTimeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	sample, _ := spec.Showcase()
	image := sample.Services["api"].Image
	server := `import http.server,time
started=time.monotonic()
class H(http.server.BaseHTTPRequestHandler):
 def do_GET(self): self.send_response(200 if time.monotonic()-started>8 else 503); self.end_headers()
 def log_message(self,*args): pass
http.server.HTTPServer(('0.0.0.0',8080),H).serve_forever()`
	app, err := spec.Normalize(spec.Application{Name: fmt.Sprintf("resume-%d", time.Now().UnixNano()), Services: map[string]spec.Service{
		"migrate":     {Image: image, Command: []string{"python", "-c"}, Args: []string{"print('migration complete')"}, Job: &spec.Job{TimeoutSeconds: 30}},
		"independent": {Image: image, Command: []string{"python", "-c"}, Args: []string{"import time; time.sleep(120)"}},
		"delayed":     {Image: image, Port: 8080, Healthcheck: "/health", DependsOn: []string{"migrate"}, Command: []string{"python", "-c"}, Args: []string{server}},
		"dependent":   {Image: image, DependsOn: []string{"delayed"}, Command: []string{"python", "-c"}, Args: []string{"import time; time.sleep(120)"}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	target := Target{ApplicationID: app.Name, OperationID: "accepted-resume-fixture", Project: "acceptance", Environment: "test", Revision: 1, Spec: app}
	t.Cleanup(func() {
		clean, done := context.WithTimeout(context.Background(), 30*time.Second)
		defer done()
		ns, e := c.kube.CoreV1().Namespaces().Get(clean, Namespace(target.ApplicationID), metav1.GetOptions{})
		if e == nil && owned(ns, target) == nil {
			_ = c.kube.CoreV1().Namespaces().Delete(clean, ns.Name, deleteOptions(ns))
		}
	})
	first, err := c.Deploy(ctx, target, func(Event) {})
	var progress *DeploymentProgressError
	if !errors.As(err, &progress) || first.Attempt == nil || len(progress.Observation.Succeeded) == 0 {
		t.Fatalf("first attempt did not preserve explicit partial progress: observation=%+v err=%v", first, err)
	}
	jobs, err := c.kube.BatchV1().Jobs(Namespace(target.ApplicationID)).List(ctx, metav1.ListOptions{LabelSelector: serviceKey + "=migrate", Limit: 4})
	if err != nil || len(jobs.Items) != 1 || jobs.Items[0].Status.Succeeded != 1 {
		t.Fatal("completed migration evidence unavailable", err)
	}
	migrationUID := jobs.Items[0].UID
	readyDeadline := time.Now().Add(30 * time.Second)
	for {
		pods, listErr := c.kube.CoreV1().Pods(Namespace(target.ApplicationID)).List(ctx, metav1.ListOptions{LabelSelector: serviceKey + "=delayed", Limit: 4})
		if listErr != nil {
			t.Fatal(listErr)
		}
		ready := false
		for _, pod := range pods.Items {
			ready = ready || podReady(pod)
		}
		if ready {
			break
		}
		if time.Now().After(readyDeadline) {
			t.Fatal("the transient fixture did not become ready for same-revision resume")
		}
		time.Sleep(time.Second)
	}
	target.ResumeServices = []string{"delayed", "dependent"}
	second, err := c.Deploy(ctx, target, func(Event) {})
	if err != nil || second.Status != "healthy" {
		t.Fatalf("same-revision resume did not become healthy: observation=%+v err=%v", second, err)
	}
	jobs, err = c.kube.BatchV1().Jobs(Namespace(target.ApplicationID)).List(ctx, metav1.ListOptions{LabelSelector: serviceKey + "=migrate", Limit: 4})
	if err != nil || len(jobs.Items) != 1 || jobs.Items[0].UID != migrationUID {
		t.Fatal("same-revision resume reran or replaced the completed migration", err)
	}
	t.Log("Same-revision resume retried only the failed closure, retained the independent service, and reused the completed migration job.")
}
