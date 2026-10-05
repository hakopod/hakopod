package cluster

import (
	"context"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestVitessHealthRejectsInventoryChangesDuringProbes(t *testing.T) {
	d := vitessTestDatabase()
	ns, object := vitessInventoryRoot(t, d)
	identity := strings.Repeat("a", 64)
	owner := metav1.OwnerReference{APIVersion: "planetscale.com/v2", Kind: "VitessCluster", Name: "database", UID: object.GetUID()}
	pod := vitessInventoryTablet(d, "tablet", identity, owner)
	pod.Status.Phase = corev1.PodRunning
	pod.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}
	inventory, err := (&Client{}).newVitessObservationInventory(context.Background(), d, ns, object, identity, []corev1.Pod{pod})
	if err != nil {
		t.Fatal(err)
	}
	if !inventory.matchesCurrent(ns, object, identity, []corev1.Pod{pod}) {
		t.Fatal("unchanged ready inventory was rejected")
	}
	for name, change := range map[string]func(*corev1.Namespace, *unstructured.Unstructured, *corev1.Pod){
		"namespace replaced":  func(n *corev1.Namespace, _ *unstructured.Unstructured, _ *corev1.Pod) { n.UID = "replacement" },
		"controller replaced": func(_ *corev1.Namespace, o *unstructured.Unstructured, _ *corev1.Pod) { o.SetUID("replacement") },
		"revision changed": func(_ *corev1.Namespace, o *unstructured.Unstructured, _ *corev1.Pod) {
			o.SetAnnotations(map[string]string{"hakopod.io/database-revision": "2"})
		},
		"controller spec changed": func(_ *corev1.Namespace, o *unstructured.Unstructured, _ *corev1.Pod) {
			o.Object["spec"] = map[string]any{"changed": true}
		},
		"tablet replaced": func(_ *corev1.Namespace, _ *unstructured.Unstructured, p *corev1.Pod) { p.UID = "replacement" },
		"tablet image changed": func(_ *corev1.Namespace, _ *unstructured.Unstructured, p *corev1.Pod) {
			p.Spec.Containers[0].Image = "unapproved"
		},
		"tablet stopped": func(_ *corev1.Namespace, _ *unstructured.Unstructured, p *corev1.Pod) { p.Status.Conditions = nil },
		"tablet identity changed": func(_ *corev1.Namespace, _ *unstructured.Unstructured, p *corev1.Pod) {
			p.Annotations[vitessIdentityAnnotation] = strings.Repeat("b", 64)
		},
	} {
		t.Run(name, func(t *testing.T) {
			n, o, p := ns.DeepCopy(), object.DeepCopy(), pod.DeepCopy()
			change(n, o, p)
			if inventory.matchesCurrent(n, o, identity, []corev1.Pod{*p}) {
				t.Fatal("changed inventory retained a healthy result")
			}
		})
	}
	if inventory.matchesCurrent(ns, object, strings.Repeat("b", 64), []corev1.Pod{pod}) || inventory.matchesCurrent(ns, object, identity, nil) || inventory.matchesCurrent(ns, object, identity, []corev1.Pod{pod, pod}) {
		t.Fatal("changed identity or incomplete inventory retained a healthy result")
	}
}
