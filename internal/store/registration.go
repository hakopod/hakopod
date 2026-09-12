package store

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Mail limits survive API restarts and do not retain raw email addresses.
// At most three messages per address per hour, one per minute, and 1000
// active addresses may request an email in one hour.
func (s *Store) AllowAuthMail(ctx context.Context, email string) (bool, error) {
	digest := sha256.Sum256([]byte(email))
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(793044230)"); err != nil {
		return false, err
	}
	if _, err = tx.Exec(ctx, "DELETE FROM auth_mail_limits WHERE window_start<now()-interval '1 hour'"); err != nil {
		return false, err
	}
	var count int
	if err = tx.QueryRow(ctx, "SELECT count(*) FROM auth_mail_limits").Scan(&count); err != nil {
		return false, err
	}
	if count >= 1000 {
		return false, nil
	}
	tag, err := tx.Exec(ctx, `INSERT INTO auth_mail_limits(digest,window_start,last_sent,attempts) VALUES($1,now(),now(),1)
 ON CONFLICT(digest) DO UPDATE SET last_sent=now(),attempts=auth_mail_limits.attempts+1
 WHERE auth_mail_limits.attempts<3 AND auth_mail_limits.last_sent<now()-interval '1 minute'`, digest[:])
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, tx.Commit(ctx)
}

type registrationDraft struct {
	Email, Name  string
	PasswordHash []byte
}

func (s *Store) BeginRegistration(ctx context.Context, name, email, password string) (string, error) {
	email, err := NormalizeEmail(email)
	if err != nil {
		return "", err
	}
	name = strings.TrimSpace(name)
	if len(name) < 1 || len(name) > 100 {
		return "", ErrInput
	}
	hash, err := HashPassword(ctx, password)
	if err != nil {
		return "", err
	}
	allowed, err := s.AllowAuthMail(ctx, email)
	if err != nil || !allowed {
		return "", err
	}
	var exists bool
	if err = s.Pool.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM identities WHERE lower(email)=$1)", email).Scan(&exists); err != nil || exists {
		return "", err
	}
	return s.NewChallenge(ctx, "registration", registrationDraft{email, name, hash}, 15*time.Minute)
}

func consumeChallengeTx(ctx context.Context, tx pgx.Tx, token, kind string) ([]byte, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 2 || len(parts[0]) != 32 || len(parts[1]) != 64 {
		return nil, ErrUnauthorized
	}
	if _, err := hex.DecodeString(parts[1]); err != nil {
		return nil, ErrUnauthorized
	}
	sum := sha256.Sum256([]byte(parts[1]))
	var data []byte
	err := tx.QueryRow(ctx, "DELETE FROM auth_challenges WHERE id=$1 AND kind=$2 AND digest=$3 AND expires_at>now() RETURNING data", parts[0], kind, sum[:]).Scan(&data)
	if errors.Is(err, pgx.ErrNoRows) {
		err = ErrUnauthorized
	}
	return data, err
}

func (s *Store) createVerifiedAccountTx(ctx context.Context, tx pgx.Tx, name, email string, hash []byte) (string, error) {
	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(793044216)"); err != nil {
		return "", err
	}
	var count int
	var setup bool
	if err := tx.QueryRow(ctx, "SELECT count(*),COALESCE(bool_or(owner),false) FROM identities WHERE email IS NOT NULL").Scan(&count, &setup); err != nil {
		return "", err
	}
	if !setup {
		return "", ErrForbidden
	}
	if count >= 200 {
		return "", ErrBusy
	}
	name = strings.TrimSpace(name)
	if name == "" {
		name = strings.Split(email, "@")[0]
	}
	if len(name) > 100 {
		name = "New account"
	}
	id := NewID()
	_, err := tx.Exec(ctx, "INSERT INTO identities(id,name,email,email_verified,password_hash,onboarding_required) VALUES($1,$2,$3,true,$4,true)", id, name, email, hash)
	if err != nil {
		var pgError *pgconn.PgError
		if errors.As(err, &pgError) && pgError.Code == "23505" {
			return "", ErrConflict
		}
		return "", err
	}
	if _, err = tx.Exec(ctx, "INSERT INTO audit_events(identity_id,action,resource) VALUES($1,'account.register',$1)", id); err != nil {
		return "", err
	}
	return id, nil
}

func (s *Store) CompleteRegistration(ctx context.Context, token string) (string, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx)
	data, err := consumeChallengeTx(ctx, tx, token, "registration")
	if err != nil {
		return "", err
	}
	var draft registrationDraft
	if json.Unmarshal(data, &draft) != nil || len(draft.PasswordHash) == 0 {
		return "", ErrUnauthorized
	}
	id, err := s.createVerifiedAccountTx(ctx, tx, draft.Name, draft.Email, draft.PasswordHash)
	if err != nil {
		return "", err
	}
	return id, tx.Commit(ctx)
}

func (s *Store) BeginPasswordReset(ctx context.Context, email string) (string, error) {
	email, err := NormalizeEmail(email)
	if err != nil {
		return "", err
	}
	allowed, err := s.AllowAuthMail(ctx, email)
	if err != nil || !allowed {
		return "", err
	}
	var id string
	var hash []byte
	err = s.Pool.QueryRow(ctx, "SELECT id,password_hash FROM identities WHERE lower(email)=$1 AND NOT disabled", email).Scan(&id, &hash)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(hash)
	return s.NewChallenge(ctx, "password-reset", map[string]string{"identity_id": id, "password_digest": hex.EncodeToString(sum[:])}, 15*time.Minute)
}

func (s *Store) ResetPassword(ctx context.Context, token, password string) error {
	hash, err := HashPassword(ctx, password)
	if err != nil {
		return err
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	data, err := consumeChallengeTx(ctx, tx, token, "password-reset")
	if err != nil {
		return err
	}
	var draft map[string]string
	if json.Unmarshal(data, &draft) != nil {
		return ErrUnauthorized
	}
	var old []byte
	if err = tx.QueryRow(ctx, "SELECT password_hash FROM identities WHERE id=$1 AND NOT disabled FOR UPDATE", draft["identity_id"]).Scan(&old); err != nil {
		return ErrUnauthorized
	}
	sum := sha256.Sum256(old)
	if subtle.ConstantTimeCompare([]byte(hex.EncodeToString(sum[:])), []byte(draft["password_digest"])) != 1 {
		return ErrUnauthorized
	}
	if _, err = tx.Exec(ctx, "UPDATE identities SET password_hash=$2,email_verified=true,failed_logins=0,locked_until=NULL,updated_at=now() WHERE id=$1", draft["identity_id"], hash); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, "UPDATE api_keys SET revoked_at=now() WHERE identity_id=$1 AND kind IN ('browser','cli') AND revoked_at IS NULL", draft["identity_id"]); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, "UPDATE device_codes SET approved=false WHERE identity_id=$1 AND consumed_at IS NULL", draft["identity_id"]); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, "DELETE FROM auth_challenges WHERE kind IN ('password-reset','mfa-login') AND data->>'identity_id'=$1", draft["identity_id"]); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, "INSERT INTO audit_events(identity_id,action,resource) VALUES($1,'account.password.reset',$1)", draft["identity_id"]); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

type OnboardingInvite struct {
	ID        string    `json:"id"`
	TeamName  string    `json:"team_name"`
	Project   string    `json:"project"`
	Role      string    `json:"role"`
	ExpiresAt time.Time `json:"expires_at"`
}
type Onboarding struct {
	Required        bool               `json:"required"`
	PersonalProject string             `json:"personal_project"`
	Invitations     []OnboardingInvite `json:"invitations"`
}

func (s *Store) Onboarding(ctx context.Context, p Principal) (Onboarding, error) {
	out := Onboarding{Invitations: []OnboardingInvite{}}
	if p.CredentialType != "browser" {
		return out, ErrForbidden
	}
	var verified bool
	if err := s.Pool.QueryRow(ctx, "SELECT i.onboarding_required,i.email_verified,COALESCE(w.project,'') FROM identities i LEFT JOIN personal_workspaces w ON w.identity_id=i.id WHERE i.id=$1 AND NOT disabled", p.ID).Scan(&out.Required, &verified, &out.PersonalProject); err != nil {
		return out, err
	}
	if !verified {
		return out, nil
	}
	status, err := s.LicenseStatus(ctx)
	if err != nil {
		return out, err
	}
	if !licenseAllows(status, "invitations") {
		return out, nil
	}
	rows, err := s.Pool.Query(ctx, `SELECT v.id,COALESCE(t.name,''),COALESCE(v.project,''),v.role,v.expires_at FROM invites v LEFT JOIN teams t ON t.id=v.team_id WHERE v.email=$1 AND v.accepted_at IS NULL AND v.expires_at>now() AND (v.team_id IS NULL OR $2) AND (v.project IS NULL OR $3) ORDER BY v.created_at DESC LIMIT 100`, p.Email, licenseAllows(status, "teams"), licenseAllows(status, "project_rbac"))
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var v OnboardingInvite
		if err = rows.Scan(&v.ID, &v.TeamName, &v.Project, &v.Role, &v.ExpiresAt); err != nil {
			return out, err
		}
		out.Invitations = append(out.Invitations, v)
	}
	return out, rows.Err()
}

func personalWorkspaceTx(ctx context.Context, tx pgx.Tx, id string) (string, error) {
	var project string
	err := tx.QueryRow(ctx, "SELECT project FROM personal_workspaces WHERE identity_id=$1", id).Scan(&project)
	if err == nil {
		return project, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return "", err
	}
	project = "personal-" + id[:12]
	if _, err = tx.Exec(ctx, "INSERT INTO projects(name) VALUES($1)", project); err != nil {
		return "", err
	}
	if _, err = tx.Exec(ctx, "INSERT INTO environments(project,name) VALUES($1,'development')", project); err != nil {
		return "", err
	}
	if _, err = tx.Exec(ctx, "INSERT INTO personal_workspaces(identity_id,project) VALUES($1,$2)", id, project); err != nil {
		return "", err
	}
	return project, nil
}

func (s *Store) CompleteOnboarding(ctx context.Context, p Principal, choice, inviteID string) error {
	if p.CredentialType != "browser" || (choice != "personal" && choice != "invite") {
		return ErrForbidden
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(793044216)"); err != nil {
		return err
	}
	var required, verified bool
	if err = tx.QueryRow(ctx, "SELECT onboarding_required,email_verified FROM identities WHERE id=$1 AND NOT disabled FOR UPDATE", p.ID).Scan(&required, &verified); err != nil {
		return err
	}
	if !required || !verified {
		return ErrForbidden
	}
	if choice == "personal" {
		_, err = personalWorkspaceTx(ctx, tx, p.ID)
	} else {
		err = s.acceptVerifiedInviteTx(ctx, tx, p.ID, p.Email, inviteID)
	}
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, "UPDATE identities SET onboarding_required=false WHERE id=$1", p.ID); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, "INSERT INTO audit_events(identity_id,key_id,action,resource) VALUES($1,$2,'account.onboarding',$3)", p.ID, p.KeyID, choice); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) acceptVerifiedInviteTx(ctx context.Context, tx pgx.Tx, id, email, inviteID string) error {
	var team, project, role string
	err := tx.QueryRow(ctx, "SELECT COALESCE(team_id,''),COALESCE(project,''),role FROM invites WHERE id=$1 AND email=$2 AND accepted_at IS NULL AND expires_at>now() FOR UPDATE", inviteID, email).Scan(&team, &project, &role)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrUnauthorized
	}
	if err != nil {
		return err
	}
	features := []string{"invitations"}
	if team != "" {
		features = append(features, "teams")
	}
	if project != "" {
		features = append(features, "project_rbac")
	}
	if err = s.requireFeaturesTx(ctx, tx, features...); err != nil {
		return err
	}
	if team != "" {
		teamRole := role
		if project != "" {
			teamRole = "member"
		}
		if _, err = tx.Exec(ctx, "INSERT INTO team_members(team_id,identity_id,role) VALUES($1,$2,$3) ON CONFLICT DO NOTHING", team, id, teamRole); err != nil {
			return err
		}
	}
	if project != "" {
		if _, err = tx.Exec(ctx, "INSERT INTO project_members(project,identity_id,role) VALUES($1,$2,$3) ON CONFLICT(project,identity_id) DO UPDATE SET role=EXCLUDED.role", project, id, role); err != nil {
			return err
		}
	}
	_, err = tx.Exec(ctx, "UPDATE invites SET accepted_at=now() WHERE id=$1", inviteID)
	return err
}

func (s *Store) InviteDetails(ctx context.Context, token string) (Invite, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return Invite{}, err
	}
	defer tx.Rollback(ctx)
	return s.inviteDetailsTx(ctx, tx, token)
}
func (s *Store) inviteDetailsTx(ctx context.Context, tx pgx.Tx, token string) (Invite, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 2 || len(parts[0]) != 32 || len(parts[1]) != 64 {
		return Invite{}, ErrUnauthorized
	}
	sum := sha256.Sum256([]byte(parts[1]))
	var v Invite
	var digest []byte
	err := tx.QueryRow(ctx, "SELECT id,email,COALESCE(team_id,''),COALESCE(project,''),role,expires_at,digest FROM invites WHERE id=$1 AND accepted_at IS NULL AND expires_at>now() FOR UPDATE", parts[0]).Scan(&v.ID, &v.Email, &v.TeamID, &v.Project, &v.Role, &v.ExpiresAt, &digest)
	if errors.Is(err, pgx.ErrNoRows) || subtle.ConstantTimeCompare(sum[:], digest) != 1 {
		return Invite{}, ErrUnauthorized
	}
	if err != nil {
		return Invite{}, err
	}
	features := []string{"invitations"}
	if v.TeamID != "" {
		features = append(features, "teams")
	}
	if v.Project != "" {
		features = append(features, "project_rbac")
	}
	return v, s.requireFeaturesTx(ctx, tx, features...)
}

// Verified provider emails may register only when public registration is enabled
// or a live invitation proves this address was invited by an authorized member.
func (s *Store) ResolveOAuthAccount(ctx context.Context, provider, subject, email, name string, register bool, inviteToken string) (string, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(793044216)"); err != nil {
		return "", err
	}
	var id string
	err = tx.QueryRow(ctx, "SELECT i.id FROM oauth_identities o JOIN identities i ON i.id=o.identity_id WHERE o.provider=$1 AND o.subject=$2 AND NOT i.disabled", provider, subject).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		err = tx.QueryRow(ctx, "SELECT id FROM identities WHERE lower(email)=$1 AND NOT disabled FOR UPDATE", email).Scan(&id)
		if errors.Is(err, pgx.ErrNoRows) {
			var invited bool
			if inviteToken != "" {
				v, e := s.inviteDetailsTx(ctx, tx, inviteToken)
				if e != nil || v.Email != email {
					return "", ErrForbidden
				}
				invited = true
			}
			if !register && !invited {
				return "", fmt.Errorf("%w: registration requires an invitation", ErrForbidden)
			}
			id, err = s.createVerifiedAccountTx(ctx, tx, name, email, nil)
		}
		if err != nil {
			return "", err
		}
		if _, err = tx.Exec(ctx, "INSERT INTO oauth_identities(provider,subject,identity_id) VALUES($1,$2,$3)", provider, subject, id); err != nil {
			return "", ErrConflict
		}
	} else if err != nil {
		return "", err
	}
	if _, err = tx.Exec(ctx, "UPDATE identities SET email_verified=true WHERE id=$1 AND lower(email)=$2", id, email); err != nil {
		return "", err
	}
	return id, tx.Commit(ctx)
}
