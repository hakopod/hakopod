package managedplatform

import (
	"reflect"
	"strings"
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

func TestTrustedNodePlacementAddsOnlyExactPoolPolicy(t *testing.T) {
	pod := corev1.PodSpec{NodeSelector: map[string]string{"kubernetes.io/os": "linux"}}
	applyTrustedNodePlacement(&pod, "worker", "databases", "runsc")
	assertScheduledOnNode(t, pod, "worker")
	if pod.NodeSelector["hakopod.com/pool"] != "databases" || len(pod.Tolerations) != 1 {
		t.Fatalf("trusted pool scheduling was not rendered: %#v", pod)
	}
	toleration := pod.Tolerations[0]
	if toleration.Key != "hakopod.com/pool" || toleration.Operator != corev1.TolerationOpEqual || toleration.Value != "databases" || toleration.Effect != corev1.TaintEffectNoSchedule {
		t.Fatalf("pool toleration is broader than the trusted policy: %#v", toleration)
	}
	if pod.RuntimeClassName == nil || *pod.RuntimeClassName != "runsc" {
		t.Fatalf("trusted runtime class was not rendered: %#v", pod.RuntimeClassName)
	}

	unpooled := corev1.PodSpec{}
	applyTrustedNodePlacement(&unpooled, "worker", "", "")
	if len(unpooled.Tolerations) != 0 || unpooled.NodeSelector["hakopod.com/pool"] != "" {
		t.Fatalf("empty scheduling pool changed OSS placement: %#v", unpooled)
	}
}

func TestRenderersRejectMalformedSchedulingPool(t *testing.T) {
	if _, err := RenderSupabase(SupabaseRenderInput{SchedulingPool: "bad/value"}); err == nil || !strings.Contains(err.Error(), "scheduling pool") {
		t.Fatal("Supabase renderer accepted a malformed scheduling pool")
	}
	if _, err := RenderNeon(NeonRenderInput{SchedulingPool: "bad/value"}); err == nil || !strings.Contains(err.Error(), "scheduling pool") {
		t.Fatal("Neon renderer accepted a malformed scheduling pool")
	}
}
