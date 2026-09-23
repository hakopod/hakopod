package cluster

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/spec"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/clientcmd"
)

func TestLiveBoundedHostedReplacement(t *testing.T) {
	if os.Getenv("HAKOPOD_SHARED_POOL_TEST") != "1" {
		t.Skip("requires sandbox in named development cluster")
	}
	path := os.Getenv("HAKOPOD_TEST_KUBECONFIG")
	config, err := clientcmd.LoadFromFile(path)
	if err != nil || config.CurrentContext != "k3d-hakopod-dev" {
		t.Fatal("requires named development cluster")
	}
	ceiling := spec.Profile{CPURequest: "2500m", CPULimit: "2500m", MemoryRequest: "16Gi", MemoryLimit: "16Gi"}
	policy := func(context.Context, string, string, spec.Application) (WorkloadPolicy, error) {
		return WorkloadPolicy{Recreate: true, NodeName: "k3d-shared-free-test-0", Pool: "free", RuntimeClass: "runsc", EgressPorts: []int32{80, 443}, Quota: map[string]string{"pods": "2", "requests.memory": "16Gi", "limits.memory": "16Gi"}}, nil
	}
	c, err := New(path, Options{DeploymentMode: DeploymentManagedCloud, OperatorNodeLimit: 2, WorkloadPolicy: policy, CloudResourceCeiling: &ceiling, AppDomain: "127.0.0.1.sslip.io", IngressClass: "haproxy", PublicPort: 18080, RolloutTimeout: 90 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	app, err := spec.Parse([]byte("name='licensed-test'\n[services.web]\nimage='python:3.13-alpine'\npublic=true\nport=8080\ncommand=['python','-m','http.server','8080']\n[services.web.resources]\nmemory_request='256Mi'\nmemory_limit='8Gi'\n"))
	if err != nil {
		t.Fatal(err)
	}
	app, err = c.Resolve(ctx, app)
	if err != nil {
		t.Fatal(err)
	}
	target := Target{ApplicationID: "licensed-pool-fixture", Project: "licensed-test", Environment: "production", Revision: 1, Spec: app}
	t.Cleanup(func() {
		clean, stop := context.WithTimeout(context.Background(), 30*time.Second)
		defer stop()
		ns, err := c.kube.CoreV1().Namespaces().Get(clean, Namespace(target.ApplicationID), metav1.GetOptions{})
		if err == nil && owned(ns, target) == nil {
			_ = c.kube.CoreV1().Namespaces().Delete(clean, ns.Name, deleteOptions(ns))
		}
	})
	for _, limit := range []string{"8Gi", "12Gi"} {
		svc := target.Spec.Services["web"]
		svc.Resources.MemoryLimit = limit
		target.Spec.Services["web"] = svc
		if result, err := c.Deploy(ctx, target, func(e Event) { t.Log(e.Type, e.Message) }); err != nil || result.Status != "healthy" {
			t.Fatal(result, err)
		}
		dep, err := c.kube.AppsV1().Deployments(Namespace(target.ApplicationID)).Get(ctx, "web", metav1.GetOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if dep.Spec.Strategy.Type != "Recreate" || dep.Spec.Template.Spec.Containers[0].Resources.Limits.Memory().Cmp(resource.MustParse(limit)) != 0 {
			t.Fatal("bounded resource replacement not enforced")
		}
		quotas, err := c.kube.CoreV1().ResourceQuotas(Namespace(target.ApplicationID)).List(ctx, metav1.ListOptions{})
		if err != nil || len(quotas.Items) != 1 {
			t.Fatal("namespace quota changed", err)
		}
		q := quotas.Items[0].Spec.Hard["limits.memory"]
		if q.Cmp(resource.MustParse("16Gi")) != 0 {
			t.Fatal("namespace memory quota changed", q)
		}
		target.Revision++
	}
}
