package cluster

import (
	"context"
	"fmt"
	"strconv"

	"github.com/hakopod/hakopod/internal/spec"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// PlacementNode deliberately excludes infrastructure addresses and metrics.
type PlacementNode struct {
	Name         string `json:"name"`
	Architecture string `json:"architecture"`
	Available    bool   `json:"available"`
	Reason       string `json:"reason"`
}

func nodeUnavailable(node corev1.Node, policy *WorkloadPolicy, gpu bool) string {
	if node.Spec.Unschedulable {
		return "Scheduling paused"
	}
	ready := false
	for _, v := range node.Status.Conditions {
		if v.Type == corev1.NodeReady && v.Status == corev1.ConditionTrue {
			ready = true
		}
	}
	if !ready {
		return "Node is not ready"
	}
	for _, v := range node.Spec.Taints {
		if v.Effect != corev1.TaintEffectNoSchedule && v.Effect != corev1.TaintEffectNoExecute {
			continue
		}
		if policy != nil && policy.Pool != "" && v.Key == "hakopod.com/pool" && v.Value == policy.Pool && v.Effect == corev1.TaintEffectNoSchedule {
			continue
		}
		if gpu && v.Key == "nvidia.com/gpu" && v.Effect == corev1.TaintEffectNoSchedule {
			continue
		}
		return "Reserved by a scheduling taint"
	}
	return ""
}

func (c *Client) PlacementNodes(ctx context.Context, t Target) ([]PlacementNode, error) {
	p, err := c.workloadPolicy(ctx, t)
	if err != nil {
		return nil, err
	}
	// A Cloud embedding must provide its trusted allocation before exposing nodes.
	if c.options.DeploymentMode == DeploymentManagedCloud && p == nil && c.options.DedicatedPublicTCPNode == "" && c.options.OperatorNodeLimit == 0 {
		return []PlacementNode{}, nil
	}
	nodes, err := c.kube.CoreV1().Nodes().List(ctx, metav1.ListOptions{Limit: 201})
	if err != nil {
		return nil, err
	}
	if nodes.Continue != "" || len(nodes.Items) > 200 {
		return nil, fmt.Errorf("node inventory exceeds 200 nodes")
	}
	out := []PlacementNode{}
	for _, n := range nodes.Items {
		if p != nil && (p.NodeName != n.Name || p.Pool != "" && n.Labels["hakopod.com/pool"] != p.Pool) {
			continue
		}
		reason := nodeUnavailable(n, p, false)
		out = append(out, PlacementNode{Name: n.Name, Architecture: n.Labels["kubernetes.io/arch"], Available: reason == "", Reason: reason})
	}
	return out, nil
}

func matchNodeRequirements(reqs []corev1.NodeSelectorRequirement, values map[string]string) bool {
	for _, r := range reqs {
		value, exists := values[r.Key]
		contains := false
		for _, v := range r.Values {
			contains = contains || v == value
		}
		switch r.Operator {
		case corev1.NodeSelectorOpIn:
			if !exists || !contains {
				return false
			}
		case corev1.NodeSelectorOpNotIn:
			if exists && contains {
				return false
			}
		case corev1.NodeSelectorOpExists:
			if !exists {
				return false
			}
		case corev1.NodeSelectorOpDoesNotExist:
			if exists {
				return false
			}
		case corev1.NodeSelectorOpGt, corev1.NodeSelectorOpLt:
			if !exists || len(r.Values) != 1 {
				return false
			}
			a, e := strconv.ParseInt(value, 10, 64)
			b, f := strconv.ParseInt(r.Values[0], 10, 64)
			if e != nil || f != nil || r.Operator == corev1.NodeSelectorOpGt && a <= b || r.Operator == corev1.NodeSelectorOpLt && a >= b {
				return false
			}
		default:
			return false
		}
	}
	return true
}
func volumeFitsNode(pv *corev1.PersistentVolume, node *corev1.Node) bool {
	if pv.Spec.NodeAffinity == nil || pv.Spec.NodeAffinity.Required == nil {
		return pv.Spec.Local == nil && pv.Spec.HostPath == nil
	}
	for _, term := range pv.Spec.NodeAffinity.Required.NodeSelectorTerms {
		if len(term.MatchExpressions)+len(term.MatchFields) > 0 && matchNodeRequirements(term.MatchExpressions, node.Labels) && matchNodeRequirements(term.MatchFields, map[string]string{"metadata.name": node.Name}) {
			return true
		}
	}
	return false
}

func (c *Client) validatePlacement(ctx context.Context, t Target) error {
	shared := map[string]string{}
	for name, svc := range t.Spec.Services {
		if svc.NodeName == "" {
			continue
		}
		n, err := c.kube.CoreV1().Nodes().Get(ctx, svc.NodeName, metav1.GetOptions{})
		if err != nil {
			return fmt.Errorf("services.%s.node_name: selected node is unavailable", name)
		}
		p, err := c.workloadPolicy(ctx, t)
		if err != nil {
			return err
		}
		if reason := nodeUnavailable(*n, p, svc.GPU != nil); reason != "" {
			return fmt.Errorf("services.%s.node_name: %s", name, reason)
		}
		if svc.Architecture != "" && n.Labels["kubernetes.io/arch"] != svc.Architecture {
			return fmt.Errorf("services.%s.node_name: node architecture does not match the service", name)
		}
		claims := []string{}
		if svc.Volume != nil {
			claims = append(claims, name+"-data")
		}
		for _, m := range svc.Mounts {
			if t.Spec.Volumes[m.Volume].AccessMode != "ReadWriteMany" {
				if previous := shared[m.Volume]; previous != "" && previous != svc.NodeName {
					return fmt.Errorf("services.%s.node_name: services sharing volume %s must use the same node", name, m.Volume)
				}
				shared[m.Volume] = svc.NodeName
			}
			claims = append(claims, "hakopod-volume-"+m.Volume)
		}
		if t.ApplicationID == "" {
			continue
		}
		for _, claimName := range claims {
			claim, err := c.kube.CoreV1().PersistentVolumeClaims(Namespace(t.ApplicationID)).Get(ctx, claimName, metav1.GetOptions{})
			if apierrors.IsNotFound(err) {
				continue
			}
			if err != nil {
				return err
			}
			if err = owned(claim, t); err != nil {
				return err
			}
			if claim.Spec.VolumeName == "" {
				if selected := claim.Annotations["volume.kubernetes.io/selected-node"]; selected != "" && selected != svc.NodeName {
					return fmt.Errorf("services.%s.node_name: volume %s is provisioning on %s; finish or review its storage placement before moving nodes", name, claimName, selected)
				}
				continue
			}
			pv, err := c.kube.CoreV1().PersistentVolumes().Get(ctx, claim.Spec.VolumeName, metav1.GetOptions{})
			if err != nil {
				return err
			}
			if !volumeFitsNode(pv, n) {
				return fmt.Errorf("services.%s.node_name: existing volume %s cannot attach on %s; migrate or restore its data before changing nodes", name, claimName, svc.NodeName)
			}
		}
	}
	return nil
}

// Pinned workloads compete on their selected node, not the cluster total.
func pinnedRequests(s spec.Service, policy *WorkloadPolicy) (int64, int64) {
	p := spec.EffectiveResources(s)
	if policy != nil && policy.MemoryRequest != "" {
		p.MemoryRequest = policy.MemoryRequest
	}
	cpu, mem := quantityValues(p.CPURequest, p.MemoryRequest)
	replicas := max(s.Replicas, 1)
	if s.Autoscaling != nil {
		replicas = max(replicas, s.Autoscaling.MaxReplicas)
	}
	return cpu * int64(replicas), mem * int64(replicas)
}
