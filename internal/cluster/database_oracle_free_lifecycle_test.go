package cluster

import (
	"context"
	"errors"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
)

func TestOracleFreeReclaimsOnlyReviewedOwnedVolumes(t *testing.T) {
	for _, mode := range []string{"owned", "foreign claim", "foreign volume", "extra claim", "fenced"} {
		t.Run(mode, func(t *testing.T) {
			d := oracleFixture()
			root, err := oracleFreeObject(d, nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			root.SetUID("root-uid")
			objects := []runtime.Object{}
			for i, name := range []string{"database", "database-additional-0"} {
				claim := &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: DatabaseNamespace(d.ID), UID: types.UID("claim-" + name), ResourceVersion: "10", Finalizers: []string{oracleFreeStorageFinalizer}, OwnerReferences: []metav1.OwnerReference{{APIVersion: "database.oracle.com/v4", Kind: "SingleInstanceDatabase", Name: "database", UID: root.GetUID(), Controller: ptr(true), BlockOwnerDeletion: ptr(true)}}}, Spec: corev1.PersistentVolumeClaimSpec{VolumeName: "pv-" + name}}
				volume := &corev1.PersistentVolume{ObjectMeta: metav1.ObjectMeta{Name: claim.Spec.VolumeName, UID: types.UID("volume-" + name), ResourceVersion: "11"}, Spec: corev1.PersistentVolumeSpec{PersistentVolumeReclaimPolicy: corev1.PersistentVolumeReclaimRetain, ClaimRef: &corev1.ObjectReference{APIVersion: "v1", Kind: "PersistentVolumeClaim", Namespace: claim.Namespace, Name: claim.Name, UID: claim.UID}}}
				if i == 1 && mode == "foreign claim" {
					claim.OwnerReferences[0].UID = "foreign"
				}
				if i == 1 && mode == "foreign volume" {
					volume.Spec.ClaimRef.UID = "foreign"
				}
				objects = append(objects, claim, volume)
			}
			if mode == "extra claim" {
				objects = append(objects, &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: "unrelated", Namespace: DatabaseNamespace(d.ID)}})
			}
			kube := fake.NewClientset(objects...)
			c := &Client{kube: kube}
			err = c.oracleFreePrepareDeletionClaims(context.Background(), d, root, func() error {
				if mode == "fenced" {
					return errors.New("lease changed")
				}
				return nil
			})
			if mode == "owned" && err != nil || mode != "owned" && err == nil {
				t.Fatal("unexpected reclaim result", err)
			}
			updates := 0
			for _, action := range kube.Actions() {
				if action.GetVerb() == "update" {
					updates++
				}
			}
			if mode == "owned" && updates != 2 || mode != "owned" && updates != 0 {
				t.Fatal("foreign or unfenced reclaim mutation", updates)
			}
			for _, name := range []string{"database", "database-additional-0"} {
				volume, err := kube.CoreV1().PersistentVolumes().Get(context.Background(), "pv-"+name, metav1.GetOptions{})
				if err != nil {
					t.Fatal(err)
				}
				expected := corev1.PersistentVolumeReclaimRetain
				if mode == "owned" {
					expected = corev1.PersistentVolumeReclaimDelete
				}
				if volume.Spec.PersistentVolumeReclaimPolicy != expected || volume.ResourceVersion != "11" {
					t.Fatal("reclaim changed an unreviewed version or policy")
				}
			}
		})
	}
}

func TestOracleFreeDeletionDoesNotLoseItsFinalizerController(t *testing.T) {
	for _, mode := range []string{"missing controller", "unexpected finalizer", "orphan claim"} {
		t.Run(mode, func(t *testing.T) {
			d := oracleFixture()
			ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: DatabaseNamespace(d.ID), UID: "namespace-uid", Labels: databaseLabels(d)}}
			root, err := oracleFreeObject(d, nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			root.SetUID("root-uid")
			root.SetResourceVersion("7")
			root.SetOwnerReferences([]metav1.OwnerReference{{APIVersion: "v1", Kind: "Namespace", Name: ns.Name, UID: ns.UID}})
			root.SetFinalizers([]string{oracleFreeFinalizer})
			stamp := metav1.Now()
			root.SetDeletionTimestamp(&stamp)
			if mode == "unexpected finalizer" {
				root.SetFinalizers([]string{"foreign.example/finalizer"})
			}
			kube := fake.NewClientset(ns)
			dynamic := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), root)
			if mode == "orphan claim" {
				dynamic = dynamicfake.NewSimpleDynamicClient(runtime.NewScheme())
				kube = fake.NewClientset(ns, &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: "database", Namespace: ns.Name}})
			}
			c := &Client{kube: kube, dynamic: dynamic}
			removed, err := c.DeleteDatabase(context.Background(), d, func() error { return nil })
			if removed || err == nil {
				t.Fatal("unsafe finalizer cleanup accepted", removed, err)
			}
			for _, action := range append(kube.Actions(), dynamic.Actions()...) {
				if action.GetVerb() == "delete" || action.GetVerb() == "update" || action.GetVerb() == "patch" {
					t.Fatal("deleted or rewrote data without its controller")
				}
			}
		})
	}
}

func TestOracleFreeDeletionRetainsLateBindingClaimsUntilReclaimed(t *testing.T) {
	for _, mode := range []string{"still provisioning", "late binding", "never provisioned", "fenced"} {
		t.Run(mode, func(t *testing.T) {
			d := oracleFixture()
			root, err := oracleFreeObject(d, nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			root.SetUID("root-uid")
			stamp := metav1.Now()
			root.SetDeletionTimestamp(&stamp)
			claim := &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{
				Name: "database", Namespace: DatabaseNamespace(d.ID), UID: "claim-uid", ResourceVersion: "12",
				Finalizers:      []string{oracleFreeStorageFinalizer, "kubernetes.io/pvc-protection"},
				OwnerReferences: []metav1.OwnerReference{{APIVersion: "database.oracle.com/v4", Kind: "SingleInstanceDatabase", Name: "database", UID: root.GetUID(), Controller: ptr(true), BlockOwnerDeletion: ptr(true)}},
			}}
			if mode != "still provisioning" {
				claim.DeletionTimestamp = &stamp
			}
			objects := []runtime.Object{claim}
			if mode != "never provisioned" {
				objects = append(objects, &corev1.PersistentVolume{ObjectMeta: metav1.ObjectMeta{Name: "late-volume", UID: "volume-uid", ResourceVersion: "13"}, Spec: corev1.PersistentVolumeSpec{
					PersistentVolumeReclaimPolicy: corev1.PersistentVolumeReclaimRetain,
					ClaimRef:                      &corev1.ObjectReference{APIVersion: "v1", Kind: "PersistentVolumeClaim", Namespace: claim.Namespace, Name: claim.Name, UID: claim.UID},
				}})
			}
			kube := fake.NewClientset(objects...)
			c := &Client{kube: kube}
			err = c.oracleFreePrepareDeletionClaims(context.Background(), d, root, func() error {
				if mode == "fenced" {
					return errors.New("lease changed")
				}
				return nil
			})
			if mode == "fenced" && err == nil || mode != "fenced" && err != nil {
				t.Fatal(err)
			}
			actual, err := kube.CoreV1().PersistentVolumeClaims(claim.Namespace).Get(context.Background(), claim.Name, metav1.GetOptions{})
			if err != nil {
				t.Fatal(err)
			}
			shouldRelease := mode == "late binding" || mode == "never provisioned"
			if shouldRelease && (len(actual.Finalizers) != 1 || actual.Finalizers[0] != "kubernetes.io/pvc-protection") || !shouldRelease && len(actual.Finalizers) != 2 {
				t.Fatal("claim protection released before deletion and reclaim")
			}
			if actual.ResourceVersion != "12" {
				t.Fatal("claim update lost its version fence")
			}
			if mode != "never provisioned" {
				volume, err := kube.CoreV1().PersistentVolumes().Get(context.Background(), "late-volume", metav1.GetOptions{})
				if err != nil {
					t.Fatal(err)
				}
				expected := corev1.PersistentVolumeReclaimDelete
				if mode == "fenced" {
					expected = corev1.PersistentVolumeReclaimRetain
				}
				if volume.Spec.PersistentVolumeReclaimPolicy != expected {
					t.Fatal("late-bound volume was not reclaimed safely")
				}
			}
		})
	}
}
