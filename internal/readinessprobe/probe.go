// Package readinessprobe implements short-lived, localhost-only health checks.
package readinessprobe

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	Protocol, ServerName, CAFile string
	Port, HTTPPort               int
	HTTPPath                     string
	Timeout                      time.Duration
}

// Check never authenticates or sends mail. SMTP checks require a greeting,
// EHLO and NOOP; STARTTLS also verifies the server certificate and repeats EHLO
// after negotiating TLS. Each invocation shares one total deadline.
func Check(ctx context.Context, cfg Config) error {
	if cfg.Port < 1 || cfg.Port > 65535 || cfg.Timeout < time.Second || cfg.Timeout > 10*time.Second || (cfg.Protocol != "tcp" && cfg.Protocol != "smtp" && cfg.Protocol != "smtp_starttls") {
		return errors.New("invalid readiness configuration")
	}
	ctx, cancel := context.WithTimeout(ctx, cfg.Timeout)
	defer cancel()
	if cfg.HTTPPath != "" {
		if cfg.HTTPPort < 1 || cfg.HTTPPort > 65535 || !strings.HasPrefix(cfg.HTTPPath, "/") || strings.HasPrefix(cfg.HTTPPath, "//") || len(cfg.HTTPPath) > 2048 {
			return errors.New("invalid HTTP readiness configuration")
		}
		transport := &http.Transport{Proxy: nil, DialContext: (&net.Dialer{}).DialContext, DisableKeepAlives: true, MaxResponseHeaderBytes: 8192}
		defer transport.CloseIdleConnections()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://127.0.0.1:"+strconv.Itoa(cfg.HTTPPort)+cfg.HTTPPath, nil)
		if err != nil {
			return errors.New("invalid HTTP readiness path")
		}
		response, err := transport.RoundTrip(req)
		if err != nil {
			return errors.New("HTTP readiness request failed")
		}
		response.Body.Close()
		if response.StatusCode < 200 || response.StatusCode >= 400 {
			return fmt.Errorf("HTTP readiness returned status %d", response.StatusCode)
		}
	}
	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp4", "127.0.0.1:"+strconv.Itoa(cfg.Port))
	if err != nil {
		return errors.New("listener connection failed")
	}
	defer conn.Close()
	deadline, _ := ctx.Deadline()
	if err = conn.SetDeadline(deadline); err != nil {
		return errors.New("cannot set listener deadline")
	}
	if cfg.Protocol == "tcp" {
		return nil
	}
	reader := bufio.NewReaderSize(conn, 4096)
	if _, err = readReply(reader, 220); err != nil {
		return errors.New("SMTP greeting check failed")
	}
	capabilities, err := command(conn, reader, "EHLO hakopod-probe.invalid\r\n", 250)
	if err != nil {
		return errors.New("SMTP EHLO check failed")
	}
	if cfg.Protocol == "smtp_starttls" {
		if cfg.ServerName == "" || !hasSTARTTLS(capabilities) {
			return errors.New("SMTP STARTTLS is unavailable")
		}
		roots, err := loadRoots(cfg.CAFile)
		if err != nil {
			return err
		}
		if _, err = command(conn, reader, "STARTTLS\r\n", 220); err != nil {
			return errors.New("SMTP STARTTLS request failed")
		}
		secure := tls.Client(conn, &tls.Config{ServerName: cfg.ServerName, RootCAs: roots, MinVersion: tls.VersionTLS12})
		if err = secure.HandshakeContext(ctx); err != nil {
			return errors.New("SMTP STARTTLS certificate or handshake check failed")
		}
		conn = secure
		reader = bufio.NewReaderSize(conn, 4096)
		if _, err = command(conn, reader, "EHLO hakopod-probe.invalid\r\n", 250); err != nil {
			return errors.New("SMTP EHLO after STARTTLS failed")
		}
	}
	if _, err = command(conn, reader, "NOOP\r\n", 250); err != nil {
		return errors.New("SMTP NOOP check failed")
	}
	return nil
}

func loadRoots(path string) (*x509.CertPool, error) {
	if path == "" {
		return nil, nil
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, errors.New("SMTP trust bundle cannot be read")
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, (1<<20)+1))
	if err != nil || len(data) > 1<<20 {
		return nil, errors.New("SMTP trust bundle exceeds 1 MiB or cannot be read")
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(data) {
		return nil, errors.New("SMTP trust bundle contains no certificates")
	}
	return roots, nil
}

func command(conn net.Conn, reader *bufio.Reader, value string, code int) ([]string, error) {
	if _, err := io.WriteString(conn, value); err != nil {
		return nil, err
	}
	return readReply(reader, code)
}

func readReply(reader *bufio.Reader, code int) ([]string, error) {
	var lines []string
	for count, size := 0, 0; count < 32; count++ {
		raw, err := reader.ReadSlice('\n')
		line := string(raw)
		if err != nil || len(line) < 5 || len(line) > 4096 {
			return nil, errors.New("invalid SMTP reply")
		}
		size += len(line)
		if size > 16384 {
			return nil, errors.New("SMTP reply exceeds limit")
		}
		got, err := strconv.Atoi(line[:3])
		if err != nil || got != code || (line[3] != '-' && line[3] != ' ') || !strings.HasSuffix(line, "\r\n") {
			return nil, errors.New("unexpected SMTP reply")
		}
		lines = append(lines, strings.TrimSpace(line[4:]))
		if line[3] == ' ' {
			return lines, nil
		}
	}
	return nil, errors.New("SMTP reply exceeds line limit")
}

func hasSTARTTLS(lines []string) bool {
	for _, line := range lines {
		fields := strings.Fields(line)
		if len(fields) > 0 && strings.EqualFold(fields[0], "STARTTLS") {
			return true
		}
	}
	return false
}
