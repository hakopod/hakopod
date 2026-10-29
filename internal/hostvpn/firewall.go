package hostvpn

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"slices"
	"strconv"
	"strings"
)

// The firewall is one nftables table, inet hakopod_access, that limits the
// dashboard port and the K3s API to the policy's networks. Loopback, cluster
// nodes and the cluster's Pod and Service networks are always allowed, so SSH
// tunnels, agents and in-cluster clients of the API keep working. Nothing else
// on the host is touched.

const firewallTable = "hakopod_access"

func removeTable(family, name string) string {
	return fmt.Sprintf("table %s %s {}\ndelete table %s %s\n", family, name, family, name)
}

// Ruleset renders the table. Declaring and deleting it first makes nft -f
// replace it in one transaction. With tunnel, the ports also accept anything
// arriving on those interfaces. auto-merge lets an allowed network contain a
// node address without nft refusing overlapping intervals.
func Ruleset(allow []netip.Prefix, ports []int, tunnel ...string) string {
	set := func(v6 bool) string {
		var out []string
		for _, p := range allow {
			if p.Addr().Is6() == v6 && !slices.Contains(out, p.String()) {
				out = append(out, p.String())
			}
		}
		slices.Sort(out)
		if len(out) == 0 {
			return ""
		}
		return "elements = { " + strings.Join(out, ", ") + " }; "
	}
	sorted := slices.Compact(slices.Sorted(slices.Values(ports)))
	var portList []string
	for _, p := range sorted {
		portList = append(portList, strconv.Itoa(p))
	}
	dport := "    tcp dport { " + strings.Join(portList, ", ") + " }"
	lines := []string{"table inet " + firewallTable + " {}", "delete table inet " + firewallTable, "table inet " + firewallTable + " {",
		"  set allow4 { type ipv4_addr; flags interval; auto-merge; " + set(false) + "}",
		"  set allow6 { type ipv6_addr; flags interval; auto-merge; " + set(true) + "}",
		"  chain input {",
		"    type filter hook input priority filter - 10; policy accept;",
		dport + ` iif "lo" accept`}
	if len(tunnel) > 0 {
		lines = append(lines, dport+` iifname { "`+strings.Join(tunnel, `", "`)+`" } accept`)
	}
	lines = append(lines, dport+" ip saddr @allow4 accept", dport+" ip6 saddr @allow6 accept", dport+" counter drop", "  }", "}")
	return strings.Join(lines, "\n") + "\n"
}

// clusterAddresses are the Pod and Service networks (host.py's k3s.yaml
// cluster-cidr and service-cidr) and every node's internal and external IP.
func (h *Host) clusterAddresses() ([]netip.Prefix, error) {
	out, err := h.Run([]string{"/opt/hakopod/tools/kubectl", "--kubeconfig=/etc/hakopod/admin-kubeconfig", "--request-timeout=10s", "get", "nodes", "-o", "json"}, "")
	if err != nil {
		return nil, err
	}
	var nodes struct {
		Items []struct {
			Status struct {
				Addresses []struct{ Type, Address string } `json:"addresses"`
			} `json:"status"`
		} `json:"items"`
	}
	if err := json.Unmarshal([]byte(out), &nodes); err != nil {
		return nil, err
	}
	addresses := slices.Clone(reserved)
	for _, node := range nodes.Items {
		for _, a := range node.Status.Addresses {
			if a.Type == "InternalIP" || a.Type == "ExternalIP" {
				p, err := network(a.Address)
				if err != nil {
					return nil, err
				}
				addresses = append(addresses, p)
			}
		}
	}
	return addresses, nil
}

// Firewall makes the table match the policy, or removes it when the policy
// is off.
func (h *Host) Firewall(raw json.RawMessage, cfg config) error {
	var body struct {
		Enabled    bool     `json:"enabled"`
		AllowCIDRs []string `json:"allow_cidrs"`
		TunnelOnly bool     `json:"tunnel_only"`
	}
	if err := strict(raw, &body, "send enabled, allow_cidrs and tunnel_only only", "enabled", "allow_cidrs", "tunnel_only"); err != nil {
		return err
	}
	if len(body.AllowCIDRs) > 256 {
		return errors.New("allow_cidrs must be a list of at most 256 CIDRs")
	}
	var allow []netip.Prefix
	for _, value := range body.AllowCIDRs {
		p, err := network(value)
		if err != nil {
			return err
		}
		if p.Bits() == 0 {
			return errors.New("allow_cidrs cannot contain a default route")
		}
		allow = append(allow, p)
	}
	if !body.Enabled {
		if _, err := os.Stat(h.FirewallSaved); err == nil {
			if err := h.nft(removeTable("inet", firewallTable)); err != nil {
				return err
			}
			return os.Remove(h.FirewallSaved)
		}
		return nil
	}
	if len(allow) == 0 {
		return errors.New("an enabled firewall needs at least one allowed CIDR")
	}
	nodes, err := h.clusterAddresses()
	if err != nil {
		return err
	}
	if body.TunnelOnly {
		// The K3s API is reachable only over the tunnel, from nodes and from
		// the cluster. The dashboard port stays open so signed webhooks
		// arrive; the dashboard's own gate refuses every other request.
		return h.applyTable(h.FirewallSaved, Ruleset(nodes, []int{6443}, vpnIface, serverIface))
	}
	ports, err := cfg.ports()
	if err != nil {
		return err
	}
	text := Ruleset(append(allow, nodes...), ports)
	return h.applyTable(h.FirewallSaved, text)
}
