package cluster

import (
	"context"

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

func vitessBackupNetworkPolicy(d database.Resource, namespaceUID types.UID, storage VitessBackupStorage) (*networkingv1.NetworkPolicy, error) {
	if err := storage.Validate(d); err != nil {
		return nil, err
	}
	endpointPort, err := storage.endpointPort()
	if err != nil {
		return nil, err
	}
	tcp, port := corev1.ProtocolTCP, intstr.FromInt(endpointPort)
	peers := make([]networkingv1.NetworkPolicyPeer, 0, len(storage.ApprovedEndpointCIDRs))
	for _, prefix := range storage.ApprovedEndpointCIDRs {
		peers = append(peers, networkingv1.NetworkPolicyPeer{IPBlock: &networkingv1.IPBlock{CIDR: prefix.Masked().String()}})
	}
	selector := metav1.LabelSelector{MatchLabels: map[string]string{"planetscale.com/cluster": "database"}, MatchExpressions: []metav1.LabelSelectorRequirement{{Key: "planetscale.com/component", Operator: metav1.LabelSelectorOpIn, Values: []string{"vttablet", "vtbackup", "vbs-subcontroller"}}}}
	return &networkingv1.NetworkPolicy{ObjectMeta: databaseIdentityMeta(d, namespaceUID, "database-vitess-backup-egress"), Spec: networkingv1.NetworkPolicySpec{PodSelector: selector, PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeEgress}, Egress: []networkingv1.NetworkPolicyEgressRule{{To: peers, Ports: []networkingv1.NetworkPolicyPort{{Protocol: &tcp, Port: &port}}}}}}, nil
}

func (c *Client) prepareVitessBackupNetwork(ctx context.Context, d database.Resource, namespaceUID types.UID, storage VitessBackupStorage, before func() error) error {
	policy, err := vitessBackupNetworkPolicy(d, namespaceUID, storage)
	if err != nil {
		return err
	}
	object, err := runtime.DefaultUnstructuredConverter.ToUnstructured(policy)
	if err != nil {
		return err
	}
	object["apiVersion"], object["kind"] = "networking.k8s.io/v1", "NetworkPolicy"
	return c.applyVitessOwnedObject(ctx, d, namespaceUID, schema.GroupVersionResource{Group: "networking.k8s.io", Version: "v1", Resource: "networkpolicies"}, &unstructured.Unstructured{Object: object}, before)
}
