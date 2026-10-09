package store

import (
	"context"
	"fmt"
	"time"

	"github.com/hakopod/hakopod/internal/backup"
	"github.com/jackc/pgx/v5"
)

type backupRecoveryQuery interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

// BackupRecoverySet compares explicitly selected archives. It never substitutes
// the newest backup, which might not belong to the intended recovery set.
func (s *Store) BackupRecoverySet(ctx context.Context, p Principal, source backup.Artifact, ids []string) (backup.Artifact, error) {
	return backupRecoverySet(ctx, s.Pool, p, source, ids)
}
func backupRecoverySet(ctx context.Context, q backupRecoveryQuery, p Principal, source backup.Artifact, ids []string) (backup.Artifact, error) {
	if len(ids) > 16 {
		return source, fmt.Errorf("%w: select at most 16 related archives", backup.ErrInput)
	}
	if len(ids) == 0 {
		return source, nil
	}
	seen := map[string]bool{source.ID: true}
	points := map[string]time.Time{}
	if source.CapturedAt != nil {
		points[source.ID] = *source.CapturedAt
	} else {
		points[source.ID] = time.Time{}
	}
	scoped := WithBackupPrincipal(ctx, p)
	for _, id := range ids {
		if len(id) != 32 || seen[id] {
			return source, fmt.Errorf("%w: related archive identifiers must be unique", backup.ErrInput)
		}
		seen[id] = true
		related, err := scanBackupArtifact(q.QueryRow(ctx, `SELECT `+backupArtifactCols+` FROM backup_artifacts WHERE id=$1 AND deleted_at IS NULL AND NOT deletion_pending AND ($2 OR destination_id IN (SELECT id FROM backup_destinations WHERE config->>'project'=$3 AND config->>'environment'=$4)) FOR SHARE`, id, backupUnscoped(scoped), backupProject(scoped), backupEnvironment(scoped)))
		if err != nil {
			return source, backupError(err)
		}
		if related.CapturedAt == nil {
			points[id] = time.Time{}
		} else {
			points[id] = *related.CapturedAt
		}
	}
	source.CompatibilityEvidence.RelatedRecoveryPoints = points
	return source, nil
}
