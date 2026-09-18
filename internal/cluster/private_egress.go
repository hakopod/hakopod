package cluster

import (
	"bytes"
	"fmt"
	"io"
	"net/netip"
	"os"

	"github.com/hakopod/hakopod/internal/spec"
	"github.com/pelletier/go-toml/v2"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

// PrivateEgressBinding is administrator-owned authorization, never application
// input. A reference grants only the listed destinations to exact service scopes.
type PrivateEgressBinding struct {
	Name        string   `toml:"name" json:"name"`
	Project     string   `toml:"project" json:"project"`
	Environment string   `toml:"environment" json:"environment"`
	Application string   `toml:"application" json:"application"`
	Services    []string `toml:"services" json:"services"`
	CIDRs       []string `toml:"cidrs" json:"cidrs"`
	Ports       []int32  `toml:"ports" json:"ports"`
}

func ValidatePrivateEgressBindings(bindings []PrivateEgressBinding) error {
	if len(bindings) > 128 {
		return fmt.Errorf("private egress: at most 128 bindings")
	}
	seen := map[string]bool{}
	for _, b := range bindings {
		if !awsScopeName.MatchString(b.Name) || seen[b.Name] {
			return fmt.Errorf("private egress: unique lowercase binding names of at most 40 characters are required")
		}
		seen[b.Name] = true
		for _, scope := range []string{b.Project, b.Environment, b.Application} {
			if !awsScopeName.MatchString(scope) {
				return fmt.Errorf("private egress %s: exact project, environment and application names are required", b.Name)
			}
		}
		if len(b.Services) < 1 || len(b.Services) > 32 || len(b.CIDRs) < 1 || len(b.CIDRs) > 16 || len(b.Ports) < 1 || len(b.Ports) > 16 {
			return fmt.Errorf("private egress %s: require 1–32 services, 1–16 CIDRs and 1–16 TCP ports", b.Name)
		}
		services, cidrs, ports := map[string]bool{}, map[string]bool{}, map[int32]bool{}
		for _, service := range b.Services {
			if !awsScopeName.MatchString(service) || services[service] {
				return fmt.Errorf("private egress %s: services require unique exact names", b.Name)
			}
			services[service] = true
		}
		for _, value := range b.CIDRs {
			p, err := netip.ParsePrefix(value)
			if err != nil || !p.Addr().Is4() || !p.Addr().IsPrivate() || p.Bits() < 16 || p != p.Masked() || cidrs[value] {
				return fmt.Errorf("private egress %s: use unique canonical RFC1918 IPv4 CIDRs between /16 and /32", b.Name)
			}
			// The supported installer owns these fixed pod and Service networks.
			// Arbitrary external CIDRs must never substitute for peer authorization.
			for _, reserved := range []string{"10.42.0.0/16", "10.43.0.0/16"} {
				if p.Overlaps(netip.MustParsePrefix(reserved)) {
					return fmt.Errorf("private egress %s: cluster pod and Service networks are not destinations", b.Name)
				}
			}
			cidrs[value] = true
		}
		for _, port := range b.Ports {
			if port < 1 || port > 65535 || ports[port] {
				return fmt.Errorf("private egress %s: use unique TCP ports between 1 and 65535", b.Name)
			}
			ports[port] = true
		}
	}
	return nil
}

func ReadPrivateEgressBindingsFile(path string) ([]PrivateEgressBinding, error) {
	if path == "" {
		return nil, nil
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("cannot read private egress bindings file")
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > 64<<10 || info.Mode().Perm()&0022 != 0 {
		return nil, fmt.Errorf("private egress bindings must be a regular file of at most 64 KiB, not writable by group or others")
	}
	data, err := io.ReadAll(io.LimitReader(file, (64<<10)+1))
	if err != nil || len(data) > 64<<10 {
		return nil, fmt.Errorf("cannot read bounded private egress bindings file")
	}
	var config struct {
		SchemaVersion int                    `toml:"schema_version"`
		Bindings      []PrivateEgressBinding `toml:"bindings"`
	}
	if err := toml.NewDecoder(bytes.NewReader(data)).DisallowUnknownFields().Decode(&config); err != nil || config.SchemaVersion != 1 {
		return nil, fmt.Errorf("private egress requires strict TOML with schema_version = 1")
	}
	if err := ValidatePrivateEgressBindings(config.Bindings); err != nil {
		return nil, err
	}
	return config.Bindings, nil
}

// Resolve again during reconciliation; a previously accepted revision is not
// authority to request an unconfigured destination after an operator change.
func (c *Client) resolvePrivateEgress(t Target) (map[string][]PrivateEgressBinding, error) {
	result := map[string][]PrivateEgressBinding{}
	if err := ValidatePrivateEgressBindings(c.options.PrivateEgressBindings); err != nil {
		return nil, err
	}
	for _, name := range spec.Names(t.Spec) {
		for _, ref := range t.Spec.Services[name].PrivateEgress {
			if c.CloudMode() || c.options.WorkloadPolicy != nil {
				return nil, fmt.Errorf("%w: %s.private_egress is available only on self-hosted installations", ErrCloudLimit, name)
			}
			found := false
			for _, b := range c.options.PrivateEgressBindings {
				if b.Name != ref || b.Project != t.Project || b.Environment != t.Environment || b.Application != t.Spec.Name {
					continue
				}
				for _, service := range b.Services {
					if service == name {
						result[name] = append(result[name], b)
						found = true
						break
					}
				}
			}
			if !found {
				return nil, fmt.Errorf("%s.private_egress: destination %s is not approved for this project, environment, application and service; ask the installation administrator", name, ref)
			}
		}
	}
	return result, nil
}

func privateEgressRules(bindings []PrivateEgressBinding) []networkingv1.NetworkPolicyEgressRule {
	rules := make([]networkingv1.NetworkPolicyEgressRule, 0, len(bindings))
	for _, b := range bindings {
		rule := networkingv1.NetworkPolicyEgressRule{}
		for _, cidr := range b.CIDRs {
			rule.To = append(rule.To, networkingv1.NetworkPolicyPeer{IPBlock: &networkingv1.IPBlock{CIDR: cidr}})
		}
		for _, port := range b.Ports {
			rule.Ports = append(rule.Ports, networkingv1.NetworkPolicyPort{Protocol: ptr(corev1.ProtocolTCP), Port: ptr(intstr.FromInt32(port))})
		}
		rules = append(rules, rule)
	}
	return rules
}
