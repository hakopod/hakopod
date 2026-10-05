package api

import (
	"context"
	"github.com/hakopod/hakopod/internal/backup"
	"github.com/hakopod/hakopod/internal/store"
	"net/http"
	"time"
)

func (s *Server) managedDatabaseRestorePlan(w http.ResponseWriter, r *http.Request) {
	if s.Backups == nil || s.Cluster == nil {
		problem(w, 503, "unavailable", "Database recovery is unavailable.")
		return
	}
	var in struct {
		ArtifactID string `json:"artifact_id"`
	}
	if !decode(w, r, &in) {
		return
	}
	d, err := s.Store.Database(r.Context(), who(r), r.PathValue("id"), true)
	if err != nil {
		failure(w, err)
		return
	}
	if d.Status != "ready" || d.Revision != 1 || d.Recovery != nil {
		problem(w, 409, "conflict", "Create a separate, unused database for recovery.")
		return
	}
	a, err := s.Store.BackupArtifact(store.WithBackupPrincipal(r.Context(), who(r)), in.ArtifactID)
	if err != nil {
		backupFailure(w, err)
		return
	}
	if a.Source.Engine != d.Spec.Engine || a.Source.ManagedDatabaseID == d.ID || a.DeletionPending {
		problem(w, 409, "conflict", "Choose a matching archive from a different database.")
		return
	}
	if err = backup.ValidateManagedRecovery(a, d.Spec.Engine, d.Spec.Version); err != nil {
		backupFailure(w, err)
		return
	}
	// Observation, native emptiness verification and durable admission are
	// separate finite stages; each remains bounded by the caller's lifetime.
	observeCtx, cancelObserve := context.WithTimeout(r.Context(), 25*time.Second)
	observed, err := s.Cluster.ObserveDatabase(observeCtx, d)
	cancelObserve()
	if err != nil || observed.Status != "ready" {
		problem(w, 409, "unavailable", "Database health could not be verified.")
		return
	}
	emptyCtx, cancelEmpty := context.WithTimeout(r.Context(), 25*time.Second)
	err = s.Cluster.DatabaseEmpty(emptyCtx, d, observed)
	cancelEmpty()
	if err != nil {
		problem(w, 409, "conflict", err.Error())
		return
	}
	target := backup.Target{Source: backup.Source{Kind: "managed_database", ManagedDatabaseID: d.ID, Engine: d.Spec.Engine}, ManagedDatabaseName: d.Spec.Name, Revision: d.Revision, Available: true, RuntimeFingerprint: observed.TopologyFingerprint}
	plan := backup.RestorePlan{ID: store.NewID(), ArtifactID: a.ID, Target: target, Confirmation: d.Spec.Name, Scope: a.Scope, ExpiresAt: time.Now().UTC().Add(10 * time.Minute), Warnings: []string{"Only this separate database will receive the archive. Source data and application connections remain available.", "The archive is downloaded and fully authenticated before recovery. A failed recovery may leave this new database partial; use another fresh target for a retry.", "Inspect the recovered data before explicitly replacing the application's saved connection and redeploying.", "Writes after the captured recovery point require another capture before final cutover."}}
	if d.Spec.Engine == "redis" {
		plan.Warnings = append(plan.Warnings, "Redis archives preserve values and expiry consistently per shard, not across all shards. Redis Cluster needs a cluster-aware client.")
	}
	if d.Spec.Engine == "vitess" {
		plan.Warnings = append(plan.Warnings, "Vitess captures each shard under its own read lock. Cross-shard transactions do not share one recovery point. The target must have the same shard map and table routing schema.")
	}
	if err = s.Store.SaveBackupRestorePlan(r.Context(), who(r), plan); err != nil {
		backupFailure(w, err)
		return
	}
	write(w, 200, plan)
}
