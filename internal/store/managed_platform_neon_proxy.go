package store

import (
	"bytes"
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

type NeonProxyEndpointRecord struct {
	EndpointID       string
	PlatformID       string
	PlatformRevision int64
	OwnerOperationID string
	Generation       int64
	Enabled          bool
	Address          string
	ServerName       string
	ProjectID        string
	BranchID         string
	ComputeID        string
	EncryptedRoles   []byte
	UpdatedAt        time.Time
}

func (s *Store) ActivateNeonProxyEndpoint(ctx context.Context, op ManagedPlatformOperation, record NeonProxyEndpointRecord) error {
	return retryManagedPlatformWrite(ctx, func() error { return s.activateNeonProxyEndpoint(ctx, op, record) })
}

func (s *Store) activateNeonProxyEndpoint(ctx context.Context, op ManagedPlatformOperation, record NeonProxyEndpointRecord) error {
	if record.EndpointID != op.PlatformID || record.PlatformID != op.PlatformID || record.PlatformRevision != op.Revision || record.OwnerOperationID != op.ID || record.Generation != op.Revision || !record.Enabled || record.Address == "" || record.ServerName == "" || record.ProjectID != op.PlatformID || record.BranchID == "" || record.ComputeID == "" || len(record.EncryptedRoles) < 29 || len(record.EncryptedRoles) > 65536 {
		return ErrInput
	}
	tx, err := s.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if op, _, _, err = s.managedPlatformOperationFenceTx(ctx, tx, op); err != nil {
		return err
	}
	if op.Kind != "create" && op.Kind != "update" {
		return ErrConflict
	}
	var old NeonProxyEndpointRecord
	err = tx.QueryRow(ctx, `SELECT endpoint_id,platform_id,platform_revision,owner_operation_id,generation,enabled,address,server_name,project_id,branch_id,compute_id,encrypted_roles,updated_at FROM managed_platform_neon_proxy_endpoints WHERE endpoint_id=$1 FOR UPDATE`, record.EndpointID).Scan(&old.EndpointID, &old.PlatformID, &old.PlatformRevision, &old.OwnerOperationID, &old.Generation, &old.Enabled, &old.Address, &old.ServerName, &old.ProjectID, &old.BranchID, &old.ComputeID, &old.EncryptedRoles, &old.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		if record.Generation != 1 {
			return ErrConflict
		}
		_, err = tx.Exec(ctx, `INSERT INTO managed_platform_neon_proxy_endpoints(endpoint_id,platform_id,platform_revision,owner_operation_id,generation,enabled,address,server_name,project_id,branch_id,compute_id,encrypted_roles) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`, record.EndpointID, record.PlatformID, record.PlatformRevision, record.OwnerOperationID, record.Generation, record.Enabled, record.Address, record.ServerName, record.ProjectID, record.BranchID, record.ComputeID, record.EncryptedRoles)
		if err != nil {
			return err
		}
		return tx.Commit(ctx)
	}
	if err != nil {
		return err
	}
	if old.PlatformID != record.PlatformID {
		return ErrConflict
	}
	if old.PlatformRevision == record.PlatformRevision && old.OwnerOperationID == record.OwnerOperationID && old.Generation == record.Generation && old.Enabled == record.Enabled && old.Address == record.Address && old.ServerName == record.ServerName && old.ProjectID == record.ProjectID && old.BranchID == record.BranchID && old.ComputeID == record.ComputeID {
		if !bytes.Equal(old.EncryptedRoles, record.EncryptedRoles) {
			if _, err = tx.Exec(ctx, "UPDATE managed_platform_neon_proxy_endpoints SET encrypted_roles=$2,updated_at=now() WHERE endpoint_id=$1", record.EndpointID, record.EncryptedRoles); err != nil {
				return err
			}
		}
		return tx.Commit(ctx)
	}
	if record.Generation <= old.Generation {
		return ErrConflict
	}
	tag, err := tx.Exec(ctx, `UPDATE managed_platform_neon_proxy_endpoints SET platform_revision=$2,owner_operation_id=$3,generation=$4,enabled=$5,address=$6,server_name=$7,project_id=$8,branch_id=$9,compute_id=$10,encrypted_roles=$11,updated_at=now() WHERE endpoint_id=$1 AND platform_id=$12 AND generation=$13`, record.EndpointID, record.PlatformRevision, record.OwnerOperationID, record.Generation, record.Enabled, record.Address, record.ServerName, record.ProjectID, record.BranchID, record.ComputeID, record.EncryptedRoles, record.PlatformID, old.Generation)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrConflict
	}
	return tx.Commit(ctx)
}

func (s *Store) RevokeNeonProxyEndpoint(ctx context.Context, op ManagedPlatformOperation) error {
	return retryManagedPlatformWrite(ctx, func() error { return s.revokeNeonProxyEndpoint(ctx, op) })
}

func (s *Store) revokeNeonProxyEndpoint(ctx context.Context, op ManagedPlatformOperation) error {
	tx, err := s.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if op, _, _, err = s.managedPlatformOperationFenceTx(ctx, tx, op); err != nil {
		return err
	}
	if op.Kind != "delete" {
		return ErrConflict
	}
	rows, err := tx.Query(ctx, `SELECT endpoint_id,generation FROM managed_platform_neon_proxy_endpoints WHERE platform_id=$1 AND enabled FOR UPDATE`, op.PlatformID)
	if err != nil {
		return err
	}
	count := 0
	var generation int64
	for rows.Next() {
		count++
		if count > 1 {
			rows.Close()
			return ErrConflict
		}
		if err = rows.Scan(new(string), &generation); err != nil {
			rows.Close()
			return err
		}
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	if count == 0 {
		return tx.Commit(ctx)
	}
	if op.Revision <= generation {
		return ErrConflict
	}
	tag, err := tx.Exec(ctx, `UPDATE managed_platform_neon_proxy_endpoints SET platform_revision=$2,owner_operation_id=$3,generation=$2,enabled=false,updated_at=now() WHERE platform_id=$1 AND enabled AND generation=$4`, op.PlatformID, op.Revision, op.ID, generation)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrConflict
	}
	return tx.Commit(ctx)
}

func (s *Store) NeonProxyEndpoint(ctx context.Context, endpoint string) (NeonProxyEndpointRecord, error) {
	var record NeonProxyEndpointRecord
	err := s.Pool.QueryRow(ctx, `SELECT e.endpoint_id,e.platform_id,e.platform_revision,e.owner_operation_id,e.generation,e.enabled,e.address,e.server_name,e.project_id,e.branch_id,e.compute_id,e.encrypted_roles,e.updated_at FROM managed_platform_neon_proxy_endpoints e JOIN managed_platforms p ON p.id=e.platform_id AND p.revision=e.platform_revision WHERE e.endpoint_id=$1 AND e.enabled AND p.status='ready' AND p.deleted_at IS NULL`, endpoint).Scan(&record.EndpointID, &record.PlatformID, &record.PlatformRevision, &record.OwnerOperationID, &record.Generation, &record.Enabled, &record.Address, &record.ServerName, &record.ProjectID, &record.BranchID, &record.ComputeID, &record.EncryptedRoles, &record.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return record, pgx.ErrNoRows
	}
	return record, err
}
