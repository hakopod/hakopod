package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/hakopod/hakopod/internal/database"
	"github.com/jackc/pgx/v5"
)

func validOracleSwitchoverReview(d database.Resource, plan database.OracleSwitchoverReview) bool {
	if d.Spec.Engine != "oracle" || d.Spec.Oracle == nil || d.Spec.Oracle.Edition != "enterprise" || d.Spec.Mode != "cluster" || plan.DatabaseID != d.ID || plan.Project != d.Project || plan.Environment != d.Environment || plan.Revision != d.Revision || plan.BrokerUID == "" || plan.TopologyFingerprint == "" || plan.Primary == "" || plan.Target == "" || plan.Target == plan.Primary || len(plan.MemberUIDs) != d.Spec.Members() || len(plan.RequestID) != 32 {
		return false
	}
	if _, err := hex.DecodeString(plan.RequestID); err != nil {
		return false
	}
	seen := map[string]bool{}
	for _, uid := range plan.MemberUIDs {
		if uid == "" || len(uid) > 128 || seen[uid] {
			return false
		}
		seen[uid] = true
	}
	target := false
	for i := 0; i < d.Spec.Members(); i++ {
		name := "database"
		if i > 0 {
			name = fmt.Sprintf("database-%d", i)
		}
		if plan.TargetController == name && plan.TargetUniqueName == fmt.Sprintf("HPDB%d", i) {
			target = true
		}
	}
	return target && len(plan.BrokerUID) <= 128 && len(plan.TopologyFingerprint) <= 128 && len(plan.Primary) <= 253 && len(plan.Target) <= 253 && !strings.ContainsAny(plan.Target+plan.Primary, "\r\n\x00")
}

// AcceptOracleSwitchover consumes the exact server-generated review in the same
// transaction as its durable operation and audit. No configuration is changed.
func (s *Store) AcceptOracleSwitchover(ctx context.Context, p Principal, id string, expected int64, reviewID, idem string) (database.Operation, error) {
	var empty database.Operation
	if len(idem) < 8 || len(idem) > 128 || len(reviewID) != 32 || expected < 1 {
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
		ID, ReviewID, Kind string
		Revision           int64
	}{id, reviewID, "switchover", expected}))
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
	if d.Revision != expected || d.Status != "ready" && d.Status != "failed" || d.Recovery != nil && (d.Recovery.RestoredAt == nil || d.Recovery.InspectedAt == nil) {
		return empty, ErrConflict
	}
	if d.Status == "failed" {
		var previousKind, previousStatus, previousPhase string
		if err = tx.QueryRow(ctx, "SELECT kind,status,phase FROM managed_database_operations WHERE database_id=$1 AND revision=$2 ORDER BY created_at DESC,id DESC LIMIT 1", id, expected).Scan(&previousKind, &previousStatus, &previousPhase); err != nil {
			return empty, err
		}
		if previousKind != "switchover" || previousStatus != "failed" || previousPhase != "review" {
			return empty, ErrConflict
		}
	}
	if idle, e := databaseMaintenanceIdle(ctx, tx, id); e != nil {
		return empty, e
	} else if !idle {
		return empty, fmt.Errorf("%w: database maintenance is in progress", ErrConflict)
	}
	var busy bool
	err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM managed_database_operations WHERE database_id=$1 AND status IN ('queued','running')) OR EXISTS(SELECT 1 FROM backup_jobs WHERE status IN ('queued','running') AND (source->>'managed_database_id'=$1 OR target->>'managed_database_id'=$1))`, id).Scan(&busy)
	if err != nil {
		return empty, err
	}
	if busy {
		return empty, fmt.Errorf("%w: a database operation or backup is in progress", ErrConflict)
	}
	var plan database.OracleSwitchoverReview
	err = tx.QueryRow(ctx, "SELECT payload FROM managed_database_reviews WHERE id=$1 AND database_id=$2 AND identity_id=$3 AND revision=$4 AND kind='switchover' AND consumed_at IS NULL AND expires_at>now() FOR UPDATE", reviewID, id, p.ID, expected).Scan(&plan)
	if errors.Is(err, pgx.ErrNoRows) {
		return empty, ErrConflict
	}
	if err != nil {
		return empty, err
	}
	if !validOracleSwitchoverReview(d, plan) || !plan.ExpiresAt.After(time.Now()) {
		return empty, ErrConflict
	}
	if _, err = tx.Exec(ctx, "UPDATE managed_database_reviews SET consumed_at=now() WHERE id=$1", reviewID); err != nil {
		return empty, err
	}
	if _, err = tx.Exec(ctx, "UPDATE managed_databases SET status='pending',updated_at=now() WHERE id=$1", id); err != nil {
		return empty, err
	}
	op, err := scanDatabaseOperation(tx.QueryRow(ctx, "INSERT INTO managed_database_operations(id,database_id,revision,identity_id,key_id,idempotency_key,request_hash,kind,spec,switchover) VALUES($1,$2,$3,$4,$5,$6,$7,'switchover',$8,$9) RETURNING "+databaseOperationCols, NewID(), id, expected, p.ID, p.KeyID, idem, hash[:], JSON(d.Spec), JSON(plan)))
	if err != nil {
		return empty, err
	}
	if _, err = tx.Exec(ctx, "INSERT INTO audit_events(identity_id,key_id,action,resource,metadata) VALUES($1,$2,'database.switchover',$3,$4)", p.ID, p.KeyID, id, JSON(map[string]any{"revision": expected, "operation_id": op.ID, "review_id": reviewID, "request_id": plan.RequestID, "target": plan.TargetController})); err != nil {
		return empty, err
	}
	return op, tx.Commit(ctx)
}

// RetryOracleSwitchover resumes the existing approved request after a worker
// timeout. It never replaces the target or token. A failed broker operation is
// still refused by the runtime and requires native recovery, not blind replay.
func (s *Store) RetryOracleSwitchover(ctx context.Context, p Principal, id, operationID string, expected int64) (database.Operation, error) {
	var empty database.Operation
	d, err := s.Database(ctx, p, id, true)
	if err != nil {
		return empty, err
	}
	if expected < 1 || len(operationID) != 32 {
		return empty, ErrInput
	}
	if s.RequireDatabaseAdmission && s.AdmitDatabase == nil {
		return empty, ErrForbidden
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return empty, err
	}
	defer tx.Rollback(ctx)
	if s.AdmitDatabase != nil {
		if err = s.AdmitDatabase(ctx, tx, p, d.Project, d.Environment, "retry-oracle-"+operationID); err != nil {
			return empty, err
		}
	}
	d, err = scanDatabase(tx.QueryRow(ctx, "SELECT "+databaseCols+" FROM managed_databases WHERE id=$1 AND deleted_at IS NULL FOR UPDATE", id))
	if err != nil {
		return empty, err
	}
	op, err := scanDatabaseOperation(tx.QueryRow(ctx, "SELECT "+databaseOperationCols+" FROM managed_database_operations WHERE id=$1 AND database_id=$2 FOR UPDATE", operationID, id))
	if err != nil {
		return empty, err
	}
	if op.IdentityID != p.ID {
		return empty, ErrForbidden
	}
	if op.Kind != "switchover" || op.Revision != expected || d.Revision != expected || op.Switchover == nil || !validOracleSwitchoverReview(d, *op.Switchover) || !d.Spec.Equal(op.Spec) {
		return empty, ErrConflict
	}
	if op.Status == "queued" || op.Status == "running" || op.Status == "succeeded" {
		return op, nil
	}
	if op.Status != "failed" || d.Status != "failed" || op.Phase == "review" || op.Phase == "switchover" {
		return empty, ErrConflict
	}
	if idle, e := databaseMaintenanceIdle(ctx, tx, id); e != nil {
		return empty, e
	} else if !idle {
		return empty, ErrConflict
	}
	var busy bool
	err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM managed_database_operations WHERE database_id=$1 AND status IN ('queued','running')) OR EXISTS(SELECT 1 FROM backup_jobs WHERE status IN ('queued','running') AND (source->>'managed_database_id'=$1 OR target->>'managed_database_id'=$1))`, id).Scan(&busy)
	if err != nil {
		return empty, err
	}
	if busy {
		return empty, ErrConflict
	}
	if _, err = tx.Exec(ctx, "INSERT INTO audit_events(identity_id,key_id,action,resource,metadata) VALUES($1,$2,'database.switchover.retry',$3,$4)", p.ID, p.KeyID, id, JSON(map[string]any{"operation_id": op.ID, "request_id": op.Switchover.RequestID, "previous_phase": op.Phase, "previous_finished_at": op.FinishedAt})); err != nil {
		return empty, err
	}
	op, err = scanDatabaseOperation(tx.QueryRow(ctx, "UPDATE managed_database_operations SET status='queued',phase='switchover-checking',message='',next_attempt_at=now(),started_at=NULL,finished_at=NULL,lease='',lease_until=NULL,key_id=$2 WHERE id=$1 RETURNING "+databaseOperationCols, operationID, p.KeyID))
	if err != nil {
		return empty, err
	}
	if _, err = tx.Exec(ctx, "UPDATE managed_databases SET status='pending',updated_at=now() WHERE id=$1", id); err != nil {
		return empty, err
	}
	return op, tx.Commit(ctx)
}
