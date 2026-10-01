package cluster

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/hakopod/hakopod/internal/database"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type DatabasePlacementNode struct {
	Name                string `json:"name"`
	Architecture        string `json:"architecture"`
	Available           bool   `json:"available"`
	Reason              string `json:"reason"`
	Zone                string `json:"zone,omitempty"`
	Region              string `json:"region,omitempty"`
	Provider            string `json:"provider,omitempty"`
	ReservedCPUMilli    int64  `json:"reserved_cpu_milli,omitempty"`
	ReservedMemoryBytes int64  `json:"reserved_memory_bytes,omitempty"`
}

// DatabasePlacementNodes uses database authority, never application placement.
// Provider and failure domains are node observations, not availability claims.
func (c *Client) DatabasePlacementNodes(ctx context.Context, project, environment string) ([]DatabasePlacementNode, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var policy *DatabasePolicy
	if c.options.DatabasePlacementPolicy != nil {
		p, err := c.options.DatabasePlacementPolicy(ctx, project, environment)
		if err != nil {
			return nil, err
		}
		policy = &p
	} else if c.options.WorkloadPolicy != nil || c.options.DatabasePolicy != nil {
		return nil, fmt.Errorf("database placement discovery is not configured")
	}
	var nodes []corev1.Node
	if policy != nil {
		if len(policy.nodes()) < 1 || len(policy.nodes()) > database.MaxMembers {
			return nil, fmt.Errorf("invalid database node allocation")
		}
		for _, name := range policy.nodes() {
			n, err := c.kube.CoreV1().Nodes().Get(ctx, name, metav1.GetOptions{})
			if err != nil {
				return nil, fmt.Errorf("an allocated database node is unavailable")
			}
			nodes = append(nodes, *n)
		}
	} else {
		if err := c.ValidateCloudCapacity(ctx); err != nil {
			return nil, err
		}
		list, err := c.kube.CoreV1().Nodes().List(ctx, metav1.ListOptions{Limit: int64(database.MaxMembers + 1)})
		if err != nil {
			return nil, err
		}
		if list.Continue != "" || len(list.Items) > database.MaxMembers {
			return nil, fmt.Errorf("database node inventory exceeds its bound")
		}
		nodes = list.Items
	}
	result := make([]DatabasePlacementNode, 0, len(nodes))
	for _, node := range nodes {
		var workload *WorkloadPolicy
		if policy != nil {
			workload = &WorkloadPolicy{NodeName: node.Name, Pool: policy.Pool, RuntimeClass: policy.RuntimeClass}
		}
		reason := nodeUnavailable(node, workload, false)
		if node.DeletionTimestamp != nil {
			reason = "Node is being removed"
		}
		item := DatabasePlacementNode{Name: node.Name, Architecture: node.Labels[corev1.LabelArchStable], Zone: node.Labels[corev1.LabelTopologyZone], Region: node.Labels[corev1.LabelTopologyRegion], Reason: reason}
		if policy != nil {
			if node.Labels["hakopod.com/pool"] != policy.Pool || node.Labels[DatabaseDefaultRuntimeLabel] != policy.RuntimeClass {
				item.Reason = "Database sandbox or pool is unavailable"
			}
			if r, ok := policy.NodeReservations[node.Name]; ok {
				item.ReservedCPUMilli = r.Capacity.CPUMilli
				item.ReservedMemoryBytes = r.Capacity.MemoryBytes
				if err := c.checkDatabaseNodeReservation(ctx, node, r); err != nil {
					item.Reason = err.Error()
				}
			} else if len(policy.NodeReservations) > 0 {
				item.Reason = "Database node reservation is incomplete"
			}
		}
		provider, _, ok := strings.Cut(node.Spec.ProviderID, "://")
		if ok && slices.Contains([]string{"aws", "azure", "gce", "hcloud", "digitalocean", "openstack", "vsphere"}, provider) {
			item.Provider = provider
		}
		item.Available = item.Reason == ""
		result = append(result, item)
	}
	slices.SortFunc(result, func(a, b DatabasePlacementNode) int { return strings.Compare(a.Name, b.Name) })
	return result, nil
}
