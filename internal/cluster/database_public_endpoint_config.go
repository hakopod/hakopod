package cluster

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net"
	"net/netip"
	"slices"
	"strconv"
	"strings"

	"github.com/hakopod/hakopod/internal/database"
	"k8s.io/apimachinery/pkg/util/validation"
)

// ValidateDatabasePublicEndpointOptions validates only trusted installation
// configuration. Tenant input never supplies an address, hostname or port.
func ValidateDatabasePublicEndpointOptions(options Options) error {
	configured := options.DatabasePublicAddress != "" || options.DatabasePublicDomain != "" || len(options.DatabasePublicPorts) > 0
	complete := options.DatabasePublicAddress != "" && options.DatabasePublicDomain != "" && len(options.DatabasePublicPorts) > 0
	if configured && !complete {
		return fmt.Errorf("database public endpoints require address, domain and ports together")
	}
	if !configured {
		return nil
	}
	if options.DeploymentMode != DeploymentSelfHosted && (options.DeploymentMode != DeploymentManagedCloud || !options.ManagedDatabasePublicEndpoints) {
		return fmt.Errorf("database public endpoints require self-hosted mode or trusted managed database endpoint authority")
	}
	address, err := netip.ParseAddr(options.DatabasePublicAddress)
	if err != nil || !address.Is4() || address.IsUnspecified() || address.IsMulticast() {
		return fmt.Errorf("database public endpoint address must be a usable IPv4 address")
	}
	if len(options.DatabasePublicDomain) > 220 || len(validation.IsDNS1123Subdomain(options.DatabasePublicDomain)) != 0 {
		return fmt.Errorf("database public endpoint domain must be a DNS domain of at most 220 characters")
	}
	if len(options.DatabasePublicPorts) > database.MaxPublicEndpoints*64 {
		return fmt.Errorf("database public endpoint port inventory exceeds its bound")
	}
	seen := map[int32]bool{}
	for _, port := range options.DatabasePublicPorts {
		if options.DeploymentMode == DeploymentSelfHosted && !slices.Contains(options.PublicTCPPorts, port) {
			return fmt.Errorf("database public endpoint port %d is not provisioned in the public TCP pool", port)
		}
		if seen[port] {
			return fmt.Errorf("database public endpoint ports must be unique")
		}
		seen[port] = true
	}
	return nil
}

// Capability discovery is bounded and read-only. DNS and native health are
// checked again by planning and publication; discovery does not reserve a port.
func (c *Client) DatabasePublicEndpointCapabilities(spec database.Spec) database.PublicEndpointCapabilities {
	result := database.PublicEndpointCapabilitiesFor(spec)
	if !result.Available {
		return result
	}
	if c.options.DeploymentMode == DeploymentManagedCloud && c.options.ManagedDatabasePublicEndpoints && !c.options.ManagedDatabasePublicEndpointsQualified {
		result.Available = false
		result.UnavailableReason = "Managed Cloud database public endpoints remain unavailable until the exact release candidate passes native endpoint, DNS, firewall, and revocation acceptance."
	} else if err := ValidateDatabasePublicEndpointOptions(c.options); err != nil {
		result.Available = false
		result.UnavailableReason = "The installation does not provide a valid database public endpoint inventory."
	} else if len(c.options.DatabasePublicPorts) == 0 {
		result.Available = false
		result.UnavailableReason = "The installation operator has not provisioned database public endpoints."
	}
	return result
}

func (c *Client) DatabasePublicEndpointAllocations() []database.PublicEndpointAllocation {
	result := make([]database.PublicEndpointAllocation, 0, len(c.options.DatabasePublicPorts))
	for _, port := range c.options.DatabasePublicPorts {
		host := "database-" + strconv.Itoa(int(port)) + "." + c.options.DatabasePublicDomain
		digest := sha256.Sum256([]byte(c.options.DatabasePublicAddress + ":" + strconv.Itoa(int(port)) + ":" + host))
		result = append(result, database.PublicEndpointAllocation{
			ID: fmt.Sprintf("%x", digest[:16]), Host: host,
			Address: c.options.DatabasePublicAddress, Port: port,
		})
	}
	return result
}

func (c *Client) VerifyDatabasePublicEndpointDNS(ctx context.Context, allocation database.PublicEndpointAllocation) error {
	wanted := net.ParseIP(allocation.Address)
	if wanted == nil || wanted.To4() == nil {
		return fmt.Errorf("database public endpoint allocation has an invalid IPv4 address")
	}
	addresses, err := net.DefaultResolver.LookupIPAddr(ctx, allocation.Host)
	if err != nil {
		return fmt.Errorf("database public endpoint hostname does not resolve: %w", err)
	}
	if len(addresses) == 0 || len(addresses) > 16 {
		return fmt.Errorf("database public endpoint hostname must resolve to 1–16 addresses")
	}
	matched := false
	for _, resolved := range addresses {
		if resolved.IP.To4() != nil && resolved.IP.Equal(wanted) {
			matched = true
			continue
		}
		return fmt.Errorf("database public endpoint hostname resolves outside its configured address")
	}
	if !matched {
		return fmt.Errorf("database public endpoint hostname does not resolve to its configured address")
	}
	return nil
}

func ParseDatabasePublicPorts(value string) ([]int32, error) {
	ports, err := ParsePublicTCPPorts(value)
	if err != nil {
		return nil, fmt.Errorf("HAKOPOD_DATABASE_PUBLIC_PORTS: %s", strings.TrimPrefix(err.Error(), "HAKOPOD_PUBLIC_TCP_PORTS "))
	}
	return ports, nil
}
