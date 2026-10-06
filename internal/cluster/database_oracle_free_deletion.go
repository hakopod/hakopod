package cluster

import (
	"context"
	"fmt"
	"slices"
	"strconv"

	"github.com/hakopod/hakopod/internal/database"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

const oracleFreeFinalizer = "database.oracle.com/singleinstancedatabasefinalizer"
const oracleFreeStorageFinalizer = "hakopod.io/oracle-free-storage"

// The scoped operator must survive until SIDB finishes its own finalizer.
func (c *Client) deleteOracleFreeController(ctx context.Context, d database.Resource, before func() error) (bool, error) {
	if c.dynamic == nil {
		return false, fmt.Errorf("Oracle Free deletion controller is unavailable")
	}
	api := c.dynamic.Resource(oracleDatabaseResource).Namespace(DatabaseNamespace(d.ID))
	object, err := api.Get(ctx, "database", metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		claims, err := c.kube.CoreV1().PersistentVolumeClaims(DatabaseNamespace(d.ID)).List(ctx, metav1.ListOptions{Limit: 3})
		if err != nil {
			return false, err
		}
		if claims.Continue != "" || len(claims.Items) > 2 {
			return false, fmt.Errorf("Oracle Free deletion claim inventory changed")
		}
		for _, claim := range claims.Items {
			if claim.DeletionTimestamp == nil {
				return false, fmt.Errorf("Oracle Free has an unexpected claim after controller deletion")
			}
		}
		return len(claims.Items) == 0, nil
	}
	if err != nil {
		return false, err
	}
	revision, err := strconv.ParseInt(object.GetAnnotations()["hakopod.io/database-revision"], 10, 64)
	if err != nil || revision < 1 {
		return false, fmt.Errorf("Oracle Free deletion revision is invalid")
	}
	owned := d
	owned.Revision = revision
	candidate := object.DeepCopy()
	candidate.SetDeletionTimestamp(nil)
	if err = c.oracleEnterpriseObjectOwned(ctx, owned, candidate); err != nil {
		return false, err
	}
	for _, finalizer := range object.GetFinalizers() {
		if finalizer != oracleFreeFinalizer && finalizer != metav1.FinalizerDeleteDependents {
			return false, fmt.Errorf("Oracle Free has an unexpected finalizer requiring inspection")
		}
	}
	// Recreate a missing scoped controller and wait for its exact current pod.
	// This also runs while SIDB is terminating, so a lost controller cannot
	// silently strand the database's finalizer.
	if err = c.oracleFreeControllerReady(ctx, owned); err != nil {
		if err = c.prepareOracleFreeController(ctx, owned, before); err != nil {
			return false, err
		}
		if err = c.oracleFreeControllerReady(ctx, owned); err != nil {
			return false, err
		}
	}
	if err = c.oracleFreePrepareDeletionClaims(ctx, owned, object, before); err != nil {
		return false, err
	}
	if object.GetDeletionTimestamp() != nil {
		return false, nil
	}
	if err = before(); err != nil {
		return false, err
	}
	uid, version := object.GetUID(), object.GetResourceVersion()
	foreground := metav1.DeletePropagationForeground
	err = api.Delete(ctx, object.GetName(), metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &uid, ResourceVersion: &version}, PropagationPolicy: &foreground})
	return false, err
}

// Capture only the two SIDB-owned claims. Never pass this namespace through a
// generic reclaim-policy loop that could pick up another resource after review.
func (c *Client) oracleFreePrepareDeletionClaims(ctx context.Context, d database.Resource, root *unstructured.Unstructured, before func() error) error {
	claims, err := c.kube.CoreV1().PersistentVolumeClaims(DatabaseNamespace(d.ID)).List(ctx, metav1.ListOptions{Limit: 3})
	if err != nil {
		return err
	}
	if claims.Continue != "" || len(claims.Items) > 2 {
		return fmt.Errorf("Oracle Free deletion claim inventory exceeds its bound")
	}
	volumes := make([]*corev1.PersistentVolume, 0, 2)
	for _, claim := range claims.Items {
		owner := metav1.GetControllerOf(&claim)
		if claim.UID == "" || claim.ResourceVersion == "" || (claim.Name != "database" && claim.Name != "database-additional-0") || len(claim.OwnerReferences) != 1 || owner == nil || owner.APIVersion != "database.oracle.com/v4" || owner.Kind != "SingleInstanceDatabase" || owner.Name != "database" || owner.UID != root.GetUID() || owner.BlockOwnerDeletion == nil || !*owner.BlockOwnerDeletion {
			return fmt.Errorf("Oracle Free deletion will not reclaim a foreign claim")
		}
		if !slices.Contains(claim.Finalizers, oracleFreeStorageFinalizer) && claim.DeletionTimestamp == nil {
			return fmt.Errorf("Oracle Free storage finalizer is missing")
		}
		if claim.Spec.VolumeName == "" {
			continue
		}
		volume, err := c.kube.CoreV1().PersistentVolumes().Get(ctx, claim.Spec.VolumeName, metav1.GetOptions{})
		if err != nil {
			return err
		}
		ref := volume.Spec.ClaimRef
		if volume.UID == "" || volume.ResourceVersion == "" || volume.DeletionTimestamp != nil || ref == nil || ref.APIVersion != "v1" || ref.Kind != "PersistentVolumeClaim" || ref.Namespace != claim.Namespace || ref.Name != claim.Name || ref.UID != claim.UID {
			return fmt.Errorf("Oracle Free deletion backing volume identity changed")
		}
		volumes = append(volumes, volume)
	}
	// An unbound claim may already have a provisioned PV before the binder
	// records VolumeName. Keep its finalizer and inspect the exact ClaimRef UID
	// before releasing a deleting claim. The API rejects a stale PVC version.
	if root.GetDeletionTimestamp() != nil {
		inventory, err := c.kube.CoreV1().PersistentVolumes().List(ctx, metav1.ListOptions{Limit: 1025})
		if err != nil {
			return err
		}
		if inventory.Continue != "" || len(inventory.Items) > 1024 {
			return fmt.Errorf("Oracle Free deletion volume inventory exceeds its bound")
		}
		for i := range inventory.Items {
			volume := &inventory.Items[i]
			ref := volume.Spec.ClaimRef
			if ref == nil || ref.Namespace != DatabaseNamespace(d.ID) {
				continue
			}
			for _, claim := range claims.Items {
				if ref.Name != claim.Name || ref.UID != claim.UID {
					continue
				}
				if ref.APIVersion != "v1" || ref.Kind != "PersistentVolumeClaim" || volume.UID == "" || volume.ResourceVersion == "" {
					return fmt.Errorf("Oracle Free deletion backing volume identity changed")
				}
				if volume.DeletionTimestamp != nil {
					continue
				}
				found := false
				for _, known := range volumes {
					if known.UID == volume.UID {
						found = true
					}
				}
				if !found {
					volumes = append(volumes, volume)
				}
			}
		}
	}
	for _, volume := range volumes {
		if volume.Spec.PersistentVolumeReclaimPolicy == corev1.PersistentVolumeReclaimDelete {
			continue
		}
		volume.Spec.PersistentVolumeReclaimPolicy = corev1.PersistentVolumeReclaimDelete
		if err = before(); err != nil {
			return err
		}
		if _, err = c.kube.CoreV1().PersistentVolumes().Update(ctx, volume, metav1.UpdateOptions{}); err != nil {
			return err
		}
	}
	if root.GetDeletionTimestamp() != nil {
		for i := range claims.Items {
			claim := &claims.Items[i]
			if claim.DeletionTimestamp == nil || !slices.Contains(claim.Finalizers, oracleFreeStorageFinalizer) {
				continue
			}
			claim.Finalizers = slices.DeleteFunc(claim.Finalizers, func(value string) bool { return value == oracleFreeStorageFinalizer })
			if err = before(); err != nil {
				return err
			}
			if _, err = c.kube.CoreV1().PersistentVolumeClaims(claim.Namespace).Update(ctx, claim, metav1.UpdateOptions{}); err != nil {
				return err
			}
		}
	}
	return nil
}
