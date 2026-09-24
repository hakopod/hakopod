package cluster

import (
	"context"
	"fmt"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"time"
)

// DeleteVolumes removes only reviewed, unused claims. A missing PVC is not
// evidence of reclaimed storage: backing PVs must also disappear.
func (c *Client) DeleteVolumes(ctx context.Context, t Target, claims []string) error {
	if len(claims) == 0 || len(claims) > 20 {
		return fmt.Errorf("volume cleanup requires 1 to 20 claims")
	}
	wanted := map[string]bool{}
	for _, name := range claims {
		if name == "" || wanted[name] {
			return fmt.Errorf("invalid cleanup claims")
		}
		wanted[name] = true
	}
	for name, s := range t.Spec.Services {
		if s.Volume != nil && wanted[name+"-data"] {
			return fmt.Errorf("volume is still required by service %s", name)
		}
		for _, m := range s.Mounts {
			if wanted["hakopod-volume-"+m.Volume] {
				return fmt.Errorf("volume is still mounted by service %s", name)
			}
		}
	}
	for name := range t.Spec.Volumes {
		if wanted["hakopod-volume-"+name] {
			return fmt.Errorf("volume remains in the application specification")
		}
	}
	if err := beforeStep(ctx, t); err != nil {
		return err
	}
	namespace := Namespace(t.ApplicationID)
	ns, err := c.kube.CoreV1().Namespaces().Get(ctx, namespace, metav1.GetOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	if err == nil {
		if err = owned(ns, t); err != nil {
			return err
		}
	}
	pods, err := c.kube.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{Limit: 1001})
	if err != nil {
		return err
	}
	if len(pods.Items) > 1000 || pods.Continue != "" {
		return fmt.Errorf("pod inventory exceeds bounded volume cleanup")
	}
	for _, pod := range pods.Items {
		for _, v := range pod.Spec.Volumes {
			if v.PersistentVolumeClaim != nil && wanted[v.PersistentVolumeClaim.ClaimName] {
				return fmt.Errorf("waiting for pod %s to release its volume", pod.Name)
			}
		}
	}
	for _, name := range claims {
		if err = beforeStep(ctx, t); err != nil {
			return err
		}
		pvc, err := c.kube.CoreV1().PersistentVolumeClaims(namespace).Get(ctx, name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			continue
		}
		if err != nil {
			return err
		}
		if err = owned(pvc, t); err != nil {
			return err
		}
		if pvc.Spec.VolumeName == "" {
			return fmt.Errorf("waiting for volume %s to bind before verified reclamation", name)
		}
		pv, err := c.kube.CoreV1().PersistentVolumes().Get(ctx, pvc.Spec.VolumeName, metav1.GetOptions{})
		if err != nil && !apierrors.IsNotFound(err) {
			return err
		}
		if err == nil {
			ref := pv.Spec.ClaimRef
			if ref == nil || ref.Namespace != namespace || ref.Name != name || ref.UID != pvc.UID || pvc.UID == "" {
				return fmt.Errorf("volume claim ownership changed; refusing deletion")
			}
			if marker := pv.Labels[previewVolumeKey]; marker != "" && marker != ownerID(t.ApplicationID) {
				return fmt.Errorf("volume belongs to another cleanup operation")
			}
			if pv.Labels == nil {
				pv.Labels = map[string]string{}
			}
			if pv.Annotations == nil {
				pv.Annotations = map[string]string{}
			}
			pv.Labels[previewVolumeKey] = ownerID(t.ApplicationID)
			pv.Annotations[previewClaimKey] = string(pvc.UID)
			pv.Spec.PersistentVolumeReclaimPolicy = corev1.PersistentVolumeReclaimDelete
			if err = beforeStep(ctx, t); err != nil {
				return err
			}
			if _, err = c.kube.CoreV1().PersistentVolumes().Update(ctx, pv, metav1.UpdateOptions{}); err != nil {
				return err
			}
		}
		if err = beforeStep(ctx, t); err != nil {
			return err
		}
		if err = c.kube.CoreV1().PersistentVolumeClaims(namespace).Delete(ctx, name, deleteOptions(pvc)); err != nil && !apierrors.IsNotFound(err) {
			return err
		}
	}
	for {
		if err = beforeStep(ctx, t); err != nil {
			return err
		}
		pending := false
		for _, name := range claims {
			_, err = c.kube.CoreV1().PersistentVolumeClaims(namespace).Get(ctx, name, metav1.GetOptions{})
			if err == nil {
				pending = true
			} else if !apierrors.IsNotFound(err) {
				return err
			}
		}
		inventory, err := c.kube.CoreV1().PersistentVolumes().List(ctx, metav1.ListOptions{Limit: 1001})
		if err != nil {
			return err
		}
		if len(inventory.Items) > 1000 || inventory.Continue != "" {
			return fmt.Errorf("disk inventory exceeds bounded volume cleanup")
		}
		for _, pv := range inventory.Items {
			ref := pv.Spec.ClaimRef
			if ref == nil || ref.Namespace != namespace || !wanted[ref.Name] {
				continue
			}
			if pv.Labels[previewVolumeKey] != ownerID(t.ApplicationID) || string(ref.UID) != pv.Annotations[previewClaimKey] {
				return fmt.Errorf("retained disk has no verified cleanup ownership; operator review required")
			}
			pending = true
		}
		if !pending {
			return nil
		}
		if err = sleepContext(ctx, time.Second); err != nil {
			return fmt.Errorf("volume reclamation is still in progress: %w", err)
		}
	}
}
