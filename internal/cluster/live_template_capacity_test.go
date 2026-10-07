package cluster

import (
	"context"
	"testing"

	"github.com/hakopod/hakopod/internal/spec"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func templateFixtureCapacity(t *testing.T, ctx context.Context, c *Client, node *corev1.Node, app spec.Application) {
	t.Helper()
	pods, err := c.kube.CoreV1().Pods("").List(ctx, metav1.ListOptions{FieldSelector: "spec.nodeName=" + node.Name, Limit: 1000})
	if err != nil || pods.Continue != "" {
		t.Fatal("cannot bound node capacity inspection", err)
	}
	var used int64
	for _, p := range pods.Items {
		if p.Status.Phase == corev1.PodSucceeded || p.Status.Phase == corev1.PodFailed {
			continue
		}
		var request int64
		for _, container := range p.Spec.Containers {
			request += container.Resources.Requests.Memory().Value()
		}
		for _, container := range p.Spec.InitContainers {
			if v := container.Resources.Requests.Memory().Value(); v > request {
				request = v
			}
		}
		used += request + p.Spec.Overhead.Memory().Value()
	}
	var needed int64
	for _, service := range app.Services {
		q := resource.MustParse(spec.EffectiveResources(service).MemoryRequest)
		needed += q.Value()
	}
	free := node.Status.Allocatable.Memory().Value() - used
	t.Logf("named development node %s architecture=%s schedulable memory=%dMi template requests=%dMi", node.Name, node.Status.NodeInfo.Architecture, free>>20, needed>>20)
	if node.Spec.Unschedulable || free < needed+(64<<20) {
		t.Fatalf("insufficient safe development capacity: need template requests plus 64Mi headroom; available %dMi, template %dMi", free>>20, needed>>20)
	}
}
