package cluster

import (
	"context"
	"fmt"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
)

func TestAlarmNodesOnlyReadsBoundedNodeConditions(t *testing.T) {
	kube := fake.NewSimpleClientset(&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node-a"}, Status: corev1.NodeStatus{Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}, {Type: corev1.NodeDiskPressure, Status: corev1.ConditionTrue, Reason: "DiskFull", Message: "capacity exhausted"}}}})
	items, err := (&Client{kube: kube}).AlarmNodes(context.Background())
	if err != nil || len(items) != 1 || len(items[0].Conditions) != 2 {
		t.Fatalf("condition read failed: %+v %v", items, err)
	}
	for _, action := range kube.Actions() {
		if action.GetResource().Resource != "nodes" || action.GetVerb() != "list" {
			t.Fatalf("alarms performed unrelated inventory request: %v", action)
		}
	}
	objects := make([]runtime.Object, 0, 201)
	for i := 0; i < 201; i++ {
		objects = append(objects, &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("node-%03d", i)}})
	}
	if _, err := (&Client{kube: fake.NewSimpleClientset(objects...)}).AlarmNodes(context.Background()); err == nil {
		t.Fatal("unbounded node inventory accepted")
	}
}
