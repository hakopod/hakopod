package cluster

import (
	"testing"

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
