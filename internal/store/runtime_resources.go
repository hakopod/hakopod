package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

type RuntimeResource struct {
	Kind        string          `json:"kind"`
	Project     string          `json:"project"`
	Environment string          `json:"environment"`
	Name        string          `json:"name"`
	Revision    int64           `json:"revision"`
	Metadata    json.RawMessage `json:"metadata"`
	CreatedAt   time.Time       `json:"created_at"`
	UpdatedAt   time.Time       `json:"updated_at"`
}

const runtimeCols = "kind,project,environment,name,revision,metadata,created_at,updated_at"

func scanRuntime(row scanner) (RuntimeResource, error) {
	var value RuntimeResource
	err := row.Scan(&value.Kind, &value.Project, &value.Environment, &value.Name, &value.Revision, &value.Metadata, &value.CreatedAt, &value.UpdatedAt)
	return value, err
}
func (s *Store) RuntimeResource(ctx context.Context, kind, project, environment, name string) (RuntimeResource, error) {
	return scanRuntime(s.Pool.QueryRow(ctx, "SELECT "+runtimeCols+" FROM runtime_resources WHERE kind=$1 AND project=$2 AND environment=$3 AND name=$4", kind, project, environment, name))
}
func (s *Store) RuntimeResources(ctx context.Context, kind, project, environment string) ([]RuntimeResource, error) {
	rows, err := s.Pool.Query(ctx, "SELECT "+runtimeCols+" FROM runtime_resources WHERE kind=$1 AND project=$2 AND environment=$3 ORDER BY name LIMIT 64", kind, project, environment)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []RuntimeResource{}
	for rows.Next() {
		item, err := scanRuntime(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}
func (s *Store) PutRuntimeResource(ctx context.Context, p Principal, kind, project, environment, name string, expected int64, metadata any) (RuntimeResource, error) {
	if !runtimeAllowed(p, kind, project, environment) {
		return RuntimeResource{}, ErrForbidden
	}
	if kind == "virtual-network" {
		value, err := validateVirtualNetworkMetadata(metadata, name)
		if err != nil {
			return RuntimeResource{}, err
		}
		metadata = value
	}
	data, err := json.Marshal(metadata)
	if err != nil || len(data) > 64<<10 {
		return RuntimeResource{}, errors.New("runtime metadata exceeds bounds")
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return RuntimeResource{}, err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1,31))", kind+":"+project+":"+environment); err != nil {
		return RuntimeResource{}, err
	}
	if project != "" {
		var exists bool
		if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM environments WHERE project=$1 AND name=$2)", project, environment).Scan(&exists); err != nil {
			return RuntimeResource{}, err
		}
		if !exists {
			return RuntimeResource{}, errors.New("project/environment does not exist")
		}
	}
	old, err := scanRuntime(tx.QueryRow(ctx, "SELECT "+runtimeCols+" FROM runtime_resources WHERE kind=$1 AND project=$2 AND environment=$3 AND name=$4 FOR UPDATE", kind, project, environment, name))
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return RuntimeResource{}, err
	}
	if old.Revision != expected {
		return RuntimeResource{}, ErrConflict
	}
	if kind == "virtual-network" {
		value := metadata.(VirtualNetworkMetadata)
		if old.Revision > 0 {
			var previous VirtualNetworkMetadata
			if json.Unmarshal(old.Metadata, &previous) != nil || previous.ID == "" {
				return RuntimeResource{}, errors.New("virtual network configuration is unavailable")
			}
			if previous.ID != value.ID {
				return RuntimeResource{}, ErrConflict
			}
		}
		if err = validateVirtualNetworkChange(ctx, tx, project, environment, name, &value.Spec); err != nil {
			return RuntimeResource{}, err
		}
	}
	if old.Revision == 0 {
		var count int
		if err = tx.QueryRow(ctx, "SELECT count(*) FROM runtime_resources WHERE kind=$1 AND project=$2 AND environment=$3", kind, project, environment).Scan(&count); err != nil {
			return RuntimeResource{}, err
		}
		if count >= 64 {
			return RuntimeResource{}, errors.New("at most 64 runtime resources per kind and scope are supported")
		}
	}
	next, err := scanRuntime(tx.QueryRow(ctx, "INSERT INTO runtime_resources(kind,project,environment,name,revision,metadata) VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT(kind,project,environment,name) DO UPDATE SET revision=excluded.revision,metadata=excluded.metadata,updated_at=now() RETURNING "+runtimeCols, kind, project, environment, name, expected+1, data))
	if err != nil {
		return RuntimeResource{}, err
	}
	if _, err = tx.Exec(ctx, "INSERT INTO runtime_resource_history(kind,project,environment,name,revision,identity_id,metadata) VALUES($1,$2,$3,$4,$5,$6,$7)", kind, project, environment, name, next.Revision, p.ID, data); err != nil {
		return RuntimeResource{}, err
	}
	if _, err = tx.Exec(ctx, "DELETE FROM runtime_resource_history WHERE kind=$1 AND project=$2 AND environment=$3 AND name=$4 AND revision<$5", kind, project, environment, name, next.Revision-30); err != nil {
		return RuntimeResource{}, err
	}
	if _, err = tx.Exec(ctx, "INSERT INTO audit_events(identity_id,key_id,action,resource,metadata) VALUES($1,$2,$3,$4,$5)", p.ID, p.KeyID, kind+".configured", project+"/"+environment+"/"+name, JSON(map[string]any{"revision": next.Revision})); err != nil {
		return RuntimeResource{}, err
	}
	return next, tx.Commit(ctx)
}
func (s *Store) DeleteRuntimeResource(ctx context.Context, p Principal, kind, project, environment, name string, expected int64) error {
	return s.deleteRuntimeResource(ctx, p, kind, project, environment, name, expected, "")
}

func (s *Store) deleteRuntimeResource(ctx context.Context, p Principal, kind, project, environment, name string, expected int64, expectedID string) error {
	if !runtimeAllowed(p, kind, project, environment) {
		return ErrForbidden
	}
	if kind == "virtual-network" && (len(expectedID) != 32 || expected < 1) {
		return ErrConflict
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1,31))", kind+":"+project+":"+environment); err != nil {
		return err
	}
	if kind == "virtual-network" {
		var currentID string
		err := tx.QueryRow(ctx, "SELECT metadata->>'id' FROM runtime_resources WHERE kind=$1 AND project=$2 AND environment=$3 AND name=$4 AND revision=$5 FOR UPDATE", kind, project, environment, name, expected).Scan(&currentID)
		if errors.Is(err, pgx.ErrNoRows) || err == nil && currentID != expectedID {
			return ErrConflict
		}
		if err != nil {
			return err
		}
		if err = validateVirtualNetworkChange(ctx, tx, project, environment, name, nil); err != nil {
			return err
		}
	}
	row, err := tx.Exec(ctx, "DELETE FROM runtime_resources WHERE kind=$1 AND project=$2 AND environment=$3 AND name=$4 AND revision=$5", kind, project, environment, name, expected)
	if err != nil {
		return err
	}
	if row.RowsAffected() != 1 {
		return ErrConflict
	}
	if _, err = tx.Exec(ctx, "DELETE FROM runtime_resource_history WHERE kind=$1 AND project=$2 AND environment=$3 AND name=$4", kind, project, environment, name); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, "INSERT INTO audit_events(identity_id,key_id,action,resource) VALUES($1,$2,$3,$4)", p.ID, p.KeyID, kind+".deleted", project+"/"+environment+"/"+name); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
func runtimeAllowed(p Principal, kind, project, environment string) bool {
	if kind == "virtual-network" {
		return project != "" && environment != "" && p.CanManageVirtualNetworks(project, environment)
	}
	if kind == "registry" {
		return project != "" && environment != "" && p.Allows("deployments:write", project, environment, "")
	}
	return p.IsAdmin() && (kind == "tls-issuer" || kind == "proxy" || kind == "enrollment") && project == "" && environment == ""
}
func (s *Store) RegistrySecretName(ctx context.Context, project, environment, name string) (string, error) {
	resource, err := s.RuntimeResource(ctx, "registry", project, environment, name)
	if err != nil {
		return "", err
	}
	var value struct {
		SecretName string `json:"secret_name"`
	}
	if json.Unmarshal(resource.Metadata, &value) != nil || value.SecretName == "" {
		return "", fmt.Errorf("registry credential metadata is unavailable")
	}
	return value.SecretName, nil
}

// Enrollment secrets expire independently in Kubernetes. Retain the audit
// event, but retire expired metadata so the active-resource bound cannot fill
// permanently during normal use.
func (s *Store) PruneExpiredEnrollments(ctx context.Context, p Principal) error {
	if !p.IsAdmin() {
		return ErrForbidden
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended('enrollment::',31))"); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, "DELETE FROM runtime_resource_history WHERE kind='enrollment' AND project='' AND environment='' AND name IN (SELECT name FROM runtime_resources WHERE kind='enrollment' AND project='' AND environment='' AND (metadata->>'expires_at')::timestamptz<now())"); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, "DELETE FROM runtime_resources WHERE kind='enrollment' AND project='' AND environment='' AND (metadata->>'expires_at')::timestamptz<now()"); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
