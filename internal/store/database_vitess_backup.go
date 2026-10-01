package store

import (
	"context"
	"fmt"

	"github.com/hakopod/hakopod/internal/backup"
	"github.com/hakopod/hakopod/internal/database"
	"github.com/jackc/pgx/v5"
)

// Serialize native storage reservation with destination rotation/deletion and
// other database creations. No customer can opt general backup keys into pods.
func (s *Store) validateVitessBackupTx(ctx context.Context, tx pgx.Tx, p Principal, d database.Resource) error {
	if d.Spec.Engine != "vitess" {
		return nil
	}
	if !p.CanManageBackups() || d.Spec.Vitess == nil {
		return ErrForbidden
	}
	ref := d.Spec.Vitess
	if _, err := backup.FindVitessBackupApproval(s.VitessBackupApprovals, ref.BackupDestinationID, ref.BackupDestinationRevision, d.Project, d.Environment, d.Spec.Name); err != nil {
		return fmt.Errorf("%w: %s", ErrForbidden, err)
	}
	destination, err := scanBackupDestination(tx.QueryRow(ctx, "SELECT "+backupDestinationCols+" FROM backup_destinations WHERE id=$1 FOR UPDATE", ref.BackupDestinationID))
	if err != nil {
		return err
	}
	if destination.Revision != ref.BackupDestinationRevision || destination.Project != d.Project || destination.Environment != d.Environment || destination.AllowHTTP {
		return ErrConflict
	}
	var used bool
	if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM managed_databases WHERE deleted_at IS NULL AND id<>$2 AND spec->'vitess'->>'backup_destination_id'=$1) OR EXISTS(SELECT 1 FROM backup_artifacts WHERE destination_id=$1 AND deleted_at IS NULL) OR EXISTS(SELECT 1 FROM backup_jobs WHERE destination_id=$1 AND status IN ('queued','running')) OR EXISTS(SELECT 1 FROM backup_schedules WHERE destination_id=$1) OR EXISTS(SELECT 1 FROM backup_imports WHERE spec->>'destination_id'=$1 AND status IN ('pending','uploading','cleaning'))", ref.BackupDestinationID, d.ID).Scan(&used); err != nil {
		return err
	}
	if used {
		return fmt.Errorf("%w: Vitess member recovery needs an exclusive destination without archives, schedules or other databases", ErrConflict)
	}
	return nil
}

func vitessDestinationUnusedTx(ctx context.Context, tx pgx.Tx, destination string) error {
	var used bool
	if err := tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM managed_databases WHERE deleted_at IS NULL AND spec->'vitess'->>'backup_destination_id'=$1)", destination).Scan(&used); err != nil {
		return err
	}
	if used {
		return fmt.Errorf("%w: this destination is reserved for Vitess member recovery; use a separate destination for archives", backup.ErrConflict)
	}
	return nil
}

func lockArchiveDestinationTx(ctx context.Context, tx pgx.Tx, destination string) error {
	var revision int64
	if err := tx.QueryRow(ctx, "SELECT revision FROM backup_destinations WHERE id=$1 FOR SHARE", destination).Scan(&revision); err != nil {
		return backupError(err)
	}
	return vitessDestinationUnusedTx(ctx, tx, destination)
}
