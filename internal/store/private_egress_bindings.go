package store

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5"
)

// PrivateEgressBindingRecord keeps the binding as opaque JSON; the cluster
// package owns its type and validation (it imports store, not the reverse).
type PrivateEgressBindingRecord struct {
	Name      string          `json:"name"`
	Revision  int64           `json:"revision"`
	Config    json.RawMessage `json:"config"`
	CreatedBy string          `json:"created_by"`
	CreatedAt time.Time       `json:"created_at"`
	UpdatedAt time.Time       `json:"updated_at"`
}

const privateEgressCols = "name,revision,config,created_by,created_at,updated_at"

func scanPrivateEgress(row scanner) (PrivateEgressBindingRecord, error) {
	var r PrivateEgressBindingRecord
	err := row.Scan(&r.Name, &r.Revision, &r.Config, &r.CreatedBy, &r.CreatedAt, &r.UpdatedAt)
	return r, err
}

func (s *Store) PrivateEgressBinding(ctx context.Context, name string) (PrivateEgressBindingRecord, error) {
	return scanPrivateEgress(s.Pool.QueryRow(ctx, "SELECT "+privateEgressCols+" FROM private_egress_bindings WHERE name=$1", name))
}

func (s *Store) PrivateEgressBindings(ctx context.Context) ([]PrivateEgressBindingRecord, error) {
	return queryPrivateEgress(ctx, s.Pool)
}

// PrivateEgressBindingConfigs is the runtime source read on every plan,
// accept and reconcile.
func (s *Store) PrivateEgressBindingConfigs(ctx context.Context) ([]json.RawMessage, error) {
	records, err := s.PrivateEgressBindings(ctx)
	configs := make([]json.RawMessage, 0, len(records))
	for _, r := range records {
		configs = append(configs, r.Config)
	}
	return configs, err
}

func queryPrivateEgress(ctx context.Context, q interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}) ([]PrivateEgressBindingRecord, error) {
	// More than the 128-binding validation limit, so overflow is detected rather than truncated.
	rows, err := q.Query(ctx, "SELECT "+privateEgressCols+" FROM private_egress_bindings ORDER BY name LIMIT 129")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []PrivateEgressBindingRecord{}
	for rows.Next() {
		r, err := scanPrivateEgress(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, r)
	}
	return result, rows.Err()
}

// PutPrivateEgressBinding validates the complete database set, with this row
// replaced, under one lock so concurrent writes cannot exceed shared limits.
func (s *Store) PutPrivateEgressBinding(ctx context.Context, principal Principal, name string, config json.RawMessage, expected int64, validate func([]json.RawMessage) error) (PrivateEgressBindingRecord, error) {
	if !principal.IsAdmin() {
		return PrivateEgressBindingRecord{}, ErrForbidden
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return PrivateEgressBindingRecord{}, err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(793044242)"); err != nil {
		return PrivateEgressBindingRecord{}, err
	}
	existing, err := queryPrivateEgress(ctx, tx)
	if err != nil {
		return PrivateEgressBindingRecord{}, err
	}
	var revision int64
	configs := []json.RawMessage{config}
	for _, r := range existing {
		if r.Name == name {
			revision = r.Revision
		} else {
			configs = append(configs, r.Config)
		}
	}
	if revision != expected || expected < 0 {
		return PrivateEgressBindingRecord{}, ErrConflict
	}
	if err = validate(configs); err != nil {
		return PrivateEgressBindingRecord{}, err
	}
	result, err := scanPrivateEgress(tx.QueryRow(ctx, "INSERT INTO private_egress_bindings(name,revision,config,created_by) VALUES($1,$2,$3,$4) ON CONFLICT(name) DO UPDATE SET revision=excluded.revision,config=excluded.config,updated_at=now() RETURNING "+privateEgressCols, name, expected+1, config, principal.ID))
	if err != nil {
		return result, err
	}
	if err = secretProviderAudit(ctx, tx, principal, "private_egress.binding.configured", name); err != nil {
		return result, err
	}
	return result, tx.Commit(ctx)
}

// DeletePrivateEgressBinding revokes immediately: plans fail at once and the
// worker's periodic service maintenance removes the NetworkPolicy rule.
func (s *Store) DeletePrivateEgressBinding(ctx context.Context, principal Principal, name string, expected int64) error {
	if !principal.IsAdmin() {
		return ErrForbidden
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(793044242)"); err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, "DELETE FROM private_egress_bindings WHERE name=$1 AND revision=$2", name, expected)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		var exists bool
		if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM private_egress_bindings WHERE name=$1)", name).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return pgx.ErrNoRows
		}
		return ErrConflict
	}
	if err = secretProviderAudit(ctx, tx, principal, "private_egress.binding.deleted", name); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
