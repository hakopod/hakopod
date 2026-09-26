package cluster

import (
	"context"
	"strings"
	"testing"

	"github.com/hakopod/hakopod/internal/spec"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

// A tunnel server publishes raw TCP only and terminates TLS itself, so it has
// no HTTP ingress rule to hang a certificate on.
func certificateOnlyTarget(t *testing.T) (*Client, *fake.Clientset, Target, string) {
	t.Helper()
	app, err := spec.Normalize(spec.Application{Name: "demo", Services: map[string]spec.Service{
		"web":    {Image: "docker.io/library/python@sha256:" + strings.Repeat("a", 64), Port: 8080, Public: true},
		"worker": {Image: "docker.io/library/python@sha256:" + strings.Repeat("a", 64)},
		"tunnel": {Image: "docker.io/library/python@sha256:" + strings.Repeat("a", 64), Port: 7000, PublicTCP: []spec.PublicTCPListener{{Port: 7443, TargetPort: 7000, SourceCIDRs: []string{"0.0.0.0/0"}}}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	target := Target{ApplicationID: "opaque-app-id", Project: "demo", Environment: "test", OperationID: "op-1", Revision: 1, Spec: app}
	kube := fake.NewClientset()
	c := &Client{kube: kube, options: Options{AppDomain: "apps.example.test", IngressClass: "haproxy", TLSIssuer: "hakopod-issuer"}}
	host := c.hostname(target, "tunnel")
	svc := target.Spec.Services["tunnel"]
	svc.CertificateMounts = []spec.CertificateMount{{Source: "ingress", Hostname: host, MountPath: "/certificates/tunnel"}}
	target.Spec.Services["tunnel"] = svc
	return c, kube, target, host
}

func TestCertificateOnlyIngressCarriesHostAndTLSWithoutBackend(t *testing.T) {
	ctx := context.Background()
	c, _, target, host := certificateOnlyTarget(t)
	if err := c.reconcilePrivateIngress(ctx, target, "tunnel", target.Spec.Services["tunnel"]); err != nil {
		t.Fatal(err)
	}
	ing, err := c.kube.NetworkingV1().Ingresses(Namespace(target.ApplicationID)).Get(ctx, "tunnel", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(ing.Spec.Rules) != 1 || ing.Spec.Rules[0].Host != host || ing.Spec.Rules[0].HTTP != nil {
		t.Fatalf("certificate-only ingress must carry exactly one host-only rule: %v", ing.Spec.Rules)
	}
	if ing.Spec.IngressClassName == nil || *ing.Spec.IngressClassName != "haproxy" {
		t.Fatal("certificate-only ingress lost its ingress class", ing.Spec.IngressClassName)
	}
	if len(ing.Spec.TLS) != 1 || len(ing.Spec.TLS[0].Hosts) != 1 || ing.Spec.TLS[0].Hosts[0] != host || ing.Spec.TLS[0].SecretName != "hakopod-tls-tunnel" {
		t.Fatalf("certificate-only ingress must request TLS for its hostname: %v", ing.Spec.TLS)
	}
	if ing.Annotations["cert-manager.io/cluster-issuer"] != "hakopod-issuer" {
		t.Fatalf("certificate-only ingress must ask the configured issuer: %v", ing.Annotations)
	}
	if ing.Labels[serviceKey] != "tunnel" {
		t.Fatal("certificate-only ingress is not labelled as owned by the service", ing.Labels)
	}
}

func TestPublicHTTPIngressKeepsRulesAndBackend(t *testing.T) {
	ctx := context.Background()
	c, _, target, _ := certificateOnlyTarget(t)
	svc := target.Spec.Services["web"]
	if err := c.applyIngress(ctx, target, "web", svc); err != nil {
		t.Fatal(err)
	}
	ing, err := c.kube.NetworkingV1().Ingresses(Namespace(target.ApplicationID)).Get(ctx, "web", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(ing.Spec.Rules) != 1 || ing.Spec.Rules[0].Host != c.hostname(target, "web") {
		t.Fatalf("public ingress rules changed: %v", ing.Spec.Rules)
	}
	path := ing.Spec.Rules[0].HTTP
	if path == nil || len(path.Paths) != 1 || path.Paths[0].Path != "/" || path.Paths[0].Backend.Service.Name != "web" || path.Paths[0].Backend.Service.Port.Number != svc.Port {
		t.Fatalf("public ingress lost its HTTP backend: %v", path)
	}
	// A public service never takes the certificate-only shape, whatever it mounts.
	svc.CertificateMounts = []spec.CertificateMount{{Source: "ingress", Hostname: c.hostname(target, "web"), MountPath: "/certificates/web"}}
	if certificateOnlyIngress(svc) {
		t.Fatal("a public HTTP service must keep its backend ingress")
	}
}

func TestCertificateOnlyIngressSurvivesRedeploy(t *testing.T) {
	ctx := context.Background()
	c, kube, target, host := certificateOnlyTarget(t)
	kube.PrependReactor("delete", "ingresses", func(action k8stesting.Action) (bool, runtime.Object, error) {
		t.Fatal("a live certificate mount lost its ingress; the certificate would be re-requested on every release")
		return false, nil, nil
	})
	for release := 0; release < 3; release++ {
		if err := c.reconcilePrivateIngress(ctx, target, "tunnel", target.Spec.Services["tunnel"]); err != nil {
			t.Fatal(err)
		}
	}
	ing, err := c.kube.NetworkingV1().Ingresses(Namespace(target.ApplicationID)).Get(ctx, "tunnel", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(ing.Spec.Rules) != 1 || ing.Spec.Rules[0].Host != host || ing.Spec.Rules[0].HTTP != nil || len(ing.Spec.TLS) != 1 {
		t.Fatalf("redeploy changed the certificate-only ingress shape: %v %v", ing.Spec.Rules, ing.Spec.TLS)
	}
}

func TestPrivateServiceWithoutExposureGetsNoIngress(t *testing.T) {
	ctx := context.Background()
	c, _, target, _ := certificateOnlyTarget(t)
	for _, name := range []string{"worker"} {
		if err := c.reconcilePrivateIngress(ctx, target, name, target.Spec.Services[name]); err != nil {
			t.Fatal(err)
		}
		if _, err := c.kube.NetworkingV1().Ingresses(Namespace(target.ApplicationID)).Get(ctx, name, metav1.GetOptions{}); !apierrors.IsNotFound(err) {
			t.Fatal("a private service must not receive an ingress", err)
		}
	}
	// A raw TCP service without an automatic mount stays without an ingress too.
	svc := target.Spec.Services["tunnel"]
	svc.CertificateMounts = nil
	if certificateOnlyIngress(svc) {
		t.Fatal("public TCP alone must not create an ingress")
	}
	if err := c.reconcilePrivateIngress(ctx, target, "tunnel", svc); err != nil {
		t.Fatal(err)
	}
	if _, err := c.kube.NetworkingV1().Ingresses(Namespace(target.ApplicationID)).Get(ctx, "tunnel", metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatal("public TCP without an automatic certificate mount received an ingress", err)
	}
}

func TestCertificateOnlyIngressIsAnIngressCertificateSource(t *testing.T) {
	ctx := context.Background()
	c, _, target, host := certificateOnlyTarget(t)
	if err := c.reconcilePrivateIngress(ctx, target, "tunnel", target.Spec.Services["tunnel"]); err != nil {
		t.Fatal(err)
	}
	// The certificate has not been issued yet, so the source is unavailable
	// rather than rejected for missing public HTTP.
	_, err := c.backendIngressSource(ctx, target, "tunnel", host)
	if err == nil || !strings.Contains(err.Error(), "has not been issued") {
		t.Fatal("a raw TCP service must be accepted as an ingress certificate source", err)
	}
	if _, err = c.backendIngressSource(ctx, target, "worker", c.hostname(target, "worker")); err == nil || !strings.Contains(err.Error(), "public HTTP or public TCP") {
		t.Fatal("a service without HTTP or TCP exposure must be rejected", err)
	}
}
