package cluster

import (
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/database"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestDatabaseMetricsRejectIncompleteOrUnrelatedSamples(t *testing.T) {
	now := time.Now()
	pod := corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "database-1", Namespace: "hdb-fixture", UID: "member-uid", CreationTimestamp: metav1.NewTime(now.Add(-time.Minute))}, Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "database"}, {Name: "sidecar"}}}, Status: corev1.PodStatus{Phase: corev1.PodRunning}}
	base := podMetrics{Metadata: pod.ObjectMeta, Timestamp: metav1.NewTime(now), Window: metav1.Duration{Duration: 15 * time.Second}}
	for _, name := range []string{"database", "sidecar"} {
		base.Containers = append(base.Containers, struct {
			Name  string              `json:"name"`
			Usage corev1.ResourceList `json:"usage"`
		}{name, corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("10m"), corev1.ResourceMemory: resource.MustParse("16Mi")}})
	}
	for _, test := range []struct {
		name      string
		change    func(*podMetrics)
		expected  int
		available bool
	}{
		{"complete", func(*podMetrics) {}, 1, true},
		{"missing member", func(*podMetrics) {}, 2, false},
		{"missing container", func(m *podMetrics) { m.Containers = m.Containers[:1] }, 1, false},
		{"stale", func(m *podMetrics) { m.Timestamp = metav1.NewTime(now.Add(-3 * time.Minute)) }, 1, false},
		{"future", func(m *podMetrics) { m.Timestamp = metav1.NewTime(now.Add(time.Minute)) }, 1, false},
		{"replaced member", func(m *podMetrics) { m.Metadata.UID = "old-uid" }, 1, false},
		{"foreign namespace", func(m *podMetrics) { m.Metadata.Namespace = "other" }, 1, false},
		{"prior lifetime", func(m *podMetrics) { m.Timestamp = metav1.NewTime(now.Add(-90 * time.Second)) }, 1, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			observation := database.Observation{Members: []database.Member{{Name: pod.Name, UID: string(pod.UID)}}}
			sample := base
			sample.Metadata = *base.Metadata.DeepCopy()
			test.change(&sample)
			applyDatabaseMetrics(&observation, []corev1.Pod{pod}, []podMetrics{sample}, test.expected)
			if observation.Metrics.Available != test.available {
				t.Fatalf("availability: %+v", observation.Metrics)
			}
			if test.available {
				if *observation.Metrics.CPU != 20 || *observation.Metrics.Memory != 32<<20 {
					t.Fatalf("usage: %+v", observation.Metrics)
				}
			} else if observation.Metrics.CPU != nil || observation.Metrics.Memory != nil || observation.Metrics.SampledAt != nil {
				t.Fatal("incomplete aggregate was exposed")
			}
		})
	}
	observation := database.Observation{Members: []database.Member{{Name: pod.Name, UID: string(pod.UID)}}}
	applyDatabaseMetrics(&observation, []corev1.Pod{pod}, []podMetrics{base, base}, 1)
	if observation.Metrics.Available {
		t.Fatal("ambiguous duplicate sample accepted")
	}
	applyDatabaseMetrics(&observation, []corev1.Pod{pod}, []podMetrics{base}, 1)
	applyDatabaseMetrics(&observation, []corev1.Pod{pod}, nil, 1)
	if observation.Members[0].Metrics.Available || observation.Metrics.Available {
		t.Fatal("previous values survived an unavailable observation")
	}
}
