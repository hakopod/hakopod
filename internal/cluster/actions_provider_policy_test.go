package cluster

import (
	"strings"
	"testing"

	networkingv1 "k8s.io/api/networking/v1"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
)

func TestServicePolicyExcludesOnlyGitLabSlotsWithTheirOwnNetworkPolicy(t *testing.T) {
	target := runnerTarget(t)
	for _, policy := range policies(target) {
		if policy.Name != "hakopod-service-runner" {
			continue
		}
		selector, err := metav1.LabelSelectorAsSelector(&policy.Spec.PodSelector)
		if err != nil {
			t.Fatal(err)
		}
		for _, provider := range []string{"", "github", "gitlab", "bitbucket"} {
			pod := labelsFor(target, "runner")
			if provider != "" {
				pod[gitlabActionsProviderLabel] = provider
			}
			if selector.Matches(labels.Set(pod)) != (provider != "gitlab") {
				t.Fatal("ordinary service policy selected the wrong provider", provider)
			}
			pod[ownerKey] = "another-application"
			if selector.Matches(labels.Set(pod)) {
				t.Fatal("service policy selected another application's provider pod")
			}
		}
		return
	}
	t.Fatal("runner service policy is missing")
}

func TestRunnerPoliciesReachOnlyTheRegistryMirrorInTheCluster(t *testing.T) {
	mirror := func(rules []networkingv1.NetworkPolicyEgressRule) int {
		count := 0
		for _, rule := range rules {
			for _, peer := range rule.To {
				if peer.NamespaceSelector != nil && peer.NamespaceSelector.MatchLabels["kubernetes.io/metadata.name"] == ActionsMirrorNamespace {
					if peer.PodSelector == nil || peer.PodSelector.MatchLabels["app.kubernetes.io/name"] != "registry-mirror" || len(rule.Ports) != 1 || rule.Ports[0].Port.IntVal != actionsMirrorPort {
						t.Fatal("mirror egress is broader than one pod and port")
					}
					count++
				}
			}
		}
		return count
	}
	target := runnerTarget(t)
	for _, policy := range policies(target) {
		if policy.Name == "hakopod-service-runner" && mirror(policy.Spec.Egress) != 1 {
			t.Fatal("GitHub runner policy must allow the registry mirror once")
		}
	}
	if mirror(gitlabSlotNetworkPolicy(target, "runner", "slot", GitLabActionsRuntime{}, nil, true).Spec.Egress) != 1 {
		t.Fatal("GitLab slot policy must allow the registry mirror once")
	}
	if mirror(gitlabSlotNetworkPolicy(target, "runner", "slot", GitLabActionsRuntime{}, nil, false).Spec.Egress) != 0 {
		t.Fatal("a revoked GitLab slot must not reach the registry mirror")
	}
	internal := runnerTarget(t)
	for name, network := range internal.Spec.Networks {
		network.Internal = true
		internal.Spec.Networks[name] = network
	}
	for _, policy := range policies(internal) {
		if mirror(policy.Spec.Egress) != 0 {
			t.Fatal("an internal-only runner must not reach the registry mirror")
		}
	}
	if mirror(gitlabSlotNetworkPolicy(internal, "runner", "slot", GitLabActionsRuntime{}, nil, true).Spec.Egress) != 0 {
		t.Fatal("an internal-only GitLab slot must not reach the registry mirror")
	}
	host := ActionsMirrorNamespace + ".svc.cluster.local:5000"
	if !strings.Contains(actionsDaemon, "--registry-mirror=http://registry-mirror."+host) || !strings.Contains(actionsDaemon, "--insecure-registry=registry-mirror."+host) {
		t.Fatal("dockerd must use the installation registry mirror")
	}
}
