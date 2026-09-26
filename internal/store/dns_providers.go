package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/hakopod/hakopod/internal/dnsprovider"
	"github.com/jackc/pgx/v5"
)

// One installation does not need many DNS credentials, and an unbounded list is
// an unbounded response. The same number caps stored rows per scope.
const maxDNSProviders = 64

const dnsProviderLock = 793044245
const dnsProviderColumns = "id,name,kind,project,environment,revision,enabled,zone_filter,credentials,created_at,updated_at"

func scanDNSProvider(row scanner) (dnsprovider.Provider, error) {
	var p dnsprovider.Provider
	err := row.Scan(&p.ID, &p.Name, &p.Kind, &p.Project, &p.Environment, &p.Revision, &p.Enabled, &p.ZoneFilter, &p.EncryptedCredentials, &p.CreatedAt, &p.UpdatedAt)
	return p, err
}

// DNSProviders lists the providers a principal may manage: every provider for an
// administrator, and the installation-wide and own-scope providers for a
// delegated principal. Sealed credentials are removed, so no read path a request
// can reach returns them.
func (s *Store) DNSProviders(ctx context.Context, p Principal) ([]dnsprovider.Provider, error) {
	if !p.CanManageDNSProviders() {
		return nil, ErrForbidden
	}
	query := "SELECT " + dnsProviderColumns + " FROM dns_providers ORDER BY project,environment,lower(name) LIMIT $1"
	args := []any{maxDNSProviders}
	if !p.IsAdmin() {
		query = "SELECT " + dnsProviderColumns + " FROM dns_providers WHERE project='' OR (project=$2 AND environment=$3) ORDER BY project,environment,lower(name) LIMIT $1"
		args = append(args, p.Project, p.Environment)
	}
	rows, err := s.Pool.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []dnsprovider.Provider{}
	for rows.Next() {
		provider, err := scanDNSProvider(rows)
		if err != nil {
			return nil, err
		}
		provider.EncryptedCredentials = nil
		result = append(result, provider)
	}
	return result, rows.Err()
}

// DNSProviderCredential reads one provider including its sealed credentials. It
// takes no principal on purpose: it is for the server's own record writer, which
// needs the bytes to talk to the provider. Handlers must not reach it; request
// paths use DNSProviders, which strips the bytes. The store never inspects or
// logs the bytes.
func (s *Store) DNSProviderCredential(ctx context.Context, id string) (dnsprovider.Provider, error) {
	return scanDNSProvider(s.Pool.QueryRow(ctx, "SELECT "+dnsProviderColumns+" FROM dns_providers WHERE id=$1", id))
}

// PutDNSProvider creates or replaces one provider under the scoped management
// gate, an advisory lock and an expected-revision check. Blank credentials
// preserve the stored ones.
func (s *Store) PutDNSProvider(ctx context.Context, principal Principal, provider dnsprovider.Provider, expected int64) (dnsprovider.Provider, error) {
	if !principal.CanManageDNSProviders() {
		return provider, ErrForbidden
	}
	// A delegated principal manages only inside its own scope and may not create
	// an installation-wide provider.
	if !principal.IsAdmin() && (provider.Project != principal.Project || provider.Environment != principal.Environment) {
		return provider, ErrForbidden
	}
	if err := provider.Validate(); err != nil {
		return provider, err
	}
	if provider.ID == "" {
		provider.ID = NewID()
	}
	if len(provider.EncryptedCredentials) > 8192 {
		return provider, fmt.Errorf("%w: encrypted credentials exceed bounds", dnsprovider.ErrInput)
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return provider, err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1)", dnsProviderLock); err != nil {
		return provider, err
	}
	old, err := scanDNSProvider(tx.QueryRow(ctx, "SELECT "+dnsProviderColumns+" FROM dns_providers WHERE id=$1 FOR UPDATE", provider.ID))
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return provider, err
	}
	if old.Revision != expected || expected < 0 {
		return provider, ErrConflict
	}
	if !principal.IsAdmin() && old.Revision > 0 && (old.Project != principal.Project || old.Environment != principal.Environment) {
		return provider, ErrForbidden
	}
	if len(provider.EncryptedCredentials) == 0 {
		if old.Revision == 0 || !old.CredentialsTransferable(provider) {
			return provider, fmt.Errorf("%w: provide a provider token", dnsprovider.ErrInput)
		}
		provider.EncryptedCredentials = old.EncryptedCredentials
	}
	if expected == 0 {
		var count int
		if err = tx.QueryRow(ctx, "SELECT count(*) FROM dns_providers WHERE project=$1 AND environment=$2", provider.Project, provider.Environment).Scan(&count); err != nil {
			return provider, err
		}
		if count >= maxDNSProviders {
			return provider, fmt.Errorf("%w: at most %d DNS providers per scope", dnsprovider.ErrInput, maxDNSProviders)
		}
	}
	provider.Revision = expected + 1
	result, err := scanDNSProvider(tx.QueryRow(ctx, "INSERT INTO dns_providers(id,name,kind,project,environment,revision,enabled,zone_filter,credentials) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9) ON CONFLICT(id) DO UPDATE SET name=excluded.name,kind=excluded.kind,project=excluded.project,environment=excluded.environment,revision=excluded.revision,enabled=excluded.enabled,zone_filter=excluded.zone_filter,credentials=excluded.credentials,updated_at=now() RETURNING "+dnsProviderColumns,
		provider.ID, provider.Name, provider.Kind, provider.Project, provider.Environment, provider.Revision, provider.Enabled, provider.ZoneFilter, provider.EncryptedCredentials))
	if err != nil {
		return provider, err
	}
	if err = dnsProviderAudit(ctx, tx, principal, "dns.provider.configure", provider.ID); err != nil {
		return provider, err
	}
	return result, tx.Commit(ctx)
}

func (s *Store) DeleteDNSProvider(ctx context.Context, principal Principal, id string, expected int64) error {
	if !principal.CanManageDNSProviders() {
		return ErrForbidden
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1)", dnsProviderLock); err != nil {
		return err
	}
	old, err := scanDNSProvider(tx.QueryRow(ctx, "SELECT "+dnsProviderColumns+" FROM dns_providers WHERE id=$1 FOR UPDATE", id))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrConflict
		}
		return err
	}
	if !principal.IsAdmin() && (old.Project != principal.Project || old.Environment != principal.Environment) {
		return ErrForbidden
	}
	used, err := dnsProviderReferenced(ctx, tx, id)
	if err != nil {
		return err
	}
	if used {
		return fmt.Errorf("%w: provider still owns DNS records", ErrConflict)
	}
	r, err := tx.Exec(ctx, "DELETE FROM dns_providers WHERE id=$1 AND revision=$2", id, expected)
	if err != nil {
		return err
	}
	if r.RowsAffected() != 1 {
		return ErrConflict
	}
	if err = dnsProviderAudit(ctx, tx, principal, "dns.provider.delete", id); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// A credential that still owns records is not deletable: deleting it would leave
// records nothing can remove. Nothing references a provider in this slice yet,
// so the guard asks whether the record table exists before querying it, and
// starts refusing the moment that table lands. The existence check comes first
// because PostgreSQL rejects a statement naming a missing table at parse time.
func dnsProviderReferenced(ctx context.Context, tx pgx.Tx, id string) (bool, error) {
	var exists bool
	if err := tx.QueryRow(ctx, "SELECT to_regclass('public.dns_records') IS NOT NULL").Scan(&exists); err != nil || !exists {
		return false, err
	}
	var used bool
	err := tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM dns_records WHERE provider_id=$1)", id).Scan(&used)
	return used, err
}

func dnsProviderAudit(ctx context.Context, tx pgx.Tx, p Principal, action, id string) error {
	_, err := tx.Exec(ctx, "INSERT INTO audit_events(identity_id,key_id,action,resource) VALUES($1,$2,$3,$4)", p.ID, p.KeyID, action, id)
	return err
}
