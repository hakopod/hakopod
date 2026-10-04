package cluster

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/database"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	kubefake "k8s.io/client-go/kubernetes/fake"
)

func mysqlObservationController(t *testing.T, d database.Resource, status any) *unstructured.Unstructured {
	t.Helper()
	object, err := DatabaseObject(d)
	if err != nil {
		t.Fatal(err)
	}
	object.SetUID("mysql-controller")
	if status != nil {
		if err = unstructured.SetNestedField(object.Object, status, "status", "cluster", "status"); err != nil {
			t.Fatal(err)
		}
	}
	return object
}

func mysqlObservationPod(t *testing.T, d database.Resource, name string, owner types.UID) *corev1.Pod {
	t.Helper()
	object, err := DatabaseObject(d)
	if err != nil {
		t.Fatal(err)
	}
	raw, found, err := unstructured.NestedMap(object.Object, "spec", "podSpec")
	if err != nil || !found {
		t.Fatal("MySQL pod specification is unavailable", err)
	}
	var spec corev1.PodSpec
	if err = runtime.DefaultUnstructuredConverter.FromUnstructured(raw, &spec); err != nil {
		t.Fatal(err)
	}
	controller := true
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{
		Name: name, Namespace: DatabaseNamespace(d.ID), UID: types.UID(name + "-uid"),
		Annotations:     map[string]string{"dev.gvisor.spec.mount.rundir.share": "pod", "dev.gvisor.spec.mount.rundir.type": "tmpfs", "dev.gvisor.spec.mount.rundir.options": "rw,rprivate,size=16777216"},
		OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "StatefulSet", Name: "database", UID: owner, Controller: &controller}},
	}, Spec: spec, Status: corev1.PodStatus{Phase: corev1.PodRunning, Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}}}
	if !databasePodMatches(*pod, d) {
		t.Fatal("MySQL observation fixture does not satisfy the runtime pod contract")
	}
	return pod
}

func TestMySQLObservationStatusGate(t *testing.T) {
	d := mysqlUnitFixture()
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: DatabaseNamespace(d.ID), UID: "namespace", Labels: databaseLabels(d)}}
	set := &appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{Name: "database", Namespace: ns.Name, UID: "mysql-statefulset", OwnerReferences: []metav1.OwnerReference{{UID: "mysql-controller"}}}}
	pod := mysqlObservationPod(t, d, "database-0", set.UID)
	member := database.Member{Name: pod.Name, UID: string(pod.UID)}

	for _, tc := range []struct {
		name    string
		status  any
		allowed bool
	}{
		{name: "online", status: "ONLINE", allowed: true},
		{name: "partial", status: "ONLINE_PARTIAL", allowed: true},
		{name: "uncertain", status: "ONLINE_UNCERTAIN"},
		{name: "offline", status: "OFFLINE"},
		{name: "no quorum", status: "NO_QUORUM"},
		{name: "missing"},
		{name: "malformed", status: int64(1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			object := mysqlObservationController(t, d, tc.status)
			c := &Client{kube: kubefake.NewSimpleClientset(ns, set, pod), dynamic: dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), object)}
			o := database.Observation{Status: "pending", Members: []database.Member{member}}
			err := c.observeMySQLDatabase(context.Background(), d, object, &o)
			if tc.allowed {
				if err == nil || !strings.Contains(err.Error(), "transport") {
					t.Fatalf("allowed status did not reach native transport verification: %v", err)
				}
			} else if err == nil || err.Error() != "waiting for MySQL Group Replication" {
				t.Fatalf("unsafe status passed the operator status gate: %v", err)
			}
			if o.Status == "ready" {
				t.Fatal("observation became ready without native verification")
			}
		})
	}
}

func TestMySQLPartialStatusDoesNotBootstrapAccount(t *testing.T) {
	d := mysqlUnitFixture()
	object := mysqlObservationController(t, d, "ONLINE_PARTIAL")
	kube := kubefake.NewSimpleClientset()
	c := &Client{kube: kube, dynamic: dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), object)}
	callbacks := 0
	if err := c.prepareMySQLAccount(context.Background(), d, []byte("fixture-password"), func() error {
		callbacks++
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if callbacks != 0 || len(kube.Actions()) != 0 {
		t.Fatalf("partial status attempted account bootstrap: callbacks=%d kube_actions=%d", callbacks, len(kube.Actions()))
	}
}

func TestObserveMySQLPartialRejectsIncompletePodInventory(t *testing.T) {
	d := mysqlUnitFixture()
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: DatabaseNamespace(d.ID), UID: "namespace", Labels: databaseLabels(d)}}
	object := mysqlObservationController(t, d, "ONLINE_PARTIAL")
	set := &appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{Name: "database", Namespace: ns.Name, UID: "mysql-statefulset", OwnerReferences: []metav1.OwnerReference{{UID: object.GetUID()}}}}

	valid := []*corev1.Pod{
		mysqlObservationPod(t, d, "database-0", set.UID),
		mysqlObservationPod(t, d, "database-1", set.UID),
		mysqlObservationPod(t, d, "database-2", set.UID),
	}
	deleting := valid[2].DeepCopy()
	stamp := metav1.NewTime(time.Now())
	deleting.DeletionTimestamp = &stamp
	unready := valid[2].DeepCopy()
	unready.Status.Conditions[0].Status = corev1.ConditionFalse
	foreign := valid[2].DeepCopy()
	foreign.OwnerReferences[0].UID = "foreign-statefulset"
	tests := []struct {
		name, message, error string
		pods                 []runtime.Object
	}{
		{name: "missing", message: "Waiting for database members to become ready.", pods: []runtime.Object{valid[0].DeepCopy(), valid[1].DeepCopy()}},
		{name: "extra", message: "Waiting for database members to become ready.", pods: []runtime.Object{valid[0].DeepCopy(), valid[1].DeepCopy(), valid[2].DeepCopy(), mysqlObservationPod(t, d, "database-3", set.UID)}},
		{name: "deleting", message: "Waiting for replaced database members to stop.", pods: []runtime.Object{valid[0].DeepCopy(), valid[1].DeepCopy(), deleting}},
		{name: "unready", message: "Waiting for database members to become ready.", pods: []runtime.Object{valid[0].DeepCopy(), valid[1].DeepCopy(), unready}},
		{name: "foreign", error: "database member ownership changed", pods: []runtime.Object{valid[0].DeepCopy(), valid[1].DeepCopy(), foreign}},
		{name: "valid", error: "database execution transport or pinned container is unavailable", pods: []runtime.Object{valid[0].DeepCopy(), valid[1].DeepCopy(), valid[2].DeepCopy()}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			objects := []runtime.Object{ns.DeepCopy(), set.DeepCopy()}
			objects = append(objects, tc.pods...)
			c := &Client{kube: kubefake.NewSimpleClientset(objects...), dynamic: dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), object.DeepCopy())}
			o, err := c.ObserveDatabase(context.Background(), d)
			if o.Status == "ready" {
				t.Fatal("partial cluster became ready with incomplete pod inventory")
			}
			if tc.error != "" {
				if err == nil || err.Error() != tc.error {
					t.Fatalf("unexpected inventory error: %v", err)
				}
			} else if err != nil || o.Message != tc.message {
				t.Fatalf("unexpected inventory result: message=%q error=%v", o.Message, err)
			}
		})
	}
}
