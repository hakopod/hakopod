package managedplatform

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestSupabaseNetworkPoliciesRestrictComponentDependencies(t *testing.T) {
	in := rendererFixture()
	manifests, err := RenderSupabase(in)
	if err != nil {
		t.Fatal(err)
	}
	policies := []*networkingv1.NetworkPolicy{}
	for _, object := range manifests.Objects {
		if p, ok := object.(*networkingv1.NetworkPolicy); ok {
			policies = append(policies, p)
		}
	}
	labels := func(component string) map[string]string {
		out := componentSelectorLabels(in.Spec.Name, component)
		out["hakopod.io/managed-platform-id"] = in.PlatformID
		return out
	}
	matches := func(selector *metav1.LabelSelector, values map[string]string) bool {
		if selector == nil {
			return false
		}
		if len(selector.MatchExpressions) != 0 {
			t.Fatal("test requires declarative exact selectors")
		}
		for key, value := range selector.MatchLabels {
			if values[key] != value {
				return false
			}
		}
		return true
	}
	allowed := func(from, to map[string]string, port int32) bool {
		ingress, egress := false, false
		for _, p := range policies {
			if matches(&p.Spec.PodSelector, to) {
				for _, r := range p.Spec.Ingress {
					for _, peer := range r.From {
						if peer.NamespaceSelector != nil || peer.IPBlock != nil || !matches(peer.PodSelector, from) {
							continue
						}
						for _, value := range r.Ports {
							if value.Protocol != nil && *value.Protocol == corev1.ProtocolTCP && value.Port != nil && value.Port.IntVal == port {
								ingress = true
							}
						}
					}
				}
			}
			if matches(&p.Spec.PodSelector, from) {
				for _, r := range p.Spec.Egress {
					for _, peer := range r.To {
						if peer.NamespaceSelector != nil || peer.IPBlock != nil || !matches(peer.PodSelector, to) {
							continue
						}
						for _, value := range r.Ports {
							if value.Protocol != nil && *value.Protocol == corev1.ProtocolTCP && value.Port != nil && value.Port.IntVal == port {
								egress = true
							}
						}
					}
				}
			}
		}
		return ingress && egress
	}
	for _, pair := range [][2]string{{"auth", "database"}, {"pooler", "database"}, {"rest", "database"}, {"realtime", "database"}, {"edge-runtime", "database"}, {"postgres-meta", "database"}, {"storage", "database"}, {"studio", "database"}} {
		if !allowed(labels(pair[0]), labels(pair[1]), 5432) {
			t.Fatalf("%s cannot use its database", pair[0])
		}
	}
	for _, c := range SupabaseComponentNames() {
		if allowed(labels(c), labels("database"), 22) {
			t.Fatalf("%s has an undeclared database port", c)
		}
		if allowed(labels("database"), labels(c), 8443) || allowed(labels("image-proxy"), labels(c), 5432) {
			t.Fatalf("%s gained reverse access", c)
		}
		if allowed(labels(c), labels("pooler"), 4000) || allowed(labels(c), labels("pooler"), 6543) {
			t.Fatalf("%s can reach the pooler administration/listener", c)
		}
	}
	if !allowed(labels("storage"), labels("image-proxy"), 5001) || !allowed(labels("storage"), labels("rest"), 3000) || !allowed(labels("api-gateway"), labels("auth"), 9999) || !allowed(labels("studio"), labels("postgres-meta"), 8080) {
		t.Fatal("required component dependency is blocked")
	}
	if allowed(labels("auth"), labels("postgres-meta"), 8080) || allowed(labels("realtime"), labels("storage"), 5000) || allowed(labels("rest"), labels("studio"), 3000) {
		t.Fatal("unrelated component access remains open")
	}
	spoofed := labels("auth")
	delete(spoofed, "hakopod.io/managed-platform-id")
	if allowed(spoofed, labels("database"), 5432) {
		t.Fatal("unowned pod can impersonate a component")
	}
	for _, p := range policies {
		for _, r := range p.Spec.Egress {
			for _, peer := range r.To {
				if peer.NamespaceSelector != nil {
					if !matches(peer.NamespaceSelector, map[string]string{"kubernetes.io/metadata.name": "kube-system"}) || !matches(peer.PodSelector, map[string]string{"k8s-app": "kube-dns"}) || matches(peer.PodSelector, map[string]string{"k8s-app": "other"}) {
						t.Fatal("DNS egress is not restricted to cluster DNS")
					}
				}
			}
		}
	}
	in.ApprovedExternalHTTPSCIDRs = []string{"93.184.216.0/24"}
	external, err := RenderSupabase(in)
	if err != nil {
		t.Fatal(err)
	}
	for _, object := range external.Objects {
		p, ok := object.(*networkingv1.NetworkPolicy)
		if !ok || p.Name != "supabase-edge-approved-https" {
			continue
		}
		unowned := labels("edge-runtime")
		delete(unowned, "hakopod.io/managed-platform-id")
		if matches(&p.Spec.PodSelector, unowned) || !matches(&p.Spec.PodSelector, labels("edge-runtime")) {
			t.Fatal("external HTTPS egress lacks exact platform identity")
		}
	}
	for _, p := range policies {
		for _, r := range p.Spec.Ingress {
			if len(r.Ports) == 0 || len(r.From) == 0 {
				t.Fatalf("%s contains wildcard ingress", p.Name)
			}
		}
		for _, r := range p.Spec.Egress {
			if len(r.Ports) == 0 || len(r.To) == 0 {
				t.Fatalf("%s contains wildcard egress", p.Name)
			}
		}
	}
}
