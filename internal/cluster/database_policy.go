package cluster

import (
	"context"
	"fmt"
	"github.com/hakopod/hakopod/internal/database"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"slices"
)

// DatabasePolicy is trusted placement, independent of application replicas.
// Some pinned controllers cannot select a RuntimeClass. Shared workers must
// therefore have an operator-verified sandbox as their default runtime.
type DatabasePolicy struct {
	NodeName, Pool, RuntimeClass, StorageClass string
	// NodeNames is an operator-authorized set with capacity reserved by the host.
	// NodeName remains compatible with existing single-worker allocation.
	NodeNames        []string
	NodeReservations map[string]DatabaseNodeReservation
	podRuntimeClass  string
}

func (p DatabasePolicy) nodes() []string {
	if p.NodeName != "" {
		return []string{p.NodeName}
	}
	return p.NodeNames
}

type DatabasePolicyResolver func(context.Context, string, string, database.Spec) (DatabasePolicy, error)

const DatabaseDefaultRuntimeLabel = "hakopod.com.node-restriction.kubernetes.io/default-runtime"

func (c *Client) databasePolicy(ctx context.Context, d database.Resource) (*DatabasePolicy, error) {
	if len(c.options.ManagedClusterNodes) > 0 {
		if err := c.ValidateCloudCapacity(ctx); err != nil {
			return nil, err
		}
	}
	if c.options.DatabasePolicy == nil {
		if c.options.WorkloadPolicy != nil {
			return nil, fmt.Errorf("database placement is not configured")
		}
		return nil, nil
	}
	p, err := c.options.DatabasePolicy(ctx, d.Project, d.Environment, d.Spec)
	if err != nil {
		return nil, err
	}
	if len(p.nodes()) == 0 || len(p.nodes()) > database.MaxMembers || (p.NodeName != "" && len(p.NodeNames) != 0) || p.Pool == "" || p.RuntimeClass != "runsc" || p.StorageClass == "" {
		return nil, fmt.Errorf("invalid database placement policy")
	}
	for _, name := range p.nodes() {
		if err = c.CheckWorkloadPool(ctx, name, p.Pool, p.RuntimeClass); err != nil {
			return nil, err
		}
		node, err := c.kube.CoreV1().Nodes().Get(ctx, name, metav1.GetOptions{})
		if err != nil || node.Labels[DatabaseDefaultRuntimeLabel] != p.RuntimeClass {
			return nil, fmt.Errorf("database worker default sandbox has not been verified")
		}
		if reservation, ok := p.NodeReservations[name]; ok {
			if err = c.checkDatabaseNodeReservation(ctx, *node, reservation); err != nil {
				return nil, err
			}
		} else if len(p.NodeReservations) > 0 {
			return nil, fmt.Errorf("database node reservation is incomplete")
		}
	}
	for _, name := range d.Spec.Placement.NodeNames {
		if !slices.Contains(p.nodes(), name) {
			return nil, fmt.Errorf("selected database node is outside its authorized allocation")
		}
	}
	if d.Spec.Placement.Spread != "" && len(p.nodes()) < d.Spec.PlacementDomains() {
		return nil, fmt.Errorf("the database allocation does not include enough distinct workers for this placement")
	}
	if _, err = c.kube.StorageV1().StorageClasses().Get(ctx, p.StorageClass, metav1.GetOptions{}); err != nil {
		return nil, fmt.Errorf("database storage class is unavailable")
	}
	if d.Spec.Engine == "clickhouse" {
		p.podRuntimeClass = clickhouseRuntimeClass
	}
	if d.Spec.Engine == "oracle" {
		p.podRuntimeClass = p.RuntimeClass
	}
	return &p, nil
}

func (c *Client) databaseObject(ctx context.Context, d database.Resource) (*unstructured.Unstructured, error) {
	object, err := DatabaseObject(d)
	if err != nil {
		return nil, err
	}
	p, err := c.databasePolicy(ctx, d)
	if err != nil {
		return object, err
	}
	if p != nil {
		applyDatabasePolicy(object, d.Spec, *p)
	}
	nodes := d.Spec.Placement.NodeNames
	if len(nodes) == 0 && p != nil {
		nodes = p.nodes()
	}
	applyDatabasePlacement(object, d.Spec, nodes)
	if d.Spec.Engine == "clickhouse" {
		runtime, err := c.clickhouseRuntime(ctx, d, p)
		if err != nil {
			return nil, err
		}
		applyClickHouseRuntime(object, runtime)
	}
	if d.Spec.Engine == "vitess" {
		storage, err := c.vitessBackupStorage(ctx, d)
		if err != nil {
			return nil, err
		}
		if err = applyVitessBackupStorage(object, d, storage); err != nil {
			return nil, err
		}
	}
	return object, nil
}

func applyDatabasePolicy(object *unstructured.Unstructured, s database.Spec, p DatabasePolicy) {
	if s.Engine == "vitess" {
		applyVitessPolicy(object, s, p)
		return
	}
	selector := map[string]any{"hakopod.com/pool": p.Pool, DatabaseDefaultRuntimeLabel: p.RuntimeClass}
	tolerations := []any{map[string]any{"key": "hakopod.com/pool", "operator": "Equal", "value": p.Pool, "effect": "NoSchedule"}}
	if s.Engine == "postgresql" {
		_ = unstructured.SetNestedMap(object.Object, selector, "spec", "affinity", "nodeSelector")
		_ = unstructured.SetNestedSlice(object.Object, tolerations, "spec", "affinity", "tolerations")
		_ = unstructured.SetNestedField(object.Object, p.StorageClass, "spec", "storage", "storageClass")
	} else if s.Engine == "oracle" {
		_ = unstructured.SetNestedMap(object.Object, selector, "spec", "template", "spec", "nodeSelector")
		_ = unstructured.SetNestedSlice(object.Object, tolerations, "spec", "template", "spec", "tolerations")
		_ = unstructured.SetNestedField(object.Object, p.RuntimeClass, "spec", "template", "spec", "runtimeClassName")
		claims, _, _ := unstructured.NestedSlice(object.Object, "spec", "volumeClaimTemplates")
		for _, raw := range claims {
			raw.(map[string]any)["spec"].(map[string]any)["storageClassName"] = p.StorageClass
		}
		_ = unstructured.SetNestedSlice(object.Object, claims, "spec", "volumeClaimTemplates")
	} else if s.Engine == "mongodb" {
		selector[corev1.LabelArchStable] = "amd64"
		_ = unstructured.SetNestedMap(object.Object, selector, "spec", "statefulSet", "spec", "template", "spec", "nodeSelector")
		_ = unstructured.SetNestedSlice(object.Object, tolerations, "spec", "statefulSet", "spec", "template", "spec", "tolerations")
		claims, _, _ := unstructured.NestedSlice(object.Object, "spec", "statefulSet", "spec", "volumeClaimTemplates")
		for _, raw := range claims {
			raw.(map[string]any)["spec"].(map[string]any)["storageClassName"] = p.StorageClass
		}
		_ = unstructured.SetNestedSlice(object.Object, claims, "spec", "statefulSet", "spec", "volumeClaimTemplates")
	} else if s.Engine == "mysql" {
		selector[corev1.LabelArchStable] = "amd64"
		for _, path := range [][]string{{"spec", "podSpec"}, {"spec", "router", "podSpec"}} {
			_ = unstructured.SetNestedMap(object.Object, selector, append(path, "nodeSelector")...)
			_ = unstructured.SetNestedSlice(object.Object, tolerations, append(path, "tolerations")...)
		}
		_ = unstructured.SetNestedField(object.Object, p.StorageClass, "spec", "datadirVolumeClaimTemplate", "storageClassName")
	} else if s.Engine == "clickhouse" {
		templates, _, _ := unstructured.NestedSlice(object.Object, "spec", "templates", "podTemplates")
		for _, raw := range templates {
			pod := raw.(map[string]any)["spec"].(map[string]any)
			pod["nodeSelector"] = selector
			pod["tolerations"] = tolerations
		}
		_ = unstructured.SetNestedSlice(object.Object, templates, "spec", "templates", "podTemplates")
		claims, _, _ := unstructured.NestedSlice(object.Object, "spec", "templates", "volumeClaimTemplates")
		for _, raw := range claims {
			raw.(map[string]any)["spec"].(map[string]any)["storageClassName"] = p.StorageClass
		}
		_ = unstructured.SetNestedSlice(object.Object, claims, "spec", "templates", "volumeClaimTemplates")
	} else {
		if s.Mode == "cluster" {
			for _, role := range []string{"redisLeader", "redisFollower"} {
				_ = unstructured.SetNestedMap(object.Object, selector, "spec", role, "nodeSelector")
				_ = unstructured.SetNestedSlice(object.Object, tolerations, "spec", role, "tolerations")
			}
		} else {
			_ = unstructured.SetNestedMap(object.Object, selector, "spec", "nodeSelector")
			_ = unstructured.SetNestedSlice(object.Object, tolerations, "spec", "tolerations")
		}
		_ = unstructured.SetNestedField(object.Object, p.StorageClass, "spec", "storage", "volumeClaimTemplate", "spec", "storageClassName")
	}
}

func databasePodPolicyMatches(pod corev1.Pod, p *DatabasePolicy) bool {
	if p == nil {
		return true
	}
	// A runtime selected explicitly must agree with the sandboxed default.
	runtimeMatches := pod.Spec.RuntimeClassName == nil || *pod.Spec.RuntimeClassName == p.RuntimeClass
	if p.podRuntimeClass != "" {
		runtimeMatches = pod.Spec.RuntimeClassName != nil && *pod.Spec.RuntimeClassName == p.podRuntimeClass
	}
	return slices.Contains(p.nodes(), pod.Spec.NodeName) && pod.Spec.NodeSelector["hakopod.com/pool"] == p.Pool && pod.Spec.NodeSelector[DatabaseDefaultRuntimeLabel] == p.RuntimeClass && runtimeMatches
}

// ValidateDatabasePlacement rejects unavailable hosted placement before creating
// a durable resource. Reconciliation rechecks the same policy before effects.
func (c *Client) ValidateDatabasePlacement(ctx context.Context, d database.Resource) error {
	if c == nil {
		return fmt.Errorf("database runtime is unavailable")
	}
	p, err := c.databasePolicy(ctx, d)
	if err != nil {
		return err
	}
	if d.Spec.Engine == "clickhouse" {
		if _, err = c.clickhouseRuntime(ctx, d, p); err != nil {
			return err
		}
	}
	return c.validateDatabasePlacementNodes(ctx, d.Spec, p)
}
