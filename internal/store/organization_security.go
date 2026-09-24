package store

import (
	"context"
	"fmt"
	"github.com/jackc/pgx/v5"
)

type OrganizationSecurity struct {
	RequireMFA   bool  `json:"require_mfa"`
	Revision     int64 `json:"revision"`
	Members      int   `json:"members"`
	ReadyMembers int   `json:"ready_members"`
}

func (s *Store) OrganizationSecurity(ctx context.Context, p Principal) (OrganizationSecurity, error) {
	if p.CredentialType != "browser" || !p.IsAdmin() {
		return OrganizationSecurity{}, ErrForbidden
	}
	var out OrganizationSecurity
	err := s.Pool.QueryRow(ctx, `SELECT require_mfa,revision,
 (SELECT count(*) FROM identities WHERE email IS NOT NULL AND NOT disabled),
 (SELECT count(*) FROM identities i WHERE email IS NOT NULL AND NOT disabled AND
 (totp_secret IS NOT NULL OR EXISTS(SELECT 1 FROM passkeys WHERE identity_id=i.id)))
 FROM organization_security WHERE singleton`).Scan(&out.RequireMFA, &out.Revision, &out.Members, &out.ReadyMembers)
	return out, err
}

func (s *Store) SetOrganizationSecurity(ctx context.Context, p Principal, required bool, revision int64) (OrganizationSecurity, error) {
	if p.CredentialType != "browser" || !p.IsAdmin() {
		return OrganizationSecurity{}, ErrForbidden
	}
	if required && !p.MFAVerified {
		return OrganizationSecurity{}, ErrMFARequired
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return OrganizationSecurity{}, err
	}
	defer tx.Rollback(ctx)
	if required {
		if err = s.requireFeaturesTx(ctx, tx, "team_mfa"); err != nil {
			return OrganizationSecurity{}, err
		}
	}
	var current int64
	if err = tx.QueryRow(ctx, "SELECT revision FROM organization_security WHERE singleton FOR UPDATE").Scan(&current); err != nil {
		return OrganizationSecurity{}, err
	}
	if revision != current {
		return OrganizationSecurity{}, ErrConflict
	}
	if required {
		var verified bool
		if err = tx.QueryRow(ctx, "SELECT k.mfa_verified AND (i.totp_secret IS NOT NULL OR EXISTS(SELECT 1 FROM passkeys WHERE identity_id=i.id)) FROM identities i JOIN api_keys k ON k.identity_id=i.id WHERE i.id=$1 AND k.id=$2 AND k.revoked_at IS NULL AND k.expires_at>now() FOR SHARE OF i,k", p.ID, p.KeyID).Scan(&verified); err != nil {
			return OrganizationSecurity{}, err
		}
		if !verified {
			return OrganizationSecurity{}, ErrMFARequired
		}
	}
	if _, err = tx.Exec(ctx, "UPDATE organization_security SET require_mfa=$1,revision=revision+1 WHERE singleton", required); err != nil {
		return OrganizationSecurity{}, err
	}
	if _, err = tx.Exec(ctx, "INSERT INTO audit_events(identity_id,key_id,action,resource,metadata) VALUES($1,$2,'organization.mfa','installation',$3)", p.ID, p.KeyID, JSON(map[string]bool{"required": required})); err != nil {
		return OrganizationSecurity{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return OrganizationSecurity{}, err
	}
	return s.OrganizationSecurity(ctx, p)
}

// Lock policy and identity in the same order as policy activation, so concurrent
// factor removals cannot remove the final factor behind an enable request.
func (s *Store) CheckFactorRemovalTx(ctx context.Context, tx pgx.Tx, p Principal, passkey string, totp bool) error {
	var required bool
	if err := tx.QueryRow(ctx, "SELECT require_mfa FROM organization_security WHERE singleton FOR SHARE").Scan(&required); err != nil {
		return err
	}
	if s.ExternalFactorPolicy != nil {
		external, err := s.ExternalFactorPolicy(ctx, tx, p.ID)
		if err != nil {
			return err
		}
		required = required || external
	}
	var identity string
	if err := tx.QueryRow(ctx, "SELECT id FROM identities WHERE id=$1 FOR UPDATE", p.ID).Scan(&identity); err != nil {
		return err
	}
	var remaining bool
	if err := tx.QueryRow(ctx, "SELECT (NOT $2 AND totp_secret IS NOT NULL) OR EXISTS(SELECT 1 FROM passkeys WHERE identity_id=$1 AND id<>$3) FROM identities WHERE id=$1", p.ID, totp, passkey).Scan(&remaining); err != nil {
		return err
	}
	if required && !remaining {
		return fmt.Errorf("%w: your organization requires at least one authenticator or passkey", ErrForbidden)
	}
	if !remaining {
		_, err := tx.Exec(ctx, "UPDATE api_keys SET mfa_verified=false WHERE identity_id=$1 AND kind IN ('browser','cli')", p.ID)
		return err
	}
	return nil
}

// A previously enabled security restriction survives expiry. A downgrade must
// never silently weaken authentication. Administrators can disable it explicitly.
func (s *Store) MFARequired(ctx context.Context) (bool, error) {
	var required bool
	err := s.Pool.QueryRow(ctx, "SELECT require_mfa FROM organization_security WHERE singleton").Scan(&required)
	return required, err
}

func (s *Store) FactorRequired(ctx context.Context, identity string) (bool, error) {
	required, err := s.MFARequired(ctx)
	if err != nil || s.ExternalFactorPolicy == nil {
		return required, err
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	external, err := s.ExternalFactorPolicy(ctx, tx, identity)
	return required || external, err
}
