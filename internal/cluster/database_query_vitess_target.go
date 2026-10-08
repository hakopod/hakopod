package cluster

import (
	"context"
	"reflect"
	"sort"
	"strconv"

	"github.com/hakopod/hakopod/internal/database"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

type vitessQueryTarget struct {
	namespace  *corev1.Namespace
	controller *unstructured.Unstructured
	pod        *corev1.Pod
	identity   string
}

// Query availability needs one owned ready gateway. Tablet replication and
// recovery health are separate checks and do not select the query endpoint.
func (c *Client) selectVitessQueryTarget(ctx context.Context, d database.Resource) (*vitessQueryTarget, error) {
	ns, err := c.kube.CoreV1().Namespaces().Get(ctx, DatabaseNamespace(d.ID), metav1.GetOptions{})
	if err != nil {
		return nil, queryUnavailable()
	}
	object, err := c.dynamic.Resource(vitessDatabaseResource).Namespace(ns.Name).Get(ctx, "database", metav1.GetOptions{})
	if err != nil || !vitessQueryControllerMatches(ns, object, d) {
		return nil, queryUnavailable()
	}
	identity, err := c.vitessIdentityFingerprint(ctx, d)
	if err != nil || len(identity) != 64 {
		return nil, queryUnavailable()
	}
	expected, err := c.databaseObject(ctx, d)
	if err != nil {
		return nil, queryUnavailable()
	}
	if err = c.applyVitessIdentity(ctx, d, expected); err != nil || !vitessQueryDesiredSpecMatches(expected, object) {
		return nil, queryUnavailable()
	}
	policy, err := c.databasePolicy(ctx, d)
	if err != nil {
		return nil, queryUnavailable()
	}
	pods, err := c.kube.CoreV1().Pods(ns.Name).List(ctx, metav1.ListOptions{LabelSelector: databaseOwner + "=" + d.ID + "," + vitessComponentLabel + "=gateway", Limit: 17, FieldSelector: activeDatabasePodFields})
	if err != nil || pods.Continue != "" || len(pods.Items) == 0 || len(pods.Items) > 16 {
		return nil, queryUnavailable()
	}
	sort.Slice(pods.Items, func(i, j int) bool { return pods.Items[i].Name < pods.Items[j].Name })
	var selected *corev1.Pod
	for _, pod := range pods.Items {
		if pod.Namespace != ns.Name || pod.UID == "" || pod.Labels[databaseOwner] != d.ID || pod.Labels[vitessComponentLabel] != "gateway" || !c.vitessPodOwned(ctx, pod, object.GetUID()) || !vitessPodMatches(pod, d) || !databasePodPolicyMatches(pod, policy) || !vitessPodIdentityMatches(pod, identity) {
			return nil, queryUnavailable()
		}
		if !vitessPodReady(pod) {
			continue
		}
		if selected == nil {
			selected = pod.DeepCopy()
		}
	}
	if selected == nil {
		return nil, queryUnavailable()
	}
	return &vitessQueryTarget{namespace: ns.DeepCopy(), controller: object.DeepCopy(), pod: selected, identity: identity}, nil
}

func vitessQueryControllerMatches(ns *corev1.Namespace, object *unstructured.Unstructured, d database.Resource) bool {
	return ns != nil && ns.UID != "" && ns.DeletionTimestamp == nil && ns.Name == DatabaseNamespace(d.ID) && ns.Labels[databaseOwner] == d.ID && ns.Labels[managedBy] == "hakopod" && object != nil && object.GetUID() != "" && object.GetDeletionTimestamp() == nil && object.GetName() == "database" && object.GetNamespace() == ns.Name && object.GetLabels()[databaseOwner] == d.ID && object.GetLabels()[managedBy] == "hakopod" && object.GetAnnotations()["hakopod.io/database-revision"] == strconv.FormatInt(d.Revision, 10)
}

func (c *Client) verifyVitessQueryTarget(ctx context.Context, d database.Resource, target *vitessQueryTarget) error {
	if target == nil {
		return queryUnavailable()
	}
	ns, err := c.kube.CoreV1().Namespaces().Get(ctx, target.namespace.Name, metav1.GetOptions{})
	if err != nil {
		return queryUnavailable()
	}
	object, err := c.dynamic.Resource(vitessDatabaseResource).Namespace(ns.Name).Get(ctx, "database", metav1.GetOptions{})
	if err != nil || !vitessQueryControllerMatches(ns, object, d) || ns.UID != target.namespace.UID || object.GetUID() != target.controller.GetUID() || !reflect.DeepEqual(object.Object["spec"], target.controller.Object["spec"]) {
		return queryUnavailable()
	}
	identity, err := c.vitessIdentityFingerprint(ctx, d)
	if err != nil || identity != target.identity {
		return queryUnavailable()
	}
	policy, err := c.databasePolicy(ctx, d)
	if err != nil {
		return queryUnavailable()
	}
	pod, err := c.kube.CoreV1().Pods(ns.Name).Get(ctx, target.pod.Name, metav1.GetOptions{})
	if err != nil || pod.UID != target.pod.UID || !reflect.DeepEqual(pod.Spec, target.pod.Spec) || !reflect.DeepEqual(pod.OwnerReferences, target.pod.OwnerReferences) || pod.Labels[databaseOwner] != d.ID || pod.Labels[vitessComponentLabel] != "gateway" || !c.vitessPodOwned(ctx, *pod, object.GetUID()) || !vitessPodMatches(*pod, d) || !databasePodPolicyMatches(*pod, policy) || !vitessPodReadyWithIdentity(*pod, target.identity) {
		return queryUnavailable()
	}
	return nil
}

// Pinned source: planetscale/vitess-operator@10a3b742c02c38f97d554739d5a257197daa48f9,
// deploy/crds/planetscale.com_vitessclusters.yaml (hexWidth and tabletPools.name).
// The pinned operator CRD defaults equal.hexWidth to zero and tablet pool.name
// to an empty string. Preserve exact comparison for every other desired field.
func vitessQueryDesiredSpecMatches(expected, live *unstructured.Unstructured) bool {
	desired := expected.DeepCopy()
	actual := live.DeepCopy()
	for _, object := range []*unstructured.Unstructured{desired, actual} {
		keyspaces, found, err := unstructured.NestedSlice(object.Object, "spec", "keyspaces")
		if err != nil || !found {
			return false
		}
		for _, entry := range keyspaces {
			keyspace, ok := entry.(map[string]any)
			if !ok {
				return false
			}
			partitions, ok := keyspace["partitionings"].([]any)
			if !ok {
				return false
			}
			for _, part := range partitions {
				partition, ok := part.(map[string]any)
				if !ok {
					return false
				}
				equal, ok := partition["equal"].(map[string]any)
				if !ok {
					continue
				}
				if _, exists := equal["hexWidth"]; !exists {
					equal["hexWidth"] = int64(0)
				}
				template, ok := equal["shardTemplate"].(map[string]any)
				if !ok {
					return false
				}
				pools, ok := template["tabletPools"].([]any)
				if !ok {
					return false
				}
				for _, entry := range pools {
					pool, ok := entry.(map[string]any)
					if !ok {
						return false
					}
					if _, exists := pool["name"]; !exists {
						pool["name"] = ""
					}
				}
			}
		}
		if err = unstructured.SetNestedSlice(object.Object, keyspaces, "spec", "keyspaces"); err != nil {
			return false
		}
	}
	return reflect.DeepEqual(desired.Object["spec"], actual.Object["spec"])
}
