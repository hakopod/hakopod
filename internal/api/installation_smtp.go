package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/hakopod/hakopod/internal/cluster"
	"github.com/hakopod/hakopod/internal/store"
	"github.com/jackc/pgx/v5"
)

type smtpSettingsView struct {
	store.SMTPSettings
	Source          string `json:"source"`
	PasswordSet     bool   `json:"password_set"`
	EncryptionReady bool   `json:"encryption_ready"`
}

type smtpConfiguration struct {
	store.SMTPSettings
	Password      string `json:"-"`
	AllowInsecure bool   `json:"-"`
}

func (s *Server) registerSMTPRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/installation/smtp", s.installationSMTP)
	mux.HandleFunc("PUT /api/v1/installation/smtp", s.putInstallationSMTP)
	mux.HandleFunc("POST /api/v1/installation/smtp/test", s.testInstallationSMTP)
}

func (s *Server) smtpAdministrator(w http.ResponseWriter, r *http.Request) bool {
	p := who(r)
	if !p.IsAdmin() || !p.IsHuman() || p.CredentialType != "browser" {
		failure(w, store.ErrForbidden)
		return false
	}
	mode, err := cluster.ParseDeploymentMode(s.Auth.DeploymentMode)
	if err != nil || mode != cluster.DeploymentSelfHosted {
		problem(w, 403, "forbidden", "SMTP settings are managed by the operator in Hakopod Cloud")
		return false
	}
	return true
}

func (s *Server) operatorSMTP() smtpConfiguration {
	host, rawPort, _ := net.SplitHostPort(s.Auth.SMTPAddress)
	port, _ := strconv.Atoi(rawPort)
	if s.Auth.SMTPAddress == "" {
		port = 587
	}
	security := s.Auth.SMTPSecurity
	if security == "" {
		security = "starttls"
	}
	return smtpConfiguration{SMTPSettings: store.SMTPSettings{Enabled: s.Auth.SMTPAllowDelivery, Host: host, Port: port,
		Security: security, Username: s.Auth.SMTPUsername, FromEmail: s.Auth.SMTPFrom},
		Password: s.Auth.SMTPPassword, AllowInsecure: s.Auth.SMTPAllowInsecure}
}

// A database failure never silently re-enables operator fallback. Cloud mail
// always uses operator configuration, even after a change of deployment mode.
func (s *Server) smtpSettings(ctx context.Context) (smtpSettingsView, error) {
	mode, err := cluster.ParseDeploymentMode(s.Auth.DeploymentMode)
	if err != nil {
		return smtpSettingsView{}, err
	}
	op := s.operatorSMTP()
	view := smtpSettingsView{SMTPSettings: op.SMTPSettings, Source: "operator", PasswordSet: op.Password != "", EncryptionReady: len(s.authEncryptionKey()) == 32}
	if mode == cluster.DeploymentManagedCloud || s.Store == nil || s.Store.Pool == nil {
		return view, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	config, err := s.Store.InstallationSMTP(ctx)
	if errors.Is(err, pgx.ErrNoRows) {
		return view, nil
	}
	if err != nil {
		return smtpSettingsView{}, err
	}
	view.SMTPSettings, view.Source, view.PasswordSet = config, "settings", len(config.EncryptedPassword) != 0
	return view, nil
}

func (s *Server) effectiveSMTP(ctx context.Context) (smtpConfiguration, error) {
	view, err := s.smtpSettings(ctx)
	if err != nil {
		return smtpConfiguration{}, err
	}
	config := smtpConfiguration{SMTPSettings: view.SMTPSettings}
	if view.Source == "operator" {
		return s.operatorSMTP(), nil
	}
	if len(config.EncryptedPassword) != 0 {
		plain, err := s.decryptAuth(config.EncryptedPassword)
		var envelope struct {
			Purpose  string `json:"purpose"`
			Password string `json:"password"`
		}
		if err != nil || json.Unmarshal(plain, &envelope) != nil || envelope.Purpose != "installation-smtp-password" {
			return smtpConfiguration{}, errors.New("SMTP credentials are unavailable")
		}
		config.Password = envelope.Password
	}
	return config, nil
}

func (s *Server) smtpMailAvailable(ctx context.Context) bool {
	config, err := s.effectiveSMTP(ctx)
	return err == nil && config.Enabled && config.Validate() == nil
}

func (s *Server) installationSMTP(w http.ResponseWriter, r *http.Request) {
	if !s.smtpAdministrator(w, r) {
		return
	}
	view, err := s.smtpSettings(r.Context())
	if err != nil {
		failure(w, err)
		return
	}
	write(w, 200, view)
}

// SMTP errors must never echo malformed input or credentials into responses.
func decodeSMTP(w http.ResponseWriter, r *http.Request, value any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(value); err != nil {
		problem(w, 400, "invalid_request", "SMTP settings require a valid JSON object with known fields")
		return false
	}
	var extra any
	if err := d.Decode(&extra); !errors.Is(err, io.EOF) {
		problem(w, 400, "invalid_request", "SMTP settings require one JSON object")
		return false
	}
	return true
}

func (s *Server) putInstallationSMTP(w http.ResponseWriter, r *http.Request) {
	if !s.smtpAdministrator(w, r) {
		return
	}
	var in struct {
		ExpectedRevision *int64  `json:"expected_revision"`
		Enabled          *bool   `json:"enabled"`
		Host             *string `json:"host"`
		Port             *int    `json:"port"`
		Security         *string `json:"security"`
		Username         *string `json:"username"`
		FromEmail        *string `json:"from_email"`
		Password         string  `json:"password"`
		ClearPassword    bool    `json:"clear_password"`
	}
	if !decodeSMTP(w, r, &in) {
		return
	}
	if in.ExpectedRevision == nil || in.Enabled == nil || in.Host == nil || in.Port == nil || in.Security == nil || in.Username == nil || in.FromEmail == nil {
		failure(w, fmt.Errorf("%w: expected_revision, enabled, host, port, security, username and from_email are required", store.ErrInput))
		return
	}
	if len(in.Password) > 4096 || strings.ContainsAny(in.Password, "\x00\r\n") || (in.Password != "" && in.ClearPassword) {
		failure(w, fmt.Errorf("%w: supply a password of at most 4096 bytes without control delimiters, or clear it", store.ErrInput))
		return
	}
	if len(s.authEncryptionKey()) != 32 {
		failure(w, fmt.Errorf("%w: configure the installation authentication encryption key before saving SMTP settings", store.ErrInput))
		return
	}
	old, err := s.smtpSettings(r.Context())
	if err != nil {
		failure(w, err)
		return
	}
	if old.Revision != *in.ExpectedRevision || *in.ExpectedRevision < 0 {
		failure(w, store.ErrConflict)
		return
	}
	config := store.SMTPSettings{Enabled: *in.Enabled, Host: *in.Host, Port: *in.Port, Security: *in.Security, Username: *in.Username, FromEmail: *in.FromEmail}
	if err := config.Validate(); err != nil {
		failure(w, err)
		return
	}
	if config.FromEmail != "" {
		config.FromEmail, _ = store.NormalizeEmail(config.FromEmail)
	}
	password := in.Password
	if !in.ClearPassword && password == "" {
		if old.Source == "operator" {
			password = s.Auth.SMTPPassword
		} else {
			config.EncryptedPassword = old.EncryptedPassword
		}
	}
	if password != "" {
		config.EncryptedPassword, err = s.encryptAuth(store.JSON(map[string]string{"purpose": "installation-smtp-password", "password": password}))
		if err != nil {
			failure(w, err)
			return
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	saved, err := s.Store.PutInstallationSMTP(ctx, who(r), config, *in.ExpectedRevision)
	if err != nil {
		failure(w, err)
		return
	}
	write(w, 200, smtpSettingsView{SMTPSettings: saved, Source: "settings", PasswordSet: len(saved.EncryptedPassword) != 0, EncryptionReady: true})
}

func (s *Server) testInstallationSMTP(w http.ResponseWriter, r *http.Request) {
	if !s.smtpAdministrator(w, r) {
		return
	}
	var in struct {
		ExpectedRevision *int64 `json:"expected_revision"`
	}
	if !decodeSMTP(w, r, &in) {
		return
	}
	if in.ExpectedRevision == nil || *in.ExpectedRevision < 0 {
		failure(w, fmt.Errorf("%w: expected_revision is required", store.ErrInput))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	config, err := s.effectiveSMTP(ctx)
	if err != nil {
		failure(w, err)
		return
	}
	if config.Revision != *in.ExpectedRevision {
		failure(w, store.ErrConflict)
		return
	}
	if !config.Enabled || config.Validate() != nil {
		problem(w, 409, "email_not_configured", "save and enable valid SMTP settings before sending a test")
		return
	}
	err = s.sendSMTPConfiguration(ctx, config, who(r).Email, "Hakopod SMTP test", "Your saved Hakopod SMTP settings delivered this test message.\n\nThis test was requested by your signed-in administrator account.")
	status := "sent"
	if err != nil {
		status = "failed"
	}
	audit, stop := context.WithTimeout(r.Context(), 3*time.Second)
	_, _ = s.Store.Pool.Exec(audit, "INSERT INTO audit_events(identity_id,key_id,action,resource,metadata) VALUES($1,$2,'installation.smtp.test','smtp',$3)", who(r).ID, who(r).KeyID, store.JSON(map[string]any{"revision": config.Revision, "status": status}))
	stop()
	if errors.Is(err, store.ErrBusy) {
		authFailure(w, err)
		return
	}
	if err != nil {
		problem(w, 502, "smtp_delivery_failed", "SMTP test delivery failed; check the saved connection, TLS and authentication settings")
		return
	}
	write(w, 200, map[string]any{"sent": true, "recipient": who(r).Email})
}
