package api

import (
	"bufio"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/spec"
	"github.com/hakopod/hakopod/internal/store"
	"github.com/jackc/pgx/v5"
)

type notificationRoundTrip func(*http.Request) (*http.Response, error)

func (f notificationRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func notificationTestDB(t *testing.T) *store.Store {
	t.Helper()
	dsn := os.Getenv("HAKOPOD_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("requires disposable PostgreSQL")
	}
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	name := "hakopod_test_" + store.NewID()
	if _, err = conn.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(dsn)
	u.Path = "/" + name
	db, err := store.Open(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	if err = db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close(); conn.Exec(ctx, "DROP DATABASE "+name+" WITH (FORCE)"); conn.Close(ctx) })
	return db
}
func TestNotificationDestinationValidationAndPrivateAddresses(t *testing.T) {
	for _, address := range []string{"127.0.0.1", "10.0.0.1", "169.254.169.254", "100.64.0.1", "::1", "::ffff:127.0.0.1", "fc00::1", "2001:db8::1", "198.18.1.1", "64:ff9b::a9fe:a9fe", "2002:7f00:1::"} {
		if notificationPublicIP(netip.MustParseAddr(address)) {
			t.Fatal("private/reserved address allowed", address)
		}
	}
	for _, address := range []string{"1.1.1.1", "2606:4700:4700::1111"} {
		if !notificationPublicIP(netip.MustParseAddr(address)) {
			t.Fatal("public address rejected")
		}
	}
	for _, destination := range []string{"http://example.com/hook", "https://user:secret@example.com/hook", "https://127.0.0.1/hook", "https://example.com:8443/hook", "https://localhost/hook", "https://example.com/hook#secret"} {
		if validateNotificationDestination("webhook", destination, strings.Repeat("s", 32)) == nil {
			t.Fatal("unsafe destination accepted", destination)
		}
	}
	if validateNotificationDestination("slack", "https://example.com/services/token", "") == nil {
		t.Fatal("non-Slack host accepted")
	}
	client := notificationClient()
	defer client.CloseIdleConnections()
	r, err := http.NewRequest("POST", "https://127.0.0.1/webhook", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = client.Do(r); err == nil {
		t.Fatal("private address dialed")
	}
	if client.CheckRedirect(nil, nil) != http.ErrUseLastResponse {
		t.Fatal("redirects allowed")
	}
}
func TestDeploymentNotificationsQueueDeliveryAndAuthorization(t *testing.T) {
	db := notificationTestDB(t)
	ctx := context.Background()
	token, err := db.Bootstrap(ctx, "notification-fixture")
	if err != nil {
		t.Fatal(err)
	}
	p, err := db.Authenticate(ctx, token)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := spec.Parse([]byte("name='notification-app'\n[services.api]\nimage='nginx:alpine'"))
	if err != nil {
		t.Fatal(err)
	}
	initial, err := db.Accept(ctx, p, "demo", "development", parsed, 0, "notification-initial")
	if err != nil {
		t.Fatal(err)
	}
	app, err := db.Application(ctx, initial.ApplicationID)
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{Store: db, Auth: AuthConfig{EncryptionKey: base64.StdEncoding.EncodeToString([]byte(strings.Repeat("k", 32))), PublicURL: "https://dashboard.example"}}
	handler := s.Handler()
	call := func(method, path, key string, body any, status int) []byte {
		t.Helper()
		r := httptest.NewRequest(method, "/api/v1/applications/"+app.ID+"/notifications"+path, strings.NewReader(string(store.JSON(body))))
		r.Header.Set("Authorization", "Bearer "+key)
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != status {
			t.Fatalf("%s %s: %d want %d: %s", method, path, w.Code, status, w.Body.String())
		}
		return w.Body.Bytes()
	}
	_, reader, err := db.CreateKey(ctx, p, store.KeyInput{Name: "notification-reader", Project: "demo", Environment: "development", Permissions: []string{"deployments:read"}, ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	_, outsider, err := db.CreateKey(ctx, p, store.KeyInput{Name: "other-app", Project: "demo", Environment: "development", Application: "other-app", Permissions: []string{"deployments:read", "deployments:write"}, ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	call("GET", "", outsider, nil, 403)
	targets := map[string]store.NotificationTarget{}
	destinations := map[string]string{"slack": "https://hooks.slack.com/services/FIXTURE/ONLY/TOKEN", "discord": "https://discord.com/api/webhooks/FIXTURE/ONLY", "webhook": "https://example.com/fixture"}
	for kind, destination := range destinations {
		body := map[string]any{"name": kind + " fixture", "kind": kind, "events": []string{"succeeded", "failed", "cancelled"}, "enabled": true, "destination": destination, "signing_secret": strings.Repeat("s", 32), "expected_revision": 0}
		call("POST", "", reader, body, 403)
		raw := call("POST", "", token, body, 201)
		if strings.Contains(string(raw), destination) || strings.Contains(string(raw), strings.Repeat("s", 32)) {
			t.Fatal("secret returned")
		}
		var target store.NotificationTarget
		json.Unmarshal(raw, &target)
		targets[kind] = target
		saved, err := db.NotificationTarget(ctx, app.ID, target.ID)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(saved.Destination), destination) {
			t.Fatal("destination not encrypted")
		}
	}
	view := string(call("GET", "", reader, nil, 200))
	if strings.Contains(view, "TOKEN") || strings.Contains(view, "signing_secret") {
		t.Fatal("read response exposed credentials")
	}
	// No events are sent before a deployment reaches a terminal state.
	var count int
	db.Pool.QueryRow(ctx, "SELECT count(*) FROM deployment_notification_deliveries").Scan(&count)
	if count != 0 {
		t.Fatal(count)
	}
	claim, err := db.Claim(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = claim.Finish(ctx, "succeeded", "", nil); err != nil {
		t.Fatal(err)
	}
	claim.Release()
	db.Pool.QueryRow(ctx, "SELECT count(*) FROM deployment_notification_deliveries").Scan(&count)
	if count != 3 {
		t.Fatal("missing terminal event notifications", count)
	}
	// Rewriting the same terminal state must not enqueue duplicates.
	db.Pool.Exec(ctx, "UPDATE deployments SET status='succeeded' WHERE id=$1", initial.ID)
	db.Pool.QueryRow(ctx, "SELECT count(*) FROM deployment_notification_deliveries").Scan(&count)
	if count != 3 {
		t.Fatal("duplicate deliveries", count)
	}
	seen := map[string]int{}
	fake := &http.Client{Transport: notificationRoundTrip(func(r *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(r.Body)
		var data map[string]any
		if json.Unmarshal(body, &data) != nil {
			t.Fatal("not JSON")
		}
		if strings.Contains(string(body), "nginx") || strings.Contains(string(body), "spec") {
			t.Fatal("spec data included")
		}
		switch r.URL.Host {
		case "hooks.slack.com":
			seen["slack"]++
			if data["mrkdwn"] != false || data["unfurl_links"] != false {
				t.Fatal(data)
			}
		case "discord.com":
			seen["discord"]++
			if len(data["allowed_mentions"].(map[string]any)["parse"].([]any)) != 0 {
				t.Fatal(data)
			}
		case "example.com":
			seen["webhook"]++
			mac := hmac.New(sha256.New, []byte(strings.Repeat("s", 32)))
			mac.Write([]byte(r.Header.Get("X-Hakopod-Timestamp") + "."))
			mac.Write(body)
			if r.Header.Get("X-Hakopod-Signature") != "sha256="+hex.EncodeToString(mac.Sum(nil)) {
				t.Fatal("bad signature")
			}
			if data["status"] != "succeeded" || data["event_id"] != r.Header.Get("X-Hakopod-Event-ID") {
				t.Fatal(data)
			}
		default:
			t.Fatal("unexpected destination")
		}
		return &http.Response{StatusCode: 204, Body: io.NopCloser(strings.NewReader("")), Header: make(http.Header)}, nil
	})}
	s.deliverNotifications(ctx, fake)
	s.deliverNotifications(ctx, fake)
	for _, kind := range []string{"slack", "discord", "webhook"} {
		if seen[kind] != 1 {
			t.Fatal(seen)
		}
	}
	history, err := db.NotificationHistory(ctx, p, app)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range history {
		if d.Status != "sent" || d.Attempts != 1 {
			t.Fatal(d)
		}
	}
	target := targets["webhook"]
	// Failure and queued cancellation also create terminal notifications.
	for i, status := range []string{"failed", "cancelled"} {
		next, err := db.Accept(ctx, p, "demo", "development", parsed, int64(i+1), "notify-"+status+"-fixture")
		if err != nil {
			t.Fatal(err)
		}
		if _, err = db.Pool.Exec(ctx, "UPDATE deployments SET status=$2 WHERE id=$1", next.ID, status); err != nil {
			t.Fatal(err)
		}
	}
	var terminal int
	if err = db.Pool.QueryRow(ctx, "SELECT count(*) FROM deployment_notification_deliveries WHERE payload->>'status' IN ('failed','cancelled')").Scan(&terminal); err != nil || terminal != 6 {
		t.Fatal(terminal, err)
	}
	// Clear only test-owned pending events so the following claim targets the test send.
	if _, err = db.Pool.Exec(ctx, "DELETE FROM deployment_notification_deliveries WHERE status='pending'"); err != nil {
		t.Fatal(err)
	}
	// Tests are durable, rate limited and honour revisions.
	call("POST", "/"+target.ID+"/test", token, map[string]int64{"expected_revision": target.Revision}, 202)
	call("POST", "/"+target.ID+"/test", token, map[string]int64{"expected_revision": target.Revision}, 400)
	job, err := db.ClaimNotification(ctx)
	if err != nil || job == nil {
		t.Fatal(err)
	}
	retryClient := &http.Client{Transport: notificationRoundTrip(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 429, Body: io.NopCloser(strings.NewReader("SECRET PROVIDER BODY"))}, nil
	})}
	outcome, message := s.deliverNotification(ctx, retryClient, *job)
	if outcome != "pending" || strings.Contains(message, "SECRET") {
		t.Fatal(outcome, message)
	}
	if err = db.FinishNotification(ctx, *job, outcome, message); err != nil {
		t.Fatal(err)
	}
	// Editing skips queued delivery and does not disclose the retained secret.
	call("PUT", "/"+target.ID, token, map[string]any{"name": "paused fixture", "kind": "webhook", "enabled": false, "events": []string{"failed"}, "expected_revision": target.Revision}, 200)
	history, _ = db.NotificationHistory(ctx, p, app)
	if history[0].Status != "skipped" {
		t.Fatal(history[0])
	}
	call("PUT", "/"+target.ID, token, map[string]any{"name": "stale", "kind": "webhook", "enabled": true, "events": []string{"failed"}, "expected_revision": target.Revision}, 409)
	call("DELETE", "/"+target.ID, token, map[string]int64{"expected_revision": target.Revision + 1}, 200)
	if _, err = db.NotificationTarget(ctx, app.ID, target.ID); err == nil {
		t.Fatal("target not deleted")
	}
}
func TestNotificationEmailUsesConfiguredSMTP(t *testing.T) {
	db := notificationTestDB(t)
	ctx := context.Background()
	token, _ := db.Bootstrap(ctx, "notification-email")
	p, _ := db.Authenticate(ctx, token)
	parsed, _ := spec.Parse([]byte("name='email-app'\n[services.api]\nimage='nginx:alpine'"))
	d, err := db.Accept(ctx, p, "demo", "development", parsed, 0, "notification-email-initial")
	if err != nil {
		t.Fatal(err)
	}
	app, err := db.Application(ctx, d.ApplicationID)
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	received := make(chan string, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		conn.SetDeadline(time.Now().Add(10 * time.Second))
		reader := bufio.NewReader(conn)
		writer := bufio.NewWriter(conn)
		reply := func(line string) { writer.WriteString(line + "\r\n"); writer.Flush() }
		reply("220 localhost SMTP fixture")
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				return
			}
			command := strings.ToUpper(strings.TrimSpace(line))
			switch {
			case strings.HasPrefix(command, "EHLO"), strings.HasPrefix(command, "HELO"):
				reply("250 localhost")
			case strings.HasPrefix(command, "MAIL FROM:"), strings.HasPrefix(command, "RCPT TO:"):
				reply("250 accepted")
			case command == "DATA":
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
					body.WriteString(line)
				}
				received <- body.String()
				reply("250 received")
			case command == "QUIT":
				reply("221 goodbye")
				return
			default:
				reply("500 unsupported")
			}
		}
	}()
	s := &Server{Store: db, Auth: AuthConfig{EncryptionKey: base64.StdEncoding.EncodeToString([]byte(strings.Repeat("k", 32))), SMTPAddress: listener.Addr().String(), SMTPFrom: "hakopod@example.test", SMTPAllowDelivery: true, SMTPAllowInsecure: true}}
	id := store.NewID()
	encrypted, err := s.encryptAuth(store.JSON(notificationSecret{Purpose: "deployment-notification", ApplicationID: app.ID, TargetID: id, Destination: "recipient@example.test"}))
	if err != nil {
		t.Fatal(err)
	}
	target, err := db.PutNotificationTarget(ctx, p, app, store.NotificationTarget{ID: id, ApplicationID: app.ID, Name: "local email fixture", Kind: "email", Enabled: true, Events: []string{"failed"}, Destination: encrypted}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.TestNotification(ctx, p, app, target.ID, target.Revision); err != nil {
		t.Fatal(err)
	}
	s.deliverNotifications(ctx, nil)
	select {
	case body := <-received:
		if !strings.Contains(body, "recipient@example.test") || !strings.Contains(body, "email-app") {
			t.Fatal("missing message content")
		}
	case <-time.After(time.Second):
		t.Fatal("email not received")
	}
	history, _ := db.NotificationHistory(ctx, p, app)
	if len(history) != 1 || history[0].Status != "sent" {
		t.Fatal(history)
	}
}
