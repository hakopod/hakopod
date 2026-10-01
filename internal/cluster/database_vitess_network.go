package cluster

import (
	"context"
	"fmt"

	"github.com/hakopod/hakopod/internal/database"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
)

func vitessNetworkPolicies(d database.Resource, uid types.UID, apiRules []networkingv1.NetworkPolicyEgressRule) []networkingv1.NetworkPolicy {
	tcp, udp := corev1.ProtocolTCP, corev1.ProtocolUDP
	port := func(value int) networkingv1.NetworkPolicyPort {
		p := intstr.FromInt(value)
		return networkingv1.NetworkPolicyPort{Protocol: &tcp, Port: &p}
	}
	ports := []networkingv1.NetworkPolicyPort{}
	for _, value := range []int{2379, 2380, 3306, 15000, 15999} {
		ports = append(ports, port(value))
	}
	// The namespace is owned by one database. Backup pods created by the
	// pinned subcontroller carry native labels, not custom workload labels.
	local := networkingv1.NetworkPolicyPeer{PodSelector: &metav1.LabelSelector{}}
	dns := port(53)
	dnsUDP := dns
	dnsUDP.Protocol = &udp
	base := networkingv1.NetworkPolicy{ObjectMeta: databaseIdentityMeta(d, uid, "database"), Spec: networkingv1.NetworkPolicySpec{PodSelector: metav1.LabelSelector{}, PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress, networkingv1.PolicyTypeEgress}, Ingress: []networkingv1.NetworkPolicyIngressRule{{From: []networkingv1.NetworkPolicyPeer{local}, Ports: ports}}, Egress: []networkingv1.NetworkPolicyEgressRule{{To: []networkingv1.NetworkPolicyPeer{local}, Ports: ports}, {To: []networkingv1.NetworkPolicyPeer{{NamespaceSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"kubernetes.io/metadata.name": "kube-system"}}}}, Ports: []networkingv1.NetworkPolicyPort{dns, dnsUDP}}}}}
	operator := networkingv1.NetworkPolicy{ObjectMeta: databaseIdentityMeta(d, uid, "database-vitess-api"), Spec: networkingv1.NetworkPolicySpec{PodSelector: metav1.LabelSelector{MatchLabels: map[string]string{vitessComponentLabel: "operator"}}, PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeEgress}, Egress: apiRules}}
	backupController := networkingv1.NetworkPolicy{ObjectMeta: databaseIdentityMeta(d, uid, "database-vitess-backup-api"), Spec: networkingv1.NetworkPolicySpec{PodSelector: metav1.LabelSelector{MatchLabels: map[string]string{"planetscale.com/cluster": "database", "planetscale.com/component": "vbs-subcontroller"}}, PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeEgress}, Egress: apiRules}}
	gateway := networkingv1.NetworkPolicy{ObjectMeta: databaseIdentityMeta(d, uid, "database-vitess-clients"), Spec: networkingv1.NetworkPolicySpec{PodSelector: metav1.LabelSelector{MatchLabels: map[string]string{vitessComponentLabel: "gateway"}}, PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress}}}
	if d.Status != "restoring" && (d.Recovery == nil || d.Recovery.RestoredAt != nil && d.Recovery.InspectedAt != nil) {
		gateway.Spec.Ingress = []networkingv1.NetworkPolicyIngressRule{{From: []networkingv1.NetworkPolicyPeer{{NamespaceSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"hakopod.io/database-access-" + d.ID: "true"}}}}, Ports: []networkingv1.NetworkPolicyPort{port(3306)}}}
	}
	return []networkingv1.NetworkPolicy{base, operator, gateway, backupController}
}

func (c *Client) vitessNetworkPolicy(ctx context.Context, d database.Resource, before func() error) error {
	ns, err := c.kube.CoreV1().Namespaces().Get(ctx, DatabaseNamespace(d.ID), metav1.GetOptions{})
	if err != nil || ns.UID == "" || ns.Labels[databaseOwner] != d.ID || ns.Labels[managedBy] != "hakopod" {
		return fmt.Errorf("Vitess network namespace ownership changed")
	}
	rules, err := c.databaseAPIEgress(ctx)
	if err != nil {
		return err
	}
	for _, policy := range vitessNetworkPolicies(d, ns.UID, rules) {
		object, err := runtime.DefaultUnstructuredConverter.ToUnstructured(&policy)
		if err != nil {
			return err
		}
		object["apiVersion"], object["kind"] = "networking.k8s.io/v1", "NetworkPolicy"
		if err = c.applyVitessOwnedObject(ctx, d, ns.UID, schema.GroupVersionResource{Group: "networking.k8s.io", Version: "v1", Resource: "networkpolicies"}, &unstructured.Unstructured{Object: object}, before); err != nil {
			return err
		}
	}
	return nil
}

// vitessPodOwned follows only the pinned controller's known ownership chain.
// Labels alone cannot authorize exec, credentials, service selectors or metrics.
func (c *Client) vitessPodOwned(ctx context.Context, pod corev1.Pod, databaseUID types.UID) bool {
	if pod.UID == "" || pod.Namespace == "" || databaseUID == "" {
		return false
	}
	return c.vitessOwnedChain(ctx, pod.Namespace, pod.UID, pod.OwnerReferences, databaseUID)
}

func (c *Client) vitessOwnedChain(ctx context.Context, namespace string, uid types.UID, refs []metav1.OwnerReference, databaseUID types.UID) bool {
	return c.vitessOwnedChainState(ctx, namespace, uid, refs, databaseUID, false)
}

func (c *Client) vitessOwnedChainState(ctx context.Context, namespace string, uid types.UID, refs []metav1.OwnerReference, databaseUID types.UID, allowDeleting bool) bool {
	if namespace == "" || uid == "" || databaseUID == "" {
		return false
	}
	resources := map[string]schema.GroupVersionResource{
		"apps/v1/ReplicaSet": {Group: "apps", Version: "v1", Resource: "replicasets"}, "apps/v1/Deployment": {Group: "apps", Version: "v1", Resource: "deployments"},
		"planetscale.com/v2/VitessShard":          vitessShardResource,
		"planetscale.com/v2/VitessKeyspace":       {Group: "planetscale.com", Version: "v2", Resource: "vitesskeyspaces"},
		"planetscale.com/v2/VitessCell":           {Group: "planetscale.com", Version: "v2", Resource: "vitesscells"},
		"planetscale.com/v2/EtcdLockserver":       {Group: "planetscale.com", Version: "v2", Resource: "etcdlockservers"},
		"planetscale.com/v2/VitessBackupStorage":  {Group: "planetscale.com", Version: "v2", Resource: "vitessbackupstorages"},
		"planetscale.com/v2/VitessBackupSchedule": {Group: "planetscale.com", Version: "v2", Resource: "vitessbackupschedules"},
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
		object, err := c.dynamic.Resource(gvr).Namespace(namespace).Get(ctx, owner.Name, metav1.GetOptions{})
		if err != nil || object.GetUID() != owner.UID || (!allowDeleting && object.GetDeletionTimestamp() != nil) {
			return false
		}
		refs = object.GetOwnerReferences()
	}
	return false
}
