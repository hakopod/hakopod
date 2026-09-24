package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/mail"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"golang.org/x/crypto/bcrypt"
)

var ErrInput = errors.New("invalid input")
var ErrMFARequired = errors.New("a two-factor authentication code is required")
var ErrBusy = errors.New("authentication is busy; retry shortly")
var passwordSlots = make(chan struct{}, 2)
var dummyPasswordHash = []byte("$2a$12$9hswx82lzTYpeHnyKuAfIebk3xaOlIRXNvLQqetXYptumZJNyZhCy")

func NormalizeEmail(value string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	parsed, err := mail.ParseAddress(value)
	if err != nil || parsed.Address != value || len(value) > 254 || !strings.Contains(value, "@") {
		return "", fmt.Errorf("%w: provide a valid email address", ErrInput)
	}
	return value, nil
}
func HashPassword(ctx context.Context, password string) ([]byte, error) {
	if len(password) < 12 || len(password) > 72 {
		return nil, fmt.Errorf("%w: password must contain 12–72 UTF-8 bytes", ErrInput)
	}
	select {
	case passwordSlots <- struct{}{}:
		defer func() { <-passwordSlots }()
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
		return nil, ErrBusy
	}
	return bcrypt.GenerateFromPassword([]byte(password), 12)
}
func checkPassword(ctx context.Context, hash []byte, password string) error {
	select {
	case passwordSlots <- struct{}{}:
		defer func() { <-passwordSlots }()
	case <-ctx.Done():
		return ctx.Err()
	default:
		return ErrBusy
	}
	if len(hash) == 0 {
		hash = dummyPasswordHash
	}
	if len(password) > 72 {
		password = "invalid-password"
	}
	if bcrypt.CompareHashAndPassword(hash, []byte(password)) != nil {
		return ErrUnauthorized
	}
	return nil
}
func (p Principal) IsHuman() bool {
	return p.Email != "" && (p.CredentialType == "browser" || p.CredentialType == "cli")
}

type ProjectRole struct {
	Permissions []string `json:"permissions,omitempty"`
	Name        string   `json:"name,omitempty"`
	Project     string   `json:"project"`
	Role        string   `json:"role"`
}

func rolePermissions(role string) []string {
	switch role {
	case "admin", "developer":
		return []string{"deployments:read", "deployments:write", "logs:read"}
	case "viewer":
		return []string{"deployments:read", "logs:read"}
	}
	return nil
}
func (p Principal) CanManageProject(project string) bool {
	if p.MFARequired {
		return false
	}
	if p.IsAdmin() {
		return true
	}
	if p.CredentialType != "browser" {
		return false
	}
	for _, r := range p.ProjectRoles {
		if r.Project == project && r.Role == "admin" {
			return true
		}
	}
	return false
}
func (s *Store) projectRoles(ctx context.Context, id string) ([]ProjectRole, error) {
	status, err := s.LicenseStatus(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := s.Pool.Query(ctx, `SELECT project,'admin' AS role FROM personal_workspaces WHERE identity_id=$1 UNION SELECT project,role FROM project_members WHERE identity_id=$1 AND $3 UNION SELECT pt.project,pt.role FROM project_teams pt JOIN team_members tm ON tm.team_id=pt.team_id WHERE tm.identity_id=$1 AND $2 AND $3 ORDER BY project,role LIMIT 400`, id, licenseAllows(status, "teams"), licenseAllows(status, "project_rbac"))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ProjectRole{}
	for rows.Next() {
		var r ProjectRole
		if err = rows.Scan(&r.Project, &r.Role); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()
	if licenseAllows(status, "custom_roles") {
		roles, err := s.Pool.Query(ctx, "SELECT id,name,permissions FROM custom_roles LIMIT 100")
		if err != nil {
			return nil, err
		}
		defer roles.Close()
		for roles.Next() {
			var id, name string
			var permissions []string
			if err = roles.Scan(&id, &name, &permissions); err != nil {
				return nil, err
			}
			for i := range out {
				if out[i].Role == id {
					out[i].Name = name
					out[i].Permissions = permissions
				}
			}
		}
		if err = roles.Err(); err != nil {
			return nil, err
		}
	}
	return out, nil
}

type User struct {
	ID            string    `json:"id"`
	Name          string    `json:"name"`
	Email         string    `json:"email"`
	Admin         bool      `json:"admin"`
	Owner         bool      `json:"owner"`
	Disabled      bool      `json:"disabled"`
	EmailVerified bool      `json:"email_verified"`
	TOTP          bool      `json:"totp_enabled"`
	CreatedAt     time.Time `json:"created_at"`
}

func (s *Store) Users(ctx context.Context) ([]User, error) {
	rows, err := s.Pool.Query(ctx, `SELECT id,name,email,admin,owner,disabled,email_verified,totp_secret IS NOT NULL,created_at FROM identities WHERE email IS NOT NULL ORDER BY created_at,id LIMIT 200`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []User{}
	for rows.Next() {
		var u User
		if err = rows.Scan(&u.ID, &u.Name, &u.Email, &u.Admin, &u.Owner, &u.Disabled, &u.EmailVerified, &u.TOTP, &u.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}
func (s *Store) SetupRequired(ctx context.Context) (bool, error) {
	var exists bool
	err := s.Pool.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM identities WHERE owner)").Scan(&exists)
	return !exists, err
}

// SetupOwner is called only after an installer secret or existing administrator
// credential has been verified. The shared bootstrap lock makes the first claim
// atomic and permanently closes setup after the chosen owner is created.
func (s *Store) SetupOwner(ctx context.Context, name, email, password, existingID string) (Principal, error) {
	email, err := NormalizeEmail(email)
	if err != nil {
		return Principal{}, err
	}
	name = strings.TrimSpace(name)
	if len(name) < 1 || len(name) > 100 {
		return Principal{}, fmt.Errorf("%w: name must contain 1–100 characters", ErrInput)
	}
	hash, err := HashPassword(ctx, password)
	if err != nil {
		return Principal{}, err
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return Principal{}, err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(793044212)"); err != nil {
		return Principal{}, err
	}
	var exists bool
	if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM identities WHERE owner)").Scan(&exists); err != nil {
		return Principal{}, err
	}
	if exists {
		return Principal{}, fmt.Errorf("%w: installer setup is already complete", ErrConflict)
	}
	id := existingID
	if id == "" {
		id = NewID()
		_, err = tx.Exec(ctx, "INSERT INTO identities(id,name,email,password_hash,admin,owner) VALUES($1,$2,$3,$4,true,true)", id, name, email, hash)
	} else {
		var valid bool
		if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM identities WHERE id=$1 AND admin AND NOT disabled AND email IS NULL)", id).Scan(&valid); err != nil {
			return Principal{}, err
		}
		if !valid {
			return Principal{}, ErrForbidden
		}
		_, err = tx.Exec(ctx, "UPDATE identities SET name=$2,email=$3,password_hash=$4,owner=true,updated_at=now() WHERE id=$1", id, name, email, hash)
	}
	if err != nil {
		return Principal{}, err
	}
	if _, err = tx.Exec(ctx, "INSERT INTO projects(name) VALUES('demo') ON CONFLICT DO NOTHING; INSERT INTO environments(project,name) VALUES('demo','development') ON CONFLICT DO NOTHING"); err != nil {
		return Principal{}, err
	}
	if existingID == "" {
		if err = s.ScheduleShowcase(ctx, tx, id); err != nil {
			return Principal{}, err
		}
	}
	if _, err = tx.Exec(ctx, "INSERT INTO audit_events(identity_id,action,resource) VALUES($1,'owner.setup',$1)", id); err != nil {
		return Principal{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return Principal{}, err
	}
	return Principal{ID: id, Name: name, Email: email, Admin: true, Owner: true}, nil
}

type PasswordIdentity struct {
	ID, Name, Email string
	TOTPSecret      []byte
	TOTPStep        int64
}

func (s *Store) CheckPassword(ctx context.Context, email, password string) (PasswordIdentity, error) {
	normalized, err := NormalizeEmail(email)
	if err != nil {
		normalized = "invalid@invalid.invalid"
	}
	var v PasswordIdentity
	var hash []byte
	var disabled bool
	var locked *time.Time
	err = s.Pool.QueryRow(ctx, "SELECT id,name,email,password_hash,disabled,locked_until,totp_secret,totp_step FROM identities WHERE lower(email)=$1", normalized).Scan(&v.ID, &v.Name, &v.Email, &hash, &disabled, &locked, &v.TOTPSecret, &v.TOTPStep)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return v, err
	}
	verify := checkPassword(ctx, hash, password)
	if errors.Is(verify, ErrBusy) {
		return PasswordIdentity{}, verify
	}
	if err != nil || disabled || (locked != nil && locked.After(time.Now())) || verify != nil || len(hash) == 0 {
		if v.ID != "" {
			_, _ = s.Pool.Exec(ctx, "UPDATE identities SET failed_logins=failed_logins+1,locked_until=CASE WHEN failed_logins>=4 THEN now()+interval '15 minutes' ELSE locked_until END WHERE id=$1", v.ID)
		}
		return PasswordIdentity{}, ErrUnauthorized
	}
	return v, nil
}
func (s *Store) SuccessfulLogin(ctx context.Context, id string) error {
	_, err := s.Pool.Exec(ctx, "UPDATE identities SET failed_logins=0,locked_until=NULL WHERE id=$1", id)
	return err
}

type Session struct {
	ScopeID            string    `json:"scope_id,omitempty"`
	Token              string    `json:"token"`
	User               Principal `json:"user"`
	ExpiresAt          time.Time `json:"expires_at"`
	OnboardingRequired bool      `json:"onboarding_required"`
}

func (s *Store) NewSession(ctx context.Context, identity, kind, project, environment string, permissions []string) (Session, error) {
	return s.NewVerifiedSession(ctx, identity, kind, project, environment, permissions, false)
}
func (s *Store) NewVerifiedSession(ctx context.Context, identity, kind, project, environment string, permissions []string, verified bool) (Session, error) {
	if kind != "browser" && kind != "cli" {
		return Session{}, ErrInput
	}
	if kind == "browser" {
		permissions = []string{"admin"}
		project = ""
		environment = ""
	}
	if kind == "cli" && (project == "" || environment == "" || len(permissions) == 0) {
		return Session{}, ErrInput
	}
	id, _, _ := makeKey()
	random := make([]byte, 32)
	if _, err := rand.Read(random); err != nil {
		return Session{}, err
	}
	raw := "hs_" + id + "_" + hex.EncodeToString(random)
	digest := sha256.Sum256([]byte(raw))
	expires := time.Now().UTC().Add(12 * time.Hour)
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return Session{}, err
	}
	defer tx.Rollback(ctx)
	var eligible, onboarding bool
	if err = tx.QueryRow(ctx, "SELECT email IS NOT NULL AND NOT disabled,onboarding_required FROM identities WHERE id=$1 FOR UPDATE", identity).Scan(&eligible, &onboarding); err != nil {
		return Session{}, err
	}
	if !eligible {
		return Session{}, ErrForbidden
	}
	// Preserve rows referenced by durable deployments, revoke older sessions.
	if _, err = tx.Exec(ctx, "UPDATE api_keys SET revoked_at=now() WHERE identity_id=$1 AND kind IN ('browser','cli') AND revoked_at IS NULL AND id IN (SELECT id FROM api_keys WHERE identity_id=$1 AND kind IN ('browser','cli') AND revoked_at IS NULL ORDER BY created_at DESC OFFSET 19)", identity); err != nil {
		return Session{}, err
	}
	if _, err = tx.Exec(ctx, "INSERT INTO api_keys(id,identity_id,name,digest,prefix,kind,permissions,project,environment,expires_at,mfa_verified) VALUES($1,$2,$3,$4,$5,$3,$6,$7,$8,$9,$10)", id, identity, kind, digest[:], "hs_"+id[:8], permissions, project, environment, expires, verified); err != nil {
		return Session{}, err
	}
	if _, err = tx.Exec(ctx, "INSERT INTO audit_events(identity_id,key_id,action,resource) VALUES($1,$2,'session.create',$3)", identity, id, kind); err != nil {
		return Session{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return Session{}, err
	}
	p, err := s.KeyPrincipal(ctx, id)
	return Session{Token: raw, User: p, ExpiresAt: expires, OnboardingRequired: onboarding}, err
}
func (s *Store) RevokeSession(ctx context.Context, p Principal, id string) error {
	if !p.IsHuman() {
		return ErrForbidden
	}
	if id == "" {
		id = p.KeyID
	}
	if p.CredentialType == "cli" && id != p.KeyID {
		return ErrForbidden
	}
	result, err := s.Pool.Exec(ctx, "UPDATE api_keys SET revoked_at=now() WHERE id=$1 AND identity_id=$2 AND kind IN ('browser','cli')", id, p.ID)
	if err == nil && result.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return err
}

type HumanSession struct {
	ID         string     `json:"id"`
	Kind       string     `json:"kind"`
	Current    bool       `json:"current"`
	CreatedAt  time.Time  `json:"created_at"`
	ExpiresAt  time.Time  `json:"expires_at"`
	LastUsedAt *time.Time `json:"last_used_at"`
}

func (s *Store) Sessions(ctx context.Context, p Principal) ([]HumanSession, error) {
	if !p.IsHuman() {
		return nil, ErrForbidden
	}
	rows, err := s.Pool.Query(ctx, "SELECT id,kind,created_at,expires_at,last_used_at FROM api_keys WHERE identity_id=$1 AND kind IN ('browser','cli') AND revoked_at IS NULL AND expires_at>now() ORDER BY created_at DESC LIMIT 20", p.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []HumanSession{}
	for rows.Next() {
		var v HumanSession
		if err = rows.Scan(&v.ID, &v.Kind, &v.CreatedAt, &v.ExpiresAt, &v.LastUsedAt); err != nil {
			return nil, err
		}
		v.Current = v.ID == p.KeyID
		out = append(out, v)
	}
	return out, rows.Err()
}
func (s *Store) UpdateUser(ctx context.Context, p Principal, id string, disabled, admin bool) error {
	if p.CredentialType != "browser" || !p.IsAdmin() {
		return ErrForbidden
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var owner bool
	if err = tx.QueryRow(ctx, "SELECT owner FROM identities WHERE id=$1 AND email IS NOT NULL FOR UPDATE", id).Scan(&owner); err != nil {
		return err
	}
	if owner && (disabled || !admin) {
		return fmt.Errorf("%w: installer owner cannot be disabled or demoted", ErrInput)
	}
	if _, err = tx.Exec(ctx, "UPDATE identities SET disabled=$2,admin=$3,updated_at=now() WHERE id=$1", id, disabled, admin); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, "INSERT INTO audit_events(identity_id,key_id,action,resource,metadata) VALUES($1,$2,'user.update',$3,$4)", p.ID, p.KeyID, id, JSON(map[string]bool{"disabled": disabled, "admin": admin})); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
