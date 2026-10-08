package cluster

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/spec"
	nodev1 "k8s.io/api/node/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/uuid"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/tools/clientcmd"
)

func cleanupRuntimeProfileFixtureClass(ctx context.Context, c *Client, expected *nodev1.RuntimeClass) error {
	classes := c.kube.NodeV1().RuntimeClasses()
	current, err := classes.Get(ctx, expected.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) && expected.UID == "" {
		// A timed-out create can still commit. Bound the readback, but do not
		// turn an unresolved creation into a claim of confirmed cleanup.
		probe, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		err = wait.PollUntilContextCancel(probe, 100*time.Millisecond, false, func(ctx context.Context) (bool, error) {
			var readErr error
			current, readErr = classes.Get(ctx, expected.Name, metav1.GetOptions{})
			if apierrors.IsNotFound(readErr) {
				return false, nil
			}
			return readErr == nil, readErr
		})
		if err != nil {
			return fmt.Errorf("fixture creation is unresolved; RuntimeClass cleanup is unconfirmed: %w", err)
		}
	}
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	marker := expected.Labels["hakopod.io/runtime-fixture"]
	if marker == "" || current.Labels["hakopod.io/runtime-fixture"] != marker || current.Handler != expected.Handler || (expected.UID != "" && current.UID != expected.UID) {
		return fmt.Errorf("fixture RuntimeClass ownership changed")
	}
	if err := classes.Delete(ctx, current.Name, deleteOptions(current)); err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	return wait.PollUntilContextCancel(ctx, 250*time.Millisecond, true, func(ctx context.Context) (bool, error) {
		found, err := classes.Get(ctx, expected.Name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return true, nil
		}
		if err != nil {
			return false, err
		}
		if found.UID != current.UID {
			return false, fmt.Errorf("another RuntimeClass replaced the deleted fixture")
		}
		return false, nil
	})
}

// This gate verifies runtime selection through the real reconciler. It does not
// qualify a custom handler's kernel limits. The fixture uses the installed runc.
func TestLiveRuntimeProfileWorkloads(t *testing.T) {
	if os.Getenv("HAKOPOD_RUNTIME_PROFILE_TEST") != "1" {
		t.Skip("requires the explicitly selected named development cluster")
	}
	path := os.Getenv("HAKOPOD_TEST_KUBECONFIG")
	config, err := clientcmd.LoadFromFile(path)
	if err != nil || config.CurrentContext != "k3d-hakopod-dev" {
		t.Fatal("runtime profile acceptance requires k3d-hakopod-dev")
	}
	ctx, stop := context.WithTimeout(context.Background(), 6*time.Minute)
	defer stop()
	id := strings.ReplaceAll(string(uuid.NewUUID()), "-", "")
	className := "runtime-fixture-" + id[:12]
	image := "docker.io/library/busybox@sha256:9db7b59979c38555a39def84a31fb98b5296952f9e3afd4f6f11f05b07adfab0"
	app, err := spec.Normalize(spec.Application{SchemaVersion: 1, Name: "runtime-fixture", Services: map[string]spec.Service{
		"worker":   {Image: image, RuntimeProfile: "bounded-worker", Command: []string{"sh", "-c", "sleep 360"}, ReadOnlyRootFilesystem: true},
		"check":    {Image: image, RuntimeProfile: "bounded-check", Command: []string{"sh", "-c", "test -r /proc/self/status"}, Job: &spec.Job{TimeoutSeconds: 30}, ReadOnlyRootFilesystem: true},
		"schedule": {Image: image, RuntimeProfile: "bounded-schedule", Command: []string{"sh", "-c", "true"}, Suspended: true, Job: &spec.Job{TimeoutSeconds: 30, Schedule: &spec.JobSchedule{Cron: "0 0 1 1 *", Timezone: "UTC"}}, ReadOnlyRootFilesystem: true},
	}})
	if err != nil {
		t.Fatal(err)
	}
	target := Target{Project: "runtime-fixture", Environment: "test", ApplicationID: id, OperationID: id, Revision: 1, Spec: app}
	var bindings []RuntimeProfileBinding
	for name, svc := range app.Services {
		bindings = append(bindings, RuntimeProfileBinding{Name: svc.RuntimeProfile, Project: target.Project, Environment: target.Environment, Application: app.Name, Service: name, RuntimeClass: className, Handler: "runc"})
	}
	c, err := New(path, Options{RuntimeProfileBindings: bindings, RolloutTimeout: 90 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	class := &nodev1.RuntimeClass{ObjectMeta: metav1.ObjectMeta{Name: className, Labels: map[string]string{"hakopod.io/runtime-fixture": id}}, Handler: "runc"}
	// Register cleanup before creation. A lost response must not orphan the class.
	t.Cleanup(func() {
		clean, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		ns, err := c.kube.CoreV1().Namespaces().Get(clean, Namespace(id), metav1.GetOptions{})
		if err == nil {
			if err := owned(ns, target); err != nil {
				t.Error("fixture namespace ownership changed")
				return
			}
			if err := c.kube.CoreV1().Namespaces().Delete(clean, ns.Name, deleteOptions(ns)); err != nil {
				t.Error(err)
				return
			}
		} else if !apierrors.IsNotFound(err) {
			t.Error(err)
			return
		}
		if err := wait.PollUntilContextCancel(clean, 250*time.Millisecond, true, func(ctx context.Context) (bool, error) {
			_, err := c.kube.CoreV1().Namespaces().Get(ctx, Namespace(id), metav1.GetOptions{})
			if apierrors.IsNotFound(err) {
				return true, nil
			}
			return false, err
		}); err != nil {
			t.Error("fixture namespace cleanup did not finish", err)
			return
		}
		if err := cleanupRuntimeProfileFixtureClass(clean, c, class); err != nil {
			t.Error("fixture RuntimeClass cleanup did not finish", err)
		}
	})
	createdClass, err := c.kube.NodeV1().RuntimeClasses().Create(ctx, class, metav1.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	class = createdClass
	if result, err := c.Deploy(ctx, target, nil); err != nil || result.Status != "healthy" {
		t.Fatal("runtime-profile deployment did not become healthy", err, result.Status)
	}
	deployment, err := c.kube.AppsV1().Deployments(Namespace(id)).Get(ctx, "worker", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	job, err := c.kube.BatchV1().Jobs(Namespace(id)).Get(ctx, jobName("check"), metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	cron, err := c.kube.BatchV1().CronJobs(Namespace(id)).Get(ctx, scheduledJobName("schedule"), metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, selected := range []*string{deployment.Spec.Template.Spec.RuntimeClassName, job.Spec.Template.Spec.RuntimeClassName, cron.Spec.JobTemplate.Spec.Template.Spec.RuntimeClassName} {
		if selected == nil || *selected != className {
			t.Fatal("a workload lost the approved RuntimeClass")
		}
	}
	pods, err := c.kube.CoreV1().Pods(Namespace(id)).List(ctx, metav1.ListOptions{LabelSelector: serviceKey + "=worker"})
	if err != nil || len(pods.Items) != 1 {
		t.Fatal("expected one real worker pod", err)
	}
	pod := pods.Items[0]
	if pod.Spec.RuntimeClassName == nil || *pod.Spec.RuntimeClassName != className || pod.Spec.SecurityContext == nil || pod.Spec.SecurityContext.RunAsNonRoot == nil || !*pod.Spec.SecurityContext.RunAsNonRoot || pod.Spec.AutomountServiceAccountToken == nil || *pod.Spec.AutomountServiceAccountToken {
		t.Fatal("the real pod lost its selected runtime or security controls")
	}
	if err := c.ValidateRuntimeProfiles(ctx, "ungranted", target.Environment, target.Spec); err == nil {
		t.Fatal("a different project inherited the runtime grant")
	}
	c.options.RuntimeProfileBindings = nil
	if err := c.ValidateDelivery(ctx, target); err == nil {
		t.Fatal("revoked grants passed delivery acceptance")
	}
	c.options.RuntimeProfileBindings = bindings
	// A new revision uses the same approved runtime. Old revisions retain their alias.
	target.Revision = 2
	svc := target.Spec.Services["worker"]
	svc.RestartNonce = id
	target.Spec.Services["worker"] = svc
	if result, err := c.Deploy(ctx, target, nil); err != nil || result.Status != "healthy" {
		t.Fatal("a subsequent runtime-profile revision failed", err, result.Status)
	}
}
