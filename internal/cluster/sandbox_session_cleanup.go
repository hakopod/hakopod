package cluster

import (
	"context"
	"fmt"
	"time"

	"github.com/hakopod/hakopod/internal/sandbox"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// CleanupSession deletes only the recorded namespace and its owned Pod.
func (c *Client) CleanupSession(ctx context.Context, r sandbox.Record) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if !sandbox.ValidID(r.ID) || !sandbox.ValidID(r.Generation) || r.ApplicationID == "" || len(r.OwnerHash) != 64 || len(r.RuntimeHash) != 64 {
		return false, fmt.Errorf("session cleanup identity is invalid")
	}
	t := Target{ApplicationID: r.ApplicationID}
	ns, err := c.kube.CoreV1().Namespaces().Get(ctx, SandboxNamespace(r.ID), metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	if err = sandboxOwned(ns, t, r); err != nil {
		return false, err
	}
	if ns.UID == "" || r.NamespaceUID != "" && string(ns.UID) != r.NamespaceUID {
		return false, fmt.Errorf("refusing cleanup of a replaced session namespace")
	}
	pods, err := c.kube.CoreV1().Pods(ns.Name).List(ctx, metav1.ListOptions{Limit: 2})
	if err != nil {
		return false, err
	}
	if pods.Continue != "" || len(pods.Items) > 1 {
		return false, fmt.Errorf("session namespace contains unexpected Pods")
	}
	for i := range pods.Items {
		pod := &pods.Items[i]
		if err = sandboxOwned(pod, t, r); err != nil {
			return false, err
		}
		if pod.Name != sandboxPodName || pod.UID == "" || r.PodUID != "" && string(pod.UID) != r.PodUID {
			return false, fmt.Errorf("refusing cleanup of a replaced session Pod")
		}
		if pod.DeletionTimestamp == nil {
			if err = c.kube.CoreV1().Pods(ns.Name).Delete(ctx, pod.Name, deleteOptions(pod)); err != nil && !apierrors.IsNotFound(err) {
				return false, err
			}
		}
	}
	if len(pods.Items) > 0 {
		return false, nil
	}
	// Persistent data is forbidden. Do not let namespace deletion erase an unexpected claim.
	claims, err := c.kube.CoreV1().PersistentVolumeClaims(ns.Name).List(ctx, metav1.ListOptions{Limit: 1})
	if err != nil {
		return false, err
	}
	if len(claims.Items) > 0 || claims.Continue != "" {
		return false, fmt.Errorf("session namespace contains an unexpected persistent volume claim")
	}
	if err = c.sandboxCleanupInventory(ctx, t, r); err != nil {
		return false, err
	}
	if ns.DeletionTimestamp == nil {
		if err = c.kube.CoreV1().Namespaces().Delete(ctx, ns.Name, deleteOptions(ns)); err != nil && !apierrors.IsNotFound(err) {
			return false, err
		}
	}
	_, err = c.kube.CoreV1().Namespaces().Get(ctx, ns.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return true, nil
	}
	return false, err
}
