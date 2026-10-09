package store

import (
	"context"
	"fmt"
	"time"

	"github.com/hakopod/hakopod/internal/database"
	"github.com/hakopod/hakopod/internal/managedplatform"
	"github.com/jackc/pgx/v5"
	"k8s.io/apimachinery/pkg/api/resource"
)

const maxRecommendationConnections = 250

// PlanDatabaseCapacity reads current reservations and bounded usage evidence.
// It does not reserve capacity or change the requested database specification.
func (s *Store) PlanDatabaseCapacity(ctx context.Context, p Principal, project, environment, databaseID string, next database.Spec) (database.CapacityPlan, error) {
	result := database.CapacityPlan{DatabaseID: databaseID, Spec: next, Advisory: true}
	if !p.AllowsDatabase(project, environment, false) {
		return result, ErrForbidden
	}
	if err := next.Validate(); err != nil {
		return result, fmt.Errorf("%w: %s", ErrInput, err)
	}
	cpu, err := next.CPUReservationMilli()
	if err != nil {
		return result, err
	}
	memory, storage := DatabaseMemoryReservation(next), DatabaseStorageReservation(next)
	result.RequestedAllocation = database.Capacity{CPUMilli: cpu, MemoryBytes: memory, StorageGiB: storage}
	result.EffectiveAllocation = result.RequestedAllocation
	memberCPU := resource.MustParse(next.CPU)
	memberMemory := resource.MustParse(next.Memory)
	result.PerMember = database.ResourceAmount{CPUMilli: memberCPU.MilliValue(), MemoryBytes: memberMemory.Value(), StorageGiB: next.StorageGiB}

	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return result, err
	}
	defer tx.Rollback(ctx)
	var current database.Resource
	if databaseID != "" {
		current, err = scanDatabase(tx.QueryRow(ctx, "SELECT "+databaseCols+" FROM managed_databases WHERE id=$1 AND deleted_at IS NULL", databaseID))
		if err != nil {
			return result, err
		}
		if current.Project != project || current.Environment != environment {
			return result, pgx.ErrNoRows
		}
		var oldCPU, oldMemory, oldStorage int64
		err = tx.QueryRow(ctx, `SELECT reserved_cpu_milli,reserved_memory_bytes,reserved_storage_gib FROM managed_databases WHERE id=$1`, databaseID).Scan(&oldCPU, &oldMemory, &oldStorage)
		if err != nil {
			return result, err
		}
		if oldCPU == 0 {
			oldCPU, err = readOnlyHistoricalDatabaseCPU(ctx, tx, databaseID, 0)
			if err != nil {
				return result, err
			}
		}
		result.EffectiveAllocation.CPUMilli = max(result.EffectiveAllocation.CPUMilli, oldCPU)
		result.EffectiveAllocation.MemoryBytes = max(result.EffectiveAllocation.MemoryBytes, oldMemory)
		result.EffectiveAllocation.StorageGiB = max(result.EffectiveAllocation.StorageGiB, oldStorage)
	}

	used, err := readOnlyDatabaseCapacityUsed(ctx, tx, project, environment, databaseID, "")
	if err != nil {
		return result, err
	}
	pool := ""
	if s.ManagedCapacityPool != nil {
		pool, err = s.ManagedCapacityPool(ctx, tx, project, environment)
		if err != nil {
			return result, err
		}
	}
	platforms, nodes, applications := managedplatform.Capacity{}, map[string]managedplatform.Capacity{}, managedplatform.Capacity{}
	if pool == "" {
		platforms, err = platformCapacityUsed(ctx, tx, project, environment, "")
		if err == nil {
			nodes, err = platformNodeCapacityUsed(ctx, tx, project, environment, "")
		}
	} else {
		used, err = readOnlyDatabaseCapacityUsed(ctx, tx, "", "", databaseID, pool)
		if err == nil {
			platforms, nodes, err = platformPoolCapacityUsed(ctx, tx, pool, "")
		}
		if err == nil {
			applications, err = applicationPoolCapacityUsed(ctx, tx, pool, "", nil)
		}
	}
	if err != nil {
		return result, err
	}
	peak := maxNodeCapacity(nodes)
	used.CPUMilli += peak.CPUMilli + applications.CPUMilli
	used.MemoryBytes += peak.MemoryBytes + applications.MemoryBytes
	used.StorageGiB += platforms.StorageGiB + applications.StorageGiB
	result.Capacity.Used = used
	if s.DatabaseCapacityBudget == nil {
		result.Capacity.Reason = "Database capacity is not available from this installation."
	} else {
		limit, e := s.DatabaseCapacityBudget(ctx, tx, project, environment)
		if e != nil {
			return result, e
		}
		if limit.CPUMilli < 1 || limit.MemoryBytes < 1 || limit.StorageGiB < 1 {
			result.Capacity.Reason = "The database capacity policy does not provide a complete CPU, memory and storage limit."
		} else {
			result.Capacity.Known = true
			result.Capacity.Limit = limit
			result.Capacity.Remaining = subtractCapacity(limit, used)
			result.Capacity.AfterPlan = subtractCapacity(result.Capacity.Remaining, result.EffectiveAllocation)
		}
	}

	usage, err := recommendationUsage(ctx, tx, current, project, environment, databaseID)
	if err != nil {
		return result, err
	}
	result.Usage = usage
	result.Recommendation, err = database.RecommendResources(next, usage)
	return result, err
}

func subtractCapacity(left, right database.Capacity) database.Capacity {
	return database.Capacity{CPUMilli: left.CPUMilli - right.CPUMilli, MemoryBytes: left.MemoryBytes - right.MemoryBytes, StorageGiB: left.StorageGiB - right.StorageGiB}
}

func readOnlyHistoricalDatabaseCPU(ctx context.Context, tx pgx.Tx, id string, total int64) (int64, error) {
	rows, err := tx.Query(ctx, `SELECT spec FROM managed_database_operations WHERE database_id=$1 AND kind<>'resize-retry' ORDER BY revision DESC LIMIT 129`, id)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		count++
		var spec database.Spec
		if err = rows.Scan(&spec); err != nil {
			return 0, err
		}
		value, e := spec.CPUReservationMilli()
		if e != nil {
			return 0, e
		}
		total = max(total, value)
	}
	if count > 128 {
		return 0, fmt.Errorf("existing database CPU history exceeds the migration bound")
	}
	return total, rows.Err()
}

func readOnlyDatabaseCapacityUsed(ctx context.Context, tx pgx.Tx, project, environment, except, pool string) (database.Capacity, error) {
	query := `SELECT d.id,d.spec,d.reserved_cpu_milli,d.reserved_memory_bytes,d.reserved_storage_gib FROM managed_databases d WHERE d.project=$1 AND d.environment=$2 AND d.id<>$3 AND d.deleted_at IS NULL ORDER BY d.id LIMIT 65`
	args := []any{project, environment, except}
	limit := database.MaxDatabases
	if pool != "" {
		query = `SELECT d.id,d.spec,d.reserved_cpu_milli,d.reserved_memory_bytes,d.reserved_storage_gib FROM managed_databases d JOIN managed_capacity_scopes s ON s.project=d.project AND s.environment=d.environment WHERE s.capacity_pool=$1 AND d.id<>$2 AND d.deleted_at IS NULL ORDER BY d.id LIMIT 1001`
		args, limit = []any{pool, except}, 1000
	}
	rows, err := tx.Query(ctx, query, args...)
	if err != nil {
		return database.Capacity{}, err
	}
	defer rows.Close()
	total, count := database.Capacity{}, 0
	for rows.Next() {
		count++
		var id string
		var spec database.Spec
		var cpu, memory, storage int64
		if err = rows.Scan(&id, &spec, &cpu, &memory, &storage); err != nil {
			return total, err
		}
		if cpu == 0 {
			cpu, err = spec.CPUReservationMilli()
			if err == nil {
				cpu, err = readOnlyHistoricalDatabaseCPU(ctx, tx, id, cpu)
			}
			if err != nil {
				return total, err
			}
		}
		total.CPUMilli += cpu
		total.MemoryBytes += memory
		total.StorageGiB += storage
	}
	if count > limit {
		return total, fmt.Errorf("database capacity inventory exceeds its bound")
	}
	return total, rows.Err()
}

func recommendationUsage(ctx context.Context, tx pgx.Tx, current database.Resource, project, environment, databaseID string) (database.RecommendationUsage, error) {
	usage := database.RecommendationUsage{}
	if databaseID == "" {
		return usage, nil
	}
	now := time.Now().UTC()
	if current.Observation.Fresh(now, current.Revision) {
		for _, member := range current.Observation.Members {
			if member.Metrics == nil || !member.Metrics.Available || member.Metrics.CPU == nil || member.Metrics.Memory == nil {
				continue
			}
			usage.CurrentAvailable = true
			if usage.CurrentCPUMilli == nil || *member.Metrics.CPU > *usage.CurrentCPUMilli {
				value := *member.Metrics.CPU
				usage.CurrentCPUMilli = &value
			}
			if usage.CurrentMemoryBytes == nil || *member.Metrics.Memory > *usage.CurrentMemoryBytes {
				value := *member.Metrics.Memory
				usage.CurrentMemoryBytes = &value
			}
		}
	}
	rows, err := tx.Query(ctx, `SELECT point FROM managed_database_metric_samples WHERE database_id=$1 AND bucket>=now()-interval '24 hours' ORDER BY bucket LIMIT 1441`, databaseID)
	if err != nil {
		return usage, err
	}
	for rows.Next() {
		var point database.MetricPoint
		if err = rows.Scan(&point); err != nil {
			rows.Close()
			return usage, err
		}
		if point.Revision != current.Revision || point.Resources == nil || !point.Resources.Available || point.Resources.CPU == nil || point.Resources.Memory == nil {
			continue
		}
		members := max(current.Spec.Members(), 1)
		cpu, memory := *point.Resources.CPU/float64(members), *point.Resources.Memory/int64(members)
		if usage.Peak24hCPUMilli == nil || cpu > *usage.Peak24hCPUMilli {
			value := cpu
			usage.Peak24hCPUMilli = &value
		}
		if usage.Peak24hMemoryBytes == nil || memory > *usage.Peak24hMemoryBytes {
			value := memory
			usage.Peak24hMemoryBytes = &value
		}
		observed := point.ObservedAt
		if usage.OldestSampleAt == nil {
			usage.OldestSampleAt = &observed
		}
		usage.NewestSampleAt = &observed
		usage.Samples24h++
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return usage, err
	}
	connRows, err := tx.Query(ctx, `SELECT DISTINCT a.id,svc.key FROM applications a CROSS JOIN LATERAL jsonb_each(a.spec->'services') svc CROSS JOIN LATERAL jsonb_each(COALESCE(svc.value->'bindings','{}'::jsonb)) b WHERE a.project=$1 AND a.environment=$2 AND b.value->>'managed_database'=$3 ORDER BY a.id,svc.key LIMIT $4`, project, environment, databaseID, maxRecommendationConnections+1)
	if err != nil {
		return usage, err
	}
	apps, services := map[string]bool{}, map[string]bool{}
	for connRows.Next() {
		var appID, service string
		if err = connRows.Scan(&appID, &service); err != nil {
			connRows.Close()
			return usage, err
		}
		if len(services) == maxRecommendationConnections {
			usage.ConnectionsTruncated = true
			break
		}
		apps[appID] = true
		services[appID+"\x00"+service] = true
	}
	connRows.Close()
	if err = connRows.Err(); err != nil {
		return usage, err
	}
	usage.ConnectedApplications, usage.ConnectedServices = len(apps), len(services)
	return usage, nil
}
