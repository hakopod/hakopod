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
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func databaseTopologyKey(s database.Spec) string {
	if s.Placement.Spread == "zones" {
		return corev1.LabelTopologyZone
	}
	return corev1.LabelHostname
}

func applyDatabasePlacement(object *unstructured.Unstructured, s database.Spec, nodes []string) {
	if s.Engine == "vitess" {
		applyVitessPlacement(object, s, nodes)
		return
	}
	var nodeAffinity map[string]any
	if len(nodes) > 0 {
		terms := make([]any, len(nodes))
		for i, name := range nodes {
			// Field selectors permit one value per term; terms are ORed.
			terms[i] = map[string]any{"matchFields": []any{map[string]any{"key": "metadata.name", "operator": "In", "values": []any{name}}}}
		}
		nodeAffinity = map[string]any{"requiredDuringSchedulingIgnoredDuringExecution": map[string]any{"nodeSelectorTerms": terms}}
	}
	if s.Engine == "postgresql" {
		if nodeAffinity != nil {
			_ = unstructured.SetNestedMap(object.Object, nodeAffinity, "spec", "affinity", "nodeAffinity")
		}
		if s.Placement.Spread != "" {
			_ = unstructured.SetNestedField(object.Object, true, "spec", "affinity", "enablePodAntiAffinity")
			_ = unstructured.SetNestedField(object.Object, "required", "spec", "affinity", "podAntiAffinityType")
			_ = unstructured.SetNestedField(object.Object, databaseTopologyKey(s), "spec", "affinity", "topologyKey")
		}
		return
	}
	if s.Engine == "mysql" {
		for _, role := range []string{"mysqld", "mysqlrouter"} {
			affinity := map[string]any{}
			if nodeAffinity != nil {
				affinity["nodeAffinity"] = nodeAffinity
			}
			if s.Placement.Spread != "" {
				affinity["podAntiAffinity"] = map[string]any{"requiredDuringSchedulingIgnoredDuringExecution": []any{map[string]any{"topologyKey": databaseTopologyKey(s), "labelSelector": map[string]any{"matchLabels": map[string]any{"component": role, "mysql.oracle.com/cluster": "database"}}}}}
			}
			if len(affinity) > 0 {
				path := []string{"spec", "podSpec", "affinity"}
				if role == "mysqlrouter" {
					path = []string{"spec", "router", "podSpec", "affinity"}
				}
				_ = unstructured.SetNestedMap(object.Object, affinity, path...)
			}
		}
		return
	}
	affinity := map[string]any{}
	if nodeAffinity != nil {
		affinity["nodeAffinity"] = nodeAffinity
	}
	if s.Placement.Spread != "" {
		// All durable members in this dedicated namespace must occupy separate
		// domains. This remains valid after a primary election or Redis reshard.
		affinity["podAntiAffinity"] = map[string]any{"requiredDuringSchedulingIgnoredDuringExecution": []any{map[string]any{
			"topologyKey":   databaseTopologyKey(s),
			"labelSelector": map[string]any{"matchExpressions": []any{map[string]any{"key": databaseRecoveryHelper, "operator": "DoesNotExist"}}},
		}}}
	}
	if len(affinity) == 0 {
		return
	}
	if s.Engine == "clickhouse" {
		if s.Placement.Spread != "" {
			affinity["podAntiAffinity"] = map[string]any{"requiredDuringSchedulingIgnoredDuringExecution": []any{map[string]any{"topologyKey": databaseTopologyKey(s), "labelSelector": map[string]any{"matchLabels": map[string]any{"clickhouse.altinity.com/chi": "database"}}}}}
		}
		templates, _, _ := unstructured.NestedSlice(object.Object, "spec", "templates", "podTemplates")
		for _, raw := range templates {
			raw.(map[string]any)["spec"].(map[string]any)["affinity"] = affinity
		}
		_ = unstructured.SetNestedSlice(object.Object, templates, "spec", "templates", "podTemplates")
		return
	}
	if s.Engine == "mongodb" {
		_ = unstructured.SetNestedMap(object.Object, affinity, "spec", "statefulSet", "spec", "template", "spec", "affinity")
		return
	}
	if s.Engine == "oracle" || s.Engine == "duckdb" {
		_ = unstructured.SetNestedMap(object.Object, affinity, "spec", "template", "spec", "affinity")
		return
	}
	if s.Mode == "cluster" {
		for _, role := range []string{"redisLeader", "redisFollower"} {
			_ = unstructured.SetNestedMap(object.Object, affinity, "spec", role, "affinity")
		}
	} else {
		_ = unstructured.SetNestedMap(object.Object, affinity, "spec", "affinity")
	}
}

func (c *Client) validateDatabasePlacementNodes(ctx context.Context, s database.Spec, policy *DatabasePolicy) error {
	names := s.Placement.NodeNames
	if len(names) == 0 && policy != nil {
		names = policy.nodes()
	}
	oracleFree := s.Engine == "oracle" && !oracleEnterprise(s)
	if len(names) == 0 && s.Placement.Spread == "" && s.Engine != "mysql" && s.Engine != "mongodb" && s.Engine != "vitess" && s.Engine != "duckdb" && !oracleFree {
		return nil
	}
	nodes, err := c.kube.CoreV1().Nodes().List(ctx, metav1.ListOptions{Limit: 257})
	if err != nil || len(nodes.Items) > 256 || nodes.Continue != "" {
		return fmt.Errorf("database placement node inventory is unavailable or exceeds its bound")
	}
	domains := map[string]bool{}
	found := map[string]bool{}
	for _, node := range nodes.Items {
		if (s.Engine == "mysql" || s.Engine == "mongodb" || s.Engine == "vitess" || s.Engine == "duckdb" || oracleFree) && node.Labels[corev1.LabelArchStable] != "amd64" {
			continue
		}
		if len(names) > 0 && !slices.Contains(names, node.Name) {
			continue
		}
		if node.Spec.Unschedulable || node.DeletionTimestamp != nil {
			continue
		}
		ready := false
		for _, condition := range node.Status.Conditions {
			if condition.Type == corev1.NodeReady && condition.Status == corev1.ConditionTrue {
				ready = true
			}
		}
		if !ready {
			continue
		}
		found[node.Name] = true
		domain := node.Labels[databaseTopologyKey(s)]
		if domain != "" {
			domains[domain] = true
		}
	}
	// A saved explicit selection must remain available in full. With automatic
	// placement, use the compatible subset of the approved pool; the scheduler
	// still receives both the approved names and the engine architecture selector.
	for _, name := range s.Placement.NodeNames {
		if !found[name] {
			return fmt.Errorf("a selected database node is unavailable for scheduling")
		}
	}
	if len(found) == 0 && policy != nil {
		return fmt.Errorf("the approved database pool has no available worker for this engine")
	}
	if s.Engine == "mysql" && len(found) == 0 {
		return fmt.Errorf("MySQL requires an available amd64 worker for its verified images")
	}
	if s.Engine == "mongodb" && len(found) == 0 {
		return fmt.Errorf("MongoDB requires an available amd64 worker for its pinned images")
	}
	if s.Engine == "vitess" && len(found) == 0 {
		return fmt.Errorf("Vitess requires an available amd64 worker for its pinned images")
	}
	if oracleFree && len(found) == 0 {
		return fmt.Errorf("Oracle Free requires an available amd64 worker for its pinned controller")
	}
	if s.Engine == "duckdb" && len(found) == 0 {
		return fmt.Errorf("DuckDB (MyDuck) requires an available amd64 worker for its verified image")
	}
	if s.Placement.Spread != "" && len(domains) < s.PlacementDomains() {
		return fmt.Errorf("this placement requires %d distinct %s with ready nodes; only %d are available", s.PlacementDomains(), s.Placement.Spread, len(domains))
	}
	return nil
}

// Node labels are observations, not proof that cloud networks, disks or quorum
// survive an outage. Do not derive provider identity from a hostname.
func (c *Client) observeDatabasePlacement(ctx context.Context, d database.Resource, o *database.Observation) {
	c.observeDatabaseMemberPlacement(ctx, o.Members)
	o.Placement = databasePlacementObservation(d.Spec, o.Members)
}

func (c *Client) observeDatabaseMemberPlacement(ctx context.Context, members []database.Member) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	cache := map[string]*corev1.Node{}
	for i := range members {
		m := &members[i]
		if m.Node == "" {
			continue
		}
		node, seen := cache[m.Node]
		if !seen {
			node, _ = c.kube.CoreV1().Nodes().Get(ctx, m.Node, metav1.GetOptions{})
			cache[m.Node] = node
		}
		if node == nil {
			continue
		}
		m.Zone = node.Labels[corev1.LabelTopologyZone]
		m.Region = node.Labels[corev1.LabelTopologyRegion]
		provider, _, ok := strings.Cut(node.Spec.ProviderID, "://")
		if ok {
			switch provider {
			case "aws", "azure", "gce", "hcloud", "digitalocean", "openstack", "vsphere":
				m.Provider = provider
			}
		}
	}
}

func databasePlacementObservation(s database.Spec, members []database.Member) *database.PlacementObservation {
	nodes, zones, regions, providers := map[string]bool{}, map[string]bool{}, map[string]bool{}, map[string]bool{}
	verified := len(members) == s.Members()
	for _, m := range members {
		if m.Node != "" {
			nodes[m.Node] = true
		} else {
			verified = false
		}
		if len(s.Placement.NodeNames) > 0 && !slices.Contains(s.Placement.NodeNames, m.Node) {
			verified = false
		}
		if m.Zone != "" {
			zones[m.Zone] = true
		}
		if m.Region != "" {
			regions[m.Region] = true
		}
		if m.Provider != "" {
			providers[m.Provider] = true
		}
	}
	if s.Placement.Spread == "nodes" {
		verified = verified && len(nodes) == s.Members()
	}
	if s.Placement.Spread == "zones" {
		verified = verified && len(zones) == s.Members()
	}
	message := "Observed placement; outage tolerance depends on replication, storage and network connectivity."
	if !verified {
		message = "Waiting for every member to satisfy the requested placement."
	}
	return &database.PlacementObservation{Verified: verified, Message: message, Nodes: len(nodes), Zones: len(zones), Regions: len(regions), Providers: len(providers)}
}
