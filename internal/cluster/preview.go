package cluster

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// DeletePreview reclaims owned application runtime after explicit consent.
// Preview expiry and retained-data cleanup both use this idempotent operation.
func (c *Client) DeletePreview(ctx context.Context, t Target) error {
	if err := beforeStep(ctx, t); err != nil {
		return err
	}
	api := c.kube.CoreV1().Namespaces()
	ns, err := api.Get(ctx, Namespace(t.ApplicationID), metav1.GetOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	if err == nil {
		if err = owned(ns, t); err != nil {
			return err
		}
		if err = c.preparePreviewVolumes(ctx, t); err != nil {
			return err
		}
		if ns.DeletionTimestamp == nil {
			if err = beforeStep(ctx, t); err != nil {
				return err
			}
			opts := deleteOptions(ns)
			opts.PropagationPolicy = ptr(metav1.DeletePropagationForeground)
			if err = api.Delete(ctx, ns.Name, opts); err != nil && !apierrors.IsNotFound(err) {
				return err
			}
		}
		for {
			if err = beforeStep(ctx, t); err != nil {
				return err
			}
			current, e := api.Get(ctx, ns.Name, metav1.GetOptions{})
			if apierrors.IsNotFound(e) {
				break
			}
			if e != nil {
				return e
			}
			if current.UID != ns.UID {
				return fmt.Errorf("preview namespace identity changed during deletion")
			}
			if err = sleepContext(ctx, time.Second); err != nil {
				return fmt.Errorf("preview namespace deletion is still in progress: %w", err)
			}
		}
	}
	// A namespace removed outside this operation can leave unmarked Retain
	// disks behind. Never release accounting without proving their reclamation.
	inventory, inventoryErr := c.kube.CoreV1().PersistentVolumes().List(ctx, metav1.ListOptions{Limit: 1001})
	if inventoryErr != nil {
		return inventoryErr
	}
	if len(inventory.Items) > 1000 || inventory.Continue != "" {
		return fmt.Errorf("disk inventory exceeds bounded cleanup; operator review required")
	}
	for _, volume := range inventory.Items {
		if ref := volume.Spec.ClaimRef; ref != nil && ref.Namespace == Namespace(t.ApplicationID) {
			if volume.Labels[previewVolumeKey] != ownerID(t.ApplicationID) || string(ref.UID) != volume.Annotations[previewClaimKey] {
				return fmt.Errorf("retained disk %s has no verified cleanup ownership; operator review required", volume.Name)
			}
		}
	}
	// A namespace may disappear before its storage provisioner finishes deleting
	// the physical disk. Marked PVs make that work observable across restarts.
	for {
		volumes, e := c.kube.CoreV1().PersistentVolumes().List(ctx, metav1.ListOptions{LabelSelector: previewVolumeKey + "=" + ownerID(t.ApplicationID), Limit: 21})
		if e != nil {
			return e
		}
		if len(volumes.Items) > 20 || volumes.Continue != "" {
			return fmt.Errorf("too many preview disks for bounded cleanup")
		}
		if len(volumes.Items) == 0 {
			break
		}
		for _, volume := range volumes.Items {
			ref := volume.Spec.ClaimRef
			if ref == nil || ref.Namespace != Namespace(t.ApplicationID) || string(ref.UID) != volume.Annotations[previewClaimKey] {
				return fmt.Errorf("preview disk ownership changed; administrator review required")
			}
		}
		if err = beforeStep(ctx, t); err != nil {
			return err
		}
		if err = sleepContext(ctx, time.Second); err != nil {
			return fmt.Errorf("preview disk reclaim is still in progress: %w", err)
		}
	}
	secrets, err := c.ListWorkloadSecrets(ctx, t.Project, t.Environment, t.Spec.Name)
	if err != nil {
		return err
	}
	for _, secret := range secrets {
		if err = beforeStep(ctx, t); err != nil {
			return err
		}
		if err = c.DeleteWorkloadSecret(ctx, t.Project, t.Environment, t.Spec.Name, secret.Name); err != nil {
			return err
		}
	}
	return nil
}

const previewVolumeKey = "hakopod.io/preview-cleanup"
const previewClaimKey = "hakopod.io/preview-claim-uid"

func (c *Client) preparePreviewVolumes(ctx context.Context, t Target) error {
	claims, err := c.kube.CoreV1().PersistentVolumeClaims(Namespace(t.ApplicationID)).List(ctx, metav1.ListOptions{Limit: 21})
	if err != nil {
		return err
	}
	if len(claims.Items) > 20 || claims.Continue != "" {
		return fmt.Errorf("too many preview claims for bounded cleanup")
	}
	for _, claim := range claims.Items {
		if err = owned(&claim, t); err != nil {
			return err
		}
	}
	for _, claim := range claims.Items {
		if claim.Spec.VolumeName == "" {
			// Retained provisioning may still be in flight. Never report data
			// cleanup complete before its backing volume can be identified.
			if claim.Spec.StorageClassName != nil && *claim.Spec.StorageClassName != "" {
				class, e := c.kube.StorageV1().StorageClasses().Get(ctx, *claim.Spec.StorageClassName, metav1.GetOptions{})
				if e != nil {
					return e
				}
				if class.ReclaimPolicy == nil || *class.ReclaimPolicy == corev1.PersistentVolumeReclaimDelete {
					continue
				}
			}
			return fmt.Errorf("preview storage %s is not bound; waiting to identify its retained disk before cleanup", claim.Name)
		}
		api := c.kube.CoreV1().PersistentVolumes()
		volume, e := api.Get(ctx, claim.Spec.VolumeName, metav1.GetOptions{})
		if apierrors.IsNotFound(e) {
			continue
		}
		if e != nil {
			return e
		}
		ref := volume.Spec.ClaimRef
		if ref == nil || ref.Namespace != claim.Namespace || ref.Name != claim.Name || ref.UID != claim.UID || claim.UID == "" {
			return fmt.Errorf("preview disk does not belong to its claim; refusing cleanup")
		}
		if marker := volume.Labels[previewVolumeKey]; marker != "" && marker != ownerID(t.ApplicationID) {
			return fmt.Errorf("preview disk belongs to another cleanup operation")
		}
		if volume.Labels == nil {
			volume.Labels = map[string]string{}
		}
		if volume.Annotations == nil {
			volume.Annotations = map[string]string{}
		}
		volume.Labels[previewVolumeKey] = ownerID(t.ApplicationID)
		volume.Annotations[previewClaimKey] = string(claim.UID)
		volume.Spec.PersistentVolumeReclaimPolicy = corev1.PersistentVolumeReclaimDelete
		if err = beforeStep(ctx, t); err != nil {
			return err
		}
		// Update uses resourceVersion so a concurrent rebinding cannot be lost.
		if _, e = api.Update(ctx, volume, metav1.UpdateOptions{}); e != nil {
			return e
		}
	}
	return nil
}
