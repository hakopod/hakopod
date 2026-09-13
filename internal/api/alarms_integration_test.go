package api_test

import (
	"bufio"
	"context"
	"net"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/api"
	"github.com/hakopod/hakopod/internal/cluster"
	"github.com/hakopod/hakopod/internal/spec"
	"github.com/hakopod/hakopod/internal/store"
)

func seedAPIAlarm(t *testing.T, hold int, email bool) (*store.Store, store.Principal, string, store.Application) {
	t.Helper()
	db, _ := database(t)
	ctx := context.Background()
	raw, err := db.Bootstrap(ctx, "alarm-api-test")
	if err != nil {
		t.Fatal(err)
	}
	p, err := db.Authenticate(ctx, raw)
	if err != nil {
		t.Fatal(err)
	}
	app := store.Application{ID: store.NewID(), Name: "web", Project: "demo", Environment: "development", Revision: 1, Spec: spec.Application{Services: map[string]spec.Service{"web": {Image: "nginx:alpine", Replicas: 1}}}}
	observed := cluster.Observation{Revision: 1, Status: "pending", ObservedAt: time.Now().UTC(), Services: []cluster.ServiceStatus{{Name: "web", Status: "deploying", Desired: 1}}}
	if _, err := db.Pool.Exec(ctx, "INSERT INTO applications(id,name,project,environment,revision,spec,observed) VALUES($1,$2,$3,$4,$5,$6,$7)", app.ID, app.Name, app.Project, app.Environment, app.Revision, store.JSON(app.Spec), store.JSON(observed)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.PutAlarmSettings(ctx, p, store.AlarmScope{Project: app.Project}, store.AlarmSettingsInput{Enabled: true, HoldSeconds: hold, EmailEnabled: email}); err != nil {
		t.Fatal(err)
	}
	return db, p, raw, app
}

func apiAlarmObservation(app store.Application, health string, at time.Time) store.AlarmObservation {
	return store.AlarmObservation{AlarmScope: store.AlarmScope{Project: app.Project, Environment: app.Environment, ApplicationID: app.ID}, Rule: "application-not-ready", ResourceType: "application", ResourceID: app.ID, ResourceName: app.Name, Health: health, Summary: "Fixture application " + health, At: at}
}

func TestAlarmHTTPPermissionsSettingsAndSeenEventGuard(t *testing.T) {
	db, p, raw, app := seedAPIAlarm(t, 0, false)
	management := &api.Server{Store: db}
	server := httptest.NewServer(management.Handler())
	defer server.Close()
	h := &authHarness{t: t, server: server, db: db}
	ctx := context.Background()
	base := time.Now().UTC()
	if err := db.ApplyAlarmObservations(ctx, []store.AlarmObservation{apiAlarmObservation(app, "unhealthy", base)}, false); err != nil {
		t.Fatal(err)
	}
	_, reader, err := db.CreateKey(ctx, p, store.KeyInput{Name: "scoped-reader", Project: app.Project, Environment: app.Environment, Permissions: []string{"deployments:read"}, ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	path := "/alarm-settings?project=demo&environment=development&application_id=" + app.ID
	settings := h.call("GET", path, reader, nil, 200)
	if settings["email_available"] != false || settings["can_manage"] != false || settings["inherited"] != true {
		t.Fatalf("incorrect settings capability: %+v", settings)
	}
	h.call("GET", "/alarm-settings", reader, nil, 403)
	h.call("PUT", path, reader, map[string]any{"enabled": true, "hold_seconds": 0, "email_enabled": false, "expected_revision": 0}, 403)
	h.call("PUT", path, raw, map[string]any{"enabled": true, "hold_seconds": 0, "email_enabled": true, "expected_revision": 0}, 400)
	h.call("PUT", path, raw, map[string]any{"enabled": true, "hold_seconds": 0, "email_enabled": false, "expected_revision": 0}, 200)
	h.call("PUT", path, raw, map[string]any{"enabled": true, "hold_seconds": 0, "email_enabled": false, "expected_revision": 0}, 409)
	h.call("PUT", path, raw, map[string]any{"enabled": true}, 400)
	h.call("GET", "/alarms?limit=51", raw, nil, 400)
	h.call("GET", "/alarms?cursor=bad", raw, nil, 400)
	h.call("GET", "/alarms", "", nil, 401)
	page := h.call("GET", "/alarms?limit=1", reader, nil, 200)
	item := page["items"].([]any)[0].(map[string]any)
	id := item["id"].(string)
	seen := item["last_event_id"]
	h.call("POST", "/alarms/"+id+"/acknowledge", reader, map[string]any{"expected_event_id": seen}, 200)
	if err := db.ApplyAlarmObservations(ctx, []store.AlarmObservation{apiAlarmObservation(app, "healthy", base.Add(time.Second))}, false); err != nil {
		t.Fatal(err)
	}
	h.call("POST", "/alarms/"+id+"/read", reader, map[string]any{"expected_event_id": seen}, 409)
	result := h.call("POST", "/alarms/"+id+"/read", reader, nil, 200)
	if result["read"] != true || result["acknowledged"] != false {
		t.Fatalf("read and acknowledgement conflated: %+v", result)
	}
	h.call("POST", "/alarms/"+id+"/acknowledge", reader, map[string]any{}, 200)
	active := h.call("GET", "/alarms?status=active", reader, nil, 200)
	if len(active["items"].([]any)) != 0 {
		t.Fatal("recovered incident appeared in active filter")
	}
}

// This SMTP fixture binds only to loopback and rejects its first connection.
// It cannot contact a real provider or deliver an external email.
func alarmRetrySMTP(t *testing.T) (string, <-chan string, *atomic.Int32) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	received := make(chan string, 2)
	connections := &atomic.Int32{}
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			func() {
				defer conn.Close()
				_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
				reader, writer := bufio.NewReader(conn), bufio.NewWriter(conn)
				reply := func(line string) { _, _ = writer.WriteString(line + "\r\n"); _ = writer.Flush() }
				if connections.Add(1) == 1 {
					reply("421 fixture temporary failure")
					return
				}
				reply("220 localhost isolated alarm fixture")
				for {
					line, err := reader.ReadString('\n')
					if err != nil {
						return
					}
					command := strings.ToUpper(strings.TrimSpace(line))
					switch {
					case strings.HasPrefix(command, "EHLO"), strings.HasPrefix(command, "HELO"), strings.HasPrefix(command, "MAIL FROM:"), strings.HasPrefix(command, "RCPT TO:"):
						reply("250 localhost")
					case command == "DATA":
						reply("354 end with dot")
						var body strings.Builder
						for body.Len() < 16384 {
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
						reply("250 accepted")
					case command == "QUIT":
						reply("221 goodbye")
						return
					default:
						reply("500 unsupported")
					}
				}
			}()
		}
	}()
	return listener.Addr().String(), received, connections
}

func TestAlarmSMTPDurableFailureRetryAndDeliveryGate(t *testing.T) {
	db, p, _, app := seedAPIAlarm(t, 0, true)
	ctx := context.Background()
	if _, err := db.Pool.Exec(ctx, "UPDATE identities SET email='operator@example.test',email_verified=true WHERE id=$1", p.ID); err != nil {
		t.Fatal(err)
	}
	if err := db.ApplyAlarmObservations(ctx, []store.AlarmObservation{apiAlarmObservation(app, "unhealthy", time.Now().UTC())}, true); err != nil {
		t.Fatal(err)
	}
	address, received, connections := alarmRetrySMTP(t)
	config := api.AuthConfig{SMTPAddress: address, SMTPFrom: "alarms@example.test", SMTPAllowInsecure: true, PublicURL: "http://localhost:4173"}
	blocked := &api.Server{Store: db, Auth: config}
	blockedCtx, stopBlocked := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { defer close(done); blocked.RunAlarms(blockedCtx) }()
	time.Sleep(100 * time.Millisecond)
	stopBlocked()
	<-done
	if connections.Load() != 0 {
		t.Fatal("SMTP contacted without the explicit delivery gate")
	}
	config.SMTPAllowDelivery = true
	management := &api.Server{Store: db, Auth: config}
	running, cancel := context.WithCancel(ctx)
	stopped := make(chan struct{})
	go func() { defer close(stopped); management.RunAlarms(running) }()
	defer func() { cancel(); <-stopped }()
	deadline := time.Now().Add(8 * time.Second)
	for {
		var retries int
		err := db.Pool.QueryRow(ctx, "SELECT count(*) FROM alarm_email_deliveries WHERE status='pending' AND attempts=1").Scan(&retries)
		if err != nil {
			t.Fatal(err)
		}
		if retries == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("SMTP failure was not retained for retry")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if _, err := db.Pool.Exec(ctx, "UPDATE alarm_email_deliveries SET next_attempt_at=now() WHERE status='pending'"); err != nil {
		t.Fatal(err)
	}
	select {
	case body := <-received:
		if !strings.Contains(body, "Recorded transition: active") || !strings.Contains(body, "/alarms") {
			t.Fatalf("alarm mail missing useful context: %s", body)
		}
	case <-time.After(8 * time.Second):
		t.Fatal("durable retry did not reach local SMTP fixture")
	}
	for {
		var sent int
		if err := db.Pool.QueryRow(ctx, "SELECT count(*) FROM alarm_email_deliveries WHERE status='sent' AND attempts=2").Scan(&sent); err != nil {
			t.Fatal(err)
		}
		if sent == 1 {
			break
		}
		if time.Now().After(deadline.Add(3 * time.Second)) {
			t.Fatal("successful SMTP retry was not committed")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if connections.Load() != 2 {
		t.Fatalf("expected one failed and one successful local connection, got %d", connections.Load())
	}
}
