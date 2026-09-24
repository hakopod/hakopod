package cluster

import (
	"context"
	"fmt"
	"github.com/hakopod/hakopod/internal/spec"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/clientcmd"
	"os"
	"testing"
	"time"
)

func TestLiveServiceVolumeCleanupPreservesOtherServices(t *testing.T) {
	if os.Getenv("HAKOPOD_PREVIEW_TEST") != "1" {
		t.Skip("opt-in volume cleanup acceptance")
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
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	sample, _ := spec.Showcase()
	name := fmt.Sprintf("volumes-live-%d", time.Now().Unix())
	service := spec.Service{Image: sample.Services["api"].Image, Port: 8080, Command: []string{"python", "-m", "http.server", "8080"}, Volume: &spec.Volume{MountPath: "/data", SizeGiB: 1}}
	app, err := spec.Normalize(spec.Application{Name: name, Services: map[string]spec.Service{"web": service, "db": service}})
	if err != nil {
		t.Fatal(err)
	}
	target := Target{ApplicationID: name, Project: "acceptance", Environment: "test", Spec: app, Revision: 1}
	defer func() {
		bounded, done := context.WithTimeout(context.Background(), 45*time.Second)
		defer done()
		_ = c.DeletePreview(bounded, target)
	}()
	if _, err = c.Deploy(ctx, target, nil); err != nil {
		t.Fatal(err)
	}
	if err = c.DeleteVolumes(ctx, target, []string{"db-data"}); err == nil {
		t.Fatal("deleted active service volume")
	}
	original := app
	next, err := spec.Normalize(app)
	if err != nil {
		t.Fatal(err)
	}
	delete(next.Services, "db")
	target.Spec = next
	target.Previous = &original
	target.Revision = 2
	if _, err = c.Deploy(ctx, target, nil); err != nil {
		t.Fatal(err)
	}
	pvc, err := c.kube.CoreV1().PersistentVolumeClaims(Namespace(name)).Get(ctx, "db-data", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	pv, err := c.kube.CoreV1().PersistentVolumes().Get(ctx, pvc.Spec.VolumeName, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	pv.Spec.PersistentVolumeReclaimPolicy = corev1.PersistentVolumeReclaimRetain
	if _, err = c.kube.CoreV1().PersistentVolumes().Update(ctx, pv, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	for {
		err = c.DeleteVolumes(ctx, target, []string{"db-data"})
		if err == nil {
			break
		}
		if ctx.Err() != nil {
			t.Fatal(err)
		}
		time.Sleep(time.Second)
	}
	if _, err = c.kube.CoreV1().PersistentVolumes().Get(ctx, pv.Name, metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatal("disk remains", err)
	}
	if _, err = c.kube.CoreV1().PersistentVolumeClaims(Namespace(name)).Get(ctx, "web-data", metav1.GetOptions{}); err != nil {
		t.Fatal("other service volume removed", err)
	}
	if _, err = c.kube.AppsV1().Deployments(Namespace(name)).Get(ctx, "web", metav1.GetOptions{}); err != nil {
		t.Fatal("other service removed", err)
	}
	if err = c.DeleteVolumes(ctx, target, []string{"db-data"}); err != nil {
		t.Fatal("cleanup replay failed", err)
	}
}
