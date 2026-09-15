package cluster

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/retry"
)

const idleHTTPAnnotation = "hakopod.io/http-idle"

var ErrIdleIneligible = errors.New("service is not eligible for automatic HTTP sleep")

// IdleHTTPHosts exposes only routes whose trusted runtime policy permits sleep.
// Application TOML cannot enable this behavior or nominate a different target.
func (c *Client) IdleHTTPHosts(ctx context.Context, t Target, service string) ([]string, error) {
	p, err := c.workloadPolicy(ctx, t)
	if err != nil {
		return nil, err
	}
	svc, ok := t.Spec.Services[service]
	if p == nil || !p.IdleHTTP || !ok || !svc.Public || svc.Port < 1 || svc.Job != nil || svc.Autoscaling != nil || svc.Replicas != 1 || svc.Suspended || len(t.Spec.Services) != 1 || len(t.Spec.Volumes) > 0 || svc.Volume != nil || len(svc.Mounts) > 0 || len(svc.PublicTCP) > 0 {
		return nil, ErrIdleIneligible
	}
	return c.serviceHostnames(ctx, t, service)
}
func idleDeployment(d *appsv1.Deployment) bool {
	return d.Spec.Replicas != nil && *d.Spec.Replicas == 0 && d.Annotations[idleHTTPAnnotation] == "sleeping"
}
func (c *Client) idleDeployment(ctx context.Context, t Target, service string) (*appsv1.Deployment, error) {
	if _, err := c.IdleHTTPHosts(ctx, t, service); err != nil {
		return nil, err
	}
	d, err := c.kube.AppsV1().Deployments(Namespace(t.ApplicationID)).Get(ctx, service, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	if err = owned(d, t); err != nil {
		return nil, err
	}
	if d.Annotations["hakopod.io/revision"] != strconv.FormatInt(t.Revision, 10) || d.Spec.Replicas == nil || *d.Spec.Replicas > 1 || d.DeletionTimestamp != nil {
		return nil, ErrIdleIneligible
	}
	return d, nil
}

// SetHTTPIdle is a runtime scaling operation, not a change to the saved replicas.
// The embedding serializes it with application edits and owns HTTP activity.
func (c *Client) SetHTTPIdle(ctx context.Context, t Target, service string, sleep bool) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		d, err := c.idleDeployment(ctx, t, service)
		if err != nil {
			return err
		}
		wasSleeping := idleDeployment(d)
		if sleep {
			if idleDeployment(d) {
				return nil
			}
			if *d.Spec.Replicas != 1 || !readyDeployment(d) {
				return ErrIdleIneligible
			}
			ready, err := c.readyPods(ctx, t, service, d)
			if err != nil {
				return err
			}
			if !ready {
				return ErrIdleIneligible
			}
			d.Spec.Replicas = ptr(int32(0))
			if d.Annotations == nil {
				d.Annotations = map[string]string{}
			}
			d.Annotations[idleHTTPAnnotation] = "sleeping"
		} else {
			if *d.Spec.Replicas == 0 && !idleDeployment(d) {
				return ErrIdleIneligible
			}
			if *d.Spec.Replicas == 1 && d.Annotations[idleHTTPAnnotation] == "" {
				return nil
			}
			d.Spec.Replicas = ptr(int32(1))
			if d.Annotations == nil {
				d.Annotations = map[string]string{}
			}
			if readyDeployment(d) {
				delete(d.Annotations, idleHTTPAnnotation)
			} else {
				d.Annotations[idleHTTPAnnotation] = "waking"
			}
		}
		_, err = c.kube.AppsV1().Deployments(d.Namespace).Update(ctx, d, metav1.UpdateOptions{})
		if err == nil && (sleep || wasSleeping) {
			reason := "HTTPIdleWake"
			if sleep {
				reason = "HTTPIdleSleep"
			}
			c.recordHTTPIdle(ctx, t, service, reason)
		}
		return err
	})
}
func (c *Client) HTTPAwake(ctx context.Context, t Target, service string) (bool, error) {
	d, err := c.idleDeployment(ctx, t, service)
	if err != nil {
		return false, err
	}
	if *d.Spec.Replicas != 1 {
		return false, nil
	}
	if !readyDeployment(d) {
		return false, nil
	}
	ready, err := c.readyPods(ctx, t, service, d)
	if err != nil || !ready {
		return false, err
	}
	// HAProxy can only forward once Kubernetes has published a ready endpoint.
	endpoints, err := c.kube.DiscoveryV1().EndpointSlices(d.Namespace).List(ctx, metav1.ListOptions{LabelSelector: "kubernetes.io/service-name=" + service, Limit: 8})
	if err != nil {
		return false, err
	}
	if endpoints.Continue != "" {
		return false, fmt.Errorf("HTTP endpoint inventory exceeds its bound")
	}
	for _, slice := range endpoints.Items {
		for _, e := range slice.Endpoints {
			if e.Conditions.Ready != nil && *e.Conditions.Ready && len(e.Addresses) > 0 {
				return true, nil
			}
		}
	}
	return false, nil
}

func (c *Client) recordHTTPIdle(ctx context.Context, t Target, service, reason string) {
	// Events expire through Kubernetes' normal event retention policy.
	_, _ = c.kube.CoreV1().Events(Namespace(t.ApplicationID)).Create(ctx, &corev1.Event{ObjectMeta: metav1.ObjectMeta{GenerateName: service + "-idle-", Namespace: Namespace(t.ApplicationID), Labels: labelsFor(t, service)}, InvolvedObject: corev1.ObjectReference{Kind: "Deployment", Namespace: Namespace(t.ApplicationID), Name: service, APIVersion: "apps/v1"}, Reason: reason, Message: map[string]string{"HTTPIdleSleep": "No active HTTP requests; scaled to zero until the next request", "HTTPIdleWake": "HTTP request woke the service"}[reason], Type: "Normal", Source: corev1.EventSource{Component: "hakopod"}, FirstTimestamp: metav1.NewTime(time.Now()), LastTimestamp: metav1.NewTime(time.Now()), Count: 1}, metav1.CreateOptions{})
}
