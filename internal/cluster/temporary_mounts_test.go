package cluster

import (
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/spec"
	corev1 "k8s.io/api/core/v1"
)

func TestLargeScratchRaisesEphemeralStorage(t *testing.T) {
	for _, c := range []struct {
		size           int64
		request, limit string
	}{{64, "16Mi", "128Mi"}, {2048, "2064Mi", "2176Mi"}} {
		svc := spec.Service{Image: "example.org/test:latest", Size: "small", Replicas: 1, TemporaryMounts: []spec.TemporaryMount{{MountPath: "/tmp", SizeMiB: c.size}}}
		r := deployment(Target{ApplicationID: "scratch"}, "test", svc, time.Minute).Spec.Template.Spec.Containers[0].Resources
		request, limit := r.Requests[corev1.ResourceEphemeralStorage], r.Limits[corev1.ResourceEphemeralStorage]
		if request.String() != c.request || limit.String() != c.limit {
			t.Fatalf("%d MiB scratch: request %s limit %s", c.size, request.String(), limit.String())
		}
	}
}
