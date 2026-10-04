package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/hakopod/hakopod/internal/database"
	"github.com/jackc/pgx/v5"
)

func validDatabaseResizeRetryReview(d database.Resource, plan database.ResizeRetryReview) bool {
	prefix := fmt.Sprintf("accepted-revision:%d:", d.Revision)
	suffix := strings.TrimPrefix(plan.Resize.TopologyFingerprint, prefix)
	hexFingerprint := regexp.MustCompile(`^[a-f0-9]{64}$`)
	validState := plan.State == "accepted" && strings.HasPrefix(plan.Resize.TopologyFingerprint, prefix) && (suffix == "" || hexFingerprint.MatchString(suffix)) ||
		plan.State == "prior" && hexFingerprint.MatchString(plan.Resize.TopologyFingerprint)
	return (d.Spec.Engine == "mysql" || d.Spec.Engine == "mongodb") && d.Spec.Mode == "cluster" && validState &&
		plan.OperationID != "" && plan.DatabaseID == d.ID && plan.Revision == d.Revision &&
		len(plan.Resize.BlockedReasons) == 0 && plan.ExpiresAt.Equal(plan.Resize.ExpiresAt) &&
		plan.Resize.Proposed.Equal(d.Spec) && plan.Resize.ExpectedRevision == d.Revision-1
}

func (s *Store) LatestDatabaseResizeOperation(ctx context.Context, p Principal, id string) (database.Operation, error) {
	if _, err := s.Database(ctx, p, id, true); err != nil {
		return database.Operation{}, err
	}
	return scanDatabaseOperation(s.Pool.QueryRow(ctx, "SELECT "+databaseOperationCols+" FROM managed_database_operations WHERE database_id=$1 AND kind IN ('resize','resize-retry') ORDER BY created_at DESC,id DESC LIMIT 1", id))
}

// AcceptDatabaseResizeRetry creates a new attempt at the existing desired
// revision. It never changes the desired spec, revision or retained allocation.
func (s *Store) AcceptDatabaseResizeRetry(ctx context.Context, p Principal, id, operationID, reviewID string, expected int64, idem string) (database.Operation, error) {
	var empty database.Operation
	if len(idem) < 8 || len(idem) > 128 || len(operationID) != 32 || len(reviewID) != 32 || expected < 1 {
		return empty, ErrInput
	}
	d, err := s.Database(ctx, p, id, true)
	if err != nil {
		return empty, err
	}
	if s.RequireDatabaseAdmission && s.AdmitDatabase == nil {
		return empty, fmt.Errorf("%w: managed database admission must be configured by Cloud", ErrForbidden)
	}
	hash := sha256.Sum256(JSON(struct {
		ID, OperationID, ReviewID, Kind string
		Revision                        int64
	}{id, operationID, reviewID, "resize-retry", expected}))
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return empty, err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1,46))", p.ID+":"+idem); err != nil {
		return empty, err
	}
	var priorID string
	var priorHash []byte
	err = tx.QueryRow(ctx, "SELECT id,request_hash FROM managed_database_operations WHERE identity_id=$1 AND idempotency_key=$2", p.ID, idem).Scan(&priorID, &priorHash)
	if err == nil {
		if !bytes.Equal(priorHash, hash[:]) {
			return empty, ErrConflict
		}
		return scanDatabaseOperation(tx.QueryRow(ctx, "SELECT "+databaseOperationCols+" FROM managed_database_operations WHERE id=$1", priorID))
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return empty, err
	}
	if s.AdmitDatabase != nil {
		if err = s.AdmitDatabase(ctx, tx, p, d.Project, d.Environment, idem); err != nil {
			return empty, err
		}
	}
	var environment string
	if err = tx.QueryRow(ctx, "SELECT name FROM environments WHERE project=$1 AND name=$2 FOR UPDATE", d.Project, d.Environment).Scan(&environment); err != nil {
		return empty, err
	}
	d, err = scanDatabase(tx.QueryRow(ctx, "SELECT "+databaseCols+" FROM managed_databases WHERE id=$1 AND deleted_at IS NULL FOR UPDATE", id))
	if err != nil {
		return empty, err
	}
	if !p.AllowsDatabase(d.Project, d.Environment, true) {
		return empty, ErrForbidden
	}
	source, err := scanDatabaseOperation(tx.QueryRow(ctx, "SELECT "+databaseOperationCols+" FROM managed_database_operations WHERE id=$1 AND database_id=$2 FOR UPDATE", operationID, id))
	if err != nil {
		return empty, err
	}
	if d.Revision != expected || d.Status != "failed" || source.Revision != expected || source.Status != "failed" || (source.Kind != "resize" && source.Kind != "resize-retry") || source.Review == nil || !source.Spec.Equal(d.Spec) {
		return empty, ErrConflict
	}
	var latestID string
	if err = tx.QueryRow(ctx, "SELECT id FROM managed_database_operations WHERE database_id=$1 AND kind IN ('resize','resize-retry') ORDER BY created_at DESC,id DESC LIMIT 1", id).Scan(&latestID); err != nil {
		return empty, err
	}
	if latestID != source.ID {
		return empty, ErrConflict
	}
	if idle, e := databaseMaintenanceIdle(ctx, tx, id); e != nil {
		return empty, e
	} else if !idle {
		return empty, ErrConflict
	}
	var busy bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM managed_database_operations WHERE database_id=$1 AND status IN ('queued','running')) OR EXISTS(SELECT 1 FROM backup_jobs WHERE status IN ('queued','running') AND (source->>'managed_database_id'=$1 OR target->>'managed_database_id'=$1))`, id).Scan(&busy); err != nil {
		return empty, err
	}
	if busy {
		return empty, ErrConflict
	}
	var plan database.ResizeRetryReview
	err = tx.QueryRow(ctx, "SELECT payload FROM managed_database_reviews WHERE id=$1 AND database_id=$2 AND identity_id=$3 AND revision=$4 AND kind='resize-retry' AND consumed_at IS NULL AND expires_at>now() FOR UPDATE", reviewID, id, p.ID, expected).Scan(&plan)
	if errors.Is(err, pgx.ErrNoRows) {
		return empty, ErrConflict
	}
	if err != nil {
		return empty, err
	}
	if !validDatabaseResizeRetryReview(d, plan) || plan.OperationID != source.ID || !plan.ExpiresAt.After(time.Now()) {
		return empty, ErrConflict
	}
	if !plan.Resize.Current.Equal(source.Review.Current) || plan.Resize.ExpectedRevision != source.Review.ExpectedRevision {
		return empty, ErrConflict
	}
	if _, err = tx.Exec(ctx, "UPDATE managed_database_reviews SET consumed_at=now() WHERE id=$1", reviewID); err != nil {
		return empty, err
	}
	op, err := scanDatabaseOperation(tx.QueryRow(ctx, "INSERT INTO managed_database_operations(id,database_id,revision,identity_id,key_id,idempotency_key,request_hash,kind,spec,review) VALUES($1,$2,$3,$4,$5,$6,$7,'resize-retry',$8,$9) RETURNING "+databaseOperationCols, NewID(), id, expected, p.ID, p.KeyID, idem, hash[:], JSON(d.Spec), JSON(plan.Resize)))
	if err != nil {
		return empty, err
	}
	metadata := map[string]any{"revision": expected, "operation_id": op.ID, "source_operation_id": source.ID, "review_id": reviewID, "state": plan.State, "previous_phase": source.Phase, "previous_message": source.Message, "previous_identity_id": source.IdentityID, "previous_key_id": source.KeyID, "previous_finished_at": source.FinishedAt}
	if _, err = tx.Exec(ctx, "INSERT INTO audit_events(identity_id,key_id,action,resource,metadata) VALUES($1,$2,'database.resize.retry',$3,$4)", p.ID, p.KeyID, id, JSON(metadata)); err != nil {
		return empty, err
	}
	if _, err = tx.Exec(ctx, "UPDATE managed_databases SET status='pending',updated_at=now() WHERE id=$1", id); err != nil {
		return empty, err
	}
	return op, tx.Commit(ctx)
}
