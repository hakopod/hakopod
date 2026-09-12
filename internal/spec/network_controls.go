package spec

import (
	"fmt"
	"sort"
	"strings"

	"k8s.io/apimachinery/pkg/util/validation"
)

// Port adds a private service endpoint. Public HTTP still uses Service.Port.
type Port struct {
	Name       string `json:"name" toml:"name"`
	Port       int32  `json:"port" toml:"port"`
	TargetPort int32  `json:"target_port" toml:"target_port"`
	Protocol   string `json:"protocol" toml:"protocol"`
}

type NetworkAccess struct {
	// An explicit empty list denies peer traffic; omission allows network members.
	From []string `json:"from" toml:"from"`
}

func ServicePorts(s Service) []Port {
	out := make([]Port, 0, len(s.Ports)+1)
	if s.Port != 0 {
		out = append(out, Port{Name: "service", Port: s.Port, TargetPort: s.Port, Protocol: "TCP"})
	}
	return append(out, s.Ports...)
}

func AllowsPeer(app Application, source, destination string) bool {
	src, srcOK := app.Services[source]
	dst, dstOK := app.Services[destination]
	if !srcOK || !dstOK {
		return false
	}
	shared := false
	for _, a := range src.Networks {
		for _, b := range dst.Networks {
			shared = shared || a == b
		}
	}
	if !shared {
		return false
	}
	if dst.NetworkAccess == nil {
		return true
	}
	for _, name := range dst.NetworkAccess.From {
		if source == name {
			return true
		}
	}
	return false
}

func validateNetworkAccess(app Application) error {
	for _, name := range Names(app) {
		s := app.Services[name]
		if len(s.Ports) > 15 {
			return fmt.Errorf("services.%s.ports: at most 15 additional ports", name)
		}
		names, endpoints := map[string]bool{}, map[string]bool{}
		if s.Port != 0 {
			names["service"], endpoints[fmt.Sprintf("TCP/%d", s.Port)] = true, true
		}
		for i := range s.Ports {
			p := &s.Ports[i]
			p.Protocol = strings.ToUpper(p.Protocol)
			if p.Protocol == "" {
				p.Protocol = "TCP"
			}
			if p.TargetPort == 0 {
				p.TargetPort = p.Port
			}
			if len(validation.IsValidPortName(p.Name)) != 0 || names[p.Name] {
				return fmt.Errorf("services.%s.ports: use unique port names (at most 15 lowercase letters, digits or hyphens)", name)
			}
			if p.Port < 1 || p.Port > 65535 || p.TargetPort < 1 || p.TargetPort > 65535 || (p.Protocol != "TCP" && p.Protocol != "UDP") {
				return fmt.Errorf("services.%s.ports: use TCP or UDP and ports between 1 and 65535", name)
			}
			key := fmt.Sprintf("%s/%d", p.Protocol, p.Port)
			if endpoints[key] {
				return fmt.Errorf("services.%s.ports: duplicate protocol/port", name)
			}
			names[p.Name], endpoints[key] = true, true
		}
		sort.Slice(s.Ports, func(i, j int) bool { return s.Ports[i].Name < s.Ports[j].Name })
		if a := s.NetworkAccess; a != nil {
			if len(a.From) > 20 {
				return fmt.Errorf("services.%s.network_access.from: at most 20 services", name)
			}
			seen := map[string]bool{}
			for _, peer := range a.From {
				if _, exists := app.Services[peer]; !exists || seen[peer] {
					return fmt.Errorf("services.%s.network_access.from: unknown or repeated service", name)
				}
				seen[peer] = true
				if !AllowsPeer(app, peer, name) {
					return fmt.Errorf("services.%s.network_access.from: %s must share a network", name, peer)
				}
			}
			if a.From == nil {
				a.From = []string{}
			}
			sort.Strings(a.From)
		}
		app.Services[name] = s
	}
	return nil
}
