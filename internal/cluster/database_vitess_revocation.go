package cluster

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"

	"github.com/hakopod/hakopod/internal/database"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
)

const vitessBackupRevoked = "hakopod.io/vitess-backup-revoked"

// VitessBackupRevocationPending means an operation must retry cleanup while
// retaining its destination reservation. A delete request is not confirmation
// that backup jobs and storage-controller processes have exited. Running
// tablets may retain native storage credentials used for member recovery;
// only provider-side key revocation invalidates those credentials.
var VitessBackupRevocationPending = errors.New("Vitess native backup authority cleanup is pending")

// ReconcileVitessBackupAuthority runs under a durable native-storage claim.
// It does not rotate database TLS, change tablet capacity or mutate user data.
func (c *Client) ReconcileVitessBackupAuthority(ctx context.Context, d database.Resource, before func() error) error {
	if d.Spec.Engine != "vitess" {
		return nil
	}
	if _, err := c.reconcileVitessBackupAuthority(ctx, d, before); err != nil {
		return err
	}
	ns, err := c.kube.CoreV1().Namespaces().Get(ctx, DatabaseNamespace(d.ID), metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	object, err := c.dynamic.Resource(vitessDatabaseResource).Namespace(ns.Name).Get(ctx, "database", metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if object.GetUID() == "" || object.GetLabels()[databaseOwner] != d.ID || object.GetLabels()[managedBy] != "hakopod" || object.GetAnnotations()[vitessBackupRevoked] == "true" || object.GetDeletionTimestamp() != nil {
		return fmt.Errorf("Vitess native backup authority requires a current owned controller")
	}
	operator, err := c.kube.AppsV1().Deployments(ns.Name).Get(ctx, "vitess-operator", metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if !vitessNamespaceObjectOwned(operator, d, ns.UID) {
		return fmt.Errorf("Vitess native backup authority operator ownership changed")
	}
	if operator.Spec.Replicas != nil && *operator.Spec.Replicas == 0 {
		operator.Spec.Replicas = ptr(int32(1))
		if err = before(); err != nil {
			return err
		}
		_, err = c.kube.AppsV1().Deployments(ns.Name).Update(ctx, operator, metav1.UpdateOptions{})
	}
	return err
}

func (c *Client) reconcileVitessBackupAuthority(ctx context.Context, d database.Resource, before func() error) (VitessBackupStorage, error) {
	storage, err := c.vitessBackupStorage(ctx, d)
	if err != nil {
		return VitessBackupStorage{}, errors.Join(err, c.revokeVitessBackupAuthority(ctx, d, before))
	}
	ns, err := c.kube.CoreV1().Namespaces().Get(ctx, DatabaseNamespace(d.ID), metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return storage, nil
	}
	if err != nil {
		return VitessBackupStorage{}, err
	}
	if ns.UID == "" || ns.Labels[databaseOwner] != d.ID || ns.Labels[managedBy] != "hakopod" {
		return VitessBackupStorage{}, fmt.Errorf("Vitess native backup namespace ownership changed")
	}
	policyAPI := c.dynamic.Resource(schema.GroupVersionResource{Group: "networking.k8s.io", Version: "v1", Resource: "networkpolicies"}).Namespace(ns.Name)
	oldPolicy, policyErr := policyAPI.Get(ctx, "database-vitess-backup-egress", metav1.GetOptions{})
	if policyErr != nil && !apierrors.IsNotFound(policyErr) {
		return VitessBackupStorage{}, policyErr
	}
	if policyErr == nil {
		if !vitessNamespaceObjectOwned(oldPolicy, d, ns.UID) {
			return VitessBackupStorage{}, fmt.Errorf("Vitess native backup network ownership changed")
		}
		approved := make([]string, len(storage.ApprovedEndpointCIDRs))
		for i, prefix := range storage.ApprovedEndpointCIDRs {
			approved[i] = prefix.String()
		}
		rules, _, _ := unstructured.NestedSlice(oldPolicy.Object, "spec", "egress")
		for _, raw := range rules {
			rule, ok := raw.(map[string]any)
			if !ok {
				return VitessBackupStorage{}, fmt.Errorf("Vitess native backup network rule is invalid")
			}
			peers, _, _ := unstructured.NestedSlice(rule, "to")
			for _, rawPeer := range peers {
				peer, ok := rawPeer.(map[string]any)
				if !ok {
					return VitessBackupStorage{}, fmt.Errorf("Vitess native backup network peer is invalid")
				}
				cidr, _, _ := unstructured.NestedString(peer, "ipBlock", "cidr")
				if !slices.Contains(approved, cidr) {
					return VitessBackupStorage{}, errors.Join(VitessBackupRevocationPending, c.revokeVitessBackupAuthority(ctx, d, before))
				}
			}
		}
	}
	api := c.dynamic.Resource(vitessDatabaseResource).Namespace(ns.Name)
	current, err := api.Get(ctx, "database", metav1.GetOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		return VitessBackupStorage{}, err
	}
	if err == nil {
		if current.GetUID() == "" || current.GetLabels()[databaseOwner] != d.ID || current.GetLabels()[managedBy] != "hakopod" || current.GetDeletionTimestamp() != nil {
			return VitessBackupStorage{}, fmt.Errorf("Vitess native backup controller ownership changed")
		}
		if current.GetAnnotations()[vitessBackupRevoked] == "true" {
			uid := current.GetUID()
			if err = c.revokeVitessBackupAuthority(ctx, d, before); err != nil {
				return VitessBackupStorage{}, err
			}
			current, err = api.Get(ctx, "database", metav1.GetOptions{})
			if err != nil {
				return VitessBackupStorage{}, err
			}
			if current.GetUID() != uid {
				return VitessBackupStorage{}, fmt.Errorf("Vitess native backup controller changed during revocation")
			}
		}
	}
	// This also reconciles an approved address shrink without a database
	// revision change. A failed secret or policy check revokes existing access.
	if err = c.prepareVitessBackupStorage(ctx, d, storage, before); err != nil {
		return VitessBackupStorage{}, errors.Join(err, c.revokeVitessBackupAuthority(ctx, d, before))
	}
	if current != nil {
		updated := current.DeepCopy()
		if err = applyVitessBackupStorage(updated, d, storage); err != nil {
			return VitessBackupStorage{}, err
		}
		annotations := updated.GetAnnotations()
		delete(annotations, vitessBackupRevoked)
		updated.SetAnnotations(annotations)
		if !reflect.DeepEqual(updated.Object, current.Object) {
			if err = before(); err != nil {
				return VitessBackupStorage{}, err
			}
			if _, err = api.Update(ctx, updated, metav1.UpdateOptions{}); err != nil {
				return VitessBackupStorage{}, err
			}
		}
	}
	return storage, nil
}

func vitessNamespaceObjectOwned(object metav1.Object, d database.Resource, namespaceUID types.UID) bool {
	if object.GetUID() == "" || object.GetNamespace() != DatabaseNamespace(d.ID) || object.GetLabels()[databaseOwner] != d.ID || object.GetLabels()[managedBy] != "hakopod" {
		return false
	}
	owners := object.GetOwnerReferences()
	return len(owners) == 1 && owners[0].UID == namespaceUID && owners[0].Kind == "Namespace" && owners[0].APIVersion == "v1" && owners[0].Name == DatabaseNamespace(d.ID)
}

func (c *Client) revokeVitessBackupAuthority(ctx context.Context, d database.Resource, before func() error) error {
	ns, err := c.kube.CoreV1().Namespaces().Get(ctx, DatabaseNamespace(d.ID), metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if ns.UID == "" || ns.Labels[databaseOwner] != d.ID || ns.Labels[managedBy] != "hakopod" {
		return fmt.Errorf("Vitess native backup revocation namespace ownership changed")
	}
	api := c.dynamic.Resource(vitessDatabaseResource).Namespace(ns.Name)
	object, err := api.Get(ctx, "database", metav1.GetOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	var databaseUID types.UID
	if err == nil {
		if object.GetUID() == "" || object.GetLabels()[databaseOwner] != d.ID || object.GetLabels()[managedBy] != "hakopod" {
			return fmt.Errorf("Vitess native backup revocation controller ownership changed")
		}
		databaseUID = object.GetUID()
		updated := object.DeepCopy()
		unstructured.RemoveNestedField(updated.Object, "spec", "backup")
		annotations := updated.GetAnnotations()
		if annotations == nil {
			annotations = map[string]string{}
		}
		annotations[vitessBackupRevoked] = "true"
		updated.SetAnnotations(annotations)
		if !reflect.DeepEqual(updated.Object, object.Object) {
			if err = before(); err != nil {
				return err
			}
			if _, err = api.Update(ctx, updated, metav1.UpdateOptions{}); err != nil {
				return err
			}
		}
	}
	// Pausing the namespace controller prevents still-existing child CRs from
	// recreating backup jobs while revocation is incomplete. Data pods and
	// their claims remain in place; vtorc still owns native primary election.
	operator, err := c.kube.AppsV1().Deployments(ns.Name).Get(ctx, "vitess-operator", metav1.GetOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	if err == nil {
		if !vitessNamespaceObjectOwned(operator, d, ns.UID) {
			return fmt.Errorf("Vitess revocation operator ownership changed")
		}
		if operator.Spec.Replicas == nil || *operator.Spec.Replicas != 0 {
			operator.Spec.Replicas = ptr(int32(0))
			if err = before(); err != nil {
				return err
			}
			if _, err = c.kube.AppsV1().Deployments(ns.Name).Update(ctx, operator, metav1.UpdateOptions{}); err != nil {
				return err
			}
		}
	}
	for _, target := range []struct {
		gvr  schema.GroupVersionResource
		name string
	}{
		{schema.GroupVersionResource{Group: "networking.k8s.io", Version: "v1", Resource: "networkpolicies"}, "database-vitess-backup-egress"},
		{schema.GroupVersionResource{Version: "v1", Resource: "secrets"}, "database-vitess-native-backup"},
	} {
		resource := c.dynamic.Resource(target.gvr).Namespace(ns.Name)
		current, getErr := resource.Get(ctx, target.name, metav1.GetOptions{})
		if apierrors.IsNotFound(getErr) {
			continue
		}
		if getErr != nil {
			return getErr
		}
		if !vitessNamespaceObjectOwned(current, d, ns.UID) {
			return fmt.Errorf("Vitess native backup revocation resource ownership changed")
		}
		uid := current.GetUID()
		if err = before(); err != nil {
			return err
		}
		if err = resource.Delete(ctx, target.name, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &uid}}); err != nil && !apierrors.IsNotFound(err) {
			return err
		}
	}
	if databaseUID != "" {
		schedules := c.dynamic.Resource(schema.GroupVersionResource{Group: "planetscale.com", Version: "v2", Resource: "vitessbackupschedules"}).Namespace(ns.Name)
		list, listErr := schedules.List(ctx, metav1.ListOptions{LabelSelector: "planetscale.com/cluster=database", Limit: 9})
		if listErr != nil {
			return listErr
		}
		if list.GetContinue() != "" || len(list.Items) > 8 {
			return fmt.Errorf("Vitess backup schedule revocation inventory exceeds its bound")
		}
		for _, item := range list.Items {
			if !c.vitessOwnedChainState(ctx, ns.Name, item.GetUID(), item.GetOwnerReferences(), databaseUID, true) {
				return fmt.Errorf("Vitess backup schedule revocation ownership changed")
			}
			suspended, _, _ := unstructured.NestedBool(item.Object, "spec", "suspend")
			if !suspended && item.GetDeletionTimestamp() == nil {
				if err = unstructured.SetNestedField(item.Object, true, "spec", "suspend"); err != nil {
					return err
				}
				if err = before(); err != nil {
					return err
				}
				if _, err = schedules.Update(ctx, &item, metav1.UpdateOptions{}); err != nil {
					return err
				}
			}
		}
	}
	return c.stopVitessBackupProcesses(ctx, d, ns.UID, databaseUID, before)
}

func (c *Client) stopVitessBackupProcesses(ctx context.Context, d database.Resource, namespaceUID, databaseUID types.UID, before func() error) error {
	ns := DatabaseNamespace(d.ID)
	pending := false
	jobs, err := c.kube.BatchV1().Jobs(ns).List(ctx, metav1.ListOptions{LabelSelector: "planetscale.com/cluster=database,planetscale.com/component=vtbackup", Limit: 17})
	if err != nil {
		return err
	}
	if jobs.Continue != "" || len(jobs.Items) > 16 {
		return fmt.Errorf("Vitess backup job revocation inventory exceeds its bound")
	}
	for _, job := range jobs.Items {
		if !c.vitessOwnedChainState(ctx, ns, job.UID, job.OwnerReferences, databaseUID, true) {
			return fmt.Errorf("Vitess backup job revocation ownership changed")
		}
		pending = true
		if job.DeletionTimestamp != nil {
			continue
		}
		if err = before(); err != nil {
			return err
		}
		foreground := metav1.DeletePropagationForeground
		if err = c.kube.BatchV1().Jobs(ns).Delete(ctx, job.Name, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &job.UID}, PropagationPolicy: &foreground}); err != nil && !apierrors.IsNotFound(err) {
			return err
		}
	}
	// One bounded namespace list also finds a forked subcontroller that has
	// inherited the operator's custom labels. Data components are excluded.
	pods, err := c.kube.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{Limit: database.MaxMembers + 32})
	if err != nil {
		return err
	}
	if pods.Continue != "" {
		return fmt.Errorf("Vitess backup process revocation inventory exceeds its bound")
	}
	for _, pod := range pods.Items {
		component := pod.Labels["planetscale.com/component"]
		operator := pod.Labels[vitessComponentLabel] == "operator"
		if component != "vtbackup" && component != "vbs-subcontroller" && !operator {
			continue
		}
		owned := c.vitessOwnedChainState(ctx, ns, pod.UID, pod.OwnerReferences, databaseUID, true)
		if operator && component != "vbs-subcontroller" {
			owned = c.vitessOperatorPodOwned(ctx, pod.Name, pod.UID, pod.OwnerReferences, d, namespaceUID)
		}
		if !owned {
			return fmt.Errorf("Vitess backup process revocation ownership changed")
		}
		pending = true
		if pod.DeletionTimestamp != nil {
			continue
		}
		if err = before(); err != nil {
			return err
		}
		if err = c.kube.CoreV1().Pods(ns).Delete(ctx, pod.Name, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &pod.UID}}); err != nil && !apierrors.IsNotFound(err) {
			return err
		}
	}
	if pending {
		return VitessBackupRevocationPending
	}
	return nil
}

func (c *Client) vitessOperatorPodOwned(ctx context.Context, name string, uid types.UID, owners []metav1.OwnerReference, d database.Resource, namespaceUID types.UID) bool {
	if name == "" || uid == "" || len(owners) != 1 || owners[0].APIVersion != "apps/v1" || owners[0].Kind != "ReplicaSet" {
		return false
	}
	ns := DatabaseNamespace(d.ID)
	rs, err := c.kube.AppsV1().ReplicaSets(ns).Get(ctx, owners[0].Name, metav1.GetOptions{})
	if err != nil || rs.UID != owners[0].UID || len(rs.OwnerReferences) != 1 {
		return false
	}
	owner := rs.OwnerReferences[0]
	if owner.APIVersion != "apps/v1" || owner.Kind != "Deployment" || owner.Name != "vitess-operator" {
		return false
	}
	deployment, err := c.kube.AppsV1().Deployments(ns).Get(ctx, owner.Name, metav1.GetOptions{})
	return err == nil && deployment.UID == owner.UID && vitessNamespaceObjectOwned(deployment, d, namespaceUID)
}
