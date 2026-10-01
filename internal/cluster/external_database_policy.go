package cluster

import (
	"context"
	"fmt"
	"net/netip"
	"net/url"
	"reflect"
	"sort"
	"strconv"
	"strings"

	"github.com/hakopod/hakopod/internal/externaldatabase"
	"github.com/hakopod/hakopod/internal/spec"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

func externalDatabaseAddressAllowed(t Target, raw string) bool {
	ip, err := netip.ParseAddr(raw)
	if err != nil || ip != ip.Unmap() || !externaldatabase.PublicAddress(ip) {
		return false
	}
	if t.policy != nil {
		for _, raw := range t.policy.DeniedEgressCIDRs {
			prefix, err := netip.ParsePrefix(raw)
			if err != nil {
				return false
			}
			if prefix.Contains(ip) {
				return false
			}
			if prefix.Addr().Is4In6() {
				if prefix.Bits() < 96 {
					return false
				}
				normalized := netip.PrefixFrom(prefix.Addr().Unmap(), prefix.Bits()-96)
				if normalized.Contains(ip) {
					return false
				}
			}
		}
	}
	return true
}

func validateExternalDatabaseSnapshot(t Target, b spec.Binding, c DatabaseConnection) error {
	if b.ExternalDatabase == "" || b.ExternalDatabaseRevision < 1 || b.ManagedDatabase != "" || b.Protocol != "mysql" && b.Protocol != "postgres" || c.CA != "" || len(c.URL) == 0 || len(c.URL) > 16<<10 || len(c.ExternalIPs) == 0 || len(c.ExternalIPs) > 16 || b.Protocol == "mysql" && c.Port != 3306 || b.Protocol == "postgres" && c.Port != 5432 && c.Port != 6432 {
		return fmt.Errorf("external database connection snapshot is incomplete")
	}
	u, err := url.Parse(c.URL)
	if err != nil || u.User == nil || u.Hostname() == "" || u.Port() != strconv.Itoa(int(c.Port)) || u.Fragment != "" || u.Opaque != "" {
		return fmt.Errorf("external database connection snapshot is invalid")
	}
	engine := "mysql"
	if b.Protocol == "postgres" {
		engine = "postgresql"
	}
	endpoint := externaldatabase.Spec{SchemaVersion: 1, Name: "snapshot", Provider: "planetscale", Engine: engine, Host: u.Hostname(), Port: int(c.Port), Database: strings.TrimPrefix(u.Path, "/")}
	password, hasPassword := u.User.Password()
	if endpoint.Validate() != nil || !hasPassword || (externaldatabase.Credentials{Username: u.User.Username(), Password: password}).Validate() != nil {
		return fmt.Errorf("external database connection endpoint is invalid")
	}
	if b.Protocol == "mysql" && (u.Scheme != "mysql" || u.Query().Get("tls") != "true" || u.Query().Get("ssl-mode") != "VERIFY_IDENTITY") || b.Protocol == "postgres" && (u.Scheme != "postgresql" || u.Query().Get("sslmode") != "verify-full" || u.Query().Get("sslrootcert") != "system") {
		return fmt.Errorf("external database connection requires verified TLS")
	}
	for _, ip := range c.ExternalIPs {
		if !externalDatabaseAddressAllowed(t, ip) {
			return fmt.Errorf("external database address is denied by the runtime network policy")
		}
	}
	return nil
}

// Called after generic Cloud egress restrictions so database ports can only
// reach these verified addresses and never widen a shared Internet rule.
func externalDatabaseEgressRules(t Target, name string) []networkingv1.NetworkPolicyEgressRule {
	rules := []networkingv1.NetworkPolicyEgressRule{}
	tcp := corev1.ProtocolTCP
	seen := map[string]bool{}
	variables := make([]string, 0, len(t.Spec.Services[name].Bindings))
	for variable := range t.Spec.Services[name].Bindings {
		variables = append(variables, variable)
	}
	sort.Strings(variables)
	for _, variable := range variables {
		b := t.Spec.Services[name].Bindings[variable]
		if b.ExternalDatabase == "" {
			continue
		}
		c, ok := t.databaseConnections[name][variable]
		if !ok || validateExternalDatabaseSnapshot(t, b, c) != nil {
			continue
		}
		ips := append([]string(nil), c.ExternalIPs...)
		sort.Strings(ips)
		for _, raw := range ips {
			key := raw + ":" + strconv.Itoa(int(c.Port))
			if seen[key] {
				continue
			}
			seen[key] = true
			ip, _ := netip.ParseAddr(raw)
			prefix := netip.PrefixFrom(ip, ip.BitLen()).String()
			rules = append(rules, networkingv1.NetworkPolicyEgressRule{To: []networkingv1.NetworkPolicyPeer{{IPBlock: &networkingv1.IPBlock{CIDR: prefix}}}, Ports: []networkingv1.NetworkPolicyPort{{Protocol: &tcp, Port: ptr(intstr.FromInt32(c.Port))}}})
		}
	}
	return rules
}

// RefreshExternalDatabaseEgress handles provider DNS changes under the existing
// durable application maintenance claim. Failed verification preserves the last
// policy. It does not change application credentials or accept new revisions.
func (c *Client) RefreshExternalDatabaseEgress(ctx context.Context, t Target, emit func(Event), name string) error {
	svc, exists := t.Spec.Services[name]
	if !exists {
		return fmt.Errorf("external database service is unavailable")
	}
	bound := false
	for _, b := range svc.Bindings {
		bound = bound || b.ExternalDatabase != ""
	}
	if !bound {
		return nil
	}
	var err error
	if err = c.resolveVirtualNetworks(ctx, &t); err != nil {
		return err
	}
	if t.policy, err = c.workloadPolicy(ctx, t); err != nil {
		return err
	}
	selected := t
	selected.Spec.Services = map[string]spec.Service{name: svc}
	if err = c.snapshotDatabaseBindings(ctx, &selected); err != nil {
		return err
	}
	t.databaseConnections = selected.databaseConnections
	if t.privateEgress, err = c.resolvePrivateEgress(t); err != nil {
		return err
	}
	if t.containerDaemon, err = c.resolveContainerDaemons(t); err != nil {
		return err
	}
	if t.serverlessGatewayIPs, err = c.serverlessGatewaySources(ctx, t); err != nil {
		return err
	}
	api := c.kube.NetworkingV1().NetworkPolicies(Namespace(t.ApplicationID))
	for _, wanted := range policies(t) {
		if wanted.Name != "hakopod-service-"+name {
			continue
		}
		current, err := api.Get(ctx, wanted.Name, metav1.GetOptions{})
		if err != nil {
			return err
		}
		if err = owned(current, t); err != nil {
			return err
		}
		if reflect.DeepEqual(current.Spec, wanted.Spec) {
			return nil
		}
		if err = beforeStep(ctx, t); err != nil {
			return err
		}
		current.Spec, current.Labels = wanted.Spec, wanted.Labels
		if _, err = api.Update(ctx, current, metav1.UpdateOptions{}); err != nil {
			return err
		}
		if emit != nil {
			emit(Event{Type: "policy", Message: "External database destinations refreshed after verified TLS and query checks", Service: name})
		}
		return nil
	}
	return fmt.Errorf("external database service policy is unavailable")
}
