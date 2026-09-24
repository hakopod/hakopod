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
type DeviceDetails struct {
	UserCode    string    `json:"user_code"`
	Project     string    `json:"project"`
	Environment string    `json:"environment"`
	Permissions []string  `json:"permissions"`
	ExpiresAt   time.Time `json:"expires_at"`
}

func (s *Store) StartDevice(ctx context.Context, project, environment string, permissions []string) (DeviceAuthorization, error) {
	if project == "" || environment == "" {
		return DeviceAuthorization{}, ErrInput
	}
	if len(permissions) == 0 {
		permissions = []string{"deployments:read", "deployments:write", "logs:read"}
	}
	if len(permissions) > 3 {
		return DeviceAuthorization{}, ErrInput
	}
	for _, p := range permissions {
		if !contains([]string{"deployments:read", "deployments:write", "logs:read"}, p) {
			return DeviceAuthorization{}, ErrForbidden
		}
	}
	var exists bool
	if err := s.Pool.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM environments WHERE project=$1 AND name=$2)", project, environment).Scan(&exists); err != nil {
		return DeviceAuthorization{}, err
	}
	if !exists {
		return DeviceAuthorization{}, ErrInput
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
	err := s.Pool.QueryRow(ctx, "SELECT user_code,project,environment,permissions,expires_at FROM device_codes WHERE user_code=$1 AND consumed_at IS NULL AND expires_at>now()", normalizeUserCode(code)).Scan(&v.UserCode, &v.Project, &v.Environment, &v.Permissions, &v.ExpiresAt)
	return v, err
}
func (s *Store) ApproveDevice(ctx context.Context, p Principal, code string, approve bool) error {
	v, err := s.DeviceDetails(ctx, p, code)
	if err != nil {
		return err
	}
	if approve {
		for _, permission := range v.Permissions {
			if !p.Allows(permission, v.Project, v.Environment, "") {
				return ErrForbidden
			}
		}
	}
	result, err := s.Pool.Exec(ctx, "UPDATE device_codes SET identity_id=$2,approved=$3,mfa_verified=$4 WHERE user_code=$1 AND approved IS NULL AND consumed_at IS NULL AND expires_at>now()", v.UserCode, p.ID, approve, p.MFAVerified)
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
	var id, identity, project, environment string
	var approved *bool
	var verified bool
	var permissions []string
	var polled *time.Time
	err = tx.QueryRow(ctx, "SELECT id,COALESCE(identity_id,''),project,environment,permissions,approved,last_polled_at,mfa_verified FROM device_codes WHERE digest=$1 AND consumed_at IS NULL AND expires_at>now() FOR UPDATE", sum[:]).Scan(&id, &identity, &project, &environment, &permissions, &approved, &polled, &verified)
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
	for _, permission := range permissions {
		if !session.User.Allows(permission, project, environment, "") {
			_ = s.RevokeSession(ctx, session.User, session.User.KeyID)
			return Session{}, ErrForbidden
		}
	}
	return session, nil
}
