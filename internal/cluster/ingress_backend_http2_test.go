package cluster

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestIngressBackendHTTP2SetAndCleared(t *testing.T) {
	target := testTarget(t)
	svc := target.Spec.Services["web"]
	svc.BackendHTTP2 = true
	target.Spec.Services["web"] = svc
	ctx := context.Background()
	c := &Client{kube: fake.NewClientset(), options: Options{AppDomain: "apps.example.test"}}
	if err := c.applyIngress(ctx, target, "web", svc); err != nil {
		t.Fatal(err)
	}
	ing, err := c.kube.NetworkingV1().Ingresses(Namespace(target.ApplicationID)).Get(ctx, "web", metav1.GetOptions{})
	if err != nil || ing.Annotations["haproxy.org/server-proto"] != "h2" {
		t.Fatal("backend HTTP/2 was not requested from the proxy", ing, err)
	}
	svc.BackendHTTP2 = false
	target.Spec.Services["web"] = svc
	if err = c.applyIngress(ctx, target, "web", svc); err != nil {
		t.Fatal(err)
	}
	ing, err = c.kube.NetworkingV1().Ingresses(Namespace(target.ApplicationID)).Get(ctx, "web", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if ing.Annotations["haproxy.org/server-proto"] != "" {
		t.Fatal("backend HTTP/2 stayed on after opting out")
	}
}

func TestIngressForeignAnnotationSurvivesBackendHTTP2Reconcile(t *testing.T) {
	ctx := context.Background()
	target := testTarget(t)
	svc := target.Spec.Services["web"]
	c := &Client{kube: fake.NewClientset(), options: Options{AppDomain: "apps.example.test"}}
	api := c.kube.NetworkingV1().Ingresses(Namespace(target.ApplicationID))
	if err := c.applyIngress(ctx, target, "web", svc); err != nil {
		t.Fatal(err)
	}
	// Another controller adds its own annotation to the ingress we own.
	seeded, err := api.Get(ctx, "web", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if seeded.Annotations == nil {
		seeded.Annotations = map[string]string{}
	}
	seeded.Annotations["other-controller/key"] = "value"
	if _, err = api.Update(ctx, seeded, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	for _, backendHTTP2 := range []bool{true, false} {
		svc.BackendHTTP2 = backendHTTP2
		target.Spec.Services["web"] = svc
		if err = c.applyIngress(ctx, target, "web", svc); err != nil {
			t.Fatal(err)
		}
		ing, err := api.Get(ctx, "web", metav1.GetOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if ing.Annotations["other-controller/key"] != "value" {
			t.Fatalf("another controller annotation was lost with backend HTTP/2 %v: %v", backendHTTP2, ing.Annotations)
		}
		wanted := ""
		if backendHTTP2 {
			wanted = "h2"
		}
		if ing.Annotations["haproxy.org/server-proto"] != wanted {
			t.Fatalf("backend HTTP/2 annotation is %q with the field %v", ing.Annotations["haproxy.org/server-proto"], backendHTTP2)
		}
	}
}

func TestIngressBackendHTTP2CoexistsWithTLSAnnotations(t *testing.T) {
	ctx := context.Background()
	target := testTarget(t)
	svc := target.Spec.Services["web"]
	svc.BackendHTTP2 = true
	target.Spec.Services["web"] = svc
	c := &Client{kube: fake.NewClientset(), options: Options{AppDomain: "apps.example.test", IngressClass: "haproxy", TLSIssuer: "hakopod-issuer"}}
	api := c.kube.NetworkingV1().Ingresses(Namespace(target.ApplicationID))
	if err := c.applyIngress(ctx, target, "web", svc); err != nil {
		t.Fatal(err)
	}
	ing, err := api.Get(ctx, "web", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for key, value := range map[string]string{"haproxy.org/server-proto": "h2", "haproxy.org/ssl-redirect": "true", "cert-manager.io/cluster-issuer": "hakopod-issuer"} {
		if ing.Annotations[key] != value {
			t.Fatalf("annotation %s is %q instead of %q: %v", key, ing.Annotations[key], value, ing.Annotations)
		}
	}
	if len(ing.Spec.TLS) != 1 {
		t.Fatal("TLS was not attached alongside backend HTTP/2", ing.Spec.TLS)
	}
	svc.BackendHTTP2 = false
	target.Spec.Services["web"] = svc
	if err = c.applyIngress(ctx, target, "web", svc); err != nil {
		t.Fatal(err)
	}
	if ing, err = api.Get(ctx, "web", metav1.GetOptions{}); err != nil {
		t.Fatal(err)
	}
	if ing.Annotations["haproxy.org/server-proto"] != "" {
		t.Fatal("backend HTTP/2 stayed on after opting out")
	}
	if ing.Annotations["haproxy.org/ssl-redirect"] != "true" || ing.Annotations["cert-manager.io/cluster-issuer"] != "hakopod-issuer" {
		t.Fatalf("clearing backend HTTP/2 removed the TLS annotations: %v", ing.Annotations)
	}
	if len(ing.Spec.TLS) != 1 {
		t.Fatal("clearing backend HTTP/2 detached TLS", ing.Spec.TLS)
	}
}
