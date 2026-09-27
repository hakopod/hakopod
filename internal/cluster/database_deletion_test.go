package cluster

import (
	"context"
	"testing"

	"github.com/hakopod/hakopod/internal/database"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
)

func TestDatabaseDeletionReclaimsOnlyMatchingClaims(t *testing.T) {
	ctx := context.Background()
	d := database.Resource{ID: "deletion-fixture"}
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: DatabaseNamespace(d.ID), UID: "namespace-fixture", Labels: databaseLabels(d)}}
	claim := &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: "database-data", Namespace: ns.Name, UID: "claim-fixture"}, Spec: corev1.PersistentVolumeClaimSpec{VolumeName: "volume-fixture"}}
	volume := &corev1.PersistentVolume{ObjectMeta: metav1.ObjectMeta{Name: claim.Spec.VolumeName}, Spec: corev1.PersistentVolumeSpec{PersistentVolumeReclaimPolicy: corev1.PersistentVolumeReclaimRetain, ClaimRef: &corev1.ObjectReference{Namespace: ns.Name, Name: claim.Name, UID: claim.UID}}}
	for _, uid := range []types.UID{"other-claim", claim.UID} {
		t.Run(string(uid), func(t *testing.T) {
			pv := volume.DeepCopy()
			pv.Spec.ClaimRef.UID = uid
			kube := fake.NewClientset(ns.DeepCopy(), claim.DeepCopy(), pv)
			c := &Client{kube: kube}
			writes := 0
			done, err := c.DeleteDatabase(ctx, d, func() error { writes++; return nil })
			if uid != claim.UID {
				if err == nil || done || writes != 0 {
					t.Fatal("foreign claim changed", done, err, writes)
				}
				if _, err = kube.CoreV1().Namespaces().Get(ctx, ns.Name, metav1.GetOptions{}); err != nil {
					t.Fatal("namespace deleted after ownership failure")
				}
				return
			}
			if err != nil || done || writes != 2 {
				t.Fatal("delete did not reclaim first", done, err, writes)
			}
			current, err := kube.CoreV1().PersistentVolumes().Get(ctx, pv.Name, metav1.GetOptions{})
			if err != nil || current.Spec.PersistentVolumeReclaimPolicy != corev1.PersistentVolumeReclaimDelete {
				t.Fatal("retained volume not reclaimed", err)
			}
			if done, err = c.DeleteDatabase(ctx, d, func() error { return nil }); err != nil || done {
				t.Fatal("quota released while PV exists", done, err)
			}
			if err = kube.CoreV1().PersistentVolumes().Delete(ctx, pv.Name, metav1.DeleteOptions{}); err != nil {
				t.Fatal(err)
			}
			if done, err = c.DeleteDatabase(ctx, d, func() error { return nil }); err != nil || !done {
				t.Fatal("reclaimed deletion did not finish", done, err)
			}
		})
	}
}
