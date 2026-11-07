package cluster

import (
	"context"
	"fmt"
	"testing"

	"github.com/hakopod/hakopod/internal/spec"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
)

func TestCustomHostnameShardsAreStableAndCapped(t *testing.T) {
	hosts := make([]string, 1000)
	for i := range hosts {
		hosts[i] = fmt.Sprintf("tenant-%d.customer-%d.example", i, i%37)
	}
	shards := customHostnameShards(hosts, 1000)
	where := map[string]int{}
	total := 0
	for n, members := range shards {
		if n < 0 || n >= 20 || len(members) > customHostnamesPerShard {
			t.Fatalf("shard %d has %d hosts", n, len(members))
		}
		for _, host := range members {
			where[host] = n
		}
		total += len(members)
	}
	if total != len(hosts) {
		t.Fatalf("placed %d of %d hosts", total, len(hosts))
	}
	// Adding a host never moves an existing one.
	grown := customHostnameShards(append(hosts, "new.customer.example"), 1000)
	for n, members := range grown {
		for _, host := range members {
			if old, ok := where[host]; ok && old != n {
				t.Fatalf("%s moved from shard %d to %d", host, old, n)
			}
		}
	}
	// A lowered max never overfills a shard.
	for _, members := range customHostnameShards(hosts[:250], 100) {
		if len(members) > customHostnamesPerShard {
			t.Fatal("shard overfilled after the limit was lowered", len(members))
		}
	}
	if CustomHostnameShardName("web", 3) != "web-ch-3" || customHostnameSecret("a.example") != customHostnameSecret("a.example") || customHostnameSecret("a.example") == customHostnameSecret("b.example") {
		t.Fatal("names must be deterministic and distinct per host")
	}
}

func customHostnameClient(t *testing.T) (*Client, Target) {
	t.Helper()
	target := testTarget(t)
	svc := target.Spec.Services["web"]
	svc.BackendHTTP2 = true
	svc.IngressTimeoutSeconds = 120
	svc.CustomHostnames = &spec.CustomHostnames{Enabled: true}
	target.Spec.Services["web"] = svc
	dynamic := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{certificateResource: "CertificateList"})
	return &Client{kube: fake.NewClientset(), dynamic: dynamic, options: Options{AppDomain: "apps.example.test", IngressClass: "haproxy", TLSIssuer: "letsencrypt"}}, target
}

func TestCustomHostnameShardMirrorsTheMainIngress(t *testing.T) {
	ctx := context.Background()
	c, target := customHostnameClient(t)
	svc := target.Spec.Services["web"]
	if err := c.applyIngress(ctx, target, "web", svc); err != nil {
		t.Fatal(err)
	}
	hosts := []string{"shop.customer.com", "store.other.example"}
	if err := c.ReconcileCustomHostnames(ctx, target, "web", hosts); err != nil {
		t.Fatal(err)
	}
	ns := Namespace(target.ApplicationID)
	main, err := c.kube.NetworkingV1().Ingresses(ns).Get(ctx, "web", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	shards, err := c.kube.NetworkingV1().Ingresses(ns).List(ctx, metav1.ListOptions{LabelSelector: customHostnameLabel})
	if err != nil || len(shards.Items) == 0 {
		t.Fatal("no shard ingress", err)
	}
	seen := map[string]bool{}
	for _, shard := range shards.Items {
		if err := owned(&shard, target); err != nil || shard.Labels[serviceKey] != "web" {
			t.Fatal("shard lacks the service ownership labels", shard.Labels)
		}
		if *shard.Spec.IngressClassName != *main.Spec.IngressClassName {
			t.Fatal("shard uses another ingress class")
		}
		for _, key := range []string{"haproxy.org/server-proto", "haproxy.org/timeout-server", "haproxy.org/ssl-redirect"} {
			if shard.Annotations[key] == "" || shard.Annotations[key] != main.Annotations[key] {
				t.Fatalf("annotation %s is %q on the shard and %q on the main ingress", key, shard.Annotations[key], main.Annotations[key])
			}
		}
		if shard.Annotations["cert-manager.io/cluster-issuer"] != "" {
			t.Fatal("ingress-shim would create a second certificate per host")
		}
		for i, rule := range shard.Spec.Rules {
			if rule.HTTP.Paths[0].Backend.Service.Name != "web" || rule.HTTP.Paths[0].Backend.Service.Port.Number != 8080 || rule.HTTP.Paths[0].Path != main.Spec.Rules[0].HTTP.Paths[0].Path {
				t.Fatal("shard backend differs from the main ingress", rule)
			}
			if shard.Spec.TLS[i].Hosts[0] != rule.Host || shard.Spec.TLS[i].SecretName != customHostnameSecret(rule.Host) {
				t.Fatal("each host needs its own TLS secret", shard.Spec.TLS)
			}
			seen[rule.Host] = true
		}
	}
	if len(seen) != 2 {
		t.Fatal(seen)
	}
	certificate, err := c.dynamic.Resource(certificateResource).Namespace(ns).Get(ctx, customHostnameSecret("shop.customer.com"), metav1.GetOptions{})
	if err != nil {
		t.Fatal("no certificate requested", err)
	}
	if issuer := certificate.Object["spec"].(map[string]any)["issuerRef"].(map[string]any); issuer["name"] != "letsencrypt" || issuer["kind"] != "ClusterIssuer" {
		t.Fatal("certificate must use the installation issuer", issuer)
	}
	if _, err := c.kube.NetworkingV1().NetworkPolicies(ns).Get(ctx, "hakopod-acme-http01", metav1.GetOptions{}); err != nil {
		t.Fatal("HTTP-01 solver must be reachable", err)
	}
	// The main ingress cleanup never sees shards, however many there are.
	previous := target.Spec
	target.Previous = &previous
	if err := c.cleanup(ctx, target); err != nil {
		t.Fatal(err)
	}
	if list, _ := c.kube.NetworkingV1().Ingresses(ns).List(ctx, metav1.ListOptions{LabelSelector: customHostnameLabel}); len(list.Items) != len(shards.Items) {
		t.Fatal("cleanup removed shards of a service that still routes tenant hostnames")
	}
}

func TestCustomHostnameAddRemoveAndCleanup(t *testing.T) {
	ctx := context.Background()
	c, target := customHostnameClient(t)
	ns := Namespace(target.ApplicationID)
	hosts := []string{"a.customer.com", "b.customer.com"}
	if err := c.ReconcileCustomHostnames(ctx, target, "web", hosts); err != nil {
		t.Fatal(err)
	}
	// cert-manager would issue the secret with the template labels.
	issued := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: customHostnameSecret("b.customer.com"), Namespace: ns, Labels: customHostnameLabels(target, "web")}}
	if _, err := c.kube.CoreV1().Secrets(ns).Create(ctx, issued, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	kube := c.kube.(*fake.Clientset)
	kube.ClearActions()
	if err := c.ReconcileCustomHostnames(ctx, target, "web", hosts); err != nil {
		t.Fatal(err)
	}
	for _, action := range kube.Actions() {
		if action.GetResource().Resource == "ingresses" && (action.GetVerb() == "update" || action.GetVerb() == "create") {
			t.Fatal("an unchanged shard was rewritten", action)
		}
	}
	if err := c.ReconcileCustomHostnames(ctx, target, "web", hosts[:1]); err != nil {
		t.Fatal(err)
	}
	if _, err := c.dynamic.Resource(certificateResource).Namespace(ns).Get(ctx, customHostnameSecret("b.customer.com"), metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatal("removed host kept its certificate", err)
	}
	if _, err := c.kube.CoreV1().Secrets(ns).Get(ctx, issued.Name, metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatal("removed host kept its TLS secret", err)
	}
	list, _ := c.kube.NetworkingV1().Ingresses(ns).List(ctx, metav1.ListOptions{LabelSelector: customHostnameLabel})
	hostsLeft := 0
	for _, shard := range list.Items {
		for _, rule := range shard.Spec.Rules {
			if rule.Host == "b.customer.com" {
				t.Fatal("removed host still routed")
			}
			hostsLeft++
		}
	}
	if hostsLeft != 1 {
		t.Fatal("remaining host lost", hostsLeft)
	}

	// Turning custom hostnames off removes everything through deploy cleanup.
	svc := target.Spec.Services["web"]
	svc.CustomHostnames = nil
	target.Spec.Services["web"] = svc
	if err := c.cleanup(ctx, target); err != nil {
		t.Fatal(err)
	}
	if list, _ := c.kube.NetworkingV1().Ingresses(ns).List(ctx, metav1.ListOptions{LabelSelector: customHostnameLabel}); len(list.Items) != 0 {
		t.Fatal("shards outlived custom_hostnames", list.Items)
	}
	if certs, _ := c.dynamic.Resource(certificateResource).Namespace(ns).List(ctx, metav1.ListOptions{}); len(certs.Items) != 0 {
		t.Fatal("certificates outlived custom_hostnames", certs.Items)
	}
}

func TestCustomHostnameCertificateStates(t *testing.T) {
	ctx := context.Background()
	c, target := customHostnameClient(t)
	if err := c.ReconcileCustomHostnames(ctx, target, "web", []string{"a.customer.com"}); err != nil {
		t.Fatal(err)
	}
	ns := Namespace(target.ApplicationID)
	api := c.dynamic.Resource(certificateResource).Namespace(ns)
	certificate, _ := api.Get(ctx, customHostnameSecret("a.customer.com"), metav1.GetOptions{})
	certificate.Object["status"] = map[string]any{"conditions": []any{map[string]any{"type": "Ready", "status": "False", "message": "Issuing certificate as Secret does not exist"}}}
	if _, err := api.Update(ctx, certificate, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	states, tls, err := c.CustomHostnameCertificates(ctx, target, "web")
	if err != nil || !tls || states["a.customer.com"].Ready || states["a.customer.com"].Reason != "Issuing certificate as Secret does not exist" {
		t.Fatal(states, tls, err)
	}
	certificate.Object["status"] = map[string]any{"conditions": []any{map[string]any{"type": "Ready", "status": "True"}}}
	if _, err := api.Update(ctx, certificate, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if states, _, _ = c.CustomHostnameCertificates(ctx, target, "web"); !states["a.customer.com"].Ready {
		t.Fatal("ready certificate not reported", states)
	}
	c.options.TLSIssuer = ""
	if _, tls, _ = c.CustomHostnameCertificates(ctx, target, "web"); tls {
		t.Fatal("without an issuer hosts are served over HTTP")
	}
}
