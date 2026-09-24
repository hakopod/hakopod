package cluster

import (
	"context"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
	"testing"
)

func TestPreviewVolumeReclaimRequiresExactClaimOwnership(t *testing.T) {
	ctx := context.Background()
	target := Target{ApplicationID: "preview-owner"}
	for _, scenario := range []string{"owned", "foreign-claim", "foreign-pv", "changed-cleanup"} {
		t.Run(scenario, func(t *testing.T) {
			labels := labelsFor(target, "web")
			if scenario == "foreign-claim" {
				labels[ownerKey] = "other"
			}
			claim := &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: "web-data", Namespace: Namespace(target.ApplicationID), UID: "claim-uid", Labels: labels}, Spec: corev1.PersistentVolumeClaimSpec{VolumeName: "disk"}}
			volume := &corev1.PersistentVolume{ObjectMeta: metav1.ObjectMeta{Name: "disk", UID: "disk-uid"}, Spec: corev1.PersistentVolumeSpec{PersistentVolumeReclaimPolicy: corev1.PersistentVolumeReclaimRetain, ClaimRef: &corev1.ObjectReference{Namespace: claim.Namespace, Name: claim.Name, UID: claim.UID}}}
			if scenario == "foreign-pv" {
				volume.Spec.ClaimRef.UID = "other-claim"
			}
			if scenario == "changed-cleanup" {
				volume.Labels = map[string]string{previewVolumeKey: "another-preview"}
			}
			kube := fake.NewSimpleClientset(claim, volume)
			c := &Client{kube: kube}
			err := c.preparePreviewVolumes(ctx, target)
			actual, _ := kube.CoreV1().PersistentVolumes().Get(ctx, "disk", metav1.GetOptions{})
			if scenario == "owned" {
				if err != nil || actual.Spec.PersistentVolumeReclaimPolicy != corev1.PersistentVolumeReclaimDelete || actual.Annotations[previewClaimKey] != "claim-uid" {
					t.Fatal("owned preview disk was not prepared", err, actual)
				}
			} else if err == nil || actual.Spec.PersistentVolumeReclaimPolicy != corev1.PersistentVolumeReclaimRetain {
				t.Fatal("unowned disk reclaim changed", err)
			}
		})
	}
}

func TestDeletedNamespaceWithUnmarkedDiskCannotReportReclaimed(t *testing.T) {
	target := Target{ApplicationID: "retained-app"}
	volume := &corev1.PersistentVolume{ObjectMeta: metav1.ObjectMeta{Name: "orphan-disk"}, Spec: corev1.PersistentVolumeSpec{PersistentVolumeReclaimPolicy: corev1.PersistentVolumeReclaimRetain, ClaimRef: &corev1.ObjectReference{Namespace: Namespace(target.ApplicationID), Name: "db-data", UID: "original-claim"}}}
	client := &Client{kube: fake.NewSimpleClientset(volume)}
	if err := client.DeletePreview(context.Background(), target); err == nil {
		t.Fatal("unmarked orphan disk falsely reported reclaimed")
	}
	current, err := client.kube.CoreV1().PersistentVolumes().Get(context.Background(), volume.Name, metav1.GetOptions{})
	if err != nil || current.Spec.PersistentVolumeReclaimPolicy != corev1.PersistentVolumeReclaimRetain {
		t.Fatal("unknown disk was mutated", err)
	}
}
