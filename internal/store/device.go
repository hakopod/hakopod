package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"errors"
	"github.com/jackc/pgx/v5"
	"strings"
	"time"
)

var ErrPending = errors.New("authorization_pending")
var ErrSlowDown = errors.New("slow_down")
var ErrDenied = errors.New("access_denied")

type DeviceAuthorization struct {
	DeviceCode string `json:"device_code"`
	UserCode   string `json:"user_code"`
	ExpiresIn  int    `json:"expires_in"`
	Interval   int    `json:"interval"`
}
type DeviceScope struct {
	ID          string   `json:"id"`
	Label       string   `json:"label"`
	Project     string   `json:"project"`
	Environment string   `json:"environment"`
	Permissions []string `json:"permissions,omitempty"`
}

// External scopes without a permission list retain the legacy deployment grants.
func deviceScopeAllows(scope DeviceScope, requested []string) bool {
	allowed := scope.Permissions
	if allowed == nil {
		allowed = []string{"deployments:read", "deployments:write", "logs:read"}
	}
	for _, permission := range requested {
		if !contains(allowed, permission) {
			return false
		}
	}
	return true
}

type DeviceDetails struct {
	Scopes      []DeviceScope `json:"scopes"`
	ScopeID     string        `json:"scope_id"`
	UserCode    string        `json:"user_code"`
	Project     string        `json:"project"`
	Environment string        `json:"environment"`
	Permissions []string      `json:"permissions"`
	ExpiresAt   time.Time     `json:"expires_at"`
}

func (s *Store) StartDevice(ctx context.Context, project, environment string, permissions []string) (DeviceAuthorization, error) {
	if (project == "") != (environment == "") {
		return DeviceAuthorization{}, ErrInput
	}
	var err error
	permissions, err = devicePermissions(permissions)
	if err != nil {
		return DeviceAuthorization{}, err
	}
	if contains(permissions, "nodes:terminal") && (project != "" || environment != "" || s.DeviceScopes != nil) {
		return DeviceAuthorization{}, ErrForbidden
	}
	if contains(permissions, "agent:admin") && (project != "" || environment != "" || s.DeviceScopes != nil) {
		return DeviceAuthorization{}, ErrForbidden
	}
	if project != "" && s.DeviceScopes == nil {
		var exists bool
		if err := s.Pool.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM environments WHERE project=$1 AND name=$2)", project, environment).Scan(&exists); err != nil {
			return DeviceAuthorization{}, err
		}
		if !exists {
			return DeviceAuthorization{}, ErrInput
		}
	}
	token := NewID() + NewID()
	sum := sha256.Sum256([]byte(token))
	random := make([]byte, 5)
	if _, err := rand.Read(random); err != nil {
		return DeviceAuthorization{}, err
	}
	userCode := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(random)
	userCode = userCode[:4] + "-" + userCode[4:]
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return DeviceAuthorization{}, err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(793044218)"); err != nil {
		return DeviceAuthorization{}, err
	}
	if _, err = tx.Exec(ctx, "DELETE FROM device_codes WHERE expires_at<now()"); err != nil {
		return DeviceAuthorization{}, err
	}
	var count int
	if err = tx.QueryRow(ctx, "SELECT count(*) FROM device_codes").Scan(&count); err != nil {
		return DeviceAuthorization{}, err
	}
	if count >= 1000 {
		return DeviceAuthorization{}, ErrBusy
	}
	if _, err = tx.Exec(ctx, "INSERT INTO device_codes(id,digest,user_code,project,environment,permissions,expires_at) VALUES($1,$2,$3,$4,$5,$6,now()+interval '10 minutes')", NewID(), sum[:], userCode, project, environment, permissions); err != nil {
		return DeviceAuthorization{}, err
	}
	return DeviceAuthorization{token, userCode, 600, 5}, tx.Commit(ctx)
}
func normalizeUserCode(code string) string {
	code = strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(code), "-", ""))
	if len(code) != 8 {
		return ""
	}
	return code[:4] + "-" + code[4:]
}
func (s *Store) DeviceDetails(ctx context.Context, p Principal, code string) (DeviceDetails, error) {
	if p.CredentialType != "browser" {
		return DeviceDetails{}, ErrForbidden
	}
	var v DeviceDetails
	err := s.Pool.QueryRow(ctx, "SELECT user_code,project,environment,permissions,expires_at,scope_id FROM device_codes WHERE user_code=$1 AND consumed_at IS NULL AND expires_at>now()", normalizeUserCode(code)).Scan(&v.UserCode, &v.Project, &v.Environment, &v.Permissions, &v.ExpiresAt, &v.ScopeID)
	if err == nil {
		if contains(v.Permissions, "agent:admin") {
			if !p.IsAdmin() || s.DeviceScopes != nil || v.Project != "" || v.Environment != "" {
				return DeviceDetails{}, ErrForbidden
			}
			v.Scopes = []DeviceScope{{ID: "installation", Label: "This installation"}}
		} else if contains(v.Permissions, "nodes:terminal") {
			if p.MFARequired || !p.IsHuman() || s.DeviceScopes != nil || len(p.HostPermissions) == 0 {
				return DeviceDetails{}, ErrForbidden
			}
			v.Scopes = []DeviceScope{{ID: "host", Label: "Your granted host access"}}
		} else {
			v.Scopes, err = s.deviceScopes(ctx, p)
		}
	}
	return v, err
}
func (s *Store) ApproveDevice(ctx context.Context, p Principal, code string, approve bool, selected ...DeviceScope) error {
	v, err := s.DeviceDetails(ctx, p, code)
	if err != nil {
		return err
	}
	if approve {
		if contains(v.Permissions, "agent:admin") && (len(selected) != 1 || selected[0].ID != "installation" || selected[0].Project != "" || selected[0].Environment != "" || !p.IsAdmin()) {
			return ErrForbidden
		}
		if contains(v.Permissions, "nodes:terminal") && !contains(v.Permissions, "agent:admin") && (len(selected) != 1 || selected[0].ID != "host" || selected[0].Project != "" || selected[0].Environment != "") {
			return ErrForbidden
		}
		choice := DeviceScope{Project: v.Project, Environment: v.Environment}
		if len(selected) > 0 {
			choice = selected[0]
		}
		if v.Project != "" && (choice.Project != v.Project || choice.Environment != v.Environment) {
			return ErrForbidden
		}
		found := false
		for _, scope := range v.Scopes {
			if scope.ID == choice.ID && scope.Project == choice.Project && scope.Environment == choice.Environment {
				if s.DeviceScopes != nil && !deviceScopeAllows(scope, v.Permissions) {
					return ErrForbidden
				}
				found = true
				v.ScopeID, v.Project, v.Environment = scope.ID, scope.Project, scope.Environment
				break
			}
		}
		if !found {
			return ErrForbidden
		}
		if s.DeviceScopes == nil && v.ScopeID != "host" {
			for _, permission := range v.Permissions {
				if !p.Allows(permission, v.Project, v.Environment, "") {
					return ErrForbidden
				}
			}
		}
	}
	result, err := s.Pool.Exec(ctx, "UPDATE device_codes SET identity_id=$2,approved=$3,mfa_verified=$4,project=$5,environment=$6,scope_id=$7 WHERE user_code=$1 AND approved IS NULL AND consumed_at IS NULL AND expires_at>now()", v.UserCode, p.ID, approve, p.MFAVerified, v.Project, v.Environment, v.ScopeID)
	if err == nil && result.RowsAffected() != 1 {
		return ErrConflict
	}
	return err
}
func (s *Store) PollDevice(ctx context.Context, token string) (Session, error) {
	if len(token) != 64 {
		return Session{}, ErrUnauthorized
	}
	sum := sha256.Sum256([]byte(token))
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return Session{}, err
	}
	defer tx.Rollback(ctx)
	var id, identity, project, environment, scopeID string
	var approved *bool
	var verified bool
	var permissions []string
	var polled *time.Time
	err = tx.QueryRow(ctx, "SELECT id,COALESCE(identity_id,''),project,environment,permissions,approved,last_polled_at,mfa_verified,scope_id FROM device_codes WHERE digest=$1 AND consumed_at IS NULL AND expires_at>now() FOR UPDATE", sum[:]).Scan(&id, &identity, &project, &environment, &permissions, &approved, &polled, &verified, &scopeID)
	if errors.Is(err, pgx.ErrNoRows) {
		return Session{}, ErrUnauthorized
	}
	if err != nil {
		return Session{}, err
	}
	if polled != nil && time.Since(*polled) < 5*time.Second {
		return Session{}, ErrSlowDown
	}
	if _, err = tx.Exec(ctx, "UPDATE device_codes SET last_polled_at=now() WHERE id=$1", id); err != nil {
		return Session{}, err
	}
	if approved == nil {
		if err = tx.Commit(ctx); err != nil {
			return Session{}, err
		}
		return Session{}, ErrPending
	}
	if _, err = tx.Exec(ctx, "UPDATE device_codes SET consumed_at=now() WHERE id=$1", id); err != nil {
		return Session{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return Session{}, err
	}
	if !*approved {
		return Session{}, ErrDenied
	}
	session, err := s.NewVerifiedSession(ctx, identity, "cli", project, environment, permissions, verified)
	if err != nil {
		return Session{}, err
	}
	if contains(permissions, "agent:admin") {
		if scopeID != "installation" || s.DeviceScopes != nil || !session.User.CanUseInstallationAgentAdministration() {
			_ = s.RevokeSession(ctx, session.User, session.User.KeyID)
			return Session{}, ErrForbidden
		}
		session.ScopeID = "installation"
		return session, nil
	}
	if contains(permissions, "nodes:terminal") {
		if scopeID != "host" || !session.User.CanUseHostCredential() || len(session.User.HostPermissions) == 0 {
			_ = s.RevokeSession(ctx, session.User, session.User.KeyID)
			return Session{}, ErrForbidden
		}
		session.ScopeID = "host"
		return session, nil
	}
	if s.DeviceScopes != nil {
		scopes, scopeErr := s.deviceScopes(ctx, session.User)
		found := false
		for _, scope := range scopes {
			if scope.ID == scopeID && scope.Project == project && scope.Environment == environment && deviceScopeAllows(scope, permissions) {
				found = true
			}
		}
		if scopeErr != nil || !found || scopeID == "" {
			_ = s.RevokeSession(ctx, session.User, session.User.KeyID)
			return Session{}, ErrForbidden
		}
		_, err = s.Pool.Exec(ctx, "INSERT INTO device_session_scopes(key_id,scope_id) VALUES($1,$2)", session.User.KeyID, scopeID)
		if err != nil {
			_ = s.RevokeSession(ctx, session.User, session.User.KeyID)
			return Session{}, err
		}
		session.ScopeID = scopeID
		return session, nil
	}
	for _, permission := range permissions {
		if !session.User.Allows(permission, project, environment, "") {
			_ = s.RevokeSession(ctx, session.User, session.User.KeyID)
			return Session{}, ErrForbidden
		}
	}
	return session, nil
}

// Scope discovery is authenticated, bounded, and rechecked at consent and issuance.
func (s *Store) deviceScopes(ctx context.Context, p Principal) ([]DeviceScope, error) {
	if !p.IsHuman() || p.MFARequired {
		return nil, ErrForbidden
	}
	if s.DeviceScopes != nil {
		return s.DeviceScopes(ctx, p)
	}
	rows, err := s.Pool.Query(ctx, "SELECT project,name FROM environments ORDER BY project,name LIMIT 1000")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []DeviceScope{}
	for rows.Next() {
		var scope DeviceScope
		if err := rows.Scan(&scope.Project, &scope.Environment); err != nil {
			return nil, err
		}
		if p.Allows("deployments:write", scope.Project, scope.Environment, "") {
			scope.Label = scope.Project + " / " + scope.Environment
			result = append(result, scope)
		}
	}
	return result, rows.Err()
}

// Device permissions require explicit consent. Defaults retain the existing deployment and log access.
func devicePermissions(in []string) ([]string, error) {
	if len(in) == 0 {
		return []string{"deployments:read", "deployments:write", "logs:read"}, nil
	}
	if len(in) > 12 {
		return nil, ErrInput
	}
	seen := map[string]bool{}
	for _, p := range in {
		if !contains([]string{"admin", "agent:admin", "agent:credentials", "deployments:read", "deployments:write", "logs:read", "pods:exec", "databases:query", "databases:write-query", "networks:write", "git:manage", "applications:manage", "nodes:terminal"}, p) {
			return nil, ErrForbidden
		}
		if seen[p] {
			return nil, ErrInput
		}
		seen[p] = true
	}
	if seen["nodes:terminal"] && !seen["agent:admin"] && len(in) != 1 {
		return nil, ErrInput
	}
	if seen["databases:write-query"] && !seen["databases:query"] {
		return nil, ErrInput
	}
	if seen["admin"] != seen["agent:admin"] {
		return nil, ErrInput
	}
	if seen["agent:admin"] {
		for _, permission := range in {
			if permission != "admin" && permission != "agent:admin" && permission != "agent:credentials" && permission != "nodes:terminal" {
				return nil, ErrInput
			}
		}
	}
	return append([]string(nil), in...), nil
}
