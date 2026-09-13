package cluster

import (
	"context"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/spec"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/kubernetes/fake"
)

func TestVirtualNetworkSelectorsKeepScopeAndPeerRestrictions(t *testing.T) {
	target := testTarget(t)
	target.SharedNetworks = map[string]string{"default": "network-id/data"}
	svc := target.Spec.Services["web"]
	svc.NetworkAccess = &spec.NetworkAccess{From: []string{}, FromApplications: []string{"orders/worker"}}
	svc.Ports = []spec.Port{{Name: "metrics", Port: 9090, TargetPort: 8081, Protocol: "TCP"}}
	target.Spec.Services["web"] = svc
	var found bool
	for _, policy := range policies(target) {
		if policy.Name != "hakopod-service-web" {
			continue
		}
		for _, rule := range policy.Spec.Ingress {
			for _, peer := range rule.From {
				if peer.PodSelector == nil || peer.PodSelector.MatchLabels[applicationNameKey] != "orders" {
					continue
				}
				found = true
				if len(rule.Ports) != 2 || rule.Ports[1].Port.IntVal != 8081 {
					t.Fatal("shared ingress did not enforce target ports")
				}
				ns, _ := metav1.LabelSelectorAsSelector(peer.NamespaceSelector)
				pod, _ := metav1.LabelSelectorAsSelector(peer.PodSelector)
				allowedNS := labels.Set{managedBy: "hakopod", scopeKey: scopeLabel(target.Project, target.Environment), ownerKey: "other-owner"}
				allowedPod := labels.Set{managedBy: "hakopod", applicationNameKey: "orders", serviceKey: "worker", sharedNetworkKey(target, "network-id/data"): "true"}
				if !ns.Matches(allowedNS) || !pod.Matches(allowedPod) {
					t.Fatal("declared peer was denied")
				}
				allowedNS[scopeKey] = scopeLabel(target.Project, "production")
				if ns.Matches(allowedNS) {
					t.Fatal("environment boundary bypassed")
				}
				allowedNS[scopeKey], allowedNS[ownerKey] = scopeLabel(target.Project, target.Environment), ownerID(target.ApplicationID)
				if ns.Matches(allowedNS) {
					t.Fatal("shared rule bypassed local peer restrictions")
				}
				allowedPod[serviceKey] = "unlisted"
				if pod.Matches(allowedPod) {
					t.Fatal("unlisted service was allowed")
				}
			}
		}
	}
	if !found {
		t.Fatal("shared ingress rule missing")
	}
	workload := deployment(target, "web", svc, time.Minute)
	if workload.Spec.Template.Labels[sharedNetworkKey(target, "network-id/data")] != "true" || workload.Spec.Template.Labels[applicationNameKey] != target.Spec.Name {
		t.Fatal("shared pod identity missing")
	}
	if sharedNetworkKey(target, "network-id/data") == sharedNetworkKey(target, "new-network-id/data") {
		t.Fatal("recreated networks retained old identities")
	}
}

func TestVirtualNetworkCannotDeployWithoutGrantResolution(t *testing.T) {
	target := testTarget(t)
	target.Spec.Networks["default"] = spec.Network{VirtualNetwork: "commerce", Segment: "data"}
	kube := fake.NewClientset()
	c := &Client{kube: kube, options: Options{AppDomain: "example.test"}}
	if _, err := c.Deploy(context.Background(), target, nil); err == nil {
		t.Fatal("network deployed without checking grants")
	}
	if len(kube.Actions()) != 0 {
		t.Fatal("cluster mutated before grant validation")
	}
}
