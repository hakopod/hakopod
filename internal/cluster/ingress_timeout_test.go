package cluster

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestIngressTimeoutSetAndCleared(t *testing.T) {
	target := testTarget(t)
	svc := target.Spec.Services["web"]
	svc.IngressTimeoutSeconds = 600
	target.Spec.Services["web"] = svc
	ctx := context.Background()
	c := &Client{kube: fake.NewClientset(), options: Options{AppDomain: "apps.example.test"}}
	get := func() map[string]string {
		ing, err := c.kube.NetworkingV1().Ingresses(Namespace(target.ApplicationID)).Get(ctx, "web", metav1.GetOptions{})
		if err != nil {
			t.Fatal(err)
		}
		return ing.Annotations
	}
	if err := c.applyIngress(ctx, target, "web", svc); err != nil {
		t.Fatal(err)
	}
	if got := get()["haproxy.org/timeout-server"]; got != "600s" {
		t.Fatalf("ingress timeout annotation %q", got)
	}
	svc.IngressTimeoutSeconds = 0
	target.Spec.Services["web"] = svc
	if err := c.applyIngress(ctx, target, "web", svc); err != nil {
		t.Fatal(err)
	}
	if got, ok := get()["haproxy.org/timeout-server"]; ok {
		t.Fatalf("ingress timeout stayed %q after it was removed", got)
	}
}
