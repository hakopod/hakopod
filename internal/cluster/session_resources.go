package cluster

import (
	"github.com/hakopod/hakopod/internal/spec"
	"k8s.io/apimachinery/pkg/api/resource"
)

// Session RuntimeClasses declare these reservations. The guard runs before the
// app, so its request is a maximum, not an additional concurrent container.
const (
	sessionRuntimeCPUOverheadMillis   int64 = 20
	sessionRuntimeMemoryOverheadBytes int64 = 50 << 20
	sessionGuardCPURequestMillis      int64 = 50
	sessionGuardMemoryRequestBytes    int64 = 32 << 20
)

func scheduledPodRequests(s spec.Service, policy *WorkloadPolicy) (resource.Quantity, resource.Quantity) {
	p := spec.EffectiveResources(s)
	if policy != nil && policy.MemoryRequest != "" {
		p.MemoryRequest = policy.MemoryRequest
	}
	cpu, memory := resource.MustParse(p.CPURequest), resource.MustParse(p.MemoryRequest)
	if s.Session != nil {
		cpu.SetMilli(max(cpu.MilliValue(), sessionGuardCPURequestMillis) + sessionRuntimeCPUOverheadMillis)
		memory.Set(max(memory.Value(), sessionGuardMemoryRequestBytes) + sessionRuntimeMemoryOverheadBytes)
	}
	return cpu, memory
}
