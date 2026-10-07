package api

import (
	"context"
	"fmt"
	"github.com/hakopod/hakopod/internal/backup"
	"github.com/hakopod/hakopod/internal/database"
	"io"
	"time"
)

func (r *backupRuntime) RecoverColdStorage(ctx context.Context, job backup.Job) (bool, error) {
	_, found, err := r.server.Store.DatabaseColdStorageFenceForJob(ctx, job.ID)
	if err != nil || !found {
		return found, err
	}
	fence, err := r.server.Store.ClaimDatabaseColdStorageCleanup(ctx, job.ID, job.Lease)
	if err != nil {
		return true, err
	}
	d, err := r.server.Store.DatabaseInternal(ctx, fence.DatabaseID)
	if err != nil || d.Revision != fence.Revision || d.Spec.Engine != "duckdb" {
		return true, fmt.Errorf("MyDuck cold cleanup database identity changed")
	}
	before := func() error { return r.server.Store.CheckDatabaseColdStorageCleanup(ctx, fence) }
	if err = r.server.Cluster.ReconcileMyDuckColdStorage(ctx, d, job.ID, fence.Kind == "backup", before); err != nil {
		return true, err
	}
	if err = r.server.Store.ReleaseDatabaseColdStorageFence(ctx, fence); err != nil {
		return true, err
	}
	return true, nil
}

func (r *backupRuntime) resolveManagedDatabase(ctx context.Context, source backup.Source) (backup.Target, error) {
	target := backup.Target{Source: source}
	if r.server.Cluster == nil {
		return target, fmt.Errorf("database controller is unavailable")
	}
	d, err := r.server.Store.DatabaseInternal(ctx, source.ManagedDatabaseID)
	if err != nil || d.Status != "ready" || d.Spec.Engine != source.Engine {
		return target, fmt.Errorf("managed database is not ready for backup")
	}
	observed, err := r.server.Cluster.ObserveDatabase(ctx, d)
	if err != nil || observed.Status != "ready" {
		return target, fmt.Errorf("managed database health could not be verified")
	}
	target.Revision = d.Revision
	target.SourceVersion = d.Spec.Version
	target.Available = true
	target.ApplicationName = d.Spec.Name
	target.RuntimeFingerprint = observed.TopologyFingerprint
	for _, member := range observed.Members {
		if member.Name == observed.Primary {
			target.Pod = member.Name
			target.PodUID = member.UID
		}
	}
	return target, nil
}
func (r *backupRuntime) dumpManagedDatabase(ctx context.Context, target backup.Target, out io.Writer) error {
	d, err := r.server.Store.DatabaseInternal(ctx, target.ManagedDatabaseID)
	if err != nil || d.Status != "ready" || d.Revision != target.Revision || d.Spec.Engine != target.Engine {
		return fmt.Errorf("managed database changed before backup")
	}
	observed, err := r.server.Cluster.ObserveDatabase(ctx, d)
	if err != nil || observed.Status != "ready" || observed.TopologyFingerprint != target.RuntimeFingerprint {
		return fmt.Errorf("managed database topology changed before backup")
	}
	if d.Spec.Engine == "postgresql" || d.Spec.Engine == "mysql" || d.Spec.Engine == "mongodb" {
		found := false
		for _, m := range observed.Members {
			if m.Name == target.Pod && m.UID == target.PodUID && m.Name == observed.Primary {
				found = true
			}
		}
		if !found {
			return fmt.Errorf("database primary changed before backup")
		}
	}
	if d.Spec.Engine == "duckdb" {
		job, ok := backup.WorkerJobFromContext(ctx)
		if !ok || job.Kind != "backup" {
			return fmt.Errorf("MyDuck cold backup requires the active backup worker")
		}
		if err = r.server.Store.AcquireDatabaseColdStorageFence(ctx, d.ID, d.Revision, job.ID, job.Lease, job.Kind); err != nil {
			return fmt.Errorf("MyDuck cold backup fence is unavailable: %w", err)
		}
		before := func() error {
			return r.server.Store.CheckDatabaseColdStorageWorker(ctx, d.ID, d.Revision, job.ID, job.Lease, job.Kind)
		}
		operationErr := r.server.Cluster.WithMyDuckColdStorage(ctx, d, observed, job.ID, false, before, nil, out)
		cleanup, cancel := context.WithTimeout(context.Background(), 25*time.Second)
		defer cancel()
		fence, claimErr := r.server.Store.ClaimDatabaseColdStorageCleanup(cleanup, job.ID, job.Lease)
		if claimErr != nil {
			return fmt.Errorf("MyDuck cold backup cleanup authority is unavailable: %w", claimErr)
		}
		cleanupBefore := func() error { return r.server.Store.CheckDatabaseColdStorageCleanup(cleanup, fence) }
		if reconcileErr := r.server.Cluster.ReconcileMyDuckColdStorage(cleanup, d, job.ID, true, cleanupBefore); reconcileErr != nil {
			return fmt.Errorf("MyDuck cold backup cleanup is incomplete: %w", reconcileErr)
		}
		if releaseErr := r.server.Store.ReleaseDatabaseColdStorageFence(cleanup, fence); releaseErr != nil {
			return fmt.Errorf("MyDuck cold backup fence could not be cleared: %w", releaseErr)
		}
		return operationErr
	}
	return r.server.Cluster.DumpDatabase(ctx, d, observed, out)
}
func managedBackupTarget(d database.Resource) backup.Target {
	return backup.Target{Source: backup.Source{Kind: "managed_database", ManagedDatabaseID: d.ID, Engine: d.Spec.Engine}, ApplicationName: d.Spec.Name, ManagedDatabaseName: d.Spec.Name, SourceVersion: d.Spec.Version, Revision: d.Revision, Available: d.Status == "ready", Message: "Live database health and ownership are checked before capture. Archives are downloaded and authenticated before verification is recorded."}
}

func (r *backupRuntime) restoreManagedDatabase(ctx context.Context, target backup.Target, input io.Reader) error {
	d, err := r.server.Store.DatabaseInternal(ctx, target.ManagedDatabaseID)
	if err != nil || d.Status != "restoring" || d.Revision != target.Revision || d.Recovery == nil {
		return fmt.Errorf("managed recovery target changed")
	}
	job, err := r.server.Store.BackupJob(ctx, d.Recovery.JobID)
	if err != nil || job.Status != "running" || job.Target == nil || job.Target.ManagedDatabaseID != d.ID || job.ArtifactID != d.Recovery.ArtifactID {
		return fmt.Errorf("managed recovery is not owned by the active backup job")
	}
	observed, err := r.server.Cluster.ObserveDatabase(ctx, d)
	if err != nil || observed.Status != "ready" {
		return fmt.Errorf("managed recovery target is not healthy")
	}
	var restoreErr error
	switch d.Spec.Engine {
	case "duckdb":
		jobContext, ok := backup.WorkerJobFromContext(ctx)
		if !ok || jobContext.Kind != "restore" || jobContext.ID != job.ID || jobContext.Lease != job.Lease {
			return fmt.Errorf("MyDuck cold restore requires the active backup worker")
		}
		if err = r.server.Store.AcquireDatabaseColdStorageFence(ctx, d.ID, d.Revision, job.ID, job.Lease, job.Kind); err != nil {
			return fmt.Errorf("MyDuck cold restore fence is unavailable: %w", err)
		}
		before := func() error {
			return r.server.Store.CheckDatabaseColdStorageWorker(ctx, d.ID, d.Revision, job.ID, job.Lease, job.Kind)
		}
		restoreErr = r.server.Cluster.WithMyDuckColdStorage(ctx, d, observed, job.ID, true, before, input, nil)
		cleanup, cancel := context.WithTimeout(context.Background(), 25*time.Second)
		defer cancel()
		fence, claimErr := r.server.Store.ClaimDatabaseColdStorageCleanup(cleanup, job.ID, job.Lease)
		if claimErr != nil {
			return fmt.Errorf("MyDuck cold restore cleanup authority is unavailable: %w", claimErr)
		}
		cleanupBefore := func() error { return r.server.Store.CheckDatabaseColdStorageCleanup(cleanup, fence) }
		if reconcileErr := r.server.Cluster.ReconcileMyDuckColdStorage(cleanup, d, job.ID, restoreErr == nil, cleanupBefore); reconcileErr != nil {
			return fmt.Errorf("MyDuck cold restore cleanup is incomplete: %w", reconcileErr)
		}
		if releaseErr := r.server.Store.ReleaseDatabaseColdStorageFence(cleanup, fence); releaseErr != nil {
			return fmt.Errorf("MyDuck cold restore fence could not be cleared: %w", releaseErr)
		}
	case "postgresql":
		restoreErr = r.server.Cluster.RestorePostgresDatabase(ctx, d, observed, input)
	case "mysql":
		restoreErr = r.server.Cluster.RestoreMySQLDatabase(ctx, d, observed, input)
	case "mongodb":
		restoreErr = r.server.Cluster.RestoreMongoDBDatabase(ctx, d, observed, input)
	case "clickhouse":
		restoreErr = r.server.Cluster.RestoreClickHouseDatabase(ctx, d, observed, input)
	case "oracle":
		restoreErr = r.server.Cluster.RestoreOracleDatabase(ctx, d, observed, input)
	case "vitess":
		restoreErr = r.server.Cluster.RestoreVitessDatabase(ctx, d, observed, input)
	case "redis":
		restoreErr = r.server.Cluster.RestoreRedisDatabase(ctx, d, observed, input)
	default:
		return fmt.Errorf("unsupported managed database recovery engine")
	}
	if restoreErr != nil {
		return restoreErr
	}
	// Recovery may replace pods to revoke existing sessions. Record those new
	// identities while the job still owns the target and before it can succeed.
	observeCtx, cancelObserve := context.WithTimeout(ctx, 25*time.Second)
	observed, err = r.server.Cluster.ObserveDatabase(observeCtx, d)
	cancelObserve()
	if err != nil || observed.Status != "ready" || observed.Revision != d.Revision {
		return fmt.Errorf("managed recovery result could not be verified")
	}
	recordCtx, cancelRecord := context.WithTimeout(ctx, 5*time.Second)
	defer cancelRecord()
	if err = r.server.Store.ObserveRecoveringDatabase(recordCtx, d.ID, d.Revision, job.ID, observed); err != nil {
		return fmt.Errorf("managed recovery result could not be recorded: %w", err)
	}
	return nil
}
