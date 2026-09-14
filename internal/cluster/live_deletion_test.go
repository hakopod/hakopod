package cluster

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/spec"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/clientcmd"
)

func TestLiveFinalServiceRemovalPreservesStorage(t *testing.T) {
	if os.Getenv("HAKOPOD_DELETION_TEST") != "1" {
		t.Skip("requires named development cluster")
	}
	path := os.Getenv("HAKOPOD_TEST_KUBECONFIG")
	cfg, err := clientcmd.LoadFromFile(path)
	if err != nil || cfg.CurrentContext != "k3d-hakopod-dev" {
		t.Fatal("refusing test outside named development cluster")
	}
	c, err := New(path, Options{RolloutTimeout: 90 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	raw, err := os.ReadFile("../../examples/shop/hakopod.toml")
	if err != nil {
		t.Fatal(err)
	}
	app, err := spec.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	delete(app.Services, "web")
	app, err = c.Resolve(ctx, app)
	if err != nil {
		t.Fatal(err)
	}
	target := Target{ApplicationID: fmt.Sprintf("removal-fixture-%d", time.Now().UnixNano()), Project: "deletion-test", Environment: "test", OperationID: "initial", Revision: 1, Spec: app}
	t.Cleanup(func() {
		cleanup, done := context.WithTimeout(context.Background(), 20*time.Second)
		defer done()
		ns, e := c.kube.CoreV1().Namespaces().Get(cleanup, Namespace(target.ApplicationID), metav1.GetOptions{})
		if e == nil && owned(ns, target) == nil {
			if e = c.kube.CoreV1().Namespaces().Delete(cleanup, ns.Name, deleteOptions(ns)); e != nil {
				t.Error(e)
			}
		}
	})
	if _, err = c.Deploy(ctx, target, nil); err != nil {
		t.Fatal(err)
	}
	pvc := &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: "retained-data", Namespace: Namespace(target.ApplicationID)}, Spec: corev1.PersistentVolumeClaimSpec{AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce}, Resources: corev1.VolumeResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("1Mi")}}}}
	if _, err = c.kube.CoreV1().PersistentVolumeClaims(pvc.Namespace).Create(ctx, pvc, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	before := target.Spec
	target.Spec.Services = map[string]spec.Service{}
	target.Previous = &before
	target.Revision = 2
	target.OperationID = "remove-final-service"
	observed, err := c.Deploy(ctx, target, nil)
	if err != nil || observed.Status != "empty" {
		t.Fatal("empty release", observed.Status, err)
	}
	pods, err := c.kube.CoreV1().Pods(pvc.Namespace).List(ctx, metav1.ListOptions{})
	if err != nil || len(pods.Items) != 0 {
		t.Fatal("release succeeded before pods stopped", err)
	}
	services, err := c.kube.CoreV1().Services(pvc.Namespace).List(ctx, metav1.ListOptions{})
	if err != nil || len(services.Items) != 0 {
		t.Fatal("service route retained", err)
	}
	if _, err = c.kube.CoreV1().PersistentVolumeClaims(pvc.Namespace).Get(ctx, pvc.Name, metav1.GetOptions{}); err != nil {
		t.Fatal("persistent data claim removed", err)
	}
	if _, err = c.Deploy(ctx, target, nil); err != nil {
		t.Fatal("empty release replay", err)
	}
	t.Log("Final service removed, pods stopped, service route removed, PVC retained, empty release replay succeeded")
}
