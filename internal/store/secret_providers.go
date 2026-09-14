package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/hakopod/hakopod/internal/secretprovider"
	"github.com/jackc/pgx/v5"
)

const secretProviderCols = "name,revision,config,credentials,created_at,updated_at"

func scanSecretProvider(row scanner) (secretprovider.Provider, error) {
	var p, config secretprovider.Provider
	var data []byte
	if err := row.Scan(&p.Name, &p.Revision, &data, &p.EncryptedCredentials, &p.CreatedAt, &p.UpdatedAt); err != nil {
		return p, err
	}
	if err := json.Unmarshal(data, &config); err != nil {
		return p, err
	}
	config.Name, config.Revision, config.EncryptedCredentials, config.CreatedAt, config.UpdatedAt = p.Name, p.Revision, p.EncryptedCredentials, p.CreatedAt, p.UpdatedAt
	return config, nil
}

// SecretProvider is internal to the resolver and administrator handlers; its
// encrypted credentials are deliberately omitted from JSON serialization.
func (s *Store) SecretProvider(ctx context.Context, name string) (secretprovider.Provider, error) {
	return scanSecretProvider(s.Pool.QueryRow(ctx, "SELECT "+secretProviderCols+" FROM secret_providers WHERE name=$1", name))
}

func (s *Store) SecretProviders(ctx context.Context) ([]secretprovider.Provider, error) {
	rows, err := s.Pool.Query(ctx, "SELECT "+secretProviderCols+" FROM secret_providers ORDER BY name LIMIT 64")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []secretprovider.Provider{}
	for rows.Next() {
		p, err := scanSecretProvider(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, p)
	}
	return result, rows.Err()
}

func (s *Store) PutSecretProvider(ctx context.Context, principal Principal, provider secretprovider.Provider, expected int64) (secretprovider.Provider, error) {
	if !principal.IsAdmin() {
		return provider, ErrForbidden
	}
	if err := provider.Validate(); err != nil {
		return provider, err
	}
	if provider.PrivateCIDRs == nil {
		provider.PrivateCIDRs = []string{}
	}
	for i := range provider.Scopes {
		if provider.Scopes[i].Environments == nil {
			provider.Scopes[i].Environments = []string{}
		}
	}
	if len(provider.EncryptedCredentials) < 28 || len(provider.EncryptedCredentials) > 32<<10 {
		return provider, fmt.Errorf("%w: encrypted credentials are required", secretprovider.ErrInput)
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return provider, err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(793044241)"); err != nil {
		return provider, err
	}
	old, err := scanSecretProvider(tx.QueryRow(ctx, "SELECT "+secretProviderCols+" FROM secret_providers WHERE name=$1 FOR UPDATE", provider.Name))
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return provider, err
	}
	if old.Revision != expected || expected < 0 {
		return provider, ErrConflict
	}
	if old.Revision > 0 && !old.SameSource(provider) {
		return provider, fmt.Errorf("%w: provider source is immutable; create a separate provider", secretprovider.ErrInput)
	}
	if expected == 0 {
		var count int
		if err = tx.QueryRow(ctx, "SELECT count(*) FROM secret_providers").Scan(&count); err != nil {
			return provider, err
		}
		if count >= 64 {
			return provider, fmt.Errorf("%w: at most 64 providers", secretprovider.ErrInput)
		}
	}
	for _, scope := range provider.Scopes {
		var exists bool
		if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM projects WHERE name=$1)", scope.Project).Scan(&exists); err != nil {
			return provider, err
		}
		if !exists {
			return provider, fmt.Errorf("%w: selected project does not exist", secretprovider.ErrInput)
		}
		for _, environment := range scope.Environments {
			if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM environments WHERE project=$1 AND name=$2)", scope.Project, environment).Scan(&exists); err != nil {
				return provider, err
			}
			if !exists {
				return provider, fmt.Errorf("%w: selected environment does not exist", secretprovider.ErrInput)
			}
		}
	}
	provider.Revision = expected + 1
	result, err := scanSecretProvider(tx.QueryRow(ctx, "INSERT INTO secret_providers(name,revision,config,credentials) VALUES($1,$2,$3,$4) ON CONFLICT(name) DO UPDATE SET revision=excluded.revision,config=excluded.config,credentials=excluded.credentials,updated_at=now() RETURNING "+secretProviderCols, provider.Name, provider.Revision, JSON(provider), provider.EncryptedCredentials))
	if err != nil {
		return provider, err
	}
	if _, err = tx.Exec(ctx, "DELETE FROM secret_provider_scopes WHERE provider=$1", provider.Name); err != nil {
		return provider, err
	}
	for _, scope := range provider.Scopes {
		environments := scope.Environments
		if environments == nil {
			environments = []string{}
		}
		if _, err = tx.Exec(ctx, "INSERT INTO secret_provider_scopes(provider,project,environments) VALUES($1,$2,$3)", provider.Name, scope.Project, environments); err != nil {
			return provider, err
		}
	}
	if err = secretProviderAudit(ctx, tx, principal, "secret.provider.configured", provider.Name); err != nil {
		return provider, err
	}
	return result, tx.Commit(ctx)
}

func (s *Store) DeleteSecretProvider(ctx context.Context, principal Principal, name string, expected int64) error {
	if !principal.IsAdmin() {
		return ErrForbidden
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(793044241)"); err != nil {
		return err
	}
	// Provider names are durable references. Refuse deletion while any desired
	// application still uses one; credentials can be rotated without deletion.
	var used bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM applications a, jsonb_each(a.spec->'services') service, LATERAL (SELECT value FROM jsonb_each(COALESCE(service.value->'secrets','{}'::jsonb)) UNION ALL SELECT value->'secret' FROM jsonb_each(COALESCE(service.value->'files','{}'::jsonb)) UNION ALL SELECT value->'password' FROM jsonb_each(COALESCE(service.value->'bindings','{}'::jsonb))) binding WHERE binding.value->>'provider'=$1)`, name).Scan(&used); err != nil {
		return err
	}
	if used {
		return fmt.Errorf("%w: provider is referenced by an application", ErrConflict)
	}
	r, err := tx.Exec(ctx, "DELETE FROM secret_providers WHERE name=$1 AND revision=$2", name, expected)
	if err != nil {
		return err
	}
	if r.RowsAffected() != 1 {
		return ErrConflict
	}
	if err = secretProviderAudit(ctx, tx, principal, "secret.provider.deleted", name); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func secretProviderAudit(ctx context.Context, tx pgx.Tx, p Principal, action, name string) error {
	_, err := tx.Exec(ctx, "INSERT INTO audit_events(identity_id,key_id,action,resource) VALUES($1,$2,$3,$4)", p.ID, p.KeyID, action, name)
	return err
}
