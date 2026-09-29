package cluster

import (
	"context"
	"errors"
	"testing"

	"github.com/hakopod/hakopod/internal/spec"
	nodev1 "k8s.io/api/node/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestPlacementPolicySeparatesDiscoveryFromWorkloadAdmission(t *testing.T) {
	ctx := context.Background()
	ordinary, runner := placementNode("ordinary"), placementNode("runner")
	runner.Labels["hakopod.io/actions-runtime"] = "ready"
	runtimeClass := &nodev1.RuntimeClass{ObjectMeta: metav1.ObjectMeta{Name: ActionsRuntime, Labels: map[string]string{managedBy: "hakopod"}}, Handler: ActionsRuntime,
		Scheduling: &nodev1.Scheduling{NodeSelector: map[string]string{"hakopod.io/actions-runtime": "ready"}}}
	kube := fake.NewClientset(ordinary, runner, runtimeClass)
	target := runnerTarget(t)
	wanted := PlacementRequest{Project: target.Project, Environment: target.Environment, Application: target.Spec.Name}
	denied := errors.New("workload is not admitted")
	discoveries, admissions := 0, 0
	c := &Client{kube: kube, options: Options{
		DeploymentMode: DeploymentManagedCloud,
		PlacementPolicy: func(_ context.Context, request PlacementRequest) (WorkloadPolicy, error) {
			discoveries++
			if request.Project != wanted.Project || request.Environment != wanted.Environment || request.Application != wanted.Application {
				return WorkloadPolicy{}, denied
			}
			node := "ordinary"
			if request.Runtime == "actions" {
				node = "runner"
			}
			return WorkloadPolicy{NodeName: node}, nil
		},
		WorkloadPolicy: func(_ context.Context, project, environment string, app spec.Application) (WorkloadPolicy, error) {
			admissions++
			if project != target.Project || environment != target.Environment || app.Name != target.Spec.Name || app.Services["runner"].Actions == nil {
				t.Error("workload admission lost the actual pool")
			}
			return WorkloadPolicy{}, denied
		},
	}}
	for runtime, wantNode := range map[string]string{"": "ordinary", "actions": "runner"} {
		nodes, err := c.PlacementNodesForRuntime(ctx, target, runtime)
		if err != nil || len(nodes) != 1 || nodes[0].Name != wantNode || !nodes[0].Available {
			t.Fatal("discovery did not preserve its runtime allocation", runtime, nodes, err)
		}
	}
	if err := c.ActionsScopeAvailable(ctx, target); err != nil || discoveries != 3 || admissions != 0 {
		t.Fatal("scope readiness used workload admission", discoveries, admissions, err)
	}
	for name, check := range map[string]func() error{
		"registration": func() error { return c.ActionsPoolAvailable(ctx, target, target.Spec.Services["runner"]) },
		"admission":    func() error { return c.validateDeliveryPolicy(ctx, target) },
		"pod": func() error {
			return c.StartActionsPod(ctx, target, "runner", "denied", target.Spec.Services["runner"])
		},
	} {
		if err := check(); !errors.Is(err, denied) {
			t.Fatal("discovery bypassed actual workload policy", name, err)
		}
	}
	if discoveries != 3 || admissions != 3 {
		t.Fatal("workload mutations used the discovery resolver", discoveries, admissions)
	}
	for _, action := range kube.Actions() {
		if action.GetVerb() != "get" && action.GetVerb() != "list" {
			t.Fatal("rejected workload mutated Kubernetes", action.GetVerb(), action.GetResource())
		}
	}
	foreign := target
	foreign.Environment = "other"
	if _, err := c.PlacementNodesForRuntime(ctx, foreign, "actions"); !errors.Is(err, denied) {
		t.Fatal("discovery ignored denied environment", err)
	}
	if err := c.ActionsScopeAvailable(ctx, foreign); !errors.Is(err, denied) {
		t.Fatal("scope readiness ignored denied environment", err)
	}
	before := discoveries
	if _, err := c.PlacementNodesForRuntime(ctx, target, "privileged"); err == nil || discoveries != before {
		t.Fatal("unknown runtime reached the allocation resolver", err)
	}
}

func TestPlacementPolicyValidatesAllocationAndFallsBack(t *testing.T) {
	ctx := context.Background()
	c := &Client{options: Options{PlacementPolicy: func(context.Context, PlacementRequest) (WorkloadPolicy, error) {
		return WorkloadPolicy{}, nil
	}}}
	if _, err := c.placementPolicy(ctx, Target{}, "actions"); err == nil {
		t.Fatal("empty trusted allocation was accepted")
	}
	c.options.PlacementPolicy = nil
	if policy, err := c.placementPolicy(ctx, Target{}, "actions"); policy != nil || err != nil {
		t.Fatal("self-hosted discovery gained an invented allocation", policy, err)
	}
	c.options.WorkloadPolicy = func(context.Context, string, string, spec.Application) (WorkloadPolicy, error) {
		return WorkloadPolicy{NodeName: "legacy-allocation"}, nil
	}
	if policy, err := c.placementPolicy(ctx, Target{}, "actions"); err != nil || policy == nil || policy.NodeName != "legacy-allocation" {
		t.Fatal("existing workload allocation fallback changed", policy, err)
	}
}
