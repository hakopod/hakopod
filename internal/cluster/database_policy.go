package cluster

import (
	"context"
	"fmt"
	"github.com/hakopod/hakopod/internal/database"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// DatabasePolicy is trusted placement, independent of application replicas.
// The pinned controllers cannot select a RuntimeClass. Shared workers must
// therefore have an operator-verified sandbox as their default runtime.
type DatabasePolicy struct{ NodeName, Pool, RuntimeClass, StorageClass string }
type DatabasePolicyResolver func(context.Context, string, string, database.Spec) (DatabasePolicy, error)

const DatabaseDefaultRuntimeLabel = "hakopod.com.node-restriction.kubernetes.io/default-runtime"

func (c *Client) databasePolicy(ctx context.Context, d database.Resource) (*DatabasePolicy, error) {
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
	if p.NodeName == "" || p.Pool == "" || p.RuntimeClass != "runsc" || p.StorageClass == "" {
		return nil, fmt.Errorf("invalid database placement policy")
	}
	if err = c.CheckWorkloadPool(ctx, p.NodeName, p.Pool, p.RuntimeClass); err != nil {
		return nil, err
	}
	node, err := c.kube.CoreV1().Nodes().Get(ctx, p.NodeName, metav1.GetOptions{})
	if err != nil || node.Labels[DatabaseDefaultRuntimeLabel] != p.RuntimeClass {
		return nil, fmt.Errorf("database worker default sandbox has not been verified")
	}
	if _, err = c.kube.StorageV1().StorageClasses().Get(ctx, p.StorageClass, metav1.GetOptions{}); err != nil {
		return nil, fmt.Errorf("database storage class is unavailable")
	}
	return &p, nil
}

func (c *Client) databaseObject(ctx context.Context, d database.Resource) (*unstructured.Unstructured, error) {
	object, err := DatabaseObject(d)
	if err != nil {
		return nil, err
	}
	p, err := c.databasePolicy(ctx, d)
	if err != nil || p == nil {
		return object, err
	}
	applyDatabasePolicy(object, d.Spec, *p)
	return object, nil
}

func applyDatabasePolicy(object *unstructured.Unstructured, s database.Spec, p DatabasePolicy) {
	selector := map[string]any{"kubernetes.io/hostname": p.NodeName, "hakopod.com/pool": p.Pool, DatabaseDefaultRuntimeLabel: p.RuntimeClass}
	tolerations := []any{map[string]any{"key": "hakopod.com/pool", "operator": "Equal", "value": p.Pool, "effect": "NoSchedule"}}
	if s.Engine == "postgresql" {
		_ = unstructured.SetNestedMap(object.Object, selector, "spec", "affinity", "nodeSelector")
		_ = unstructured.SetNestedSlice(object.Object, tolerations, "spec", "affinity", "tolerations")
		_ = unstructured.SetNestedField(object.Object, p.StorageClass, "spec", "storage", "storageClass")
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
	return pod.Spec.NodeName == p.NodeName && pod.Spec.NodeSelector[DatabaseDefaultRuntimeLabel] == p.RuntimeClass && (pod.Spec.RuntimeClassName == nil || *pod.Spec.RuntimeClassName == p.RuntimeClass)
}

// ValidateDatabasePlacement rejects unavailable hosted placement before creating
// a durable resource. Reconciliation rechecks the same policy before effects.
func (c *Client) ValidateDatabasePlacement(ctx context.Context, d database.Resource) error {
	if c == nil {
		return fmt.Errorf("database runtime is unavailable")
	}
	_, err := c.databasePolicy(ctx, d)
	return err
}
