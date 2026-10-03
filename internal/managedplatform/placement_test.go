package managedplatform

import (
	"reflect"
	"testing"

	corev1 "k8s.io/api/core/v1"
)

func assertScheduledOnNode(t *testing.T, pod corev1.PodSpec, expected string) {
	t.Helper()
	if pod.NodeName != "" {
		t.Fatal("direct node assignment bypasses scheduling and delayed volume binding")
	}
	if _, exists := pod.NodeSelector["kubernetes.io/hostname"]; exists {
		t.Fatal("placement must use the reviewed Node name, not its hostname label")
	}
	if pod.Affinity == nil || pod.Affinity.NodeAffinity == nil {
		t.Fatal("workload has no required node placement")
	}
	affinity := pod.Affinity.NodeAffinity
	if affinity.RequiredDuringSchedulingIgnoredDuringExecution == nil || len(affinity.PreferredDuringSchedulingIgnoredDuringExecution) != 0 {
		t.Fatal("node placement must be required, not preferred")
	}
	terms := affinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms
	if len(terms) != 1 || len(terms[0].MatchExpressions) != 0 || len(terms[0].MatchFields) != 1 {
		t.Fatal("node placement permits an alternative node or label-based identity")
	}
	field := terms[0].MatchFields[0]
	if field.Key != "metadata.name" || field.Operator != corev1.NodeSelectorOpIn || !reflect.DeepEqual(field.Values, []string{expected}) {
		t.Fatalf("workload must schedule on reviewed Node %s; got %#v", expected, field)
	}
}
