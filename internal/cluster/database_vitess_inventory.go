package cluster

import (
	"context"
	"fmt"
	"reflect"

	"github.com/hakopod/hakopod/internal/database"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
)

// vitessObservationInventory is valid only for one ObserveDatabase call. It
// freezes the already-listed tablet Pods after their namespace, controller,
// identity, configuration and owner chains have been verified.
type vitessObservationInventory struct {
	database     database.Resource
	identity     string
	namespaceUID types.UID
	controller   *unstructured.Unstructured
	tablets      map[string]corev1.Pod
}

type vitessOwnerCacheEntry struct {
	uid      types.UID
	deleting bool
	owners   []metav1.OwnerReference
}

func (c *Client) newVitessObservationInventory(ctx context.Context, d database.Resource, namespace *corev1.Namespace, object *unstructured.Unstructured, identity string, pods []corev1.Pod) (*vitessObservationInventory, error) {
	if namespace == nil || namespace.UID == "" || namespace.DeletionTimestamp != nil || namespace.Name != DatabaseNamespace(d.ID) || namespace.Labels[databaseOwner] != d.ID || namespace.Labels[managedBy] != "hakopod" {
		return nil, fmt.Errorf("Vitess inventory namespace ownership changed")
	}
	if object == nil || object.GetUID() == "" || object.GetDeletionTimestamp() != nil || object.GetNamespace() != namespace.Name || object.GetName() != "database" || object.GetLabels()[databaseOwner] != d.ID || object.GetLabels()[managedBy] != "hakopod" {
		return nil, fmt.Errorf("Vitess inventory controller ownership changed")
	}
	if len(identity) != 64 || len(pods) > database.MaxMembers {
		return nil, fmt.Errorf("Vitess tablet inventory exceeds its bound")
	}
	result := &vitessObservationInventory{database: d, identity: identity, namespaceUID: namespace.UID, controller: object.DeepCopy(), tablets: make(map[string]corev1.Pod, len(pods))}
	owners := make(map[string]vitessOwnerCacheEntry, len(pods)*2)
	for _, pod := range pods {
		if pod.Name == "" || pod.Namespace != namespace.Name || pod.UID == "" || pod.DeletionTimestamp != nil || pod.Labels[databaseOwner] != d.ID || pod.Labels[vitessComponentLabel] != "tablet" || result.tablets[pod.Name].Name != "" {
			return nil, fmt.Errorf("Vitess tablet inventory changed")
		}
		if !c.vitessOwnedChainCached(ctx, pod.Namespace, pod.UID, pod.OwnerReferences, object.GetUID(), owners) || !vitessPodMatches(pod, d) || !vitessPodIdentityMatches(pod, identity) {
			return nil, fmt.Errorf("Vitess tablet inventory could not be verified")
		}
		result.tablets[pod.Name] = *pod.DeepCopy()
	}
	return result, nil
}

func (c *Client) vitessOwnedChainCached(ctx context.Context, namespace string, uid types.UID, refs []metav1.OwnerReference, databaseUID types.UID, cache map[string]vitessOwnerCacheEntry) bool {
	resources := map[string]schema.GroupVersionResource{
		"apps/v1/ReplicaSet": {Group: "apps", Version: "v1", Resource: "replicasets"}, "apps/v1/Deployment": {Group: "apps", Version: "v1", Resource: "deployments"},
		"planetscale.com/v2/VitessShard": vitessShardResource, "planetscale.com/v2/VitessKeyspace": {Group: "planetscale.com", Version: "v2", Resource: "vitesskeyspaces"},
		"planetscale.com/v2/VitessCell": {Group: "planetscale.com", Version: "v2", Resource: "vitesscells"}, "planetscale.com/v2/EtcdLockserver": {Group: "planetscale.com", Version: "v2", Resource: "etcdlockservers"},
		"planetscale.com/v2/VitessBackupStorage": {Group: "planetscale.com", Version: "v2", Resource: "vitessbackupstorages"}, "planetscale.com/v2/VitessBackupSchedule": {Group: "planetscale.com", Version: "v2", Resource: "vitessbackupschedules"},
		"batch/v1/Job": {Group: "batch", Version: "v1", Resource: "jobs"},
	}
	seen := map[types.UID]bool{uid: true}
	for depth := 0; depth < 6; depth++ {
		if len(refs) != 1 {
			return false
		}
		owner := refs[0]
		if owner.UID == databaseUID && owner.Name == "database" && owner.Kind == "VitessCluster" && owner.APIVersion == "planetscale.com/v2" {
			return true
		}
		gvr, ok := resources[owner.APIVersion+"/"+owner.Kind]
		if !ok || owner.UID == "" || seen[owner.UID] {
			return false
		}
		seen[owner.UID] = true
		key := gvr.String() + "/" + namespace + "/" + owner.Name
		entry, ok := cache[key]
		if !ok {
			current, err := c.dynamic.Resource(gvr).Namespace(namespace).Get(ctx, owner.Name, metav1.GetOptions{})
			if err != nil {
				return false
			}
			entry = vitessOwnerCacheEntry{uid: current.GetUID(), deleting: current.GetDeletionTimestamp() != nil, owners: append([]metav1.OwnerReference(nil), current.GetOwnerReferences()...)}
			cache[key] = entry
		}
		if entry.uid != owner.UID || entry.deleting {
			return false
		}
		refs = entry.owners
	}
	return false
}

func (i *vitessObservationInventory) tablet(member database.Member) (*corev1.Pod, string, error) {
	if i == nil || member.Name == "" || member.UID == "" {
		return nil, "", fmt.Errorf("Vitess observation inventory is unavailable")
	}
	pod, ok := i.tablets[member.Name]
	if !ok || pod.UID != types.UID(member.UID) || pod.DeletionTimestamp != nil {
		return nil, "", fmt.Errorf("Vitess observation target changed")
	}
	copy := pod.DeepCopy()
	return copy, "vttablet", nil
}

// tabletForExec rechecks the exact Pod UID, configuration and identity
// immediately before this member's first native command. Kubernetes exec is
// addressed by Pod name. A final inventory read also rejects replacements that
// occurred during the native probes before publishing their health result.
func (c *Client) tabletForExec(ctx context.Context, member database.Member, inventory *vitessObservationInventory) (*corev1.Pod, string, error) {
	stored, container, err := inventory.tablet(member)
	if err != nil {
		return nil, "", err
	}
	current, err := c.kube.CoreV1().Pods(stored.Namespace).Get(ctx, stored.Name, metav1.GetOptions{})
	if err != nil || current.UID != stored.UID || current.DeletionTimestamp != nil || current.Namespace != stored.Namespace || current.Labels[databaseOwner] != inventory.database.ID || current.Labels[vitessComponentLabel] != "tablet" || !reflect.DeepEqual(current.OwnerReferences, stored.OwnerReferences) || !vitessPodMatches(*current, inventory.database) || !vitessPodIdentityMatches(*current, inventory.identity) {
		return nil, "", fmt.Errorf("Vitess observation target changed")
	}
	return stored, container, nil
}

func (c *Client) verifyVitessObservationInventory(ctx context.Context, inventory *vitessObservationInventory) error {
	if inventory == nil || inventory.controller == nil {
		return fmt.Errorf("Vitess observation inventory is unavailable")
	}
	d := inventory.database
	ns, err := c.kube.CoreV1().Namespaces().Get(ctx, DatabaseNamespace(d.ID), metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("Vitess observation namespace is unavailable")
	}
	object, err := c.dynamic.Resource(vitessDatabaseResource).Namespace(ns.Name).Get(ctx, "database", metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("Vitess observation controller is unavailable")
	}
	identity, err := c.vitessIdentityFingerprint(ctx, d)
	if err != nil {
		return err
	}
	pods, err := c.kube.CoreV1().Pods(ns.Name).List(ctx, metav1.ListOptions{Limit: database.MaxMembers + 1, LabelSelector: vitessComponentLabel + "=tablet", FieldSelector: activeDatabasePodFields})
	if err != nil || pods.Continue != "" || !inventory.matchesCurrent(ns, object, identity, pods.Items) {
		return fmt.Errorf("Vitess inventory changed during health checks")
	}
	// Rebuild the bounded owner graph once: a deleted shard or keyspace can
	// leave its old Pod owner references visible until garbage collection runs.
	_, err = c.newVitessObservationInventory(ctx, d, ns, object, identity, pods.Items)
	return err
}

func (i *vitessObservationInventory) matchesCurrent(ns *corev1.Namespace, object *unstructured.Unstructured, identity string, pods []corev1.Pod) bool {
	if i == nil || i.controller == nil || ns == nil || object == nil || ns.Name != DatabaseNamespace(i.database.ID) || ns.UID != i.namespaceUID || ns.DeletionTimestamp != nil || ns.Labels[databaseOwner] != i.database.ID || ns.Labels[managedBy] != "hakopod" || identity != i.identity {
		return false
	}
	if object.GetUID() != i.controller.GetUID() || object.GetDeletionTimestamp() != nil || object.GetNamespace() != ns.Name || object.GetName() != "database" || object.GetLabels()[databaseOwner] != i.database.ID || object.GetLabels()[managedBy] != "hakopod" || object.GetAnnotations()["hakopod.io/database-revision"] != i.controller.GetAnnotations()["hakopod.io/database-revision"] || !reflect.DeepEqual(object.Object["spec"], i.controller.Object["spec"]) {
		return false
	}
	if len(pods) != len(i.tablets) || len(pods) > database.MaxMembers {
		return false
	}
	seen := make(map[string]bool, len(pods))
	for _, current := range pods {
		stored, ok := i.tablets[current.Name]
		if !ok || seen[current.Name] || current.Namespace != stored.Namespace || current.UID != stored.UID || current.DeletionTimestamp != nil || current.Labels[databaseOwner] != i.database.ID || current.Labels[vitessComponentLabel] != "tablet" || !reflect.DeepEqual(current.OwnerReferences, stored.OwnerReferences) || !reflect.DeepEqual(current.Spec, stored.Spec) || !vitessPodIdentityMatches(current, i.identity) || !vitessPodReady(current) {
			return false
		}
		seen[current.Name] = true
	}
	return true
}
