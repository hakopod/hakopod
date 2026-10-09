package cluster

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/spec"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/clientcmd"
)

// This acceptance test owns one disposable namespace in the named development
// cluster. It uses the cached showcase image and does not publish an ingress.
func TestLiveServiceStartupBudgets(t *testing.T) {
	if os.Getenv("HAKOPOD_STARTUP_BUDGET_TEST") != "1" {
		t.Skip("set HAKOPOD_STARTUP_BUDGET_TEST=1 for the delayed-listener acceptance fixture")
	}
	path := os.Getenv("HAKOPOD_TEST_KUBECONFIG")
	configuration, err := clientcmd.LoadFromFile(path)
	if err != nil || configuration.CurrentContext != "k3d-hakopod-dev" {
		t.Fatal("requires named k3d-hakopod-dev context")
	}
	client, err := New(path, Options{RolloutTimeout: 6 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	showcase, err := spec.Showcase()
	if err != nil {
		t.Fatal(err)
	}
	image := showcase.Services["api"].Image
	listener := `import http.server; http.server.HTTPServer(('0.0.0.0',8080),http.server.SimpleHTTPRequestHandler).serve_forever()`
	delayed := `import time,http.server; time.sleep(10); http.server.HTTPServer(('0.0.0.0',8080),http.server.SimpleHTTPRequestHandler).serve_forever()`
	application, err := spec.Normalize(spec.Application{Name: fmt.Sprintf("startup-budget-%d", time.Now().UnixNano()), Services: map[string]spec.Service{
		"foundation": {Image: image, Port: 8080, StartupTimeoutSeconds: 25, Command: []string{"python", "-c"}, Args: []string{listener}},
		"delayed":    {Image: image, Port: 8080, DependsOn: []string{"foundation"}, StartupTimeoutSeconds: 25, Command: []string{"python", "-c"}, Args: []string{delayed}},
		"dependent":  {Image: image, Port: 8080, StartupTimeoutSeconds: 25, DependsOn: []string{"delayed"}, Command: []string{"python", "-c"}, Args: []string{listener}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	target := Target{ApplicationID: application.Name, OperationID: "startup-budget-1", Project: "acceptance", Environment: "test", Revision: 1, Spec: application}
	defer func() {
		clean, stop := context.WithTimeout(context.Background(), 30*time.Second)
		defer stop()
		namespace, e := client.kube.CoreV1().Namespaces().Get(clean, Namespace(target.ApplicationID), metav1.GetOptions{})
		if e == nil && owned(namespace, target) == nil {
			if e = client.kube.CoreV1().Namespaces().Delete(clean, namespace.Name, deleteOptions(namespace)); e != nil {
				t.Error(e)
			}
		}
	}()

	type recordedEvent struct {
		Event
		At time.Time
	}
	var eventLock sync.Mutex
	events := []recordedEvent{}
	emit := func(event Event) {
		eventLock.Lock()
		events = append(events, recordedEvent{Event: event, At: time.Now()})
		eventLock.Unlock()
	}
	started := time.Now()
	observed, err := client.Deploy(ctx, target, emit)
	if err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(started); elapsed <= 6*time.Second {
		t.Fatalf("explicit startup budget did not outlive the %s installation default: %s", 6*time.Second, elapsed)
	}
	if observed.Status != "healthy" {
		t.Fatalf("delayed-listener fixture status = %s", observed.Status)
	}
	readyAt, dependentAt := time.Time{}, time.Time{}
	for _, event := range events {
		if event.Service == "delayed" && event.Type == "ready" {
			readyAt = event.At
		}
		if event.Service == "dependent" && event.Type == "applying" {
			dependentAt = event.At
		}
	}
	if readyAt.IsZero() || dependentAt.IsZero() || dependentAt.Before(readyAt) {
		t.Fatalf("dependent service was not gated by delayed readiness: ready=%s dependent=%s", readyAt, dependentAt)
	}

	expiring := application
	expiring.Services = maps.Clone(application.Services)
	expiring.Services["expiring"] = spec.Service{Image: image, Port: 8080, DependsOn: []string{"delayed"}, StartupTimeoutSeconds: 10, Command: []string{"python", "-c"}, Args: []string{`import time,http.server; time.sleep(30); http.server.HTTPServer(('0.0.0.0',8080),http.server.SimpleHTTPRequestHandler).serve_forever()`}}
	expiring.Services["gated-after-expiry"] = spec.Service{Image: image, Port: 8080, DependsOn: []string{"expiring"}, Command: []string{"python", "-c"}, Args: []string{listener}}
	expiring, err = spec.Normalize(expiring)
	if err != nil {
		t.Fatal(err)
	}
	target.Spec, target.Revision, target.OperationID = expiring, 2, "startup-budget-2"
	started = time.Now()
	if _, err = client.Deploy(ctx, target, emit); err == nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("short startup budget did not expire with a deadline: %v", err)
	}
	if elapsed := time.Since(started); elapsed < 9*time.Second || elapsed > 20*time.Second {
		t.Fatalf("short startup budget expired outside its bound: %s", elapsed)
	}
	if _, err = client.kube.AppsV1().Deployments(Namespace(target.ApplicationID)).Get(ctx, "gated-after-expiry", metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatalf("dependent service progressed after startup expiry: %v", err)
	}

	cancelling := application
	cancelling.Services = maps.Clone(application.Services)
	cancelling.Services["cancelled-startup"] = spec.Service{Image: image, Port: 8080, DependsOn: []string{"delayed"}, StartupTimeoutSeconds: 120, Command: []string{"python", "-c"}, Args: []string{`import time,http.server; time.sleep(300); http.server.HTTPServer(('0.0.0.0',8080),http.server.SimpleHTTPRequestHandler).serve_forever()`}}
	cancelling.Services["gated-after-cancel"] = spec.Service{Image: image, Port: 8080, DependsOn: []string{"cancelled-startup"}, Command: []string{"python", "-c"}, Args: []string{listener}}
	cancelling, err = spec.Normalize(cancelling)
	if err != nil {
		t.Fatal(err)
	}
	target.Spec, target.Revision, target.OperationID = cancelling, 3, "startup-budget-3"
	deploymentContext, stopDeployment := context.WithCancel(ctx)
	result := make(chan error, 1)
	go func() {
		_, deployErr := client.Deploy(deploymentContext, target, emit)
		result <- deployErr
	}()
	creationDeadline := time.Now().Add(30 * time.Second)
	for {
		_, err = client.kube.AppsV1().Deployments(Namespace(target.ApplicationID)).Get(ctx, "cancelled-startup", metav1.GetOptions{})
		if err == nil {
			break
		}
		if time.Now().After(creationDeadline) {
			stopDeployment()
			t.Fatal("cancellation fixture deployment was not created")
		}
		time.Sleep(250 * time.Millisecond)
	}
	stopDeployment()
	select {
	case err = <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("deployment did not preserve cancellation: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("cancelled startup wait did not return")
	}
	if _, err = client.kube.AppsV1().Deployments(Namespace(target.ApplicationID)).Get(ctx, "gated-after-cancel", metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatalf("dependent service progressed after cancellation: %v", err)
	}
}
