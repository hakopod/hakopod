package store

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"unicode"

	"github.com/jackc/pgx/v5"
)

// SMTPSettings contains only nonsecret configuration in its JSON representation.
// EncryptedPassword is an authenticated envelope owned by the API resolver.
type SMTPSettings struct {
	Revision          int64  `json:"revision"`
	Enabled           bool   `json:"enabled"`
	Host              string `json:"host"`
	Port              int    `json:"port"`
	Security          string `json:"security"`
	Username          string `json:"username"`
	FromEmail         string `json:"from_email"`
	EncryptedPassword []byte `json:"-"`
}

func (c SMTPSettings) Validate() error {
	if c.Port < 1 || c.Port > 65535 || (c.Security != "starttls" && c.Security != "tls") {
		return fmt.Errorf("%w: select STARTTLS or TLS and a port from 1 to 65535", ErrInput)
	}
	if len(c.Host) > 253 || len(c.Username) > 256 || len(c.FromEmail) > 254 ||
		strings.IndexFunc(c.Username, unicode.IsControl) >= 0 {
		return fmt.Errorf("%w: SMTP configuration exceeds its bounds or contains control characters", ErrInput)
	}
	if c.Host != "" && net.ParseIP(c.Host) == nil {
		for _, label := range strings.Split(c.Host, ".") {
			if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
				return fmt.Errorf("%w: SMTP host must be a hostname or IP address without a URL, port or credentials", ErrInput)
			}
			for _, ch := range label {
				if !(ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9' || ch == '-') {
					return fmt.Errorf("%w: SMTP host must be a hostname or IP address without a URL, port or credentials", ErrInput)
				}
			}
		}
	}
	if c.FromEmail != "" {
		if _, err := NormalizeEmail(c.FromEmail); err != nil {
			return fmt.Errorf("%w: SMTP sender must be an email address", ErrInput)
		}
	}
	if c.Enabled && (c.Host == "" || c.FromEmail == "") {
		return fmt.Errorf("%w: SMTP host and sender email are required to enable delivery", ErrInput)
	}
	if n := len(c.EncryptedPassword); n != 0 && (n < 28 || n > 32<<10) {
		return fmt.Errorf("%w: invalid encrypted SMTP password", ErrInput)
	}
	return nil
}

const smtpColumns = "revision,enabled,host,port,security,username,from_email,password"

func scanSMTP(row scanner) (SMTPSettings, error) {
	var c SMTPSettings
	err := row.Scan(&c.Revision, &c.Enabled, &c.Host, &c.Port, &c.Security, &c.Username, &c.FromEmail, &c.EncryptedPassword)
	return c, err
}

// InstallationSMTP is internal to mail delivery and authorized settings handlers.
func (s *Store) InstallationSMTP(ctx context.Context) (SMTPSettings, error) {
	return scanSMTP(s.Pool.QueryRow(ctx, "SELECT "+smtpColumns+" FROM installation_smtp WHERE singleton"))
}

func (s *Store) PutInstallationSMTP(ctx context.Context, p Principal, c SMTPSettings, expected int64) (SMTPSettings, error) {
	if !p.IsAdmin() || !p.IsHuman() || p.CredentialType != "browser" {
		return c, ErrForbidden
	}
	if err := c.Validate(); err != nil {
		return c, err
	}
	if expected < 0 {
		return c, ErrConflict
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return c, err
	}
	defer tx.Rollback(ctx)
	// The lock also serializes first writes, before the singleton row exists.
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(793044244)"); err != nil {
		return c, err
	}
	old, err := scanSMTP(tx.QueryRow(ctx, "SELECT "+smtpColumns+" FROM installation_smtp WHERE singleton FOR UPDATE"))
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return c, err
	}
	if old.Revision != expected {
		return c, ErrConflict
	}
	if c.EncryptedPassword == nil {
		c.EncryptedPassword = []byte{}
	}
	c.Revision = expected + 1
	result, err := scanSMTP(tx.QueryRow(ctx, `INSERT INTO installation_smtp(singleton,revision,enabled,host,port,security,username,from_email,password)
		VALUES(true,$1,$2,$3,$4,$5,$6,$7,$8) ON CONFLICT(singleton) DO UPDATE SET
		revision=excluded.revision,enabled=excluded.enabled,host=excluded.host,port=excluded.port,security=excluded.security,
		username=excluded.username,from_email=excluded.from_email,password=excluded.password,updated_at=now() RETURNING `+smtpColumns,
		c.Revision, c.Enabled, c.Host, c.Port, c.Security, c.Username, c.FromEmail, c.EncryptedPassword))
	if err != nil {
		return c, err
	}
	if _, err = tx.Exec(ctx, "INSERT INTO audit_events(identity_id,key_id,action,resource,metadata) VALUES($1,$2,'installation.smtp.updated','smtp',$3)", p.ID, p.KeyID,
		JSON(map[string]any{"revision": c.Revision, "enabled": c.Enabled, "security": c.Security})); err != nil {
		return c, err
	}
	return result, tx.Commit(ctx)
}
