package api

import (
	"context"
	"fmt"
	"github.com/hakopod/hakopod/internal/backup"
	"github.com/hakopod/hakopod/internal/database"
	"io"
)

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
	if d.Spec.Engine == "postgresql" {
		found := false
		for _, m := range observed.Members {
			if m.Name == target.Pod && m.UID == target.PodUID && m.Name == observed.Primary {
				found = true
			}
		}
		if !found {
			return fmt.Errorf("PostgreSQL primary changed before backup")
		}
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
	if d.Spec.Engine == "postgresql" {
		return r.server.Cluster.RestorePostgresDatabase(ctx, d, observed, input)
	}
	return r.server.Cluster.RestoreRedisDatabase(ctx, d, observed, input)
}
