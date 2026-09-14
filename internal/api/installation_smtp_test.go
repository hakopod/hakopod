package api_test

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/api"
	"github.com/hakopod/hakopod/internal/cluster"
	"github.com/hakopod/hakopod/internal/store"
)

type smtpMessage struct {
	recipient, username, password, body string
	secure                              bool
}
type settingsSMTPFixture struct {
	address     string
	roots       *x509.CertPool
	messages    chan smtpMessage
	connections atomic.Int32
}

// All traffic remains on loopback. Trust is supplied only to the in-process
// client; the production settings API offers no TLS-verification bypass.
func settingsSMTP(t *testing.T, security string) *settingsSMTPFixture {
	t.Helper()
	certificateSource := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	certificate := certificateSource.TLS.Certificates[0]
	roots := x509.NewCertPool()
	roots.AddCert(certificateSource.Certificate())
	certificateSource.Close()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	fixture := &settingsSMTPFixture{address: listener.Addr().String(), roots: roots, messages: make(chan smtpMessage, 16)}
	var wg sync.WaitGroup
	acceptDone := make(chan struct{})
	t.Cleanup(func() { listener.Close(); <-acceptDone; wg.Wait() })
	go func() {
		defer close(acceptDone)
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			fixture.connections.Add(1)
			wg.Add(1)
			go func(conn net.Conn) {
				defer wg.Done()
				defer conn.Close()
				_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
				secure := security == "tls"
				if secure {
					conn = tls.Server(conn, &tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS12})
				}
				reader := bufio.NewReader(conn)
				writer := bufio.NewWriter(conn)
				reply := func(line string) { _, _ = writer.WriteString(line + "\r\n"); _ = writer.Flush() }
				reply("220 localhost isolated SMTP settings fixture")
				message := smtpMessage{secure: secure}
				for {
					line, err := reader.ReadString('\n')
					if err != nil {
						return
					}
					if len(line) > 16384 {
						return
					}
					command := strings.TrimSpace(line)
					upper := strings.ToUpper(command)
					switch {
					case strings.HasPrefix(upper, "EHLO"), strings.HasPrefix(upper, "HELO"):
						reply("250-localhost")
						if !secure && security == "starttls" {
							reply("250-STARTTLS")
						}
						reply("250 AUTH PLAIN")
					case upper == "STARTTLS" && security == "starttls" && !secure:
						reply("220 upgrade to TLS")
						secured := tls.Server(conn, &tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS12})
						if secured.Handshake() != nil {
							return
						}
						conn = secured
						reader = bufio.NewReader(conn)
						writer = bufio.NewWriter(conn)
						secure = true
						message.secure = true
					case strings.HasPrefix(upper, "AUTH PLAIN "):
						raw, err := base64.StdEncoding.DecodeString(command[len("AUTH PLAIN "):])
						if err != nil {
							return
						}
						parts := strings.Split(string(raw), "\x00")
						if len(parts) != 3 {
							return
						}
						message.username, message.password = parts[1], parts[2]
						reply("235 authenticated")
					case strings.HasPrefix(upper, "MAIL FROM:"):
						reply("250 accepted")
					case strings.HasPrefix(upper, "RCPT TO:"):
						message.recipient = strings.Trim(strings.TrimSpace(command[len("RCPT TO:"):]), "<>")
						reply("250 accepted")
					case upper == "DATA":
						reply("354 end with dot")
						var body strings.Builder
						for {
							line, err = reader.ReadString('\n')
							if err != nil {
								return
							}
							if line == ".\r\n" {
								break
							}
							if body.Len()+len(line) > 128<<10 {
								return
							}
							body.WriteString(line)
						}
						message.body = body.String()
						fixture.messages <- message
						reply("250 accepted")
					case upper == "QUIT":
						reply("221 goodbye")
						return
					default:
						reply("500 unsupported")
					}
				}
			}(conn)
		}
	}()
	return fixture
}
func smtpInput(address, security string, revision int64) map[string]any {
	host, port, _ := net.SplitHostPort(address)
	n, _ := strconv.Atoi(port)
	return map[string]any{"expected_revision": revision, "enabled": true, "host": host, "port": n, "security": security, "username": "fixture-user", "from_email": "settings@example.test"}
}
func nextSMTP(t *testing.T, f *settingsSMTPFixture) smtpMessage {
	t.Helper()
	select {
	case m := <-f.messages:
		return m
	case <-time.After(5 * time.Second):
		t.Fatal("isolated SMTP fixture received no message")
		return smtpMessage{}
	}
}
func assertSMTPRedacted(t *testing.T, response map[string]any, secrets ...string) {
	t.Helper()
	raw := string(store.JSON(response))
	for _, secret := range secrets {
		if secret != "" && strings.Contains(raw, secret) {
			t.Fatal("SMTP response exposed a credential")
		}
	}
	for _, key := range []string{"password", "encrypted_password", "EncryptedPassword"} {
		if _, ok := response[key]; ok {
			t.Fatal("SMTP response contains a secret field")
		}
	}
}

func TestInstallationSMTPSettingsPersistenceRetentionRevisionAndFreeAccess(t *testing.T) {
	h := newAuthHarness(t, func(c *api.AuthConfig) {
		c.SMTPAllowDelivery = true
		c.SMTPAddress = "operator.example.test:587"
		c.SMTPUsername = "operator-user"
		c.SMTPPassword = "operator-private-password"
		c.SMTPFrom = "operator@example.test"
	})
	ctx := context.Background()
	token := h.owner()
	if _, err := h.db.Pool.Exec(ctx, "UPDATE installation_license SET token='',token_digest=NULL"); err != nil {
		t.Fatal(err)
	}
	initial := h.call("GET", "/installation/smtp", token, nil, 200)
	if initial["revision"] != float64(0) || initial["source"] != "operator" || initial["password_set"] != true || initial["encryption_ready"] != true {
		t.Fatalf("operator fallback missing: %v", initial)
	}
	assertSMTPRedacted(t, initial, "operator-private-password")
	input := smtpInput("smtp.example.test:587", "starttls", 0)
	input["enabled"] = false
	saved := h.call("PUT", "/installation/smtp", token, input, 200)
	if saved["revision"] != float64(1) || saved["source"] != "settings" || saved["password_set"] != true {
		t.Fatalf("first save failed: %v", saved)
	}
	record, err := h.db.InstallationSMTP(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(record.EncryptedPassword) < 28 || bytes.Contains(record.EncryptedPassword, []byte("operator-private-password")) {
		t.Fatal("password not encrypted at rest")
	}
	if bytes.Contains(store.JSON(record), record.EncryptedPassword) {
		t.Fatal("encrypted password serialized")
	}
	input["expected_revision"] = 1
	input["password"] = "saved-private-password"
	h.call("PUT", "/installation/smtp", token, input, 200)
	updated, _ := h.db.InstallationSMTP(ctx)
	input["expected_revision"] = 2
	input["password"] = ""
	h.call("PUT", "/installation/smtp", token, input, 200)
	retained, _ := h.db.InstallationSMTP(ctx)
	if !bytes.Equal(updated.EncryptedPassword, retained.EncryptedPassword) {
		t.Fatal("blank password did not retain encrypted credential")
	}
	// Recreate the API server against the same database: persistence is not a cache.
	restarted := httptest.NewServer((&api.Server{Store: h.db, Auth: h.config}).Handler())
	defer restarted.Close()
	reopened := &authHarness{t: t, server: restarted, db: h.db, config: h.config}
	again := reopened.call("GET", "/installation/smtp", token, nil, 200)
	assertSMTPRedacted(t, again, "saved-private-password", "operator-private-password")
	if again["revision"] != float64(3) || again["password_set"] != true {
		t.Fatal("saved SMTP settings did not survive API restart")
	}
	input["expected_revision"] = 1
	h.call("PUT", "/installation/smtp", token, input, 409)
	input["expected_revision"] = 3
	input["clear_password"] = true
	input["password"] = "must-not-echo"
	bad := h.call("PUT", "/installation/smtp", token, input, 400)
	assertSMTPRedacted(t, bad, "must-not-echo")
	input["password"] = ""
	cleared := h.call("PUT", "/installation/smtp", token, input, 200)
	if cleared["password_set"] != false {
		t.Fatal("explicit clear retained password")
	}
	current, _ := h.db.InstallationSMTP(ctx)
	if len(current.EncryptedPassword) != 0 {
		t.Fatal("password ciphertext remains after clear")
	}
	p, err := h.db.Authenticate(ctx, token)
	if err != nil {
		t.Fatal(err)
	}
	outcomes := make(chan error, 2)
	var wg sync.WaitGroup
	for _, host := range []string{"one.example.test", "two.example.test"} {
		wg.Add(1)
		go func(host string) {
			defer wg.Done()
			c := current
			c.Host = host
			_, e := h.db.PutInstallationSMTP(ctx, p, c, 4)
			outcomes <- e
		}(host)
	}
	wg.Wait()
	close(outcomes)
	success, conflict := 0, 0
	for err := range outcomes {
		if err == nil {
			success++
		} else if errors.Is(err, store.ErrConflict) {
			conflict++
		} else {
			t.Fatal(err)
		}
	}
	if success != 1 || conflict != 1 {
		t.Fatal("concurrent stale writer overwrote SMTP settings")
	}
	var audit string
	if err = h.db.Pool.QueryRow(ctx, "SELECT coalesce(string_agg(metadata::text,''),'') FROM audit_events WHERE action LIKE 'installation.smtp.%'").Scan(&audit); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(audit, "private-password") {
		t.Fatal("SMTP audit leaked password")
	}
}

func TestInstallationSMTPRequiresSelfHostedBrowserAdministrator(t *testing.T) {
	h := newAuthHarness(t, nil)
	ctx := context.Background()
	token := h.owner()
	p, err := h.db.Authenticate(ctx, token)
	if err != nil {
		t.Fatal(err)
	}
	cli, err := h.db.NewSession(ctx, p.ID, "cli", "demo", "development", []string{"admin"})
	if err != nil {
		t.Fatal(err)
	}
	_, machine, err := h.db.CreateKey(ctx, p, store.KeyInput{Name: "smtp-test-machine", Permissions: []string{"admin"}, ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	viewerID := store.NewID()
	if _, err = h.db.Pool.Exec(ctx, "INSERT INTO identities(id,name,email,email_verified) VALUES($1,'Viewer','viewer@example.test',true)", viewerID); err != nil {
		t.Fatal(err)
	}
	viewer, err := h.db.NewSession(ctx, viewerID, "browser", "", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, credential := range []string{cli.Token, machine, viewer.Token} {
		for _, route := range []struct{ method, path string }{{"GET", "/installation/smtp"}, {"PUT", "/installation/smtp"}, {"POST", "/installation/smtp/test"}} {
			h.call(route.method, route.path, credential, map[string]any{}, 403)
		}
	}
	h.call("GET", "/installation/smtp", "", nil, 401)
	cloud := h.config
	cloud.DeploymentMode = cluster.DeploymentManagedCloud
	server := httptest.NewServer((&api.Server{Store: h.db, Auth: cloud}).Handler())
	defer server.Close()
	ch := &authHarness{t: t, server: server, db: h.db, config: cloud}
	for _, route := range []struct{ method, path string }{{"GET", "/installation/smtp"}, {"PUT", "/installation/smtp"}, {"POST", "/installation/smtp/test"}} {
		ch.call(route.method, route.path, token, map[string]any{}, 403)
	}
	// Browser cookies retain the existing Origin/CSRF check.
	req, _ := http.NewRequest("PUT", h.server.URL+"/api/v1/installation/smtp", bytes.NewReader(store.JSON(smtpInput("smtp.example.test:587", "starttls", 0))))
	req.AddCookie(&http.Cookie{Name: "hakopod_session", Value: token})
	req.Header.Set("Content-Type", "application/json")
	response, err := h.server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 403 {
		t.Fatal("cookie update bypassed Origin check")
	}
}

func TestInstallationSMTPSTARTTLSAndImplicitTLSUseSavedSettings(t *testing.T) {
	for _, security := range []string{"starttls", "tls"} {
		t.Run(security, func(t *testing.T) {
			fixture := settingsSMTP(t, security)
			h := newAuthHarness(t, func(c *api.AuthConfig) { c.SMTPRootCAs = fixture.roots })
			token := h.owner()
			input := smtpInput(fixture.address, security, 0)
			input["password"] = "local-private-password"
			h.call("PUT", "/installation/smtp", token, input, 200)
			status := h.call("GET", "/auth/status", "", nil, 200)
			if status["email_delivery"] != true || status["password_recovery"] != true || status["signup_enabled"] != false {
				t.Fatal("saved SMTP not reflected without opening signup")
			}
			h.call("POST", "/installation/smtp/test", token, map[string]any{"expected_revision": 0}, 409)
			h.call("POST", "/installation/smtp/test", token, map[string]any{"expected_revision": 1, "recipient": "other@example.test"}, 400)
			result := h.call("POST", "/installation/smtp/test", token, map[string]any{"expected_revision": 1}, 200)
			if result["recipient"] != "owner@example.test" || result["sent"] != true {
				t.Fatal("SMTP test target was not current administrator")
			}
			message := nextSMTP(t, fixture)
			if !message.secure || message.recipient != "owner@example.test" || message.username != "fixture-user" || message.password != "local-private-password" || !strings.Contains(message.body, "Hakopod SMTP test") {
				t.Fatal("saved TLS/authentication or fixed test recipient incorrect")
			}
			team := h.call("POST", "/teams", token, map[string]string{"name": "SMTP settings fixture"}, 201)
			h.call("POST", "/teams/"+team["id"].(string)+"/invites", token, map[string]any{"email": "invited@example.test", "role": "member", "deliver": true}, 201)
			if m := nextSMTP(t, fixture); m.recipient != "invited@example.test" || !m.secure {
				t.Fatal("invitation did not use saved SMTP transport")
			}
			h.call("POST", "/auth/password/forgot", "", map[string]string{"email": "owner@example.test"}, 202)
			if m := nextSMTP(t, fixture); m.recipient != "owner@example.test" || !strings.Contains(m.body, "Reset your Hakopod password") {
				t.Fatal("recovery did not use saved SMTP transport")
			}
			// Disabling the persistent override must gate every mail path immediately.
			input["expected_revision"] = 1
			input["enabled"] = false
			delete(input, "password")
			h.call("PUT", "/installation/smtp", token, input, 200)
			status = h.call("GET", "/auth/status", "", nil, 200)
			if status["email_delivery"] != false || status["password_recovery"] != false {
				t.Fatal("disabled settings remain available")
			}
			h.call("POST", "/installation/smtp/test", token, map[string]any{"expected_revision": 2}, 409)
			h.call("POST", "/auth/password/forgot", "", map[string]string{"email": "owner@example.test"}, 409)
		})
	}
}

func TestInstallationSMTPStrictTLSAndRedactedErrors(t *testing.T) {
	fixture := settingsSMTP(t, "tls")
	h := newAuthHarness(t, nil)
	token := h.owner()
	input := smtpInput(fixture.address, "tls", 0)
	input["password"] = "never-return-this-password"
	h.call("PUT", "/installation/smtp", token, input, 200)
	result := h.call("POST", "/installation/smtp/test", token, map[string]any{"expected_revision": 1}, 502)
	assertSMTPRedacted(t, result, "never-return-this-password")
	if len(fixture.messages) != 0 {
		t.Fatal("untrusted TLS certificate accepted")
	}
	plain, _ := registrationSMTP(t)
	host, port, _ := net.SplitHostPort(plain)
	n, _ := strconv.Atoi(port)
	input["expected_revision"] = 1
	input["host"] = host
	input["port"] = n
	input["security"] = "starttls"
	h.call("PUT", "/installation/smtp", token, input, 200)
	h.call("POST", "/installation/smtp/test", token, map[string]any{"expected_revision": 2}, 502)
	for _, mutate := range []func(map[string]any){func(v map[string]any) { v["host"] = "https://user:never-return-this-password@example.test" }, func(v map[string]any) { v["security"] = "none" }, func(v map[string]any) { v["port"] = 0 }, func(v map[string]any) { v["from_email"] = "a@example.test\r\nBcc: leaked@example.test" }} {
		v := smtpInput("smtp.example.test:587", "starttls", 2)
		mutate(v)
		assertSMTPRedacted(t, h.call("PUT", "/installation/smtp", token, v, 400), "never-return-this-password")
	}
	request, _ := http.NewRequest("PUT", h.server.URL+"/api/v1/installation/smtp", strings.NewReader(`{"password":"never-return-this-password","host":12}`))
	request.Header.Set("Authorization", "Bearer "+token)
	response, err := h.server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	raw, _ := io.ReadAll(response.Body)
	if response.StatusCode != 400 || bytes.Contains(raw, []byte("never-return-this-password")) {
		t.Fatal("malformed input leaked password")
	}
}

func TestInstallationSMTPDisabledOverrideAndCloudFallback(t *testing.T) {
	fixture := settingsSMTP(t, "starttls")
	h := newAuthHarness(t, func(c *api.AuthConfig) {
		c.SMTPAddress = fixture.address
		c.SMTPFrom = "operator@example.test"
		c.SMTPAllowDelivery = true
		c.SMTPRootCAs = fixture.roots
	})
	token := h.owner()
	input := smtpInput(fixture.address, "starttls", 0)
	input["enabled"] = false
	h.call("PUT", "/installation/smtp", token, input, 200)
	if h.call("GET", "/auth/status", "", nil, 200)["email_delivery"] != false {
		t.Fatal("disabled override fell back to enabled operator mail")
	}
	cloud := h.config
	cloud.DeploymentMode = cluster.DeploymentManagedCloud
	server := httptest.NewServer((&api.Server{Store: h.db, Auth: cloud}).Handler())
	defer server.Close()
	ch := &authHarness{t: t, server: server, db: h.db, config: cloud}
	if ch.call("GET", "/auth/status", "", nil, 200)["email_delivery"] != true {
		t.Fatal("cloud transport consumed customer SMTP override")
	}
	if fixture.connections.Load() != 0 {
		t.Fatal("availability checks contacted SMTP")
	}
}

func TestInstallationSMTPEncryptionKeyRequiredAndCiphertextFailureClosed(t *testing.T) {
	h := newAuthHarness(t, func(c *api.AuthConfig) { c.EncryptionKey = "" })
	token := h.owner()
	if h.call("GET", "/installation/smtp", token, nil, 200)["encryption_ready"] != false {
		t.Fatal("missing encryption key not reported")
	}
	h.call("PUT", "/installation/smtp", token, smtpInput("smtp.example.test:587", "starttls", 0), 400)
	valid := h.config
	valid.EncryptionKey = base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{3}, 32))
	server := httptest.NewServer((&api.Server{Store: h.db, Auth: valid}).Handler())
	defer server.Close()
	vh := &authHarness{t: t, server: server, db: h.db, config: valid}
	input := smtpInput("smtp.example.test:587", "starttls", 0)
	input["password"] = "private-ciphertext-test"
	vh.call("PUT", "/installation/smtp", token, input, 200)
	if h.call("GET", "/auth/status", "", nil, 200)["email_delivery"] != false {
		t.Fatal("lost encryption key kept mail available")
	}
	if _, err := h.db.Pool.Exec(context.Background(), "UPDATE installation_smtp SET password=$1", bytes.Repeat([]byte{4}, 32)); err != nil {
		t.Fatal(err)
	}
	if vh.call("GET", "/auth/status", "", nil, 200)["email_delivery"] != false {
		t.Fatal("corrupt password fell back to operator configuration")
	}
}

func TestInstallationSMTPFeedsDurableAlarmDelivery(t *testing.T) {
	fixture := settingsSMTP(t, "starttls")
	db, p, _, app := seedAPIAlarm(t, 0, true)
	ctx := context.Background()
	if _, err := db.Pool.Exec(ctx, "UPDATE identities SET email='alarm-admin@example.test',email_verified=true WHERE id=$1", p.ID); err != nil {
		t.Fatal(err)
	}
	session, err := db.NewSession(ctx, p.ID, "browser", "", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	config := api.AuthConfig{PublicURL: "http://localhost:4173", EncryptionKey: strings.Repeat("ab", 32), SMTPRootCAs: fixture.roots}
	management := &api.Server{Store: db, Auth: config}
	server := httptest.NewServer(management.Handler())
	defer server.Close()
	h := &authHarness{t: t, server: server, db: db, config: config}
	input := smtpInput(fixture.address, "starttls", 0)
	input["password"] = "alarm-private-password"
	h.call("PUT", "/installation/smtp", session.Token, input, 200)
	if h.call("GET", "/alarm-settings?project=demo", session.Token, nil, 200)["email_available"] != true {
		t.Fatal("alarm settings did not see saved transport")
	}
	if err := db.ApplyAlarmObservations(ctx, []store.AlarmObservation{apiAlarmObservation(app, "unhealthy", time.Now().UTC())}, true); err != nil {
		t.Fatal(err)
	}
	running, cancel := context.WithCancel(ctx)
	stopped := make(chan struct{})
	go func() { defer close(stopped); management.RunAlarms(running) }()
	defer func() { cancel(); <-stopped }()
	message := nextSMTP(t, fixture)
	if !message.secure || message.recipient != "alarm-admin@example.test" || message.password != "alarm-private-password" || !strings.Contains(message.body, "Recorded transition: active") {
		t.Fatal("alarm worker did not use the saved SMTP configuration")
	}
	input["expected_revision"] = 1
	input["enabled"] = false
	delete(input, "password")
	h.call("PUT", "/installation/smtp", session.Token, input, 200)
	if h.call("GET", "/alarm-settings?project=demo", session.Token, nil, 200)["email_available"] != false {
		t.Fatal("alarm settings ignored disabled transport")
	}
}

func TestInstallationSMTPPasswordBounds(t *testing.T) {
	h := newAuthHarness(t, nil)
	token := h.owner()
	input := smtpInput("smtp.example.test:587", "starttls", 0)
	input["enabled"] = false
	input["password"] = strings.Repeat("\\", 4096)
	h.call("PUT", "/installation/smtp", token, input, 200)
	saved, err := h.db.InstallationSMTP(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(saved.EncryptedPassword) <= 8192 || len(saved.EncryptedPassword) > 32<<10 {
		t.Fatal("encrypted envelope does not fit the documented password limit")
	}
	input["expected_revision"] = 1
	input["password"] = strings.Repeat("x", 4097)
	h.call("PUT", "/installation/smtp", token, input, 400)
	input["password"] = strings.Repeat("x", 20<<10)
	h.call("PUT", "/installation/smtp", token, input, 400)
}
