package cluster

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/hakopod/hakopod/internal/database"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
	kubetesting "k8s.io/client-go/testing"
)

// Unit fixtures model a SIDB whose operator and garbage collector are deleting
// resources concurrently. No fake state is exposed through the API.
func oracleFreeDeletingFixture(t *testing.T) (database.Resource, *corev1.Namespace, *unstructured.Unstructured, *corev1.PersistentVolumeClaim, *corev1.PersistentVolume) {
	t.Helper()
	d := oracleFixture()
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: DatabaseNamespace(d.ID), UID: "namespace-uid", Labels: databaseLabels(d)}}
	root, err := oracleFreeObject(d, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	root.SetUID("root-uid")
	root.SetResourceVersion("10")
	root.SetOwnerReferences([]metav1.OwnerReference{{APIVersion: "v1", Kind: "Namespace", Name: ns.Name, UID: ns.UID}})
	root.SetFinalizers([]string{oracleFreeFinalizer, metav1.FinalizerDeleteDependents})
	stamp := metav1.Now()
	root.SetDeletionTimestamp(&stamp)
	claim := &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{
		Name: "database-additional-0", Namespace: ns.Name, UID: "claim-uid", ResourceVersion: "11", DeletionTimestamp: &stamp,
		Finalizers:      []string{oracleFreeStorageFinalizer, "kubernetes.io/pvc-protection"},
		OwnerReferences: []metav1.OwnerReference{{APIVersion: "database.oracle.com/v4", Kind: "SingleInstanceDatabase", Name: root.GetName(), UID: root.GetUID(), Controller: ptr(true), BlockOwnerDeletion: ptr(true)}},
	}, Spec: corev1.PersistentVolumeClaimSpec{VolumeName: "database-volume"}}
	volume := &corev1.PersistentVolume{ObjectMeta: metav1.ObjectMeta{Name: claim.Spec.VolumeName, UID: "volume-uid", ResourceVersion: "12"}, Spec: corev1.PersistentVolumeSpec{
		PersistentVolumeReclaimPolicy: corev1.PersistentVolumeReclaimDelete,
		ClaimRef:                      &corev1.ObjectReference{APIVersion: "v1", Kind: "PersistentVolumeClaim", Namespace: ns.Name, Name: claim.Name, UID: claim.UID},
	}}
	return d, ns, root, claim, volume
}

func TestOracleFreeDeletionAcceptsOnlyOwnedReclamationProgress(t *testing.T) {
	for _, mode := range []string{"missing", "deleting", "foreign deleting", "retained deleting", "live root missing", "live claim missing", "live root deleting", "late-bound retained deleting"} {
		t.Run(mode, func(t *testing.T) {
			d, ns, root, claim, volume := oracleFreeDeletingFixture(t)
			objects := append([]runtime.Object{ns, claim}, oracleFreeTestControllerObjects(t, d, ns.UID)...)
			if mode == "deleting" || mode == "foreign deleting" || mode == "retained deleting" || mode == "live root deleting" || mode == "late-bound retained deleting" {
				volume.DeletionTimestamp = root.GetDeletionTimestamp()
				objects = append(objects, volume)
			}
			switch mode {
			case "foreign deleting":
				volume.Spec.ClaimRef.UID = "foreign"
			case "retained deleting", "late-bound retained deleting":
				volume.Spec.PersistentVolumeReclaimPolicy = corev1.PersistentVolumeReclaimRetain
			case "live root missing", "live root deleting":
				root.SetDeletionTimestamp(nil)
			case "live claim missing":
				claim.DeletionTimestamp = nil
			}
			if mode == "late-bound retained deleting" {
				claim.Spec.VolumeName = ""
			}
			kube := fake.NewClientset(objects...)
			c := &Client{kube: kube, dynamic: dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), root)}
			done, err := c.DeleteDatabase(context.Background(), d, func() error { return nil })
			allowed := mode == "missing" || mode == "deleting"
			if done || allowed && err != nil || !allowed && err == nil {
				t.Fatal("unexpected deletion progress", done, err)
			}
			actual, err := kube.CoreV1().PersistentVolumeClaims(ns.Name).Get(context.Background(), claim.Name, metav1.GetOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if slices.Contains(actual.Finalizers, oracleFreeStorageFinalizer) == allowed || !slices.Contains(actual.Finalizers, "kubernetes.io/pvc-protection") {
				t.Fatal("storage protection changed outside verified reclamation")
			}
			for _, action := range kube.Actions() {
				if action.GetVerb() == "delete" || !allowed && action.GetVerb() == "update" {
					t.Fatal("unsafe deletion mutation", action.GetVerb(), action.GetResource())
				}
			}
		})
	}
}

func TestOracleFreeDeletionRelistsAfterConcurrentStorageMutation(t *testing.T) {
	for _, mode := range []string{"claim conflict", "claim removed", "volume conflict", "volume removed", "foreign after conflict"} {
		t.Run(mode, func(t *testing.T) {
			d, ns, root, claim, volume := oracleFreeDeletingFixture(t)
			resource := "persistentvolumeclaims"
			if mode == "volume conflict" || mode == "volume removed" {
				resource = "persistentvolumes"
				volume.Spec.PersistentVolumeReclaimPolicy = corev1.PersistentVolumeReclaimRetain
			}
			kube := fake.NewClientset(append([]runtime.Object{ns, claim, volume}, oracleFreeTestControllerObjects(t, d, ns.UID)...)...)
			c := &Client{kube: kube, dynamic: dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), root)}
			attempts := 0
			kube.PrependReactor("update", resource, func(action kubetesting.Action) (bool, runtime.Object, error) {
				attempts++
				if attempts > 1 {
					return false, nil, nil
				}
				name, namespace := claim.Name, ns.Name
				if resource == "persistentvolumes" {
					name, namespace = volume.Name, ""
				}
				if mode == "claim removed" || mode == "volume removed" {
					if err := kube.Tracker().Delete(schema.GroupVersionResource{Version: "v1", Resource: resource}, namespace, name); err != nil {
						t.Fatal(err)
					}
					return true, nil, apierrors.NewNotFound(schema.GroupResource{Resource: resource}, name)
				}
				return true, nil, apierrors.NewConflict(schema.GroupResource{Resource: resource}, name, errors.New("concurrent controller update"))
			})
			done, err := c.DeleteDatabase(context.Background(), d, func() error { return nil })
			if done || err != nil || attempts != 1 {
				t.Fatal("concurrent cleanup must remain queued", done, err, attempts)
			}
			if mode == "foreign after conflict" {
				changed := claim.DeepCopy()
				changed.OwnerReferences[0].UID = "foreign"
				if err := kube.Tracker().Update(schema.GroupVersionResource{Version: "v1", Resource: "persistentvolumeclaims"}, changed, ns.Name); err != nil {
					t.Fatal(err)
				}
			}
			done, err = c.DeleteDatabase(context.Background(), d, func() error { return nil })
			if done || mode == "foreign after conflict" && err == nil || mode != "foreign after conflict" && err != nil {
				t.Fatal("next attempt did not revalidate ownership", done, err)
			}
			if mode != "claim removed" {
				actual, err := kube.CoreV1().PersistentVolumeClaims(ns.Name).Get(context.Background(), claim.Name, metav1.GetOptions{})
				if err != nil || slices.Contains(actual.Finalizers, oracleFreeStorageFinalizer) != (mode == "foreign after conflict") {
					t.Fatal("unexpected protection after retry", err)
				}
			}
			if mode == "foreign after conflict" && attempts != 1 {
				t.Fatal("retry mutated a changed owner")
			}
		})
	}
}

func TestOracleFreeDeletionRelistsAfterSIDBDeleteRace(t *testing.T) {
	for _, missing := range []bool{false, true} {
		d, ns, root, _, _ := oracleFreeDeletingFixture(t)
		root.SetDeletionTimestamp(nil)
		kube := fake.NewClientset(append([]runtime.Object{ns}, oracleFreeTestControllerObjects(t, d, ns.UID)...)...)
		dynamic := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), root)
		c := &Client{kube: kube, dynamic: dynamic}
		calls := 0
		dynamic.PrependReactor("delete", "singleinstancedatabases", func(action kubetesting.Action) (bool, runtime.Object, error) {
			calls++
			if calls > 1 {
				return false, nil, nil
			}
			if missing {
				if err := dynamic.Tracker().Delete(oracleDatabaseResource, ns.Name, root.GetName()); err != nil {
					t.Fatal(err)
				}
				return true, nil, apierrors.NewNotFound(oracleDatabaseResource.GroupResource(), root.GetName())
			}
			return true, nil, apierrors.NewConflict(oracleDatabaseResource.GroupResource(), root.GetName(), errors.New("operator status update"))
		})
		for i := 0; i < 2; i++ {
			if done, err := c.DeleteDatabase(context.Background(), d, func() error { return nil }); done || err != nil {
				t.Fatal("SIDB delete race failed durable progress", done, err)
			}
		}
	}
}
