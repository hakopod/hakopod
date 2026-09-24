package cluster

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const resizeQuotaKey = "hakopod.io/resize-quota"

type resizeQuotaJournal struct {
	Operation string `json:"operation"`
	Storage   string `json:"storage"`
	Claims    string `json:"claims"`
	ExtraGiB  int64  `json:"extra_gib"`
}

// The engine's durable admission has already reserved both copies. Temporarily
// permit exactly one staging PVC in Kubernetes, without increasing compute or
// weakening any other workspace resource budget. The annotation survives an
// engine restart and prevents retries from accumulating extra allowance.
func (c *Client) resizeQuota(ctx context.Context, t Target, extraGiB int64) error {
	if extraGiB < 1 || extraGiB > 200 {
		return fmt.Errorf("invalid migration capacity")
	}
	api := c.kube.CoreV1().ResourceQuotas(Namespace(t.ApplicationID))
	q, err := api.Get(ctx, "hakopod-budget", metav1.GetOptions{})
	if err != nil {
		return err
	}
	if err = owned(q, t); err != nil {
		return err
	}
	var j resizeQuotaJournal
	if raw := q.Annotations[resizeQuotaKey]; raw != "" {
		if json.Unmarshal([]byte(raw), &j) != nil || j.Operation != t.OperationID || j.ExtraGiB != extraGiB {
			return fmt.Errorf("another migration owns the storage budget")
		}
		return nil
	}
	storage, ok := q.Spec.Hard[corev1.ResourceRequestsStorage]
	if !ok {
		return fmt.Errorf("storage budget is missing")
	}
	claims, ok := q.Spec.Hard[corev1.ResourcePersistentVolumeClaims]
	if !ok {
		return fmt.Errorf("volume budget is missing")
	}
	j = resizeQuotaJournal{Operation: t.OperationID, Storage: storage.String(), Claims: claims.String(), ExtraGiB: extraGiB}
	storage.Add(resource.MustParse(strconv.FormatInt(extraGiB, 10) + "Gi"))
	claims.Add(resource.MustParse("1"))
	q.Spec.Hard[corev1.ResourceRequestsStorage] = storage
	q.Spec.Hard[corev1.ResourcePersistentVolumeClaims] = claims
	if q.Annotations == nil {
		q.Annotations = map[string]string{}
	}
	raw, _ := json.Marshal(j)
	q.Annotations[resizeQuotaKey] = string(raw)
	if err = beforeStep(ctx, t); err != nil {
		return err
	}
	_, err = api.Update(ctx, q, metav1.UpdateOptions{})
	return err
}

func (c *Client) restoreResizeQuota(ctx context.Context, t Target) error {
	api := c.kube.CoreV1().ResourceQuotas(Namespace(t.ApplicationID))
	q, err := api.Get(ctx, "hakopod-budget", metav1.GetOptions{})
	if err != nil {
		return err
	}
	if err = owned(q, t); err != nil {
		return err
	}
	raw := q.Annotations[resizeQuotaKey]
	if raw == "" {
		return nil
	}
	var j resizeQuotaJournal
	if json.Unmarshal([]byte(raw), &j) != nil || j.Operation != t.OperationID {
		return fmt.Errorf("migration storage budget ownership changed")
	}
	storage, err := resource.ParseQuantity(j.Storage)
	if err != nil {
		return err
	}
	claims, err := resource.ParseQuantity(j.Claims)
	if err != nil {
		return err
	}
	q.Spec.Hard[corev1.ResourceRequestsStorage] = storage
	q.Spec.Hard[corev1.ResourcePersistentVolumeClaims] = claims
	delete(q.Annotations, resizeQuotaKey)
	if err = beforeStep(ctx, t); err != nil {
		return err
	}
	_, err = api.Update(ctx, q, metav1.UpdateOptions{})
	return err
}
