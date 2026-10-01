package cluster

import (
	"context"
	"fmt"

	"github.com/hakopod/hakopod/internal/database"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Stop PostgreSQL reconciliation before deleting its namespace. A live Cluster
// can recreate claims and other objects while namespace cleanup is in progress.
func (c *Client) deletePostgresController(ctx context.Context, d database.Resource, before func() error) (bool, error) {
	if c.dynamic == nil {
		return false, fmt.Errorf("PostgreSQL deletion controller is unavailable")
	}
	namespace := DatabaseNamespace(d.ID)
	api := c.dynamic.Resource(pgDatabaseResource).Namespace(namespace)
	object, err := api.Get(ctx, "database", metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	if object.GetUID() == "" || object.GetLabels()[databaseOwner] != d.ID || object.GetLabels()[managedBy] != "hakopod" {
		return false, fmt.Errorf("PostgreSQL deletion controller ownership changed")
	}
	if object.GetDeletionTimestamp() != nil {
		return false, nil
	}
	// Reclaim retained claims before foreground collection can remove them.
	if err = c.prepareDatabaseVolumeDeletion(ctx, namespace, before); err != nil {
		return false, err
	}
	if err = before(); err != nil {
		return false, err
	}
	uid := object.GetUID()
	foreground := metav1.DeletePropagationForeground
	err = api.Delete(ctx, object.GetName(), metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &uid}, PropagationPolicy: &foreground})
	return false, err
}
