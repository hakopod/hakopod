package cluster

import (
	"context"
	"fmt"
	"net"

	"github.com/hakopod/hakopod/internal/spec"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// WorkloadPolicy is trusted runtime configuration, never application TOML.
// The embedding owns admission and plan rules; the engine enforces placement,
// sandbox selection and resource limits on every reconciliation.
type WorkloadPolicy struct {
	IdleHTTP          bool
	NodeName          string
	Pool              string
	RuntimeClass      string
	MemoryRequest     string
	Quota             map[string]string
	EgressPorts       []int32
	DeniedEgressCIDRs []string
}

type WorkloadPolicyResolver func(context.Context, string, string, spec.Application) (WorkloadPolicy, error)

func (c *Client) workloadPolicy(ctx context.Context, t Target) (*WorkloadPolicy, error) {
	if c.options.WorkloadPolicy == nil {
		return nil, nil
	}
	p, err := c.options.WorkloadPolicy(ctx, t.Project, t.Environment, t.Spec)
	if err != nil {
		return nil, err
	}
	if p.NodeName == "" || len(p.Quota) > 32 || len(p.EgressPorts) > 16 || len(p.DeniedEgressCIDRs) > 16 {
		return nil, fmt.Errorf("invalid runtime workload policy")
	}
	for _, value := range p.Quota {
		if q, e := resource.ParseQuantity(value); e != nil || q.Sign() < 0 {
			return nil, fmt.Errorf("invalid runtime resource quota")
		}
	}
	if p.MemoryRequest != "" {
		if q, e := resource.ParseQuantity(p.MemoryRequest); e != nil || q.Sign() <= 0 {
			return nil, fmt.Errorf("invalid runtime memory request")
		}
	}
	for _, port := range p.EgressPorts {
		if port < 1 || port > 65535 {
			return nil, fmt.Errorf("invalid runtime egress port")
		}
	}
	for _, cidr := range p.DeniedEgressCIDRs {
		if _, _, e := net.ParseCIDR(cidr); e != nil {
			return nil, fmt.Errorf("invalid runtime egress exclusion")
		}
	}
	return &p, nil
}

func (c *Client) validateWorkloadPolicy(ctx context.Context, t Target) error {
	p, err := c.workloadPolicy(ctx, t)
	if err != nil || p == nil {
		return err
	}
	for name, svc := range t.Spec.Services {
		if svc.NodeName != "" && svc.NodeName != p.NodeName {
			return fmt.Errorf("services.%s.node_name conflicts with the node allocated by the runtime", name)
		}
	}
	if p.RuntimeClass != "" {
		runtime, err := c.kube.NodeV1().RuntimeClasses().Get(ctx, p.RuntimeClass, metav1.GetOptions{})
		if err != nil || runtime.Handler != p.RuntimeClass {
			return fmt.Errorf("the configured workload sandbox is unavailable")
		}
	}
	return nil
}

func applyWorkloadPolicy(p *WorkloadPolicy, pod *corev1.PodSpec) {
	if p == nil {
		return
	}
	if pod.NodeSelector == nil {
		pod.NodeSelector = map[string]string{}
	}
	pod.NodeSelector["kubernetes.io/hostname"] = p.NodeName
	if p.Pool != "" {
		pod.NodeSelector["hakopod.com/pool"] = p.Pool
		pod.Tolerations = append(pod.Tolerations, corev1.Toleration{Key: "hakopod.com/pool", Value: p.Pool, Operator: corev1.TolerationOpEqual, Effect: corev1.TaintEffectNoSchedule})
	}
	if p.RuntimeClass != "" {
		pod.RuntimeClassName = ptr(p.RuntimeClass)
	}
	if p.MemoryRequest != "" {
		for i := range pod.Containers {
			pod.Containers[i].Resources.Requests[corev1.ResourceMemory] = resource.MustParse(p.MemoryRequest)
		}
	}
}

// CheckWorkloadPool observes configured scheduling prerequisites before the
// product allocates shared capacity. It never admits arbitrary cluster nodes.
func (c *Client) CheckWorkloadPool(ctx context.Context, nodeName, pool, runtimeClass string) error {
	if c.kube == nil || nodeName == "" || pool == "" || runtimeClass == "" {
		return fmt.Errorf("workload pool is not configured")
	}
	node, err := c.kube.CoreV1().Nodes().Get(ctx, nodeName, metav1.GetOptions{})
	if err != nil || node.Spec.Unschedulable || node.Labels["hakopod.com/pool"] != pool {
		return fmt.Errorf("workload pool is unavailable")
	}
	ready := false
	for _, condition := range node.Status.Conditions {
		if condition.Type == corev1.NodeReady && condition.Status == corev1.ConditionTrue {
			ready = true
		}
	}
	if !ready {
		return fmt.Errorf("workload pool is not ready")
	}
	runtime, err := c.kube.NodeV1().RuntimeClasses().Get(ctx, runtimeClass, metav1.GetOptions{})
	if err != nil || runtime.Handler != runtimeClass {
		return fmt.Errorf("workload sandbox is unavailable")
	}
	return nil
}
