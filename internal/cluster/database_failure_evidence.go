package cluster

import (
	"fmt"
	"slices"

	"github.com/hakopod/hakopod/internal/database"
	corev1 "k8s.io/api/core/v1"
)

// databaseFailureEvidence records only exact Kubernetes termination facts.
// Exit code 137 alone does not prove an out-of-memory termination.
func databaseFailureEvidence(d database.Resource, pods []corev1.Pod, observed database.Observation) []database.FailureEvidence {
	items := make([]database.FailureEvidence, 0)
	seen := map[string]bool{}
	for i := range pods {
		pod := &pods[i]
		limits := map[string]*int64{}
		for _, container := range append(slices.Clone(pod.Spec.InitContainers), pod.Spec.Containers...) {
			if value := container.Resources.Limits.Memory(); value != nil && !value.IsZero() {
				bytes := value.Value()
				limits[container.Name] = &bytes
			}
		}
		statuses := append(slices.Clone(pod.Status.InitContainerStatuses), pod.Status.ContainerStatuses...)
		for _, status := range statuses {
			terminated := status.State.Terminated
			if terminated == nil {
				terminated = status.LastTerminationState.Terminated
			}
			if terminated == nil || terminated.Reason != "OOMKilled" || terminated.FinishedAt.IsZero() {
				continue
			}
			key := string(pod.UID) + "\x00" + status.Name + "\x00" + terminated.FinishedAt.UTC().Format("2006-01-02T15:04:05.999999999Z07:00")
			if seen[key] || len(items) == database.MaxFailureEvidencePerObservation {
				continue
			}
			seen[key] = true
			exitCode, restarts := terminated.ExitCode, status.RestartCount
			summary := fmt.Sprintf("%s member %s was terminated because Kubernetes reported OOMKilled.", databaseEngineName(d.Spec.Engine), pod.Name)
			if limit := limits[status.Name]; limit != nil {
				summary = fmt.Sprintf("%s member %s exceeded its %s memory limit.", databaseEngineName(d.Spec.Engine), pod.Name, memoryLimitLabel(*limit))
			}
			items = append(items, database.FailureEvidence{Code: "memory_limit_exceeded", Summary: summary, OccurredAt: terminated.FinishedAt.Time.UTC(), ObservedAt: observed.ObservedAt, Revision: d.Revision, Member: pod.Name, MemberUID: string(pod.UID), Container: status.Name, Reason: terminated.Reason, ExitCode: &exitCode, RestartCount: &restarts, MemoryLimitBytes: limits[status.Name], Source: "kubernetes_container_status"})
		}
	}
	slices.SortFunc(items, func(a, b database.FailureEvidence) int { return b.OccurredAt.Compare(a.OccurredAt) })
	return items
}

func databaseEngineName(engine string) string {
	switch engine {
	case "postgresql":
		return "PostgreSQL"
	case "redis":
		return "Redis"
	case "mysql":
		return "MySQL"
	case "mongodb":
		return "MongoDB"
	case "clickhouse":
		return "ClickHouse"
	case "vitess":
		return "Vitess"
	case "oracle":
		return "Oracle"
	case "duckdb":
		return "DuckDB"
	default:
		return "Database"
	}
}

func memoryLimitLabel(bytes int64) string {
	const mib = int64(1024 * 1024)
	const gib = int64(1024 * 1024 * 1024)
	if bytes > 0 && bytes%gib == 0 {
		return fmt.Sprintf("%d GiB", bytes/gib)
	}
	if bytes > 0 && bytes%mib == 0 {
		return fmt.Sprintf("%d MiB", bytes/mib)
	}
	return fmt.Sprintf("%d-byte", bytes)
}
