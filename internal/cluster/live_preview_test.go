package cluster

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/spec"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/clientcmd"
)

func TestLivePreviewDeletesOnlyOwnedRuntime(t *testing.T) {
	if os.Getenv("HAKOPOD_PREVIEW_TEST") != "1" {
		t.Skip("opt-in preview cleanup acceptance")
	}
	path := os.Getenv("HAKOPOD_TEST_KUBECONFIG")
	cfg, err := clientcmd.LoadFromFile(path)
	if err != nil || cfg.CurrentContext != "k3d-hakopod-dev" {
		t.Fatal("requires named k3d-hakopod-dev context")
	}
	c, err := New(path, Options{RolloutTimeout: 90 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	sample, _ := spec.Showcase()
	name := fmt.Sprintf("preview-live-%d", time.Now().Unix())
	app, err := spec.Normalize(spec.Application{Name: name, Services: map[string]spec.Service{"web": {Image: sample.Services["api"].Image, Port: 8080, Command: []string{"python", "-m", "http.server", "8080"}, Volume: &spec.Volume{MountPath: "/data", SizeGiB: 1}}}})
	if err != nil {
		t.Fatal(err)
	}
	target := Target{ApplicationID: name, Project: "acceptance", Environment: "test", Spec: app, Revision: 1}
	defer func() {
		bounded, done := context.WithTimeout(context.Background(), 30*time.Second)
		defer done()
		_ = c.DeletePreview(bounded, target)
	}()
	if err = c.PutWorkloadSecret(ctx, target.Project, target.Environment, app.Name, "fixture", "test-preview-value"); err != nil {
		t.Fatal(err)
	}
	if _, err = c.Deploy(ctx, target, nil); err != nil {
		t.Fatal(err)
	}
	ns, err := c.kube.CoreV1().Namespaces().Get(ctx, Namespace(name), metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	ns.Labels[ownerKey] = "different-owner"
	if _, err = c.kube.CoreV1().Namespaces().Update(ctx, ns, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	denied := c.DeletePreview(ctx, target)
	ns, err = c.kube.CoreV1().Namespaces().Get(ctx, Namespace(name), metav1.GetOptions{})
	if err != nil {
		t.Fatal("foreign namespace was touched", err)
	}
	ns.Labels = labelsFor(target, "")
	if _, err = c.kube.CoreV1().Namespaces().Update(ctx, ns, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if denied == nil {
		t.Fatal("foreign namespace cleanup was accepted")
	}
	pvc, err := c.kube.CoreV1().PersistentVolumeClaims(Namespace(name)).Get(ctx, "web-data", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	volume := pvc.Spec.VolumeName
	pv, err := c.kube.CoreV1().PersistentVolumes().Get(ctx, volume, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	pv.Spec.PersistentVolumeReclaimPolicy = corev1.PersistentVolumeReclaimRetain
	if _, err = c.kube.CoreV1().PersistentVolumes().Update(ctx, pv, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if err = c.DeletePreview(ctx, target); err != nil {
		t.Fatal(err)
	}
	if _, err = c.kube.CoreV1().Namespaces().Get(ctx, Namespace(name), metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatal("preview namespace remains", err)
	}
	items, err := c.ListWorkloadSecrets(ctx, target.Project, target.Environment, app.Name)
	if err != nil || len(items) != 0 {
		t.Fatal("native preview secrets remain", err)
	}
	if volume != "" {
		for {
			_, err = c.kube.CoreV1().PersistentVolumes().Get(ctx, volume, metav1.GetOptions{})
			if apierrors.IsNotFound(err) {
				break
			}
			if err != nil {
				t.Fatal(err)
			}
			if err = sleepContext(ctx, time.Second); err != nil {
				t.Fatal("preview disk was not reclaimed", err)
			}
		}
	}
	if err = c.DeletePreview(ctx, target); err != nil {
		t.Fatal("cleanup not idempotent", err)
	}
}
