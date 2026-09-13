package cluster

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"

	"github.com/hakopod/hakopod/internal/spec"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const scopeKey = "hakopod.io/scope"
const applicationNameKey = "hakopod.io/application-name"

func scopeLabel(project, environment string) string {
	hash := sha256.Sum256([]byte(project + "/" + environment))
	return fmt.Sprintf("%x", hash[:16])
}

func sharedNetworkKey(t Target, identity string) string {
	hash := sha256.Sum256([]byte(t.Project + "/" + t.Environment + "/" + identity))
	return fmt.Sprintf("hakopod.io/shared-%x", hash[:16])
}

func (c *Client) resolveVirtualNetworks(ctx context.Context, target *Target) error {
	target.SharedNetworks = nil
	for _, network := range target.Spec.Networks {
		if network.VirtualNetwork == "" {
			continue
		}
		if target.Project == "" || target.Environment == "" || c.options.VirtualNetworks == nil {
			return errors.New("virtual network grants cannot be verified")
		}
		identities, err := c.options.VirtualNetworks(ctx, target.Project, target.Environment, target.Spec)
		if err != nil {
			return err
		}
		for local, definition := range target.Spec.Networks {
			if definition.VirtualNetwork != "" && identities[local] == "" {
				return errors.New("virtual network grant is missing")
			}
		}
		target.SharedNetworks = identities
		return nil
	}
	return nil
}

func virtualNetworkRules(t Target, service spec.Service, policy *networkingv1.NetworkPolicy) {
	seen := map[string]bool{}
	for _, local := range service.Networks {
		identity := t.SharedNetworks[local]
		if identity == "" || seen[identity] {
			continue
		}
		seen[identity] = true
		peer := networkingv1.NetworkPolicyPeer{
			// Namespace and pod selectors are ANDed in one peer. Other scopes and
			// this application's local rules cannot be bypassed by shared labels.
			NamespaceSelector: &metav1.LabelSelector{
				MatchLabels:      map[string]string{managedBy: "hakopod", scopeKey: scopeLabel(t.Project, t.Environment)},
				MatchExpressions: []metav1.LabelSelectorRequirement{{Key: ownerKey, Operator: metav1.LabelSelectorOpNotIn, Values: []string{ownerID(t.ApplicationID)}}},
			},
			PodSelector: &metav1.LabelSelector{MatchLabels: map[string]string{managedBy: "hakopod", sharedNetworkKey(t, identity): "true"}},
		}
		// The destination's ingress policy permits only its declared target
		// ports. No node port, public ingress or blanket namespace access is added.
		policy.Spec.Egress = append(policy.Spec.Egress, networkingv1.NetworkPolicyEgressRule{To: []networkingv1.NetworkPolicyPeer{peer}})
		ports := networkPorts(service)
		if len(ports) == 0 {
			continue
		}
		if service.NetworkAccess == nil {
			policy.Spec.Ingress = append(policy.Spec.Ingress, networkingv1.NetworkPolicyIngressRule{From: []networkingv1.NetworkPolicyPeer{peer}, Ports: ports})
			continue
		}
		for _, allowed := range service.NetworkAccess.FromApplications {
			parts := strings.Split(allowed, "/")
			if len(parts) != 2 {
				continue
			}
			selected := peer.DeepCopy()
			selected.PodSelector.MatchLabels[applicationNameKey] = parts[0]
			selected.PodSelector.MatchLabels[serviceKey] = parts[1]
			policy.Spec.Ingress = append(policy.Spec.Ingress, networkingv1.NetworkPolicyIngressRule{From: []networkingv1.NetworkPolicyPeer{*selected}, Ports: ports})
		}
	}
}
