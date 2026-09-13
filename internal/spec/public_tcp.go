package spec

import (
	"fmt"
	"net/netip"
	"sort"
)

// PublicTCPListener forwards bytes unchanged, including application-managed
// STARTTLS. TargetPort names a declared TCP container port, not an HTTP route.
type PublicTCPListener struct {
	Port        int32    `json:"port" toml:"port"`
	TargetPort  int32    `json:"target_port" toml:"target_port"`
	SourceCIDRs []string `json:"source_cidrs" toml:"source_cidrs"`
}

func validatePublicTCP(app Application) error {
	ports := map[int32]bool{}
	for _, name := range Names(app) {
		svc := app.Services[name]
		if len(svc.PublicTCP) > 0 && svc.Port == 0 {
			return fmt.Errorf("services.%s.public_tcp: declare a primary port for readiness before publishing TCP listeners", name)
		}
		if len(svc.PublicTCP) > 4 {
			return fmt.Errorf("services.%s.public_tcp: at most four listeners", name)
		}
		for i := range svc.PublicTCP {
			listener := &svc.PublicTCP[i]
			if listener.Port < 1 || listener.Port > 65535 || ports[listener.Port] {
				return fmt.Errorf("services.%s.public_tcp: external ports must be unique across the application and between 1 and 65535", name)
			}
			if !ValidPublicTCPPort(listener.Port) {
				return fmt.Errorf("services.%s.public_tcp: port %d is reserved for platform traffic", name, listener.Port)
			}
			if svc.Public && listener.TargetPort == svc.Port {
				return fmt.Errorf("services.%s.public_tcp: an HTTP ingress port cannot also be used as a TCP backend; declare a separate private TCP port", name)
			}
			ports[listener.Port] = true
			matches := 0
			for _, p := range ServicePorts(svc) {
				if p.Protocol == "TCP" && p.TargetPort == listener.TargetPort {
					matches++
				}
			}
			if matches != 1 {
				return fmt.Errorf("services.%s.public_tcp.target_port: reference exactly one declared TCP container port", name)
			}
			if len(listener.SourceCIDRs) < 1 || len(listener.SourceCIDRs) > 16 {
				return fmt.Errorf("services.%s.public_tcp.source_cidrs: specify 1–16 IPv4 CIDRs; use 0.0.0.0/0 explicitly for public access", name)
			}
			seen := map[string]bool{}
			for j, cidr := range listener.SourceCIDRs {
				prefix, err := netip.ParsePrefix(cidr)
				if err != nil || !prefix.Addr().Is4() {
					return fmt.Errorf("services.%s.public_tcp.source_cidrs: use IPv4 CIDR notation", name)
				}
				value := prefix.Masked().String()
				if seen[value] {
					return fmt.Errorf("services.%s.public_tcp.source_cidrs: duplicate network", name)
				}
				seen[value] = true
				listener.SourceCIDRs[j] = value
			}
			sort.Strings(listener.SourceCIDRs)
		}
		sort.Slice(svc.PublicTCP, func(i, j int) bool { return svc.PublicTCP[i].Port < svc.PublicTCP[j].Port })
		app.Services[name] = svc
	}
	if len(ports) > 16 {
		return fmt.Errorf("public_tcp: at most 16 listeners per application")
	}
	return nil
}

// PublicTCPServicePort resolves a validated listener to its private Service port.
func PublicTCPServicePort(svc Service, listener PublicTCPListener) int32 {
	for _, p := range ServicePorts(svc) {
		if p.Protocol == "TCP" && p.TargetPort == listener.TargetPort {
			return p.Port
		}
	}
	return 0
}

func HasPublicTCP(app Application) bool {
	for _, svc := range app.Services {
		if len(svc.PublicTCP) != 0 {
			return true
		}
	}
	return false
}

// ValidPublicTCPPort excludes management and HTTP ports from public TCP ingress.
func ValidPublicTCPPort(port int32) bool {
	if port < 1 || port > 65535 {
		return false
	}
	switch port {
	case 22, 53, 80, 443, 1024, 1042, 2379, 2380, 6060, 6443, 8080, 8443, 10250:
		return false
	}
	return true
}
