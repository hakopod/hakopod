package cluster

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/hakopod/hakopod/internal/database"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	kubefake "k8s.io/client-go/kubernetes/fake"
	kubetesting "k8s.io/client-go/testing"
	"k8s.io/client-go/util/retry"
)

var (
	myduckStatefulSetResource = schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "statefulsets"}
	myduckNamespaceResource   = schema.GroupVersionResource{Version: "v1", Resource: "namespaces"}
	myduckClaimResource       = schema.GroupVersionResource{Version: "v1", Resource: "persistentvolumeclaims"}
	myduckVolumeResource      = schema.GroupVersionResource{Version: "v1", Resource: "persistentvolumes"}
)

func myduckColdConflictFixture(t *testing.T) (*Client, *kubefake.Clientset, database.Resource, myduckColdRecord) {
	t.Helper()
	d, set := myduckFixtureSet(t)
	namespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: set.Namespace, UID: "namespace-uid", Labels: map[string]string{databaseOwner: d.ID, managedBy: "hakopod"}}}
	zero := int32(0)
	set.Spec.Replicas = &zero
	set.ResourceVersion = "1"
	set.Labels = map[string]string{databaseOwner: d.ID, managedBy: "hakopod"}
	set.Annotations = map[string]string{"hakopod.io/database-revision": "1"}
	claim := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: "data-database-0", Namespace: set.Namespace, UID: "claim-uid", Labels: map[string]string{databaseOwner: d.ID, managedBy: "hakopod"}},
		Spec:       corev1.PersistentVolumeClaimSpec{AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce}, VolumeName: "data-volume"},
		Status:     corev1.PersistentVolumeClaimStatus{Phase: corev1.ClaimBound},
	}
	volume := &corev1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{Name: claim.Spec.VolumeName, UID: "volume-uid"},
		Spec:       corev1.PersistentVolumeSpec{ClaimRef: &corev1.ObjectReference{Name: claim.Name, Namespace: claim.Namespace, UID: claim.UID}},
		Status:     corev1.PersistentVolumeStatus{Phase: corev1.VolumeBound},
	}
	record := myduckColdRecord{JobID: strings.Repeat("b", 32), Revision: d.Revision, NamespaceUID: namespace.UID, SetUID: set.UID, ClaimUID: claim.UID, VolumeUID: volume.UID, MemberUID: "member-uid", Node: "node-a"}
	encoded, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	set.Annotations[myduckColdAnnotation] = string(encoded)
	kube := kubefake.NewSimpleClientset(namespace, set, claim, volume)
	return &Client{kube: kube}, kube, d, record
}

func myduckTrackerObject(t *testing.T, kube *kubefake.Clientset, resource schema.GroupVersionResource, namespace, name string) runtime.Object {
	t.Helper()
	object, err := kube.Tracker().Get(resource, namespace, name)
	if err != nil {
		t.Fatal(err)
	}
	return object
}

func myduckTrackerUpdate(t *testing.T, kube *kubefake.Clientset, resource schema.GroupVersionResource, object runtime.Object, namespace string) {
	t.Helper()
	if err := kube.Tracker().Update(resource, object, namespace); err != nil {
		t.Fatal(err)
	}
}

func TestMyDuckColdCleanupRetriesConflictWithFreshState(t *testing.T) {
	for _, test := range []struct {
		name             string
		resume           bool
		expectedReplicas int32
	}{{name: "resume backup source", resume: true, expectedReplicas: 1}, {name: "keep failed restore stopped", resume: false, expectedReplicas: 0}} {
		t.Run(test.name, func(t *testing.T) {
			client, kube, d, record := myduckColdConflictFixture(t)
			updates := 0
			kube.PrependReactor("update", "statefulsets", func(action kubetesting.Action) (bool, runtime.Object, error) {
				updates++
				candidate := action.(kubetesting.UpdateAction).GetObject().(*appsv1.StatefulSet)
				if updates == 1 {
					current := myduckTrackerObject(t, kube, myduckStatefulSetResource, candidate.Namespace, candidate.Name).(*appsv1.StatefulSet).DeepCopy()
					current.ResourceVersion = "2"
					current.Labels["controller.example/observation"] = "preserve"
					myduckTrackerUpdate(t, kube, myduckStatefulSetResource, current, current.Namespace)
					return true, nil, apierrors.NewConflict(schema.GroupResource{Group: "apps", Resource: "statefulsets"}, candidate.Name, errors.New("stale resource version"))
				}
				if candidate.ResourceVersion != "2" || candidate.Labels["controller.example/observation"] != "preserve" {
					t.Fatalf("retry did not mutate the freshly read StatefulSet: resourceVersion=%q labels=%v", candidate.ResourceVersion, candidate.Labels)
				}
				return false, nil, nil
			})
			checks := 0
			if err := client.ReconcileMyDuckColdStorage(context.Background(), d, record.JobID, test.resume, func() error { checks++; return nil }); err != nil {
				t.Fatal(err)
			}
			updated, err := kube.AppsV1().StatefulSets(DatabaseNamespace(d.ID)).Get(context.Background(), "database", metav1.GetOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if updates != 2 || checks != 2 || updated.Spec.Replicas == nil || *updated.Spec.Replicas != test.expectedReplicas || updated.Annotations[myduckColdAnnotation] != "" || updated.Labels["controller.example/observation"] != "preserve" {
				t.Fatalf("unexpected conflict cleanup result: updates=%d checks=%d set=%#v", updates, checks, updated)
			}
		})
	}
}

func TestMyDuckColdCleanupRejectsChangedStateAfterConflict(t *testing.T) {
	revoked := errors.New("cleanup authority revoked")
	tests := map[string]func(*testing.T, *kubefake.Clientset, database.Resource, myduckColdRecord){
		"set uid": func(t *testing.T, kube *kubefake.Clientset, d database.Resource, _ myduckColdRecord) {
			set := myduckTrackerObject(t, kube, myduckStatefulSetResource, DatabaseNamespace(d.ID), "database").(*appsv1.StatefulSet).DeepCopy()
			set.UID = "replacement-set"
			set.ResourceVersion = "2"
			myduckTrackerUpdate(t, kube, myduckStatefulSetResource, set, set.Namespace)
		},
		"cold receipt restore":         myduckChangeColdRecord(func(record *myduckColdRecord) { record.Restore = !record.Restore }),
		"cold receipt job id":          myduckChangeColdRecord(func(record *myduckColdRecord) { record.JobID = strings.Repeat("c", 32) }),
		"cold receipt revision":        myduckChangeColdRecord(func(record *myduckColdRecord) { record.Revision++ }),
		"cold receipt node":            myduckChangeColdRecord(func(record *myduckColdRecord) { record.Node = "node-b" }),
		"cold receipt member uid":      myduckChangeColdRecord(func(record *myduckColdRecord) { record.MemberUID = "replacement-member" }),
		"database revision annotation": myduckChangeColdSet(func(set *appsv1.StatefulSet) { set.Annotations["hakopod.io/database-revision"] = "2" }),
		"namespace uid": func(t *testing.T, kube *kubefake.Clientset, d database.Resource, _ myduckColdRecord) {
			namespace := myduckTrackerObject(t, kube, myduckNamespaceResource, "", DatabaseNamespace(d.ID)).(*corev1.Namespace).DeepCopy()
			namespace.UID = "replacement-namespace"
			myduckTrackerUpdate(t, kube, myduckNamespaceResource, namespace, "")
		},
		"claim uid": func(t *testing.T, kube *kubefake.Clientset, d database.Resource, _ myduckColdRecord) {
			claim := myduckTrackerObject(t, kube, myduckClaimResource, DatabaseNamespace(d.ID), "data-database-0").(*corev1.PersistentVolumeClaim).DeepCopy()
			claim.UID = "replacement-claim"
			myduckTrackerUpdate(t, kube, myduckClaimResource, claim, claim.Namespace)
		},
		"volume uid": func(t *testing.T, kube *kubefake.Clientset, _ database.Resource, _ myduckColdRecord) {
			volume := myduckTrackerObject(t, kube, myduckVolumeResource, "", "data-volume").(*corev1.PersistentVolume).DeepCopy()
			volume.UID = "replacement-volume"
			myduckTrackerUpdate(t, kube, myduckVolumeResource, volume, "")
		},
		"nil replicas":     myduckChangeColdSet(func(set *appsv1.StatefulSet) { set.Spec.Replicas = nil }),
		"nonzero replicas": myduckChangeColdSet(func(set *appsv1.StatefulSet) { one := int32(1); set.Spec.Replicas = &one }),
		"ownership":        myduckChangeColdSet(func(set *appsv1.StatefulSet) { set.Labels[databaseOwner] = strings.Repeat("c", 32) }),
		"helper reappearance": func(t *testing.T, kube *kubefake.Clientset, d database.Resource, record myduckColdRecord) {
			set := myduckTrackerObject(t, kube, myduckStatefulSetResource, DatabaseNamespace(d.ID), "database").(*appsv1.StatefulSet)
			helper := myduckStoragePod(d, set, record)
			helper.UID = "reappeared-helper"
			if err := kube.Tracker().Add(helper); err != nil {
				t.Fatal(err)
			}
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			client, kube, d, record := myduckColdConflictFixture(t)
			updates := 0
			kube.PrependReactor("update", "statefulsets", func(action kubetesting.Action) (bool, runtime.Object, error) {
				updates++
				candidate := action.(kubetesting.UpdateAction).GetObject().(*appsv1.StatefulSet)
				if updates == 1 {
					mutate(t, kube, d, record)
					return true, nil, apierrors.NewConflict(schema.GroupResource{Group: "apps", Resource: "statefulsets"}, candidate.Name, errors.New("stale resource version"))
				}
				return false, nil, nil
			})
			if err := client.ReconcileMyDuckColdStorage(context.Background(), d, record.JobID, true, func() error { return nil }); err == nil {
				t.Fatal("changed state was accepted after conflict")
			}
			if updates != 1 {
				t.Fatalf("changed state reached another update: %d", updates)
			}
		})
	}

	t.Run("revoked before callback", func(t *testing.T) {
		client, kube, d, record := myduckColdConflictFixture(t)
		updates, checks := 0, 0
		kube.PrependReactor("update", "statefulsets", func(action kubetesting.Action) (bool, runtime.Object, error) {
			updates++
			candidate := action.(kubetesting.UpdateAction).GetObject().(*appsv1.StatefulSet)
			return true, nil, apierrors.NewConflict(schema.GroupResource{Group: "apps", Resource: "statefulsets"}, candidate.Name, errors.New("stale resource version"))
		})
		err := client.ReconcileMyDuckColdStorage(context.Background(), d, record.JobID, true, func() error {
			checks++
			if checks > 1 {
				return revoked
			}
			return nil
		})
		if !errors.Is(err, revoked) || updates != 1 || checks != 2 {
			t.Fatalf("revoked cleanup continued: err=%v updates=%d checks=%d", err, updates, checks)
		}
	})
}

func myduckChangeColdSet(change func(*appsv1.StatefulSet)) func(*testing.T, *kubefake.Clientset, database.Resource, myduckColdRecord) {
	return func(t *testing.T, kube *kubefake.Clientset, d database.Resource, _ myduckColdRecord) {
		set := myduckTrackerObject(t, kube, myduckStatefulSetResource, DatabaseNamespace(d.ID), "database").(*appsv1.StatefulSet).DeepCopy()
		change(set)
		set.ResourceVersion = "2"
		myduckTrackerUpdate(t, kube, myduckStatefulSetResource, set, set.Namespace)
	}
}

func myduckChangeColdRecord(change func(*myduckColdRecord)) func(*testing.T, *kubefake.Clientset, database.Resource, myduckColdRecord) {
	return func(t *testing.T, kube *kubefake.Clientset, d database.Resource, record myduckColdRecord) {
		change(&record)
		encoded, err := json.Marshal(record)
		if err != nil {
			t.Fatal(err)
		}
		myduckChangeColdSet(func(set *appsv1.StatefulSet) { set.Annotations[myduckColdAnnotation] = string(encoded) })(t, kube, d, record)
	}
}

func TestMyDuckColdCleanupPersistentConflictPreservesReceipt(t *testing.T) {
	client, kube, d, record := myduckColdConflictFixture(t)
	updates := 0
	kube.PrependReactor("update", "statefulsets", func(action kubetesting.Action) (bool, runtime.Object, error) {
		updates++
		candidate := action.(kubetesting.UpdateAction).GetObject().(*appsv1.StatefulSet)
		return true, nil, apierrors.NewConflict(schema.GroupResource{Group: "apps", Resource: "statefulsets"}, candidate.Name, errors.New("stale resource version"))
	})
	if err := client.ReconcileMyDuckColdStorage(context.Background(), d, record.JobID, true, func() error { return nil }); err == nil || !apierrors.IsConflict(err) {
		t.Fatalf("persistent conflict was not returned: %v", err)
	}
	set, err := kube.AppsV1().StatefulSets(DatabaseNamespace(d.ID)).Get(context.Background(), "database", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if updates < 2 || updates > retry.DefaultBackoff.Steps || set.Annotations[myduckColdAnnotation] == "" || set.Spec.Replicas == nil || *set.Spec.Replicas != 0 {
		t.Fatalf("persistent conflict changed the fenced StatefulSet: updates=%d set=%#v", updates, set)
	}
}
