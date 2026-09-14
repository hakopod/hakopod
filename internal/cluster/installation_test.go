package cluster

import (
	"context"
	"github.com/hakopod/hakopod/internal/spec"
	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
	"strings"
	"testing"
)

func TestStoragePreflightAndBoundVolume(t *testing.T) {
	c := &Client{kube: fake.NewClientset()}
	target := Target{ApplicationID: "storage-test", Spec: spec.Application{Services: map[string]spec.Service{"main": {Volume: &spec.Volume{SizeGiB: 1, MountPath: "/data"}}}}}
	if err := c.validateStorage(context.Background(), target); err == nil || !strings.Contains(err.Error(), "Infrastructure > Setup") {
		t.Fatal("missing storage accepted", err)
	}
	_, err := c.kube.StorageV1().StorageClasses().Create(context.Background(), &storagev1.StorageClass{ObjectMeta: metav1.ObjectMeta{Name: "local", Annotations: map[string]string{"storageclass.kubernetes.io/is-default-class": "true"}}, Provisioner: "test.local"}, metav1.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err = c.validateStorage(context.Background(), target); err != nil {
		t.Fatal(err)
	}
	_ = c.kube.StorageV1().StorageClasses().Delete(context.Background(), "local", metav1.DeleteOptions{})
	_, err = c.kube.CoreV1().PersistentVolumeClaims(Namespace(target.ApplicationID)).Create(context.Background(), &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: "main-data"}, Status: corev1.PersistentVolumeClaimStatus{Phase: corev1.ClaimBound}}, metav1.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err = c.validateStorage(context.Background(), target); err != nil {
		t.Fatal("existing bound storage blocked", err)
	}
}
