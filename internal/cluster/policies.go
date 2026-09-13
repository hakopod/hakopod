package cluster

import (
	"context"
	"fmt"
	"sort"

	"github.com/hakopod/hakopod/internal/spec"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

func labelsFor(t Target, service string) map[string]string {
	labels := map[string]string{managedBy: "hakopod", ownerKey: ownerID(t.ApplicationID)}
	if service != "" {
		labels[serviceKey] = service
	}
	return labels
}

func selectorFor(t Target, service string) metav1.LabelSelector {
	return metav1.LabelSelector{MatchLabels: labelsFor(t, service)}
}

func owned(meta metav1.Object, t Target) error {
	if meta.GetLabels()[managedBy] != "hakopod" || meta.GetLabels()[ownerKey] != ownerID(t.ApplicationID) {
		return fmt.Errorf("refusing to mutate unowned %s/%s", meta.GetNamespace(), meta.GetName())
	}
	return nil
}

func networkKey(name string) string { return "hakopod.io/net-" + name }

// policies allows only declared service ports between members of a shared
// network. Both source egress and destination ingress must permit the flow.
func policies(t Target) []*networkingv1.NetworkPolicy {
	ns := Namespace(t.ApplicationID)
	policies := []*networkingv1.NetworkPolicy{{
		ObjectMeta: metav1.ObjectMeta{Name: "hakopod-default-deny", Namespace: ns, Labels: labelsFor(t, "")},
		Spec:       networkingv1.NetworkPolicySpec{PodSelector: metav1.LabelSelector{}, PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress, networkingv1.PolicyTypeEgress}},
	}}
	for _, name := range spec.Names(t.Spec) {
		svc := t.Spec.Services[name]
		policy := &networkingv1.NetworkPolicy{
			ObjectMeta: metav1.ObjectMeta{Name: "hakopod-service-" + name, Namespace: ns, Labels: labelsFor(t, name)},
			Spec:       networkingv1.NetworkPolicySpec{PodSelector: selectorFor(t, name), PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress, networkingv1.PolicyTypeEgress}},
		}
		// DNS is scoped to kube-dns pods, never all kube-system addresses.
		udp, tcp := corev1.ProtocolUDP, corev1.ProtocolTCP
		dnsPort := intstr.FromInt32(53)
		policy.Spec.Egress = append(policy.Spec.Egress, networkingv1.NetworkPolicyEgressRule{
			To:    []networkingv1.NetworkPolicyPeer{{NamespaceSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"kubernetes.io/metadata.name": "kube-system"}}, PodSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"k8s-app": "kube-dns"}}}},
			Ports: []networkingv1.NetworkPolicyPort{{Protocol: &udp, Port: &dnsPort}, {Protocol: &tcp, Port: &dnsPort}},
		})
		external := false
		for _, network := range svc.Networks {
			if !t.Spec.Networks[network].Internal {
				external = true
			}
		}
		for _, source := range spec.Names(t.Spec) {
			if !spec.AllowsPeer(t.Spec, source, name) {
				continue
			}
			ports := networkPorts(svc)
			if len(ports) == 0 {
				continue
			}
			selector := selectorFor(t, source)
			policy.Spec.Ingress = append(policy.Spec.Ingress, networkingv1.NetworkPolicyIngressRule{From: []networkingv1.NetworkPolicyPeer{{PodSelector: &selector}}, Ports: ports})
		}
		for _, destName := range spec.Names(t.Spec) {
			dest := t.Spec.Services[destName]
			ports := networkPorts(dest)
			if len(ports) == 0 || !spec.AllowsPeer(t.Spec, name, destName) {
				continue
			}
			selector := selectorFor(t, destName)
			policy.Spec.Egress = append(policy.Spec.Egress, networkingv1.NetworkPolicyEgressRule{
				To:    []networkingv1.NetworkPolicyPeer{{PodSelector: &selector}},
				Ports: ports,
			})
		}
		if external {
			// IPv4-only K3s is the supported installation profile. RFC1918,
			// CGNAT, metadata/link-local, loopback, multicast and reserved ranges
			// cannot be reached via the external egress exception. In particular
			// it cannot bypass per-service rules for pod or Service CIDRs.
			policy.Spec.Egress = append(policy.Spec.Egress, networkingv1.NetworkPolicyEgressRule{To: []networkingv1.NetworkPolicyPeer{{IPBlock: &networkingv1.IPBlock{
				CIDR: "0.0.0.0/0", Except: []string{"0.0.0.0/8", "10.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8", "169.254.0.0/16", "172.16.0.0/12", "192.0.0.0/24", "192.168.0.0/16", "198.18.0.0/15", "224.0.0.0/4", "240.0.0.0/4"},
			}}}})
		}
		if svc.Public {
			port := intstr.FromInt32(svc.Port)
			policy.Spec.Ingress = append(policy.Spec.Ingress, networkingv1.NetworkPolicyIngressRule{
				From:  []networkingv1.NetworkPolicyPeer{{NamespaceSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"kubernetes.io/metadata.name": "haproxy-controller"}}, PodSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"app.kubernetes.io/name": "kubernetes-ingress", "app.kubernetes.io/instance": "hakopod-ingress"}}}},
				Ports: []networkingv1.NetworkPolicyPort{{Protocol: &tcp, Port: &port}},
			})
		}
		virtualNetworkRules(t, svc, policy)
		policies = append(policies, policy)
	}
	return policies
}

func networkPorts(service spec.Service) []networkingv1.NetworkPolicyPort {
	ports := make([]networkingv1.NetworkPolicyPort, 0, len(service.Ports)+1)
	for _, p := range spec.ServicePorts(service) {
		port := intstr.FromInt32(p.TargetPort)
		protocol := corev1.Protocol(p.Protocol)
		ports = append(ports, networkingv1.NetworkPolicyPort{Protocol: &protocol, Port: &port})
	}
	return ports
}

func shareNetwork(a, b []string) bool {
	for _, x := range a {
		for _, y := range b {
			if x == y {
				return true
			}
		}
	}
	return false
}

func (c *Client) applyPolicies(ctx context.Context, t Target) error {
	api := c.kube.NetworkingV1().NetworkPolicies(Namespace(t.ApplicationID))
	for _, wanted := range policies(t) {
		if err := beforeStep(ctx, t); err != nil {
			return err
		}
		current, err := api.Get(ctx, wanted.Name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			_, err = api.Create(ctx, wanted, metav1.CreateOptions{})
		} else if err == nil {
			if err = owned(current, t); err != nil {
				return err
			}
			current.Spec, current.Labels = wanted.Spec, wanted.Labels
			_, err = api.Update(ctx, current, metav1.UpdateOptions{})
		}
		if err != nil {
			return fmt.Errorf("apply network policy %s: %w", wanted.Name, err)
		}
	}
	// Remove policies belonging to removed services before any pod changes.
	items, err := api.List(ctx, metav1.ListOptions{LabelSelector: managedBy + "=hakopod," + ownerKey + "=" + ownerID(t.ApplicationID), Limit: 100})
	if err != nil {
		return err
	}
	if items.Continue != "" {
		return fmt.Errorf("too many owned network policies; manual diagnosis required")
	}
	sort.Slice(items.Items, func(i, j int) bool { return items.Items[i].Name < items.Items[j].Name })
	for _, item := range items.Items {
		if service := item.Labels[serviceKey]; service != "" {
			if _, ok := t.Spec.Services[service]; !ok {
				if err := beforeStep(ctx, t); err != nil {
					return err
				}
				uid, version := item.UID, item.ResourceVersion
				if err := api.Delete(ctx, item.Name, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &uid, ResourceVersion: &version}}); err != nil && !apierrors.IsNotFound(err) {
					return err
				}
			}
		}
	}
	return nil
}
