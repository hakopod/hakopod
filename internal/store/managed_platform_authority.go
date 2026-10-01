package store

import (
	"context"
	"crypto/sha256"
	"errors"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"
)

type managedPlatformAuthoritySnapshot struct {
	Principal        Principal
	LicenseRevision  int64
	SecurityRevision int64
	CustomRoles      []managedPlatformCustomRole
}

type managedPlatformCustomRole struct {
	ID          string
	Name        string
	Permissions []string
	Revision    int64
}

// managedPlatformAuthorityTx reads every authority input used by
// Principal.Allows from the accepting transaction. Serializable callers keep
// a concurrent revocation, role change, license change, or MFA policy change
// from committing unnoticed with a review, acceptance, or ownership mutation.
func (s *Store) managedPlatformAuthorityTx(ctx context.Context, tx pgx.Tx, keyID string) (managedPlatformAuthoritySnapshot, []byte, error) {
	var snapshot managedPlatformAuthoritySnapshot
	var p Principal
	err := tx.QueryRow(ctx, `SELECT i.id,i.name,i.admin,k.id,k.project,k.environment,k.application,k.permissions,i.project,i.environment,i.permissions,COALESCE(i.email,''),i.owner,k.kind,i.avatar_style,i.avatar_seed,i.profile_revision,(k.mfa_verified AND (i.totp_secret IS NOT NULL OR EXISTS(SELECT 1 FROM passkeys WHERE identity_id=i.id)))
		FROM api_keys k JOIN identities i ON i.id=k.identity_id
		WHERE k.id=$1 AND k.revoked_at IS NULL
		AND (k.expires_at>now() OR (k.never_expires AND k.kind='machine' AND EXISTS(SELECT 1 FROM automation_key_scopes s WHERE s.key_id=k.id)))
		AND NOT i.disabled
		FOR SHARE OF k,i`, keyID).Scan(
		&p.ID, &p.Name, &p.Admin, &p.KeyID, &p.Project, &p.Environment,
		&p.Application, &p.Permissions, &p.IdentityProject,
		&p.IdentityEnvironment, &p.IdentityPermissions, &p.Email, &p.Owner,
		&p.CredentialType, &p.AvatarStyle, &p.AvatarSeed, &p.ProfileRevision,
		&p.MFAVerified,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return snapshot, nil, ErrUnauthorized
	}
	if err != nil {
		return snapshot, nil, err
	}

	var requireMFA bool
	if err = tx.QueryRow(ctx, "SELECT require_mfa,revision FROM organization_security WHERE singleton FOR SHARE").Scan(&requireMFA, &snapshot.SecurityRevision); err != nil {
		return snapshot, nil, err
	}
	if p.IsHuman() {
		p.MFARequired = requireMFA && !p.MFAVerified
	}

	record, err := readLicense(ctx, tx, " FOR SHARE")
	if err != nil {
		return snapshot, nil, err
	}
	snapshot.LicenseRevision = record.Revision
	status := s.licenseStatus(record)
	if p.Email != "" {
		rows, queryErr := tx.Query(ctx, `SELECT project,'admin' AS role FROM personal_workspaces WHERE identity_id=$1
			UNION SELECT project,role FROM project_members WHERE identity_id=$1 AND $3
			UNION SELECT pt.project,pt.role FROM project_teams pt JOIN team_members tm ON tm.team_id=pt.team_id WHERE tm.identity_id=$1 AND $2 AND $3
			ORDER BY project,role LIMIT 400`, p.ID, licenseAllows(status, "teams"), licenseAllows(status, "project_rbac"))
		if queryErr != nil {
			return snapshot, nil, queryErr
		}
		for rows.Next() {
			var role ProjectRole
			if err = rows.Scan(&role.Project, &role.Role); err != nil {
				rows.Close()
				return snapshot, nil, err
			}
			p.ProjectRoles = append(p.ProjectRoles, role)
		}
		if err = rows.Err(); err != nil {
			rows.Close()
			return snapshot, nil, err
		}
		rows.Close()
	}

	if licenseAllows(status, "custom_roles") {
		rows, queryErr := tx.Query(ctx, "SELECT id,name,permissions,revision FROM custom_roles ORDER BY id LIMIT 101")
		if queryErr != nil {
			return snapshot, nil, queryErr
		}
		for rows.Next() {
			var role managedPlatformCustomRole
			if err = rows.Scan(&role.ID, &role.Name, &role.Permissions, &role.Revision); err != nil {
				rows.Close()
				return snapshot, nil, err
			}
			slices.Sort(role.Permissions)
			snapshot.CustomRoles = append(snapshot.CustomRoles, role)
		}
		if err = rows.Err(); err != nil {
			rows.Close()
			return snapshot, nil, err
		}
		rows.Close()
		if len(snapshot.CustomRoles) > 100 {
			return snapshot, nil, ErrConflict
		}
		for i := range p.ProjectRoles {
			for _, role := range snapshot.CustomRoles {
				if p.ProjectRoles[i].Role == role.ID {
					p.ProjectRoles[i].Name = role.Name
					p.ProjectRoles[i].Permissions = append([]string(nil), role.Permissions...)
				}
			}
		}
	}

	slices.Sort(p.Permissions)
	slices.Sort(p.IdentityPermissions)
	slices.SortFunc(p.ProjectRoles, func(a, b ProjectRole) int {
		if value := strings.Compare(a.Project, b.Project); value != 0 {
			return value
		}
		return strings.Compare(a.Role, b.Role)
	})
	for i := range p.ProjectRoles {
		slices.Sort(p.ProjectRoles[i].Permissions)
	}
	snapshot.Principal = p
	fingerprint := sha256.Sum256(JSON(struct {
		IdentityID, KeyID, CredentialType                        string
		KeyProject, KeyEnvironment, KeyApplication               string
		IdentityProject, IdentityEnvironment                     string
		KeyPermissions, IdentityPermissions                      []string
		ProjectRoles                                             []ProjectRole
		CustomRoles                                              []managedPlatformCustomRole
		Admin, Owner, MFAVerified, MFARequired                   bool
		ProfileRevision, LicenseRevision, SecurityPolicyRevision int64
	}{
		p.ID, p.KeyID, p.CredentialType,
		p.Project, p.Environment, p.Application,
		p.IdentityProject, p.IdentityEnvironment,
		p.Permissions, p.IdentityPermissions,
		p.ProjectRoles, snapshot.CustomRoles,
		p.Admin, p.Owner, p.MFAVerified, p.MFARequired,
		p.ProfileRevision, snapshot.LicenseRevision, snapshot.SecurityRevision,
	}))
	return snapshot, fingerprint[:], nil
}
