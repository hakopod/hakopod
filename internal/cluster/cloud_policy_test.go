package cluster

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/spec"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
)

func cloudClient(nodes int) *Client {
	objects := []runtime.Object{}
	for n := 0; n < nodes; n++ {
		objects = append(objects, &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprint("node-", n)}})
	}
	return &Client{kube: fake.NewClientset(objects...), options: Options{DeploymentMode: DeploymentManagedCloud}}
}
func TestCloudLimitsAndSingleNodeCapability(t *testing.T) {
	for _, count := range []int{0, 1, 2, 3} {
		c := cloudClient(count)
		value, err := c.CloudCapabilities(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if value.Enforced != true || value.NodeLimit != 1 || value.Version != 1 {
			t.Fatal("missing contract")
		}
		err = c.ValidateCloudCapacity(context.Background())
		if (err == nil) != (count == 1) {
			t.Fatalf("count %d: %v", count, err)
		}
	}
	c := cloudClient(1)
	app := spec.Application{Services: map[string]spec.Service{"web": {Size: "small", Replicas: 1}}}
	if err := c.ValidateCloudSpec(app); err != nil {
		t.Fatal(err)
	}
	for _, service := range []spec.Service{{Size: "compute"}, {Size: "gpu"}, {Size: "small", Replicas: 4}, {Size: "small", AWSIdentity: "host"}, {Size: "small", Autoscaling: &spec.Autoscaling{MaxReplicas: 4}}} {
		app.Services["web"] = service
		if err := c.ValidateCloudSpec(app); !errors.Is(err, ErrCloudLimit) {
			t.Fatalf("accepted %#v: %v", service, err)
		}
		c.options.DeploymentMode = DeploymentSelfHosted
		if err := c.ValidateCloudSpec(app); err != nil {
			t.Fatal("restricted self-hosted resources", err)
		}
		c.options.DeploymentMode = DeploymentManagedCloud
	}
	if _, err := c.CreateEnrollment(context.Background(), 30*time.Minute); !errors.Is(err, ErrCloudLimit) {
		t.Fatal("Cloud created extra node invitation", err)
	}
	if len(c.kube.(*fake.Clientset).Actions()) != 0 {
		t.Fatal("pure policy or rejected enrollment called cluster")
	}
}
func TestCloudReconciliationRejectsBeforeMutation(t *testing.T) {
	c := cloudClient(1)
	target := Target{Spec: spec.Application{Services: map[string]spec.Service{"web": {Size: "compute", Replicas: 1}}}}
	if err := c.ValidateDelivery(context.Background(), target); !errors.Is(err, ErrCloudLimit) {
		t.Fatal(err)
	}
	if len(c.kube.(*fake.Clientset).Actions()) != 0 {
		t.Fatal("invalid specification reached Kubernetes")
	}
}

func TestOperatorNodeCapacityIsBoundedAndDoesNotRelaxCustomerPolicy(t *testing.T) {
	for _, limit := range []int{-1, 0, 1, 2, 3} {
		for _, count := range []int{0, 1, 2, 3, 4} {
			t.Run(fmt.Sprintf("limit=%d/count=%d", limit, count), func(t *testing.T) {
				c := cloudClient(count)
				c.options.OperatorNodeLimit = limit
				allowed := (limit == 0 || limit == 1) && count == 1 || limit == 2 && count >= 1 && count <= 2
				err := c.ValidateCloudCapacity(context.Background())
				if (err == nil) != allowed {
					t.Fatalf("allowed=%v err=%v", allowed, err)
				}
				if allowed {
					caps, err := c.CloudCapabilities(context.Background())
					expected := limit
					if expected == 0 {
						expected = 1
					}
					if err != nil || caps.NodeLimit != expected || caps.NodeCount != count || !caps.NodeCountComplete {
						t.Fatalf("%+v %v", caps, err)
					}
				}
			})
		}
	}
	c := cloudClient(2)
	c.options.OperatorNodeLimit = 2
	c.kube.(*fake.Clientset).PrependReactor("list", "nodes", func(action ktesting.Action) (bool, runtime.Object, error) {
		return true, &corev1.NodeList{ListMeta: metav1.ListMeta{Continue: "more"}, Items: []corev1.Node{{}, {}}}, nil
	})
	if err := c.ValidateCloudCapacity(context.Background()); !errors.Is(err, ErrCloudLimit) {
		t.Fatalf("incomplete inventory accepted: %v", err)
	}
}
