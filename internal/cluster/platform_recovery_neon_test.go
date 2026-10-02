package cluster

import (
	"context"
	"testing"

	"github.com/hakopod/hakopod/internal/store"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
)

func TestParseRecoveryLSNOutput(t *testing.T) {
	for _, test := range []struct {
		value string
		want  uint64
	}{
		{"0/16B6C50", 0x16b6c50},
		{"BEGIN\n1/00000001\nCOMMIT\n", 1<<32 | 1},
	} {
		got, err := parseRecoveryLSNOutput(test.value)
		if err != nil || got != test.want {
			t.Fatalf("parseRecoveryLSNOutput(%q) = %#x, %v; want %#x", test.value, got, err, test.want)
		}
	}
	if _, err := parseRecoveryLSNOutput("COMMIT"); err == nil {
		t.Fatal("invalid SQL output was accepted as an LSN")
	}
}

func TestEnsureNeonRecoveryConfigMapReplaysAndRejectsForeignOwner(t *testing.T) {
	ctx := context.Background()
	kube := fake.NewSimpleClientset()
	namespaceUID := types.UID("namespace-uid")
	desired := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "neon-pageserver-0-recovery-abc", Namespace: "managed-platform-test", OwnerReferences: []metav1.OwnerReference{{UID: namespaceUID}}}, Data: map[string]string{"pageserver.toml": "remote_storage={prefix_in_bucket='/target/recovery/op/pageserver-0'}"}}
	if err := ensureNeonRecoveryConfigMap(ctx, kube, namespaceUID, desired.DeepCopy()); err != nil {
		t.Fatalf("create recovery prefix config: %v", err)
	}
	if err := ensureNeonRecoveryConfigMap(ctx, kube, namespaceUID, desired.DeepCopy()); err != nil {
		t.Fatalf("exact recovery prefix replay failed: %v", err)
	}
	foreign := desired.DeepCopy()
	foreign.Name = "neon-pageserver-1-recovery-abc"
	foreign.OwnerReferences[0].UID = types.UID("foreign-namespace")
	if _, err := kube.CoreV1().ConfigMaps(foreign.Namespace).Create(ctx, foreign, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	foreign.OwnerReferences[0].UID = namespaceUID
	if err := ensureNeonRecoveryConfigMap(ctx, kube, namespaceUID, foreign); err == nil {
		t.Fatal("foreign-owned recovery prefix config was accepted")
	}
}

func TestNeonProviderRecoveryClaimExcludesKubernetesInventory(t *testing.T) {
	for _, claim := range []store.PlatformResourceClaim{{Component: "configmap.neon-pageserver-0-r1", Kind: "configmap"}, {Component: "statefulset.neon-pageserver-0", Kind: "statefulset"}, {Component: "namespace.managed-platform-test", Kind: "namespace"}, {Component: "pageserver-registration-0", Kind: "runtime_component"}} {
		if provider, err := neonProviderRecoveryClaim(claim); err != nil || provider {
			t.Fatalf("non-replaceable claim entered provider recovery planning: %#v %v", claim, err)
		}
	}
	for _, claim := range []store.PlatformResourceClaim{{Component: "tenant", Kind: "neon_tenant"}, {Component: "timeline", Kind: "neon_timeline"}, {Component: "compute-primary", Kind: "runtime_component"}} {
		if provider, err := neonProviderRecoveryClaim(claim); err != nil || !provider {
			t.Fatalf("provider claim was excluded from recovery planning: %#v %v", claim, err)
		}
	}
}

func TestNeonComputeExecIdentityRequiresExactTwoContainerPod(t *testing.T) {
	images := map[string]string{"compute": "example/compute@sha256:" + repeatHex("a"), "compute-tls": "example/tls@sha256:" + repeatHex("b")}
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "compute-0", Namespace: "managed-platform-test", UID: types.UID("pod-uid"), ResourceVersion: "7"}, Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "compute", Image: images["compute"]}, {Name: "compute-tls", Image: images["compute-tls"]}}}, Status: corev1.PodStatus{Phase: corev1.PodRunning, ContainerStatuses: []corev1.ContainerStatus{{Name: "compute", Ready: true, ImageID: "containerd://sha256:" + repeatHex("a")}, {Name: "compute-tls", Ready: true, ImageID: "containerd://sha256:" + repeatHex("b")}}}}
	if err := validateNeonComputeContainers(pod, images); err != nil {
		t.Fatalf("accepted two-container compute pod was rejected: %v", err)
	}
	changed := pod.DeepCopy()
	changed.Spec.Containers[1].Image = "example/tls@sha256:" + repeatHex("c")
	if err := validateNeonComputeContainers(changed, images); err == nil {
		t.Fatal("changed compute TLS sidecar was accepted")
	}
	changed = pod.DeepCopy()
	changed.UID = types.UID("other-pod")
	if sameNeonExecPod(pod, changed) {
		t.Fatal("changed pod UID was accepted for execution")
	}
}

func repeatHex(value string) string {
	result := ""
	for len(result) < 64 {
		result += value
	}
	return result
}
