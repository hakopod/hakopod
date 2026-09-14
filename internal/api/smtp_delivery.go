package api

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"mime"
	"net"
	"net/smtp"
	"strconv"
	"strings"
	"time"

	"github.com/hakopod/hakopod/internal/store"
)

var authMailSlots = make(chan struct{}, 2)

func (s *Server) sendAuthMail(ctx context.Context, recipient, subject, body string) error {
	config, err := s.effectiveSMTP(ctx)
	if err != nil {
		return err
	}
	return s.sendSMTPConfiguration(ctx, config, recipient, subject, body)
}

func (s *Server) sendSMTPConfiguration(ctx context.Context, config smtpConfiguration, recipient, subject, body string) error {
	select {
	case authMailSlots <- struct{}{}:
		defer func() { <-authMailSlots }()
	case <-ctx.Done():
		return ctx.Err()
	default:
		return store.ErrBusy
	}
	return s.deliverSMTP(ctx, config, recipient, subject, body)
}

// Public registration and recovery must not expose account existence through
// SMTP latency. Reserve a slot before starting work; there is no waiting queue.
func (s *Server) startAuthMail(ctx context.Context, recipient, subject, body, challenge, kind string) {
	select {
	case authMailSlots <- struct{}{}:
	default:
		_, _ = s.Store.ConsumeChallenge(ctx, challenge, kind)
		return
	}
	go func() {
		defer func() { <-authMailSlots }()
		mailContext, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if s.deliverAuthMail(mailContext, recipient, subject, body) != nil {
			_, _ = s.Store.ConsumeChallenge(mailContext, challenge, kind)
			_, _ = s.Store.Pool.Exec(mailContext, "INSERT INTO audit_events(identity_id,action,resource) VALUES('','auth.email.failed',$1)", kind)
		}
	}()
}

func (s *Server) deliverAuthMail(ctx context.Context, recipient, subject, body string) error {
	config, err := s.effectiveSMTP(ctx)
	if err != nil {
		return err
	}
	return s.deliverSMTP(ctx, config, recipient, subject, body)
}

// Bound all server replies, including a hostile SMTP banner or TLS handshake.
type smtpReplyConn struct {
	net.Conn
	remaining int
}

func (c *smtpReplyConn) Read(p []byte) (int, error) {
	if c.remaining <= 0 {
		return 0, errors.New("SMTP response exceeds limit")
	}
	if len(p) > c.remaining {
		p = p[:c.remaining]
	}
	n, err := c.Conn.Read(p)
	c.remaining -= n
	return n, err
}

func (s *Server) deliverSMTP(ctx context.Context, config smtpConfiguration, recipient, subject, body string) error {
	if !config.Enabled {
		return store.ErrForbidden
	}
	if err := config.Validate(); err != nil {
		return err
	}
	if len(subject) > 200 || strings.ContainsAny(subject, "\r\n") || len(body) > 64<<10 || len(config.Password) > 4096 {
		return errors.New("SMTP message exceeds limits")
	}
	recipient, err := store.NormalizeEmail(recipient)
	if err != nil {
		return err
	}
	from, err := store.NormalizeEmail(config.FromEmail)
	if err != nil {
		return err
	}
	address := net.JoinHostPort(config.Host, strconv.Itoa(config.Port))
	conn, err := (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "tcp", address)
	if err != nil {
		return err
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	deadline := time.Now().Add(10 * time.Second)
	if end, ok := ctx.Deadline(); ok && end.Before(deadline) {
		deadline = end
	}
	_ = conn.SetDeadline(deadline)
	var transport net.Conn = &smtpReplyConn{Conn: conn, remaining: 256 << 10}
	tlsConfig := &tls.Config{ServerName: config.Host, MinVersion: tls.VersionTLS12, RootCAs: s.Auth.SMTPRootCAs}
	if config.Security == "tls" {
		secured := tls.Client(transport, tlsConfig)
		if err := secured.HandshakeContext(ctx); err != nil {
			return err
		}
		transport = secured
	}
	client, err := smtp.NewClient(transport, config.Host)
	if err != nil {
		return err
	}
	defer client.Close()
	if config.Security == "starttls" {
		if ok, _ := client.Extension("STARTTLS"); ok {
			if err = client.StartTLS(tlsConfig); err != nil {
				return err
			}
		} else {
			ip := net.ParseIP(config.Host)
			if !config.AllowInsecure || (config.Host != "localhost" && (ip == nil || !ip.IsLoopback())) {
				return errors.New("SMTP requires STARTTLS")
			}
		}
	}
	if config.Username != "" {
		if err = client.Auth(smtp.PlainAuth("", config.Username, config.Password, config.Host)); err != nil {
			return err
		}
	}
	if err = client.Mail(from); err != nil {
		return err
	}
	if err = client.Rcpt(recipient); err != nil {
		return err
	}
	writer, err := client.Data()
	if err != nil {
		return err
	}
	body = strings.NewReplacer("\r\n", "\n", "\r", "\n").Replace(body)
	_, err = fmt.Fprintf(writer, "From: %s\r\nTo: %s\r\nSubject: %s\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=UTF-8\r\n\r\n%s\r\n", from, recipient, mime.QEncoding.Encode("utf-8", subject), strings.ReplaceAll(body, "\n", "\r\n"))
	if err != nil {
		return err
	}
	if err = writer.Close(); err != nil {
		return err
	}
	return client.Quit()
}
