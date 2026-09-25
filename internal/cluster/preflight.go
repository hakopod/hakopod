package cluster

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/hakopod/hakopod/internal/spec"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type PreflightCheck struct {
	Code    string `json:"code"`
	Status  string `json:"status"`
	Message string `json:"message"`
}

type PreflightReport struct {
	Checks             []PreflightCheck `json:"checks"`
	CPURequestMillis   int64            `json:"cpu_request_millis"`
	MemoryRequestBytes int64            `json:"memory_request_bytes"`
	StorageGiB         int64            `json:"storage_gib"`
	ObservedAt         time.Time        `json:"observed_at"`
}

func (r PreflightReport) Validate() error {
	for _, c := range r.Checks {
		if c.Status == "blocked" {
			return fmt.Errorf("%s: %s", c.Code, c.Message)
		}
	}
	return nil
}

// Preflight reports observed capacity, never a scheduling reservation. Missing
// node statistics stay unknown; aggregate checks cannot prove bin packing.
func (c *Client) Preflight(parent context.Context, t Target) (PreflightReport, error) {
	r := PreflightReport{Checks: []PreflightCheck{}, ObservedAt: time.Now().UTC()}
	ctx, cancel := context.WithTimeout(parent, 10*time.Second)
	defer cancel()
	add := func(code, status, message string) { r.Checks = append(r.Checks, PreflightCheck{code, status, message}) }
	policy, err := c.workloadPolicy(ctx, t)
	if err != nil {
		return r, err
	}
	var jobCPU, jobMemory int64
	for _, s := range t.Spec.Services {
		p := spec.EffectiveResources(s)
		if policy != nil && policy.MemoryRequest != "" {
			p.MemoryRequest = policy.MemoryRequest
		}
		cpu := resource.MustParse(p.CPURequest)
		memory := resource.MustParse(p.MemoryRequest)
		if s.Job != nil && s.Job.Schedule == nil {
			jobCPU = max(jobCPU, cpu.MilliValue())
			jobMemory = max(jobMemory, memory.Value())
		} else {
			replicas := max(s.Replicas, 1)
			if s.Autoscaling != nil {
				replicas = max(replicas, s.Autoscaling.MaxReplicas)
			}
			r.CPURequestMillis += cpu.MilliValue() * int64(replicas)
			r.MemoryRequestBytes += memory.Value() * int64(replicas)
		}
		if s.Volume != nil {
			r.StorageGiB += s.Volume.SizeGiB
		}
	}
	r.CPURequestMillis += jobCPU
	r.MemoryRequestBytes += jobMemory
	for _, v := range t.Spec.Volumes {
		r.StorageGiB += v.SizeGiB
	}
	if c.kube == nil {
		add("nodes", "unknown", "Kubernetes is unavailable; capacity has not been verified.")
		return r, nil
	}
	if len(t.Spec.Services) == 0 {
		return r, nil
	}
	nodes, err := c.kube.CoreV1().Nodes().List(ctx, metav1.ListOptions{Limit: 201})
	if err != nil {
		return r, fmt.Errorf("capacity observation unavailable: %w", err)
	}
	if nodes.Continue != "" || len(nodes.Items) > 200 {
		return r, fmt.Errorf("capacity observation exceeds 200 nodes")
	}
	if len(nodes.Items) == 0 {
		add("nodes", "unknown", "No nodes were observed. Scheduling capacity cannot be verified.")
		return r, nil
	}
	eligible := map[string]corev1.Node{}
	var cpuFree, memoryFree int64
	nodeFree := map[string][2]int64{}
	for _, node := range nodes.Items {
		if policy != nil && (node.Name != policy.NodeName || policy.Pool != "" && node.Labels["hakopod.com/pool"] != policy.Pool) {
			continue
		}
		ready := false
		for _, cond := range node.Status.Conditions {
			if cond.Type == corev1.NodeReady && cond.Status == corev1.ConditionTrue {
				ready = true
			}
		}
		if !ready || node.Spec.Unschedulable {
			continue
		}
		blocked := false
		for _, taint := range node.Spec.Taints {
			if policy != nil && policy.Pool != "" && taint.Key == "hakopod.com/pool" && taint.Value == policy.Pool && taint.Effect == corev1.TaintEffectNoSchedule {
				continue
			}
			if (taint.Effect == corev1.TaintEffectNoSchedule || taint.Effect == corev1.TaintEffectNoExecute) && taint.Key != "nvidia.com/gpu" {
				blocked = true
			}
		}
		if blocked {
			continue
		}
		eligible[node.Name] = node
		nodeFree[node.Name] = [2]int64{node.Status.Allocatable.Cpu().MilliValue(), node.Status.Allocatable.Memory().Value()}
		cpuFree += node.Status.Allocatable.Cpu().MilliValue()
		memoryFree += node.Status.Allocatable.Memory().Value()
	}
	if len(eligible) == 0 {
		add("nodes", "blocked", "No ready, schedulable nodes accept these workloads. Inspect Infrastructure > Nodes for taints and node health.")
		return r, nil
	}
	for name, s := range t.Spec.Services {
		p := spec.EffectiveResources(s)
		if policy != nil && policy.MemoryRequest != "" {
			p.MemoryRequest = policy.MemoryRequest
		}
		cpu := resource.MustParse(p.CPURequest)
		memory := resource.MustParse(p.MemoryRequest)
		fits := false
		for _, n := range eligible {
			if s.NodeName != "" && n.Name != s.NodeName {
				continue
			}
			if s.Architecture != "" && n.Labels["kubernetes.io/arch"] != s.Architecture {
				continue
			}
			gpuTaint := false
			for _, taint := range n.Spec.Taints {
				if taint.Key == "nvidia.com/gpu" {
					gpuTaint = true
				}
			}
			if gpuTaint && s.GPU == nil {
				continue
			}
			if n.Status.Allocatable.Cpu().Cmp(cpu) >= 0 && n.Status.Allocatable.Memory().Cmp(memory) >= 0 {
				fits = true
			}
		}
		if !fits {
			add("service_capacity", "blocked", name+": no eligible node fits its node placement, architecture and resource requests. Choose a smaller profile or add capacity.")
		}
	}
	pods, err := c.kube.CoreV1().Pods("").List(ctx, metav1.ListOptions{Limit: 10001, FieldSelector: "status.phase!=Succeeded,status.phase!=Failed"})
	if err != nil {
		return r, err
	}
	if pods.Continue != "" || len(pods.Items) > 10000 {
		return r, fmt.Errorf("capacity observation exceeds 10000 active pods")
	}
	for _, pod := range pods.Items {
		if pod.Status.Phase == corev1.PodSucceeded || pod.Status.Phase == corev1.PodFailed {
			continue
		}
		if _, ok := eligible[pod.Spec.NodeName]; !ok {
			continue
		}
		if pod.Namespace == Namespace(t.ApplicationID) && pod.Labels[ownerKey] == ownerID(t.ApplicationID) && pod.Labels[managedBy] == "hakopod" {
			continue
		}
		cpu, memory := podRequests(pod.Spec)
		cpuFree -= cpu + pod.Spec.Overhead.Cpu().MilliValue()
		memoryFree -= memory + pod.Spec.Overhead.Memory().Value()
		free := nodeFree[pod.Spec.NodeName]
		free[0] -= cpu + pod.Spec.Overhead.Cpu().MilliValue()
		free[1] -= memory + pod.Spec.Overhead.Memory().Value()
		nodeFree[pod.Spec.NodeName] = free
	}
	pinned := map[string][2]int64{}
	pinnedJobs := map[string][2]int64{}
	for _, svc := range t.Spec.Services {
		node := svc.NodeName
		if node == "" {
			continue
		}
		cpu, memory := pinnedRequests(svc, policy)
		if svc.Job != nil && svc.Job.Schedule == nil {
			v := pinnedJobs[node]
			v[0] = max(v[0], cpu)
			v[1] = max(v[1], memory)
			pinnedJobs[node] = v
		} else {
			v := pinned[node]
			v[0] += cpu
			v[1] += memory
			pinned[node] = v
		}
	}
	for node, job := range pinnedJobs {
		v := pinned[node]
		v[0] += job[0]
		v[1] += job[1]
		pinned[node] = v
	}
	for node, requested := range pinned {
		free := nodeFree[node]
		if requested[0] > free[0] || requested[1] > free[1] {
			add("node_capacity", "blocked", node+": "+capacityShortage(requested[0], requested[1], free[0], free[1]))
		}
	}
	if cpuFree < r.CPURequestMillis || memoryFree < r.MemoryRequestBytes {
		add("resources", "blocked", capacityShortage(r.CPURequestMillis, r.MemoryRequestBytes, cpuFree, memoryFree))
	} else {
		add("resources", "passed", "Aggregate resource requests fit observed capacity. Rolling updates, node placement and concurrent deployments can require additional headroom.")
	}
	// Kubelet stats are read only when a real REST transport is available.
	if c.restClient() == nil {
		add("disk", "unknown", "Node disk statistics are unavailable; image download and unpack headroom must be verified.")
		return r, nil
	}
	diskKnown, diskOK := 0, 0
	for name := range eligible {
		stream, err := c.kube.CoreV1().RESTClient().Get().AbsPath("/api/v1/nodes/" + name + "/proxy/stats/summary").Stream(ctx)
		if err != nil {
			continue
		}
		var summary struct {
			Node struct {
				Fs struct {
					Available *uint64 `json:"availableBytes"`
				} `json:"fs"`
				Runtime struct {
					ImageFs struct {
						Available *uint64 `json:"availableBytes"`
					} `json:"imageFs"`
				} `json:"runtime"`
			} `json:"node"`
		}
		err = json.NewDecoder(io.LimitReader(stream, 2<<20)).Decode(&summary)
		stream.Close()
		if err != nil || summary.Node.Fs.Available == nil {
			continue
		}
		diskKnown++
		available := *summary.Node.Fs.Available
		if summary.Node.Runtime.ImageFs.Available != nil {
			available = min(available, *summary.Node.Runtime.ImageFs.Available)
		}
		if available >= 2<<30 {
			diskOK++
		}
	}
	if diskKnown == len(eligible) && diskOK == 0 {
		add("disk", "blocked", "Eligible nodes have less than 2 GiB free disk for image downloads and unpacking. Free disk or expand the node before deploying.")
	} else if diskKnown < len(eligible) {
		add("disk", "unknown", "Some node disk statistics are unavailable. Verify image download and unpack headroom before installing large templates.")
	} else {
		add("disk", "passed", "At least one eligible node has 2 GiB free disk. Large images and persistent data need additional capacity; this is a minimum headroom check.")
	}
	return r, nil
}

func quantityValues(cpu, memory string) (int64, int64) {
	c := resource.MustParse(cpu)
	m := resource.MustParse(memory)
	return c.MilliValue(), m.Value()
}

// Describe reservations, not usage or limits: the scheduler must fit requests.
func capacityShortage(cpu, memory, freeCPU, freeMemory int64) string {
	freeCPU, freeMemory = max(0, freeCPU), max(0, freeMemory)
	shortages := []string{}
	if cpu > freeCPU {
		shortages = append(shortages, fmt.Sprintf("CPU short by %g cores", float64(cpu-freeCPU)/1000))
	}
	if memory > freeMemory {
		shortages = append(shortages, "memory short by "+resource.NewQuantity(memory-freeMemory, resource.BinarySI).String())
	}
	return fmt.Sprintf("%s. Requested: %g CPU cores and %s memory. Available after other reservations: %g CPU cores and %s memory. Scheduling uses reservations, not current usage. Reduce resource requests or replicas, or add capacity.", strings.Join(shortages, "; "), float64(cpu)/1000, resource.NewQuantity(memory, resource.BinarySI).String(), float64(freeCPU)/1000, resource.NewQuantity(freeMemory, resource.BinarySI).String())
}

// Restartable init containers remain alongside app containers. Account for
// their running sum as well as each initialization stage, like the scheduler.
func podRequests(pod corev1.PodSpec) (int64, int64) {
	var cpu, memory, sideCPU, sideMemory, initCPU, initMemory int64
	for _, c := range pod.Containers {
		cpu += c.Resources.Requests.Cpu().MilliValue()
		memory += c.Resources.Requests.Memory().Value()
	}
	for _, c := range pod.InitContainers {
		cCPU, cMemory := c.Resources.Requests.Cpu().MilliValue(), c.Resources.Requests.Memory().Value()
		if c.RestartPolicy != nil && *c.RestartPolicy == corev1.ContainerRestartPolicyAlways {
			sideCPU += cCPU
			sideMemory += cMemory
			initCPU, initMemory = max(initCPU, sideCPU), max(initMemory, sideMemory)
		} else {
			initCPU, initMemory = max(initCPU, sideCPU+cCPU), max(initMemory, sideMemory+cMemory)
		}
	}
	return max(cpu+sideCPU, initCPU), max(memory+sideMemory, initMemory)
}
