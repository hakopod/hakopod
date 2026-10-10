package cluster

import (
	"context"
	"errors"
	"github.com/hakopod/hakopod/internal/spec"
	"k8s.io/client-go/kubernetes/fake"
	"testing"
	"time"
)

func TestUnhealthyServiceStillExpiresItsOwnReadinessWindow(t *testing.T) {
	target := testTarget(t)
	target.Spec.Services["api"] = spec.Service{Image: target.Spec.Services["api"].Image}
	dep := deployment(target, "api", target.Spec.Services["api"], time.Minute)
	dep.Generation = 1
	client := &Client{kube: fake.NewClientset(dep), options: Options{RolloutTimeout: 40 * time.Millisecond}}
	parent, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	started := time.Now()
	err := client.waitReady(parent, target, "api", 1)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("unhealthy service did not timeout: %v", err)
	}
	if time.Since(started) > 500*time.Millisecond {
		t.Fatal("aggregate budget replaced individual readiness bound")
	}
	if parent.Err() != nil {
		t.Fatal("unhealthy timeout consumed aggregate parent")
	}
}

func TestNilRuntimeHasNoInstallationReadinessBudget(t *testing.T) {
	var client *Client
	if client.ServiceRolloutTimeout() != 0 {
		t.Fatal("nil runtime invented a readiness allowance")
	}
}
