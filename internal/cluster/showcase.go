package cluster

import (
	"context"
	"errors"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

var ErrSampleHasData = errors.New("the sample namespace contains persistent data; automatic sample removal is refused")

func (c *Client) CheckShowcaseRemoval(ctx context.Context, applicationID string) error {
	target := Target{ApplicationID: applicationID, Project: "demo", Environment: "development"}
	ns, err := c.kube.CoreV1().Namespaces().Get(ctx, Namespace(applicationID), metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if err = owned(ns, target); err != nil {
		return err
	}
	pvcs, err := c.kube.CoreV1().PersistentVolumeClaims(ns.Name).List(ctx, metav1.ListOptions{Limit: 1})
	if err != nil {
		return err
	}
	if len(pvcs.Items) > 0 || pvcs.Continue != "" {
		return ErrSampleHasData
	}
	return nil
}

// RemoveShowcase deletes only the exact tracked, stateless sample namespace.
// The caller holds the deployment worker's application lock and has detached
// its desired state, so a resync cannot recreate it during removal.
func (c *Client) RemoveShowcase(ctx context.Context, applicationID string) error {
	if err := c.CheckShowcaseRemoval(ctx, applicationID); err != nil {
		return err
	}
	ns, err := c.kube.CoreV1().Namespaces().Get(ctx, Namespace(applicationID), metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if err = owned(ns, Target{ApplicationID: applicationID, Project: "demo", Environment: "development"}); err != nil {
		return err
	}
	if err = c.kube.CoreV1().Namespaces().Delete(ctx, ns.Name, deleteOptions(ns)); err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	timer := time.NewTicker(500 * time.Millisecond)
	defer timer.Stop()
	for {
		current, err := c.kube.CoreV1().Namespaces().Get(ctx, ns.Name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return nil
		}
		if err != nil {
			return err
		}
		if current.UID != ns.UID {
			return errors.New("sample namespace was replaced during removal; inspect its ownership")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
		}
	}
}
