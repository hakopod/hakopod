package cluster

import (
	"context"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
	"testing"
)

func TestVolumeCleanupRejectsMountedAndForeignDisks(t *testing.T) {
	for _, scenario := range []string{"mounted", "foreign-pvc", "foreign-pv", "missing-pvc"} {
		t.Run(scenario, func(t *testing.T) {
			target := Target{ApplicationID: "owned-volume-app"}
			ns := Namespace(target.ApplicationID)
			pvc := &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: "db-data", Namespace: ns, UID: "claim", Labels: labelsFor(target, "db")}, Spec: corev1.PersistentVolumeClaimSpec{VolumeName: "disk"}}
			pv := &corev1.PersistentVolume{ObjectMeta: metav1.ObjectMeta{Name: "disk"}, Spec: corev1.PersistentVolumeSpec{PersistentVolumeReclaimPolicy: corev1.PersistentVolumeReclaimRetain, ClaimRef: &corev1.ObjectReference{Namespace: ns, Name: "db-data", UID: pvc.UID}}}
			if scenario == "foreign-pvc" {
				pvc.Labels[ownerKey] = "other"
			}
			if scenario == "foreign-pv" {
				pv.Spec.ClaimRef.UID = "other"
			}
			kube := fake.NewSimpleClientset(pv)
			if scenario != "missing-pvc" {
				_, _ = kube.CoreV1().PersistentVolumeClaims(ns).Create(context.Background(), pvc, metav1.CreateOptions{})
			}
			if scenario == "mounted" {
				_, _ = kube.CoreV1().Pods(ns).Create(context.Background(), &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "consumer"}, Spec: corev1.PodSpec{Volumes: []corev1.Volume{{Name: "data", VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: "db-data"}}}}}}, metav1.CreateOptions{})
			}
			c := &Client{kube: kube}
			if err := c.DeleteVolumes(context.Background(), target, []string{"db-data"}); err == nil {
				t.Fatal("unsafe cleanup accepted")
			}
			current, err := kube.CoreV1().PersistentVolumes().Get(context.Background(), "disk", metav1.GetOptions{})
			if err != nil || current.Spec.PersistentVolumeReclaimPolicy != corev1.PersistentVolumeReclaimRetain {
				t.Fatal("foreign or busy disk changed", err)
			}
		})
	}
}
