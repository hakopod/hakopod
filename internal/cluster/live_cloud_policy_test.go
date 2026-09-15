package cluster

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/hakopod/hakopod/internal/spec"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/clientcmd"
)

func TestLiveCloudPolicy(t *testing.T) {
	if os.Getenv("HAKOPOD_CLOUD_POLICY_TEST") != "1" {
		t.Skip("set HAKOPOD_CLOUD_POLICY_TEST=1 for the named development cluster")
	}
	path := os.Getenv("HAKOPOD_TEST_KUBECONFIG")
	config, err := clientcmd.LoadFromFile(path)
	if err != nil || config.CurrentContext != "k3d-hakopod-dev" {
		t.Fatal("requires k3d-hakopod-dev")
	}
	c, err := New(path, Options{DeploymentMode: DeploymentManagedCloud})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	caps, err := c.CloudCapabilities(ctx)
	if err != nil || caps.NodeCount != 1 || !caps.NodeCountComplete {
		t.Fatal("single-node fixture required", caps, err)
	}
	app := spec.Application{Name: "cloud-policy-test", Services: map[string]spec.Service{"web": {Image: "docker.io/library/nginx@sha256:" + strings.Repeat("a", 64), Size: "compute", Replicas: 1}}}
	target := Target{ApplicationID: "cloud-policy-denied-acceptance", Project: "cloud-policy-test", Environment: "test", Revision: 1, Spec: app}
	if _, err := c.Deploy(ctx, target, nil); !errors.Is(err, ErrCloudLimit) {
		t.Fatal("runtime did not enforce limit", err)
	}
	if _, err := c.kube.CoreV1().Namespaces().Get(ctx, Namespace(target.ApplicationID), metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatal("rejected deployment created resources", err)
	}
	service := app.Services["web"]
	service.Size = "small"
	app.Services["web"] = service
	target.Spec = app
	if err := c.ValidateDelivery(ctx, target); err != nil {
		t.Fatal("valid configuration failed read-only preflight", err)
	}
	if _, err := c.CreateEnrollment(ctx, 30*60*1e9); !errors.Is(err, ErrCloudLimit) {
		t.Fatal("additional-node invitation accepted", err)
	}
}

func TestLiveOperatorTwoNodeCapacity(t *testing.T) {
	if os.Getenv("HAKOPOD_OPERATOR_TWO_NODE_TEST") != "1" {
		t.Skip("requires two nodes in the named development cluster")
	}
	path := os.Getenv("HAKOPOD_TEST_KUBECONFIG")
	config, err := clientcmd.LoadFromFile(path)
	if err != nil || config.CurrentContext != "k3d-hakopod-dev" {
		t.Fatal("requires k3d-hakopod-dev")
	}
	c, err := New(path, Options{DeploymentMode: DeploymentManagedCloud, OperatorNodeLimit: 2})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	caps, err := c.CloudCapabilities(ctx)
	if err != nil || caps.NodeCount != 2 || !caps.NodeCountComplete {
		t.Fatal("requires two real nodes", caps, err)
	}
	if err := c.ValidateCloudCapacity(ctx); err != nil {
		t.Fatal(err)
	}
	c.options.OperatorNodeLimit = 0
	if err := c.ValidateCloudCapacity(ctx); !errors.Is(err, ErrCloudLimit) {
		t.Fatalf("customer accepted second node: %v", err)
	}
}
