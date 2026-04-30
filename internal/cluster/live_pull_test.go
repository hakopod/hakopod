package cluster

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/spec"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/clientcmd"
)

// Directly apply a well-formed, unavailable digest to model a registry artifact
// disappearing after resolution. The public API correctly rejects nonexistent
// artifacts earlier; this separate test exercises actual kubelet pull failures.
func TestLiveImagePullFailure(t *testing.T) {
	if os.Getenv("HAKOPOD_FAILURE_TEST") != "1" {
		t.Skip("set HAKOPOD_FAILURE_TEST=1 for isolated kubelet pull-failure diagnostics")
	}
	path := os.Getenv("HAKOPOD_TEST_KUBECONFIG")
	if path == "" {
		path = filepath.Join("..", "..", ".local", "kubeconfig")
	}
	config, err := clientcmd.LoadFromFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if config.CurrentContext != "k3d-hakopod-dev" {
		t.Fatalf("refusing context %q", config.CurrentContext)
	}
	c, err := New(path, Options{RolloutTimeout: 20 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	app, err := spec.Normalize(spec.Application{Name: "pull-check", Services: map[string]spec.Service{"api": {Image: "docker.io/library/python@sha256:" + strings.Repeat("0", 64), Port: 8080, Size: "small"}}})
	if err != nil {
		t.Fatal(err)
	}
	target := Target{ApplicationID: fmt.Sprintf("pull-failure-test-%d", time.Now().UnixNano()), Project: "runtime-test", Environment: "test", OperationID: "unavailable-artifact", Revision: 1, Spec: app}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		ns, err := c.kube.CoreV1().Namespaces().Get(ctx, Namespace(target.ApplicationID), metav1.GetOptions{})
		if err != nil {
			return
		}
		if err := owned(ns, target); err != nil {
			t.Error(err)
			return
		}
		if err := c.kube.CoreV1().Namespaces().Delete(ctx, ns.Name, deleteOptions(ns)); err != nil {
			t.Error(err)
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Second)
	defer cancel()
	observed, err := c.Deploy(ctx, target, nil)
	if err == nil || observed.Status == "healthy" {
		t.Fatal("unavailable digest was reported ready")
	}
	message := ""
	for attempt := 0; attempt < 12; attempt++ {
		message = c.podMessage(ctx, target, "api")
		if strings.Contains(message, "ErrImagePull") || strings.Contains(message, "ImagePullBackOff") {
			break
		}
		if err := sleepContext(ctx, time.Second); err != nil {
			t.Fatal(err)
		}
	}
	if !strings.Contains(message, "ErrImagePull") && !strings.Contains(message, "ImagePullBackOff") {
		t.Fatalf("missing actual kubelet image-pull diagnosis: %s", message)
	}
	t.Log("real kubelet image-pull failure diagnosed: " + message)
}
