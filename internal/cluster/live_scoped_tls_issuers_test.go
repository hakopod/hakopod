package cluster

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/spec"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/clientcmd"
)

// This uses the CI development cluster's pinned cert-manager installation.
// Synthetic self-signed fixtures prove issuance, not public ACME validation.
func TestLiveScopedTLSIssuers(t *testing.T) {
	if os.Getenv("HAKOPOD_SCOPED_TLS_ISSUERS_TEST") != "1" {
		t.Skip("requires named development cluster issuer acceptance")
	}
	path := os.Getenv("HAKOPOD_TEST_KUBECONFIG")
	cfg, err := clientcmd.LoadFromFile(path)
	if err != nil || cfg.CurrentContext != "k3d-hakopod-dev" {
		t.Fatal("refusing issuer test outside k3d-hakopod-dev")
	}
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	defaultName := "hp-default-" + suffix
	c, err := New(path, Options{DeploymentMode: DeploymentManagedCloud, AppDomain: "127.0.0.1.sslip.io", IngressClass: "haproxy", TLSIssuer: defaultName})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	createFixture := func(path string, body any) {
		raw, _ := json.Marshal(body)
		if _, e := c.restClient().Post().AbsPath(path).Body(raw).DoRaw(ctx); e != nil {
			t.Fatal(e)
		}
	}
	createFixture("/apis/cert-manager.io/v1/clusterissuers", map[string]any{"apiVersion": "cert-manager.io/v1", "kind": "ClusterIssuer", "metadata": map[string]any{"name": defaultName}, "spec": map[string]any{"selfSigned": map[string]any{}}})
	t.Cleanup(func() {
		clean, done := context.WithTimeout(context.Background(), 30*time.Second)
		defer done()
		if _, e := c.restClient().Delete().AbsPath("/apis/cert-manager.io/v1/clusterissuers/" + defaultName).DoRaw(clean); e != nil {
			t.Error(e)
		}
	})
	target := Target{ApplicationID: "issuer-live-" + suffix, Project: "issuer-fixture", Environment: "test", Revision: 1, Spec: spec.Application{Name: "issuer-fixture", Services: map[string]spec.Service{"web": {Public: true, Port: 8080}}}}
	other := Target{ApplicationID: "issuer-other-" + suffix, Project: "other", Environment: "test"}
	for _, app := range []Target{target, other} {
		ns := Namespace(app.ApplicationID)
		_, err = c.kube.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns, Labels: labelsFor(app, "")}}, metav1.CreateOptions{})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			clean, done := context.WithTimeout(context.Background(), 30*time.Second)
			defer done()
			found, e := c.kube.CoreV1().Namespaces().Get(clean, ns, metav1.GetOptions{})
			if e == nil && owned(found, app) == nil {
				if e = c.kube.CoreV1().Namespaces().Delete(clean, ns, deleteOptions(found)); e != nil {
					t.Error(e)
				}
			}
		})
	}
	// Exercise the real product's constrained HTTP01 configuration and retry.
	first, err := c.CreateApplicationTLSIssuer(ctx, target, "acme-fixture", "fixture@example.test", false)
	if err != nil {
		t.Fatal(err)
	}
	if first.Kind != "Issuer" {
		t.Fatal(first)
	}
	if _, err = c.CreateApplicationTLSIssuer(ctx, target, "acme-fixture", "fixture@example.test", false); err != nil {
		t.Fatal(err)
	}
	if err = c.ValidateTLSIssuer(ctx, other, "acme-fixture", "Issuer"); err == nil {
		t.Fatal("cross-application issuer was accepted")
	}
	if err = c.ValidateTLSIssuer(ctx, target, "foreign-cluster-issuer", "ClusterIssuer"); err == nil {
		t.Fatal("foreign global issuer was accepted")
	}
	defaults, err := c.DefaultTLSIssuers(ctx)
	if err != nil || len(defaults.Items) != 1 || !defaults.Items[0].Default {
		t.Fatalf("default discovery: %#v %v", defaults, err)
	}
	localName := "synthetic-fixture"
	createFixture(applicationIssuerPath(target), map[string]any{"apiVersion": "cert-manager.io/v1", "kind": "Issuer", "metadata": map[string]any{"name": localName, "namespace": Namespace(target.ApplicationID), "labels": labelsFor(target, "")}, "spec": map[string]any{"selfSigned": map[string]any{}}})
	for _, ref := range []struct{ name, kind string }{{defaultName, "ClusterIssuer"}, {localName, "Issuer"}, {defaultName, "ClusterIssuer"}} {
		svc := target.Spec.Services["web"]
		svc.TLS = &spec.TLSConfig{Issuer: ref.name, IssuerKind: ref.kind}
		target.Spec.Services["web"] = svc
		if err = c.applyIngress(ctx, target, "web", svc); err != nil {
			t.Fatal(err)
		}
		ing, e := c.kube.NetworkingV1().Ingresses(Namespace(target.ApplicationID)).Get(ctx, "web", metav1.GetOptions{})
		if e != nil {
			t.Fatal(e)
		}
		wanted, absent := "cert-manager.io/cluster-issuer", "cert-manager.io/issuer"
		if ref.kind == "Issuer" {
			wanted, absent = absent, wanted
		}
		if ing.Annotations[wanted] != ref.name || ing.Annotations[absent] != "" {
			t.Fatalf("issuer annotations: %#v", ing.Annotations)
		}
		for {
			raw, e := c.restClient().Get().AbsPath("/apis/cert-manager.io/v1/namespaces/" + Namespace(target.ApplicationID) + "/certificates/hakopod-tls-web").DoRaw(ctx)
			var cert struct {
				Metadata metav1.ObjectMeta `json:"metadata"`
				Spec     struct {
					IssuerRef struct{ Name, Kind string } `json:"issuerRef"`
				} `json:"spec"`
				Status struct {
					Conditions []struct {
						Type, Status       string
						ObservedGeneration int64 `json:"observedGeneration"`
					} `json:"conditions"`
				} `json:"status"`
			}
			ready := false
			if e == nil && json.Unmarshal(raw, &cert) == nil && cert.Spec.IssuerRef.Name == ref.name && cert.Spec.IssuerRef.Kind == ref.kind {
				for _, condition := range cert.Status.Conditions {
					if condition.Type == "Ready" && condition.Status == "True" && condition.ObservedGeneration == cert.Metadata.Generation {
						ready = true
					}
				}
			}
			if ready {
				status, e := c.ServiceTLS(ctx, target, "web")
				if e != nil || !status.Ready || status.IssuerKind != ref.kind {
					t.Fatalf("TLS status: %#v %v", status, e)
				}
				break
			}
			select {
			case <-ctx.Done():
				t.Fatalf("certificate not issued for %s/%s: %v", ref.kind, ref.name, ctx.Err())
			case <-time.After(time.Second):
			}
		}
	}
	t.Log("Verified owned namespaced creation, immutable retry, isolation, default discovery and actual synthetic certificate issuance across issuer kind changes; public ACME issuance was not tested.")
}
