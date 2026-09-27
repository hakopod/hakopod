package cluster

import (
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestManagedDatabaseRetainedVolumeDeletionLive(t *testing.T) {
	c, ctx := liveRecoveryClient(t)
	d, _ := newRecoveryFixture(t, ctx, c, "redis", "8")
	claims, err := c.kube.CoreV1().PersistentVolumeClaims(DatabaseNamespace(d.ID)).List(ctx, metav1.ListOptions{Limit: 2})
	if err != nil || len(claims.Items) != 1 {
		t.Fatal("standalone fixture claim unavailable", err)
	}
	claim := claims.Items[0]
	pv, err := c.kube.CoreV1().PersistentVolumes().Get(ctx, claim.Spec.VolumeName, metav1.GetOptions{})
	if err != nil || pv.Spec.ClaimRef == nil || pv.Spec.ClaimRef.UID != claim.UID {
		t.Fatal("fixture volume identity unavailable", err)
	}
	pv.Spec.PersistentVolumeReclaimPolicy = corev1.PersistentVolumeReclaimRetain
	if _, err = c.kube.CoreV1().PersistentVolumes().Update(ctx, pv, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	t.Log("verified retained fixture volume before confirmed deletion")
	for ctx.Err() == nil {
		done, err := c.DeleteDatabase(ctx, d, func() error { return nil })
		if err != nil {
			t.Fatal(err)
		}
		if done {
			if _, err = c.kube.CoreV1().PersistentVolumes().Get(ctx, pv.Name, metav1.GetOptions{}); !apierrors.IsNotFound(err) {
				t.Fatal("deletion completed before exact PV disappeared", err)
			}
			t.Log("confirmed deletion reclaimed the retained volume")
			return
		}
		if err = sleepContext(ctx, 2*time.Second); err != nil {
			break
		}
	}
	t.Fatal("retained database deletion did not complete")
}
