package cluster

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/hakopod/hakopod/internal/spec"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/retry"
)

const sessionTemplateLabel = "hakopod.io/session-template"

func sessionTemplateName(service string) string {
	h := sha256.Sum256([]byte(service))
	return fmt.Sprintf("session-template-%x", h[:12])
}
func sessionTemplateData(t Target, s spec.Service) map[string]string {
	encoded, _ := json.Marshal(s)
	hash := sha256.Sum256(encoded)
	return map[string]string{"revision": strconv.FormatInt(t.Revision, 10), "service_sha256": fmt.Sprintf("%x", hash), "suspended": strconv.FormatBool(s.Suspended)}
}

// The marker records a deployed template, without copying environment or secrets.
func (c *Client) applySessionTemplate(ctx context.Context, t Target, name string, s spec.Service) error {
	if err := beforeStep(ctx, t); err != nil {
		return err
	}
	labels := labelsFor(t, name)
	labels[sessionTemplateLabel] = "true"
	wanted := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: sessionTemplateName(name), Namespace: Namespace(t.ApplicationID), Labels: labels}, Data: sessionTemplateData(t, s)}
	api := c.kube.CoreV1().ConfigMaps(wanted.Namespace)
	return retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		if err := beforeStep(ctx, t); err != nil {
			return err
		}
		current, err := api.Get(ctx, wanted.Name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			_, err = api.Create(ctx, wanted, metav1.CreateOptions{})
			return err
		}
		if err != nil {
			return err
		}
		if err = owned(current, t); err != nil {
			return err
		}
		if current.Labels[serviceKey] != name || current.Labels[sessionTemplateLabel] != "true" {
			return fmt.Errorf("session template ownership is invalid")
		}
		current.Data = wanted.Data
		_, err = api.Update(ctx, current, metav1.UpdateOptions{})
		return err
	})
}

func (c *Client) observeSessionTemplate(ctx context.Context, t Target, name string, s spec.Service) (ServiceStatus, error) {
	result := ServiceStatus{Name: name, Status: "missing", Image: s.Image}
	cm, err := c.kube.CoreV1().ConfigMaps(Namespace(t.ApplicationID)).Get(ctx, sessionTemplateName(name), metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return result, nil
	}
	if err != nil {
		return result, err
	}
	if err = owned(cm, t); err != nil {
		return result, err
	}
	if cm.Labels[serviceKey] != name || cm.Labels[sessionTemplateLabel] != "true" {
		return result, fmt.Errorf("session template ownership is invalid")
	}
	wanted := sessionTemplateData(t, s)
	for key, value := range wanted {
		if cm.Data[key] != value {
			result.Status = "pending"
			return result, nil
		}
	}
	result.Status = "configured"
	result.Message = "Session template configured. No worker starts until an authorized request arrives"
	if s.Suspended {
		result.Status = "stopped"
		result.Message = "Session template paused"
	}
	return result, nil
}

func (c *Client) cleanupSessionTemplates(ctx context.Context, t Target) error {
	api := c.kube.CoreV1().ConfigMaps(Namespace(t.ApplicationID))
	list, err := api.List(ctx, metav1.ListOptions{LabelSelector: managedBy + "=hakopod," + ownerKey + "=" + ownerID(t.ApplicationID) + "," + sessionTemplateLabel + "=true", Limit: 21})
	if err != nil {
		return err
	}
	if list.Continue != "" || len(list.Items) > 20 {
		return fmt.Errorf("too many session templates for bounded cleanup")
	}
	for _, cm := range list.Items {
		s, ok := t.Spec.Services[cm.Labels[serviceKey]]
		if ok && s.Session != nil {
			continue
		}
		if err = owned(&cm, t); err != nil {
			return err
		}
		if err = beforeStep(ctx, t); err != nil {
			return err
		}
		if err = api.Delete(ctx, cm.Name, deleteOptions(&cm)); err != nil && !apierrors.IsNotFound(err) {
			return err
		}
	}
	return nil
}
