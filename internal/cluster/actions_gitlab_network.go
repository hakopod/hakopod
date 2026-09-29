package cluster

import (
	"context"
	"errors"
	"net/netip"
	"net/url"
	"slices"
	"strconv"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

func gitlabSlotNetworkPolicy(t Target, service, id string, runtime GitLabActionsRuntime, denied []string, allow bool) *networkingv1.NetworkPolicy {
	labels := labelsFor(t, service)
	labels[gitlabActionsProviderLabel], labels[gitlabActionsSlotLabel] = "gitlab", id
	p := &networkingv1.NetworkPolicy{ObjectMeta: metav1.ObjectMeta{Name: "actions-" + id, Namespace: Namespace(t.ApplicationID), Labels: labels}, Spec: networkingv1.NetworkPolicySpec{PodSelector: metav1.LabelSelector{MatchLabels: labels}, PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress, networkingv1.PolicyTypeEgress}}}
	p.Spec.Egress = []networkingv1.NetworkPolicyEgressRule{{
		To:    []networkingv1.NetworkPolicyPeer{{NamespaceSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"kubernetes.io/metadata.name": "kube-system"}}, PodSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"k8s-app": "kube-dns"}}}},
		Ports: []networkingv1.NetworkPolicyPort{{Protocol: ptr(corev1.ProtocolUDP), Port: ptr(intstr.FromInt32(53))}, {Protocol: ptr(corev1.ProtocolTCP), Port: ptr(intstr.FromInt32(53))}},
	}}
	if !allow {
		return p
	}
	external := false
	for _, network := range t.Spec.Services[service].Networks {
		external = external || !t.Spec.Networks[network].Internal
	}
	if !external {
		return p
	}
	blocked := append(slices.Clone(managedActionsSpecialNetworks), "10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16")
	blocked = append(blocked, denied...)
	if t.policy != nil {
		blocked = append(blocked, t.policy.DeniedEgressCIDRs...)
	}
	var exceptions []string
	for _, raw := range blocked {
		if prefix, err := netip.ParsePrefix(raw); err == nil && prefix.Addr().Is4() && prefix.Bits() > 0 {
			exceptions = append(exceptions, raw)
		}
	}
	slices.Sort(exceptions)
	exceptions = slices.Compact(exceptions)
	rule := networkingv1.NetworkPolicyEgressRule{To: []networkingv1.NetworkPolicyPeer{{IPBlock: &networkingv1.IPBlock{CIDR: "0.0.0.0/0", Except: exceptions}}}}
	if t.policy != nil {
		for _, port := range t.policy.EgressPorts {
			rule.Ports = append(rule.Ports, networkingv1.NetworkPolicyPort{Protocol: ptr(corev1.ProtocolTCP), Port: ptr(intstr.FromInt32(port))})
		}
	}
	p.Spec.Egress = append(p.Spec.Egress, rule)
	for _, destination := range runtime.PrivateDestinations {
		u, _ := url.Parse(destination.Origin)
		port := 443
		if u.Port() != "" {
			port, _ = strconv.Atoi(u.Port())
		}
		for _, address := range destination.Addresses {
			p.Spec.Egress = append(p.Spec.Egress, networkingv1.NetworkPolicyEgressRule{To: []networkingv1.NetworkPolicyPeer{{IPBlock: &networkingv1.IPBlock{CIDR: address}}}, Ports: []networkingv1.NetworkPolicyPort{{Protocol: ptr(corev1.ProtocolTCP), Port: ptr(intstr.FromInt(port))}}})
		}
	}
	return p
}

// Each slot owns one exact network policy. Rotation never changes another
// slot's grant, and an unsafe fresh inventory revokes egress before returning.
func (c *Client) RefreshGitLabActionsNetwork(ctx context.Context, t Target, service, id string, runtime GitLabActionsRuntime) error {
	if !gitlabActionsSlotID.MatchString(id) || !gitlabActionsServiceName.MatchString(service) {
		return errors.New("GitLab network policy slot is invalid")
	}
	if err := ValidateGitLabActionsRuntime(runtime); err != nil {
		return err
	}
	var denied []string
	var safetyErr error
	if runtime.ControlPlaneTrust != nil {
		denied, safetyErr = c.validateGitLabPrivateNetworks(ctx, runtime)
	}
	if c.options.WorkloadPolicy != nil {
		if len(runtime.PrivateDestinations) > 0 {
			safetyErr = errors.New("GitLab private grants are unavailable on a shared workload runtime")
		}
		policy, err := c.options.WorkloadPolicy(ctx, t.Project, t.Environment, t.Spec)
		if err != nil {
			safetyErr = errors.New("GitLab workload egress policy is unavailable")
		} else {
			t.policy = &policy
		}
	}
	wanted := gitlabSlotNetworkPolicy(t, service, id, runtime, denied, safetyErr == nil)
	if err := beforeStep(ctx, t); err != nil {
		return err
	}
	api := c.kube.NetworkingV1().NetworkPolicies(wanted.Namespace)
	current, err := api.Get(ctx, wanted.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, err = api.Create(ctx, wanted, metav1.CreateOptions{})
	} else if err == nil {
		if owned(current, t) != nil || current.Labels[gitlabActionsProviderLabel] != "gitlab" || current.Labels[gitlabActionsSlotLabel] != id || current.Labels[serviceKey] != service || current.DeletionTimestamp != nil {
			return errors.New("GitLab network policy belongs to a different slot")
		}
		if !apiequality.Semantic.DeepEqual(current.Spec, wanted.Spec) {
			if err := beforeStep(ctx, t); err != nil {
				return err
			}
			current.Spec, current.Labels = wanted.Spec, wanted.Labels
			_, err = api.Update(ctx, current, metav1.UpdateOptions{})
		}
	}
	if err != nil {
		return errors.New("GitLab slot network policy could not be reconciled")
	}
	return safetyErr
}

func (c *Client) deleteGitLabSlotNetwork(ctx context.Context, t Target, id string) error {
	api := c.kube.NetworkingV1().NetworkPolicies(Namespace(t.ApplicationID))
	current, err := api.Get(ctx, "actions-"+id, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return errors.New("GitLab slot network policy could not be inspected")
	}
	if owned(current, t) != nil || current.Labels[gitlabActionsProviderLabel] != "gitlab" || current.Labels[gitlabActionsSlotLabel] != id {
		return errors.New("GitLab network policy belongs to a different slot")
	}
	if err := beforeStep(ctx, t); err != nil {
		return err
	}
	if err = api.Delete(ctx, current.Name, deleteOptions(current)); err != nil && !apierrors.IsNotFound(err) {
		return errors.New("GitLab slot network policy could not be removed")
	}
	if _, err = api.Get(ctx, current.Name, metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		return errors.New("GitLab slot network policy absence is not confirmed")
	}
	return nil
}
