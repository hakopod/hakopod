package database

import (
	"fmt"
	"k8s.io/apimachinery/pkg/util/validation"
	"slices"
)

func (p Placement) canonical() Placement {
	p.NodeNames = append([]string(nil), p.NodeNames...)
	slices.Sort(p.NodeNames)
	return p
}

// Equal compares the allowed node set; ordering and an omitted empty list do
// not change where the scheduler may place a member.
func (p Placement) Equal(other Placement) bool {
	return p.Spread == other.Spread && slices.Equal(p.canonical().NodeNames, other.canonical().NodeNames)
}

func (p Placement) Validate(mode string, members int) error {
	if p.Spread != "" && p.Spread != "nodes" && p.Spread != "zones" {
		return fmt.Errorf("placement.spread must be nodes or zones, or omitted for scheduler defaults")
	}
	if mode == "standalone" && p.Spread != "" {
		return fmt.Errorf("placement spreading requires cluster mode")
	}
	if len(p.NodeNames) > MaxMembers {
		return fmt.Errorf("placement may select at most %d nodes", MaxMembers)
	}
	seen := map[string]bool{}
	for _, node := range p.NodeNames {
		if len(validation.IsDNS1123Subdomain(node)) != 0 || seen[node] {
			return fmt.Errorf("placement.node_names must contain distinct valid node names")
		}
		seen[node] = true
	}
	if p.Spread != "" && len(p.NodeNames) > 0 && len(p.NodeNames) < members {
		return fmt.Errorf("placement spreading requires at least one selected node per database member")
	}
	return nil
}
